package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
	helmrelease "helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestHelmMetadataLinksEverySourceAndPullRequest(t *testing.T) {
	metadata := newHelmDeploymentMetadata(core.App{ID: "app-1", SourceRepo: "https://github.example.com/Example/devops", HelmProvenance: core.HelmProvenance{
		WorkflowResourceID: "resource-1", WorkflowRevisionID: "revision-1",
		PullRequests: []core.HelmPullRequest{{Repository: "Example/service", Number: 42}, {Repository: "Example/ui", Number: 27}},
		Sources:      map[string]core.WorkflowSourceRevision{"service": {Repository: "Example/service", CommitSHA: "service-sha"}, "ui": {Repository: "Example/ui", CommitSHA: "ui-sha"}},
	}}, core.Deployment{ID: "deployment-1", CommitSHA: "chart-sha"})
	manifest := `apiVersion: v1
kind: ConfigMap
metadata:
  name: preview
data:
  release: ready
`
	rendered, err := metadata.Run(bytes.NewBufferString(manifest))
	if err != nil {
		t.Fatal(err)
	}
	var object struct {
		Metadata struct {
			Labels      map[string]string `yaml:"labels"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"metadata"`
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(rendered.Bytes(), &object); err != nil {
		t.Fatal(err)
	}
	if object.Data["release"] != "ready" || object.Metadata.Labels["dispatch.app/has-pr"] != "true" || object.Metadata.Labels["dispatch.app/app-id"] != "app-1" {
		t.Fatalf("manifest metadata or chart data missing: %+v", object)
	}
	var linked helmDeploymentMetadata
	if err := json.Unmarshal([]byte(object.Metadata.Annotations[helmProvenanceAnnotation]), &linked); err != nil {
		t.Fatal(err)
	}
	if len(linked.Provenance.PullRequests) != 2 || linked.Provenance.Sources["ui"].CommitSHA != "ui-sha" || linked.ChartCommitSHA != "chart-sha" {
		t.Fatalf("missing preview provenance: %+v", linked)
	}
	if !strings.Contains(metadata.description(), "revision-1") {
		t.Fatal("Helm release description omitted workflow revision")
	}

	client := fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.preview.v3", Namespace: "previews", Labels: map[string]string{"owner": "helm"}}})
	if err := labelHelmRelease(context.Background(), client, &helmrelease.Release{Name: "preview", Version: 3, Namespace: "previews"}, metadata); err != nil {
		t.Fatal(err)
	}
	secret, err := client.CoreV1().Secrets("previews").Get(context.Background(), "sh.helm.release.v1.preview.v3", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if secret.Labels["owner"] != "helm" || secret.Labels["dispatch.app/has-pr"] != "true" || secret.Annotations[helmProvenanceAnnotation] != metadata.annotation() {
		t.Fatalf("Helm storage metadata missing: %+v", secret.ObjectMeta)
	}
}
