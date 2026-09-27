package workflow

import (
	"strings"
	"testing"
)

func TestParseApplication(t *testing.T) {
	documents, err := Parse("dispatch.yaml", []byte(`
apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: storefront
spec:
  sources:
    service:
      repository: example/storefront-api
    ui:
      repository: example/storefront-web
    chart:
      repository: example/deployment-config
      path: charts/storefront
  jobs:
    build-service:
      runFrom: service
      run: ./scripts/build.sh
      secrets:
        REGISTRY_PASSWORD:
          secretRef: registry-password
          key: auth.password
      outputs: [imageRepository, imageTag]
    build-ui:
      runFrom: chart
      sources: [ui]
      run: ./scripts/build-ui.sh "{{ sources.ui.path }}"
      outputs: [imageRepository, imageTag]
  deployments:
    application:
      helm:
        sourceRef: chart
        bindings:
          backend.image.repository:
            outputRef: build-service.imageRepository
  stages:
    - name: development
      targetRef: development
      deploy: [application]
      url: https://preview.example.com
      checks:
        e2e:
          pipelineRef: storefront-e2e
          with:
            base-url: "{{ stage.url }}"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || documents[0].Spec == nil {
		t.Fatalf("unexpected documents: %#v", documents)
	}
	if got := documents[0].Spec.Sources["service"].Branch; got != "main" {
		t.Fatalf("default branch = %q", got)
	}
	if got := documents[0].Spec.Jobs["build-service"].Reuse; got != "onInputMatch" {
		t.Fatalf("default reuse = %q", got)
	}
	if got := documents[0].Spec.Stages[0].Approval; got != "automatic" {
		t.Fatalf("default approval = %q", got)
	}
	if got := documents[0].Spec.Jobs["build-service"].Secrets["REGISTRY_PASSWORD"].Key; got != "auth.password" {
		t.Fatalf("JSON key = %q", got)
	}
}

func TestParsePipeline(t *testing.T) {
	_, err := Parse("e2e.yaml", []byte(`
apiVersion: dispatch/v1alpha1
kind: Pipeline
metadata:
  name: storefront-e2e
spec:
  inputs:
    base-url:
      required: true
  sources:
    tests:
      repository: example/storefront-tests
  jobs:
    test:
      runFrom: tests
      run: ./scripts/e2e.sh "{{ inputs.base-url }}"
  finally:
    results:
      runFrom: tests
      run: ./scripts/upload-results.sh
`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestServiceTemplateContract(t *testing.T) {
	yaml := `apiVersion: dispatch/v1alpha1
kind: ServiceTemplate
metadata: {name: shared-postgres}
spec:
  serviceType: postgresql
  inputs:
    cluster: {type: service, serviceType: postgresql, required: true}
    database: {type: string, required: true}
  provision:
    run: ./create-db.sh
  outputs:
    host: {}
    database: {}
    username: {}
    password: {sensitive: true}
`
	documents, err := Parse("service.yaml", []byte(yaml))
	if err != nil || len(documents) != 1 || documents[0].ServiceTemplate == nil {
		t.Fatalf("parse: %v", err)
	}
	if documents[0].ServiceTemplate.Provision.Reuse != "never" {
		t.Fatal("provisioning must not reuse a previous run")
	}
	encoded, err := documents[0].MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse("service.yaml", encoded); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	bad := strings.Replace(yaml, "./create-db.sh", `echo "{{ inputs.database }}"`, 1)
	if _, err := Parse("service.yaml", []byte(bad)); err == nil {
		t.Fatal("input interpolation into shell should be rejected")
	}
	bad = strings.Replace(yaml, "    host: {}", "    connectionUrl: {}\n    host: {}", 1)
	if _, err := Parse("service.yaml", []byte(bad)); err == nil {
		t.Fatal("conflicting connection outputs should be rejected")
	}
}

func TestParseRejectsUnknownFieldsAndReferences(t *testing.T) {
	for name, source := range map[string]string{
		"unknown field": `apiVersion: dispatch/v1alpha1
kind: Application
metadata: {name: test}
spec:
  sources: {app: {repository: owner/repo}}
  surprise: true`,
		"unknown source": `apiVersion: dispatch/v1alpha1
kind: Application
metadata: {name: test}
spec:
  sources: {app: {repository: owner/repo}}
  jobs:
    build:
      runFrom: missing
      run: ./build.sh`,
		"unknown output": `apiVersion: dispatch/v1alpha1
kind: Application
metadata: {name: test}
spec:
  sources: {app: {repository: owner/repo}}
  jobs:
    build:
      runFrom: app
      run: ./build.sh
      outputs: [image]
  deployments:
    app:
      helm:
        sourceRef: app
        bindings:
          image.tag: {outputRef: build.tag}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse("dispatch.yaml", []byte(source)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDigestUsesDefaultedDocument(t *testing.T) {
	first, err := Parse("a.yaml", []byte(`apiVersion: dispatch/v1alpha1
kind: Application
metadata: {name: test}
spec:
  sources: {app: {repository: owner/repo}}
`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Parse("b.yaml", []byte(`apiVersion: dispatch/v1alpha1
kind: Application
metadata: {name: test}
spec:
  sources: {app: {repository: owner/repo, branch: main}}
`))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := first[0].Digest()
	b, _ := second[0].Digest()
	if a != b || !strings.HasPrefix(a, "sha256:") {
		t.Fatalf("digests differ: %s %s", a, b)
	}
}
