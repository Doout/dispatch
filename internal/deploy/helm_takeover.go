package deploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	kube "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
)

type HelmTakeoverReview struct {
	Release           string `json:"release"`
	Namespace         string `json:"namespace"`
	Target            string `json:"target"`
	Revision          int    `json:"revision"`
	PreviousAppID     string `json:"previousAppId"`
	PreviousProjectID string `json:"previousProjectId"`
	Version           string `json:"version"`
}

// TransferHelmRelease inspects the deployed revision and, on confirmation,
// transfers it in one resourceVersion-guarded write.
// Earlier Helm revisions remain historical evidence of the original owner.
func TransferHelmRelease(ctx context.Context, app core.App, server core.Server, confirm string, checkPriorOwner func(string) error) (HelmTakeoverReview, error) {
	if err := ValidateHelmTarget(app, server); err != nil {
		return HelmTakeoverReview{}, err
	}
	prepared, cleanup, err := prepareKubernetesServer(ctx, server)
	if err != nil {
		return HelmTakeoverReview{}, errors.New("Cannot verify the Kubernetes target credentials or identity.")
	}
	defer cleanup()
	ns := helmNamespace(app, server)
	settings := cli.New()
	selectHelmCredentials(settings, prepared)
	cfg, err := scopedHelmGetter(settings.RESTClientGetter(), prepared).ToRESTConfig()
	if err != nil {
		return HelmTakeoverReview{}, errors.New("Cannot open the Kubernetes target.")
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return HelmTakeoverReview{}, errors.New("Cannot open the Kubernetes client.")
	}
	return takeoverRelease(ctx, client, app, server, ns, os.Getenv("HELM_DRIVER"), confirm, checkPriorOwner)
}

// Use Helm's own decoder and encoder. The adapters retain the existing object's
// metadata and set resourceVersion on the SDK write, which normally omits it.
type reviewedSecret struct {
	typed.SecretInterface
	ctx     context.Context
	prior   *core.App
	current *kube.Secret
}

func (s reviewedSecret) List(context.Context, metav1.ListOptions) (*kube.SecretList, error) {
	return &kube.SecretList{Items: []kube.Secret{*s.current.DeepCopy()}}, nil
}

func (s reviewedSecret) Update(_ context.Context, encoded *kube.Secret, opts metav1.UpdateOptions) (*kube.Secret, error) {
	next := s.current.DeepCopy()
	next.Data["release"] = encoded.Data["release"]
	updateHandoffMetadata(next, encoded.Labels, *s.prior)
	return s.SecretInterface.Update(s.ctx, next, opts)
}

type reviewedConfigMap struct {
	typed.ConfigMapInterface
	ctx     context.Context
	prior   *core.App
	current *kube.ConfigMap
}

func (s reviewedConfigMap) List(context.Context, metav1.ListOptions) (*kube.ConfigMapList, error) {
	return &kube.ConfigMapList{Items: []kube.ConfigMap{*s.current.DeepCopy()}}, nil
}

func (s reviewedConfigMap) Update(_ context.Context, encoded *kube.ConfigMap, opts metav1.UpdateOptions) (*kube.ConfigMap, error) {
	next := s.current.DeepCopy()
	next.Data["release"] = encoded.Data["release"]
	updateHandoffMetadata(next, encoded.Labels, *s.prior)
	return s.ConfigMapInterface.Update(s.ctx, next, opts)
}

func updateHandoffMetadata(obj metav1.Object, labels map[string]string, prior core.App) {
	existing := obj.GetLabels()
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["dispatch.app/transferred-from-app"] = prior.ID
	annotations["dispatch.app/transferred-from-project"] = prior.ProjectID
	if existing == nil {
		existing = map[string]string{}
	}
	existing["dispatch.app/managed-by"] = "dispatch"
	existing["dispatch.app/app-id"] = labels["dispatch.app/app-id"]
	existing["dispatch.app/project-id"] = labels["dispatch.app/project-id"]
	if raw := annotations[helmProvenanceAnnotation]; raw != "" {
		updated, err := transferProvenance(raw, labels["dispatch.app/app-id"], labels["dispatch.app/project-id"])
		if err == nil {
			annotations[helmProvenanceAnnotation] = updated
		}
	}
	obj.SetLabels(existing)
	obj.SetAnnotations(annotations)
}

