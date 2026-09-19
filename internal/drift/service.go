package drift

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
	jsonpatch "github.com/evanphx/json-patch"
	"github.com/oklog/ulid/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"time"
)

type Service struct {
	Store   store.Store
	Vault   *secretcrypto.Vault
	Connect Factory
}

func New(data store.Store, vault *secretcrypto.Vault) *Service {
	return &Service{Store: data, Vault: vault, Connect: Connect}
}
func scope(id string) string { return "deployment-drift:" + id }

// Capture receives the exact rendered manifest returned by the successful Helm action.
func (s *Service) Capture(ctx context.Context, d core.Deployment, app core.App, server core.Server, manifest string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	namespace := app.HelmNamespace
	if namespace == "" && server.Kubernetes != nil {
		namespace = server.Kubernetes.Namespace
	}
	if namespace == "" {
		namespace = "default"
	}
	release := app.HelmRelease
	if release == "" {
		release = app.Name
	}
	objects, err := Parse(manifest, namespace)
	if err != nil {
		return err
	}
	conn, err := s.Connect(ctx, server)
	if err != nil {
		return errors.New("cannot capture deployed resource identities")
	}
	defer conn.Close()
	for _, obj := range objects {
		client, err := conn.resource(obj, namespace)
		if err != nil {
			return errors.New("cannot map deployed resource")
		}
		live, err := client.Get(ctx, obj.GetName(), metav1.GetOptions{})
		if err != nil {
			return errors.New("cannot capture deployed resource identity")
		}
		obj.SetUID(live.GetUID())
		if obj.GetLabels()["dispatch.service-binding"] != "true" {
			annotations := obj.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations["meta.helm.sh/release-name"] = release
			annotations["meta.helm.sh/release-namespace"] = namespace
			obj.SetAnnotations(annotations)
			labels := obj.GetLabels()
			if labels == nil {
				labels = map[string]string{}
			}
			labels["app.kubernetes.io/managed-by"] = "Helm"
			obj.SetLabels(labels)
		}
	}
	raw, err := json.Marshal(objects)
	if err != nil {
		return err
	}
	cipher, err := s.Vault.Encrypt(scope(d.ID), raw)
	if err != nil {
		return errors.New("cannot encrypt deployed resource baseline")
	}
	return s.Store.SaveDriftBaseline(ctx, core.DriftBaseline{DeploymentID: d.ID, AppID: app.ID, ServerID: server.ID, Namespace: namespace, Release: release, Ciphertext: cipher})
}
func (s *Service) load(ctx context.Context, appID string) (core.DriftBaseline, core.Server, []*unstructured.Unstructured, error) {
	d, err := s.Store.LatestSuccessfulDeployment(ctx, appID)
	if err != nil {
		return core.DriftBaseline{}, core.Server{}, nil, errors.New("No successful deployment is available.")
	}
	b, err := s.Store.GetDriftBaseline(ctx, d.ID)
	if err != nil {
		return b, core.Server{}, nil, errors.New("No saved resource baseline. Deploy once to enable drift checks.")
	}
	app, err := s.Store.GetApp(ctx, appID)
	if err != nil {
		return b, core.Server{}, nil, errors.New("The application is unavailable.")
	}
	if b.AppID != appID || b.ServerID != app.ServerID {
		return b, core.Server{}, nil, errors.New("The application target changed. Deploy to the new target first.")
	}
	server, err := s.Store.GetServer(ctx, b.ServerID)
	if err != nil {
		return b, server, nil, errors.New("The deployment target is unavailable.")
	}
	raw, err := s.Vault.Decrypt(scope(b.DeploymentID), b.Ciphertext)
	if err != nil {
		return b, server, nil, errors.New("The saved resource baseline could not be opened.")
	}
	var objects []*unstructured.Unstructured
	if json.Unmarshal(raw, &objects) != nil {
		return b, server, nil, errors.New("The saved resource baseline is invalid.")
	}
	return b, server, objects, nil
}
func emptyCheck() core.DriftCheck {
	return core.DriftCheck{State: "unknown", Health: "unknown", Location: "Dispatch controller", Resources: []core.DriftResource{}}
}
func (s *Service) Check(ctx context.Context, app string) (core.DriftCheck, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := emptyCheck()
	now := time.Now().UTC()
	result.CheckedAt = &now
	if old, err := s.Store.GetDriftCheck(ctx, app); err == nil {
		result.LastSuccessfulCheckAt = old.LastSuccessfulCheckAt
	}
	b, server, objects, err := s.load(ctx, app)
	result.DeploymentID = b.DeploymentID
	if err != nil {
		result.Message = err.Error()
	} else {
		conn, e := s.Connect(ctx, server)
		if e != nil {
			result.Message = "Target unavailable or resource discovery denied."
		} else {
			defer conn.Close()
			result.State, result.Health = "synced", "not_applicable"
			unknownHealth := false
			for _, want := range objects {
				item := core.DriftResource{APIVersion: want.GetAPIVersion(), Kind: want.GetKind(), Namespace: want.GetNamespace(), Name: want.GetName(), State: "synced", Health: "unknown", Differences: []core.DriftDifference{}}
				client, e := conn.resource(want, b.Namespace)
				var live *unstructured.Unstructured
				if e == nil {
					live, e = client.Get(ctx, want.GetName(), metav1.GetOptions{})
				}
				switch {
				case apierrors.IsNotFound(e):
					item.State, item.Health = "missing", "degraded"
				case e != nil:
					item.State = "unknown"
				case !owned(want, live, b):
					item.State = "unknown"
				default:
					item.Health = health(live)
					Merge(clean(want).Object, clean(live).Object, "", &item.Differences)
					if len(item.Differences) > 0 {
						item.State = "out_of_sync"
					}
					if len(item.Differences) > 200 {
						item.Differences = item.Differences[:200]
						item.Truncated = true
					}
				}
				item.Namespace = want.GetNamespace()
				if item.State == "unknown" {
					result.State = "unknown"
				} else if item.State != "synced" && result.State != "unknown" {
					result.State = "out_of_sync"
				}
				if item.Health == "healthy" && result.Health == "not_applicable" {
					result.Health = "healthy"
				}
				if item.Health == "degraded" {
					result.Health = "degraded"
				} else if item.Health == "progressing" && result.Health != "degraded" {
					result.Health = "progressing"
				} else if item.Health == "unknown" {
					unknownHealth = true
				}
				result.Resources = append(result.Resources, item)
			}
			if unknownHealth && (result.Health == "healthy" || result.Health == "not_applicable") {
				result.Health = "unknown"
			}
			if result.State == "unknown" {
				result.Message = "Some resources could not be read or no longer belong to this deployment."
			} else {
				result.LastSuccessfulCheckAt = &now
				result.Message = "Compared fields saved with the last successful deployment."
			}
		}
	}
	// A timeout must still replace a previously green result with Unknown.
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer saveCancel()
	return result, s.Store.SaveDriftCheck(saveCtx, app, result)
}
func owned(want, live *unstructured.Unstructured, b core.DriftBaseline) bool {
	if name := live.GetAnnotations()["meta.helm.sh/release-name"]; name != "" && name != b.Release {
		return false
	}
	if want.GetUID() != "" && want.GetUID() == live.GetUID() {
		return true
	}
	if live.GetAnnotations()["meta.helm.sh/release-name"] == b.Release && live.GetAnnotations()["meta.helm.sh/release-namespace"] == b.Namespace {
		return true
	}
	return live.GetLabels()["dispatch.app"] == b.AppID && live.GetLabels()["dispatch.deployment"] == b.DeploymentID
}

