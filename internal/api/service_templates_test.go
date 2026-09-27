package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/serviceconn"
	"github.com/doout/dispatch/internal/workflow"
)

func TestServiceTemplateProvisionEncryptsOutputs(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := projects[0].ID
	if err := a.store.CreateSecret(ctx, core.Secret{ID: "template-source-credential", Name: "template source credential", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateConfigSource(ctx, core.ConfigSource{ID: "template-source", ProjectID: project, CredentialSecretID: "template-source-credential", Name: "template source", Repository: "example/config", Branch: "main", Path: "deployment", Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	document := `apiVersion: dispatch/v1alpha1
kind: ServiceTemplate
metadata:
  name: test-postgres
spec:
  serviceType: postgresql
  inputs:
    database:
      type: string
      required: true
    password:
      type: secret
      required: true
  provision:
    run: |
      printf 'host=localhost\nport=5432\ndatabase=%s\nusername=app\npassword=%s\nsslmode=disable\n' "$DISPATCH_INPUT_DATABASE" "$DISPATCH_INPUT_PASSWORD" > "$DISPATCH_OUTPUT_FILE"
      printf 'provisioned %s\n' "$DISPATCH_INPUT_PASSWORD"
  outputs:
    host: {}
    port: {}
    database: {}
    username: {}
    password:
      sensitive: true
    sslmode: {}
`
	docs, err := workflow.Parse("deployment/test-postgres.yaml", []byte(document))
	if err != nil || len(docs) != 1 {
		t.Fatalf("parse: %v", err)
	}
	if err := a.store.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "template-resource", ConfigSourceID: "template-source", APIVersion: workflow.APIVersion, Kind: workflow.KindServiceTemplate, Name: "test-postgres", Path: "deployment/test-postgres.yaml", Document: document, ConfigSHA: "abc123", Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	response := serviceRequestTest(t, a, "POST", "/api/v1/service-templates/template-resource/runs", map[string]any{"name": "orders", "inputs": map[string]string{"database": "orders", "password": "unique-password-123"}}, 202)
	if strings.Contains(string(response), "unique-password-123") {
		t.Fatal("credential in start response")
	}
	var run core.ServiceProvisionRun
	if err := json.Unmarshal(response, &run); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for run.State == "queued" || run.State == "running" {
		if time.Now().After(deadline) {
			t.Fatal("provisioner did not finish")
		}
		time.Sleep(20 * time.Millisecond)
		run, err = a.store.GetServiceProvisionRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if run.State != "succeeded" {
		t.Fatalf("run: %+v", run)
	}
	if strings.Contains(run.Error+run.Phase, "unique-password-123") {
		t.Fatal("credential in run")
	}
	service, err := a.store.GetService(ctx, run.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	if service.Fields["password"].EncryptedValue == "" || service.Fields["password"].Value != "" {
		t.Fatal("password was not encrypted")
	}
	if service.TemplateID != "template-resource" || service.TemplateConfigSHA != "abc123" {
		t.Fatalf("provenance: %+v", service)
	}
	values, err := (serviceconn.Resolver{Vault: a.eventConfig.Vault}).Resolve(ctx, service)
	if err != nil || values["password"] != "unique-password-123" || values["database"] != "orders" {
		t.Fatalf("resolved service: %v", err)
	}
}