func takeoverRelease(ctx context.Context, client kubernetes.Interface, app core.App, server core.Server, ns, storage, confirmation string, checkPriorOwner func(string) error) (review HelmTakeoverReview, err error) {
	name := helmReleaseName(app)
	var prior core.App
	var backend driver.Driver
	var object metav1.Object
	selector := metav1.ListOptions{LabelSelector: "owner=helm,name=" + name}
	// Select by the actual storage keys, including pending and failed records.
	// Helm's History skips undecodable records, which could conceal a newer writer.
	var snapshotKey string
	var latest int
	selectRecord := func(obj metav1.Object) error {
		v, e := strconv.Atoi(obj.GetLabels()["version"])
		if e != nil || v < 1 || obj.GetName() != fmt.Sprintf("sh.helm.release.v1.%s.v%d", name, v) {
			return errors.New("Unexpected Helm storage metadata.")
		}
		if v > latest {
			latest = v
			object = obj
			snapshotKey = obj.GetName()
		}
		return nil
	}
	switch storage {
	case "", "secret", "secrets":
		items := client.CoreV1().Secrets(ns)
		list, e := items.List(ctx, selector)
		if e != nil {
			return review, errors.New("Cannot read Helm release Secrets in the target namespace.")
		}
		for i := range list.Items {
			if err = selectRecord(&list.Items[i]); err != nil {
				return review, err
			}
		}
		if item, ok := object.(*kube.Secret); ok {
			if item.Type != "helm.sh/release.v1" {
				return review, errors.New("Unexpected Helm Secret type.")
			}
			backend = driver.NewSecrets(reviewedSecret{items, ctx, &prior, item})
		}
	case "configmap", "configmaps":
		items := client.CoreV1().ConfigMaps(ns)
		list, e := items.List(ctx, selector)
		if e != nil {
			return review, errors.New("Cannot read Helm release ConfigMaps in the target namespace.")
		}
		for i := range list.Items {
			if err = selectRecord(&list.Items[i]); err != nil {
				return review, err
			}
		}
		if item, ok := object.(*kube.ConfigMap); ok {
			backend = driver.NewConfigMaps(reviewedConfigMap{items, ctx, &prior, item})
		}
	default:
		return review, errors.New("Takeover requires Kubernetes Secret or ConfigMap Helm storage.")
	}
	if object == nil {
		return review, errors.New("No Helm release exists at this target and namespace.")
	}
	if object.GetLabels()["status"] != "deployed" {
		return review, errors.New("The latest Helm revision must be deployed before takeover.")
	}
	// Query handles malformed records without dereferencing a missing release.
	matches, e := backend.Query(map[string]string{"owner": "helm", "name": name, "version": strconv.Itoa(latest)})
	if e != nil || len(matches) != 1 {
		return review, errors.New("Cannot decode the current Helm release.")
	}
	r := matches[0]
	if r.Info == nil || r.Name != name || r.Namespace != ns || r.Version != latest || r.Info.Status != release.StatusDeployed {
		return review, errors.New("The current Helm revision does not match this application target.")
	}
	labels := object.GetLabels()
	prior = core.App{ID: labels["dispatch.app/app-id"], ProjectID: labels["dispatch.app/project-id"], HelmRelease: name}
	// Older releases can carry an authoritative description without storage labels.
	const prefix = "Dispatch provenance: "
	if raw, ok := strings.CutPrefix(r.Info.Description, prefix); ok {
		var m helmDeploymentMetadata
		if json.Unmarshal([]byte(raw), &m) != nil || (prior.ID != "" && prior.ID != m.AppID) || (prior.ProjectID != "" && m.ProjectID != "" && prior.ProjectID != m.ProjectID) {
			return review, errors.New("Conflicting Doodship ownership in Helm storage.")
		}
		prior.ID = m.AppID
		if m.ProjectID != "" {
			prior.ProjectID = m.ProjectID
		}
	}
	managedBy := labels["dispatch.app/managed-by"]
	if managedBy != "" && managedBy != "dispatch" {
		return review, errors.New("Helm storage belongs to another manager.")
	}
	if prior.ID == "" || requireHelmReleaseOwner(r, prior, ns) != nil {
		return review, errors.New("Cannot verify a previous Doodship owner for this release.")
	}
	if raw := object.GetAnnotations()[helmProvenanceAnnotation]; raw != "" {
		var m helmDeploymentMetadata
		if json.Unmarshal([]byte(raw), &m) != nil || m.AppID != prior.ID || (m.ProjectID != "" && m.ProjectID != prior.ProjectID) {
			return review, errors.New("Conflicting Doodship ownership on the Helm storage object.")
		}
	}
	if prior.ID == app.ID && prior.ProjectID == app.ProjectID {
		return review, errors.New("This application already owns the release. Retry the deployment.")
	}
	if err := checkPriorOwner(prior.ID); err != nil {
		return review, err
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{app.ID, app.ProjectID, app.SpecDigest(), server.ID, ns, name, snapshotKey, string(object.GetUID()), object.GetResourceVersion(), prior.ID, prior.ProjectID}, "\x00")))
	review = HelmTakeoverReview{Release: name, Namespace: ns, Target: server.Name, Revision: latest, PreviousAppID: prior.ID, PreviousProjectID: prior.ProjectID, Version: fmt.Sprintf("%x", digest)}
	if confirmation != "" && confirmation != review.Version {
		return review, errors.New("The release or target changed. Inspect it again before confirming.")
	}
	if err := prepareTransferredRelease(r, prior, app, server.Kubernetes.Validation != nil); err != nil {
		return review, err
	}
	if confirmation == "" {
		return review, nil
	}
	if err := backend.Update(snapshotKey, r); err != nil {
		return review, errors.New("Could not transfer the reviewed revision. Inspect it again and check target write access.")
	}
	return review, nil
}