type repair struct {
	client  dynamic.ResourceInterface
	object  *unstructured.Unstructured
	patch   []byte
	missing bool
}

func (s *Service) Reapply(ctx context.Context, app, deployment, actor string) (core.DriftCheck, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	b, server, objects, err := s.load(ctx, app)
	if err != nil {
		return emptyCheck(), err
	}
	if deployment == "" || b.DeploymentID != deployment {
		return emptyCheck(), errors.New("The successful deployment changed. Refresh before reapplying.")
	}
	action := core.DriftAction{ID: ulid.Make().String(), AppID: app, DeploymentID: deployment, Actor: actor, State: "running", Message: "Reapplying saved deployed configuration", CreatedAt: time.Now().UTC()}
	if err = s.Store.SaveDriftAction(ctx, action); err != nil {
		return emptyCheck(), err
	}
	applyErr := func() error {
		conn, err := s.Connect(ctx, server)
		if err != nil {
			return errors.New("Target unavailable or resource discovery denied.")
		}
		defer conn.Close()
		plan := []repair{}
		for _, want := range objects {
			client, err := conn.resource(want, b.Namespace)
			if err != nil {
				return errors.New("A deployed resource type is unavailable.")
			}
			live, err := client.Get(ctx, want.GetName(), metav1.GetOptions{})
			desired := clean(want)
			if apierrors.IsNotFound(err) {
				if _, err = client.Create(ctx, desired, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "dispatch-reapply"}); err != nil {
					return errors.New("Preflight failed. A missing resource cannot be recreated.")
				}
				plan = append(plan, repair{client: client, object: desired, missing: true})
				continue
			}
			if err != nil {
				return errors.New("A deployed resource could not be read.")
			}
			if !owned(want, live, b) {
				return errors.New("A resource no longer belongs to this deployment. Reapply was refused.")
			}
			diffs := []core.DriftDifference{}
			merged := Merge(desired.Object, live.Object, "", &diffs)
			if len(diffs) == 0 {
				continue
			}
			before, _ := json.Marshal(live.Object)
			after, _ := json.Marshal(merged)
			patch, err := jsonpatch.CreateMergePatch(before, after)
			if err != nil {
				return errors.New("Cannot prepare resource changes.")
			}
			// ResourceVersion preconditions prevent overwriting concurrent edits.
			var payload map[string]any
			_ = json.Unmarshal(patch, &payload)
			metadata, _ := payload["metadata"].(map[string]any)
			if metadata == nil {
				metadata = map[string]any{}
			}
			metadata["resourceVersion"] = live.GetResourceVersion()
			payload["metadata"] = metadata
			patch, _ = json.Marshal(payload)
			if _, err = client.Patch(ctx, want.GetName(), types.MergePatchType, patch, metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "dispatch-reapply"}); err != nil {
				return errors.New("Preflight failed. Resource permissions or immutable fields prevent reapply.")
			}
			plan = append(plan, repair{client: client, object: desired, patch: patch})
		}
		for _, r := range plan {
			if r.missing {
				_, err = r.client.Create(ctx, r.object, metav1.CreateOptions{FieldManager: "dispatch-reapply"})
			} else {
				_, err = r.client.Patch(ctx, r.object.GetName(), types.MergePatchType, r.patch, metav1.PatchOptions{FieldManager: "dispatch-reapply"})
			}
			if err != nil {
				return errors.New("Reapply stopped after a resource changed or became unavailable. Some resources may have been updated; check again.")
			}
		}
		return nil
	}()
	action.State, action.Message = "succeeded", "Saved deployed configuration reapplied"
	if applyErr != nil {
		action.State, action.Message = "failed", applyErr.Error()
	}
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 35*time.Second)
	defer saveCancel()
	if err = s.Store.SaveDriftAction(saveCtx, action); err != nil {
		return emptyCheck(), errors.New("Cannot save the reapply result. Check runtime state before retrying.")
	}
	result, err := s.Check(saveCtx, app)
	if applyErr != nil {
		return result, applyErr
	}
	return result, err
}
