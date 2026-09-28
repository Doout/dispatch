package workflow

import (
	"strings"
	"testing"
)

func TestBuiltinServiceTemplateDefaultsAndValidation(t *testing.T) {
	base := "apiVersion: dispatch/v1alpha1\nkind: ServiceTemplate\nmetadata:\n  name: database\nspec:\n  serviceType: postgresql\n  provision:\n"
	for _, provider := range []string{"docker", "helm"} {
		doc := base + "    " + provider + ":\n      serverRef: target\n"
		docs, err := Parse("template.yaml", []byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		if docs[0].ServiceTemplate.Provision.Provider() != provider || !docs[0].ServiceTemplate.Outputs["connectionUrl"].Sensitive {
			t.Fatal("missing default provider or connection contract")
		}
		yaml, _ := docs[0].MarshalYAML()
		if _, err := Parse("template.yaml", yaml); err != nil {
			t.Fatal(err)
		}
		topology, err := BuildTopology("template.yaml", yaml)
		if err != nil || len(topology.Nodes) != 1 || topology.Nodes[0].Detail != "target" {
			t.Fatal("missing built-in provider topology")
		}
	}
	tests := []struct{ body, error string }{
		{"    docker: {}\n", "serverRef"},
		{"    docker: {serverRef: target}\n    helm: {serverRef: target}\n", "exactly one"},
		{"    docker: {serverRef: target}\n    run: echo hi\n", "exactly one"},
		{"    docker: {serverRef: target}\n    builder: docker\n", "builders"},
		{"    docker: {serverRef: target, image: --privileged}\n", "image"},
		{"    docker: {serverRef: target, storageMountPath: '/data,extra'}\n", "absolute"},
		{"    docker: {serverRef: target, environment: {POSTGRES_PASSWORD: changed}}\n", "managed"},
		{"    docker: {serverRef: target, environment: {A: '{{ inputs.unknown }}'}}\n", "unknown"},
		{"    helm: {serverRef: target, namespace: 'invalid/name'}\n", "namespace"},
		{"    helm: {serverRef: target, storage: '-1Gi'}\n", "positive"},
		{"    helm: {serverRef: target, values: {password: literal}}\n", "require"},
		{"    helm: {serverRef: target, chart: ../../tmp}\n", "OCI"},
		{"    helm: {serverRef: target, chart: db, repository: 'http://example.test'}\n", "HTTPS"},
	}
	for _, test := range tests {
		if _, err := Parse("template.yaml", []byte(base+test.body)); err == nil || !strings.Contains(err.Error(), test.error) {
			t.Errorf("expected %s for %s: %v", test.error, test.body, err)
		}
	}
}

func TestGenericBuiltinServiceTemplateConnectionMapping(t *testing.T) {
	doc := `apiVersion: dispatch/v1alpha1
kind: ServiceTemplate
metadata:
  name: cache
spec:
  serviceType: generic
  inputs:
    region: {type: string}
  provision:
    helm:
      serverRef: target
      chart: cache
      repository: https://charts.example.test
      values:
        region: '{{ inputs.region }}'
        auth: {password: '{{ service.password }}'}
      connection:
        host: '{{ service.resource }}.{{ service.namespace }}.svc'
        token: '{{ service.password }}'
  outputs:
    host: {}
    token: {sensitive: true}
`
	if _, err := Parse("template.yaml", []byte(doc)); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse("template.yaml", []byte(strings.Replace(doc, "sensitive: true", "sensitive: false", 1))); err == nil {
		t.Fatal("accepted a plaintext credential output")
	}
	if _, err := Parse("template.yaml", []byte(strings.Replace(doc, "token: '{{ service.password }}'\n  outputs:", "other: x\n  outputs:", 1))); err == nil {
		t.Fatal("accepted missing output mapping")
	}
}