func transferProvenance(raw, appID, projectID string) (string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return "", errors.New("Invalid stored Doodship provenance.")
	}
	m["appId"], _ = json.Marshal(appID)
	m["projectId"], _ = json.Marshal(projectID)
	b, err := json.Marshal(m)
	return string(b), err
}

func prepareTransferredRelease(r *release.Release, prior, app core.App, isolated bool) error {
	const prefix = "Dispatch provenance: "
	raw := strings.TrimPrefix(r.Info.Description, prefix)
	hasDescription := strings.HasPrefix(r.Info.Description, prefix)

	var output bytes.Buffer
	decoder := yaml.NewDecoder(strings.NewReader(r.Manifest))
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("Cannot verify the stored Helm manifest.")
		}
		if len(document.Content) == 0 || document.Content[0].Tag == "!!null" {
			continue
		}
		object := document.Content[0]
		meta := yamlField(object, "metadata")
		annotations := yamlField(meta, "annotations")
		provenance := yamlField(annotations, helmProvenanceAnnotation)
		if provenance == nil {
			return errors.New("Release resources have missing Doodship ownership.")
		}
		p := provenance.Value
		var owner helmDeploymentMetadata
		if json.Unmarshal([]byte(p), &owner) != nil || owner.AppID != prior.ID || owner.DeploymentID == "" || (owner.ProjectID != "" && owner.ProjectID != prior.ProjectID) {
			return errors.New("Release resources have missing or conflicting Doodship ownership.")
		}
		if isolated {
			var scope map[string]any
			if err := object.Decode(&scope); err != nil {
				return errors.New("Cannot verify resource namespace.")
			}
			resourceMeta, _ := scope["metadata"].(map[string]any)
			if err := enforceHelmNamespace(scope, resourceMeta, r.Namespace); err != nil {
				return err
			}
		}
		if !hasDescription {
			raw = p
			hasDescription = true
		}
		provenance.Value, err = transferProvenance(p, app.ID, app.ProjectID)
		if err != nil {
			return err
		}
		setOwnerLabels(meta, app)
		if kind := yamlField(object, "kind"); kind != nil && kind.Value == "StatefulSet" {
			if claims := yamlField(yamlField(object, "spec"), "volumeClaimTemplates"); claims != nil {
				for _, claim := range claims.Content {
					setOwnerLabels(yamlField(claim, "metadata"), app)
				}
			}
		}
		b, err := yaml.Marshal(&document)
		if err != nil {
			return errors.New("Cannot update the stored Helm manifest.")
		}
		output.WriteString("---\n")
		output.Write(b)
	}
	if hasDescription {
		updated, err := transferProvenance(raw, app.ID, app.ProjectID)
		if err != nil {
			return err
		}
		r.Info.Description = prefix + updated
	}
	r.Manifest = output.String()
	if r.Labels == nil {
		r.Labels = map[string]string{}
	}
	r.Labels["dispatch.app/managed-by"] = "dispatch"
	r.Labels["dispatch.app/app-id"] = app.ID
	r.Labels["dispatch.app/project-id"] = app.ProjectID
	return requireHelmReleaseOwner(r, app, r.Namespace)
}

// Edit only the ownership scalars. Chart values and selectors may legitimately
// contain the same IDs, or YAML scalars that cannot round-trip through Go maps.
func yamlField(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func setOwnerLabels(meta *yaml.Node, app core.App) {
	labels := yamlField(meta, "labels")
	if v := yamlField(labels, "dispatch.app/app-id"); v != nil {
		v.Value = app.ID
		v.Tag = "!!str"
	}
	if v := yamlField(labels, "dispatch.app/project-id"); v != nil {
		v.Value = app.ProjectID
		v.Tag = "!!str"
	}
}
