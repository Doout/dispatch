package deploy

import (
	"bytes"
	"strings"
	"testing"
)

func TestRegisteredHelmTargetRejectsNamespaceEscapeAndRemovedAPIs(t *testing.T) {
	for _, manifest := range []string{
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: stolen\n  namespace: another-project\n",
		"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: another-project\n",
		"apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: global-resource\n",
		"apiVersion: extensions/v1beta1\nkind: Deployment\nmetadata:\n  name: removed-api\n",
		"apiVersion: unknown.example/v1\nkind: UnknownResource\nmetadata:\n  name: unknown-scope\n",
	} {
		if _, err := (helmDeploymentMetadata{TargetNamespace: "owned"}).Run(bytes.NewBufferString(manifest)); err == nil {
			t.Fatal("unsafe manifest accepted", manifest)
		}
	}
	result, err := (helmDeploymentMetadata{TargetNamespace: "owned", AppID: "app", DeploymentID: "operation", SpecDigest: "accepted-spec"}).Run(bytes.NewBufferString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: safe\ndata:\n  key: value\n"))
	if err != nil || !strings.Contains(result.String(), "namespace: owned") || !strings.Contains(result.String(), "accepted-spec") {
		t.Fatal("namespace and accepted evidence missing", result, err)
	}
}
