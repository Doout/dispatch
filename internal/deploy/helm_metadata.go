package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
	helmrelease "helm.sh/helm/v3/pkg/release"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

const helmProvenanceAnnotation = "dispatch.app/provenance"

type helmDeploymentMetadata struct {
	AppID          string              `json:"appId"`
	DeploymentID   string              `json:"deploymentId"`
	ChartCommitSHA string              `json:"chartCommitSha,omitempty"`
	SourceRepo     string              `json:"sourceRepo,omitempty"`
	Provenance     core.HelmProvenance `json:"provenance"`
}

func newHelmDeploymentMetadata(app core.App, deployment core.Deployment) helmDeploymentMetadata {
	repository := app.SourceRepo
	if parsed, err := url.Parse(repository); err == nil && parsed.Scheme != "" {
		parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
		repository = parsed.String()
	}
	return helmDeploymentMetadata{AppID: app.ID, DeploymentID: deployment.ID, ChartCommitSHA: deployment.CommitSHA,
		SourceRepo: repository, Provenance: app.HelmProvenance}
}

func (m helmDeploymentMetadata) labels() map[string]string {
	labels := map[string]string{"dispatch.app/managed-by": "dispatch"}
	if m.AppID != "" {
		labels["dispatch.app/app-id"] = m.AppID
	}
	if m.DeploymentID != "" {
		labels["dispatch.app/deployment-id"] = m.DeploymentID
	}
	if m.Provenance.WorkflowResourceID != "" {
		labels["dispatch.app/workflow-resource-id"] = m.Provenance.WorkflowResourceID
	}
	if len(m.Provenance.PullRequests) > 0 {
		labels["dispatch.app/has-pr"] = "true"
		if len(m.Provenance.PullRequests) == 1 {
			labels["dispatch.app/pr-number"] = strconv.Itoa(m.Provenance.PullRequests[0].Number)
		}
	}
	return labels
}

func (m helmDeploymentMetadata) annotation() string {
	raw, _ := json.Marshal(m)
	return string(raw)
}

func (m helmDeploymentMetadata) description() string { return "Dispatch provenance: " + m.annotation() }

// Run stamps the objects Helm stores in its release manifest as well as the
// objects sent to Kubernetes. It does not alter pod selectors or chart values.
func (m helmDeploymentMetadata) Run(rendered *bytes.Buffer) (*bytes.Buffer, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(rendered.Bytes()))
	var output bytes.Buffer
	for {
		var document map[string]any
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode Helm manifest: %w", err)
		}
		if len(document) == 0 {
			continue
		}
		object, _ := document["metadata"].(map[string]any)
		if object == nil {
			object = map[string]any{}
			document["metadata"] = object
		}
		labels, _ := object["labels"].(map[string]any)
		if labels == nil {
			labels = map[string]any{}
			object["labels"] = labels
		}
		for key, value := range m.labels() {
			labels[key] = value
		}
		annotations, _ := object["annotations"].(map[string]any)
		if annotations == nil {
			annotations = map[string]any{}
			object["annotations"] = annotations
		}
		annotations[helmProvenanceAnnotation] = m.annotation()
		if output.Len() > 0 {
			output.WriteString("\n---\n")
		}
		raw, err := yaml.Marshal(document)
		if err != nil {
			return nil, fmt.Errorf("encode Helm manifest: %w", err)
		}
		output.Write(raw)
	}
	return &output, nil
}

func (c *sdkHelmClient) labelRelease(ctx context.Context, release *helmrelease.Release, metadata helmDeploymentMetadata) error {
	driver := os.Getenv("HELM_DRIVER")
	if driver == "sql" || driver == "memory" {
		return nil
	}
	client, err := serviceKubeClient(c.server)
	if err != nil {
		return fmt.Errorf("connect to label Helm release: %w", err)
	}
	if driver == "configmap" {
		return labelHelmConfigMap(ctx, client, release, metadata)
	}
	return labelHelmRelease(ctx, client, release, metadata)
}

func labelHelmRelease(ctx context.Context, client kubernetes.Interface, release *helmrelease.Release, metadata helmDeploymentMetadata) error {
	name := fmt.Sprintf("sh.helm.release.v1.%s.v%d", release.Name, release.Version)
	secrets := client.CoreV1().Secrets(release.Namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, err := secrets.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		for key, value := range metadata.labels() {
			secret.Labels[key] = value
		}
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations[helmProvenanceAnnotation] = metadata.annotation()
		_, err = secrets.Update(ctx, secret, metav1.UpdateOptions{})
		return err
	})
}

func labelHelmConfigMap(ctx context.Context, client kubernetes.Interface, release *helmrelease.Release, metadata helmDeploymentMetadata) error {
	name := fmt.Sprintf("sh.helm.release.v1.%s.v%d", release.Name, release.Version)
	configMaps := client.CoreV1().ConfigMaps(release.Namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		item, err := configMaps.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if item.Labels == nil {
			item.Labels = map[string]string{}
		}
		for key, value := range metadata.labels() {
			item.Labels[key] = value
		}
		if item.Annotations == nil {
			item.Annotations = map[string]string{}
		}
		item.Annotations[helmProvenanceAnnotation] = metadata.annotation()
		_, err = configMaps.Update(ctx, item, metav1.UpdateOptions{})
		return err
	})
}
