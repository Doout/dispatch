package automationclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestApplicationAndRollbackUseActualDurableReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, path, kind, input string
		args                    Arguments
	}{
		{"app_create", "/api/v1/apps", "application", `{"projectId":"project-1","serverId":"server-1","name":"web","composeContent":"services: {}"}`, Arguments{Key: "create-app-once"}},
		{"deployment_rollback", "/api/v1/deployments/deployment-1/rollback", "deployment", `{"confirmDeploymentId":"deployment-1","expectedCurrentDeploymentId":"current-1","expectedReviewDigest":"review-digest","confirmDatabaseNotReverted":true}`, Arguments{DeploymentID: "deployment-1", Key: "rollback-once"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != tc.path || r.Header.Get("Idempotency-Key") != tc.args.Key {
					t.Fatal("changed request identity", r.Method, r.URL.Path)
				}
				var got, want any
				json.NewDecoder(r.Body).Decode(&got)
				json.Unmarshal([]byte(tc.input), &want)
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(want)
				if string(gotJSON) != string(wantJSON) {
					t.Error("changed reviewed input", string(gotJSON))
				}
				w.Header().Set("Location", "/api/v1/mutation-receipts/receipt-1")
				w.Header().Set("Idempotency-Replayed", "true")
				w.WriteHeader(202)
				io.WriteString(w, `{"id":"receipt-1","operationId":"original-operation","resourceId":"owned-resource","operationKind":"`+tc.kind+`","state":"accepted"}`)
			})
			tc.args.Input = json.RawMessage(tc.input)
			for range 2 {
				r := c.Call(context.Background(), tc.name, tc.args)
				if !r.OK || !r.Replayed || r.Continuation == nil || r.Continuation.Kind != "receipt" || r.Continuation.ID != "receipt-1" || r.Continuation.OperationID != "original-operation" || r.Continuation.ResourceID != "owned-resource" || r.Continuation.Key != tc.args.Key {
					t.Fatal("lost actual receipt", r)
				}
			}
		})
	}
}

func TestWorkflowValidationNeverInventsConfigurationOrConfirmation(t *testing.T) {
	var requests atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
	for _, tc := range []struct {
		name string
		args Arguments
	}{
		{"app_create", Arguments{Input: json.RawMessage(`{"projectId":"project-1","serverId":"server-1","name":"web","sourceRepo":"https://example.com/web"}`)}},
		{"app_create", Arguments{Key: "create-app-once", Input: json.RawMessage(`{"projectId":"project-1","serverId":"server-1","name":"web"}`)}},
		{"app_create", Arguments{Key: "create-app-once", Input: json.RawMessage(`{"projectId":"project-1","serverId":"server-1","name":"web","composeContent":"services: {}","sourceCredentialId":"global-secret"}`)}},
		{"app_bindings_set", Arguments{AppID: "app-1", Input: json.RawMessage(`null`)}},
		{"app_helm_values_set", Arguments{AppID: "app-1", Input: json.RawMessage(`{}`)}},
		{"app_health_set", Arguments{AppID: "app-1", Input: json.RawMessage(`{"timeoutSeconds":601}`)}},
		{"deployment_rollback", Arguments{DeploymentID: "deployment-1", Key: "rollback-once", Input: json.RawMessage(`{"confirmDeploymentId":"other","expectedCurrentDeploymentId":"current","expectedReviewDigest":"digest","confirmDatabaseNotReverted":true}`)}},
		{"deployment_rollback", Arguments{DeploymentID: "deployment-1", Key: "rollback-once", Input: json.RawMessage(`{"confirmDeploymentId":"deployment-1","expectedCurrentDeploymentId":"current","expectedReviewDigest":"digest","confirmDatabaseNotReverted":false}`)}},
		{"server_adopt", Arguments{ServerID: "server-1", Input: json.RawMessage(`{"resourceId":"machine","revision":1}`)}},
		{"server_retry", Arguments{ServerID: "server-1", OperationID: "operation-1", Key: "unsupported-key"}},
		{"server_enrollment", Arguments{ServerID: "server-1", Key: "unsupported-key"}},
	} {
		if r := c.Call(context.Background(), tc.name, tc.args); r.OK || r.ExitCode() != 2 {
			t.Fatal(tc.name, "accepted invalid request", r)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid workflow reached server")
	}
}

func TestWorkflowConfigurationAndRecoveryKeepOriginalContinuation(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, response, input, kind, id string
		args                                          Arguments
	}{
		{"app_bindings_set", "PUT", "/api/v1/apps/app-1/service-bindings", `[]`, `[]`, "application", "app-1", Arguments{AppID: "app-1"}},
		{"app_helm_values_set", "PUT", "/api/v1/apps/app-1/helm-values", `{"overrides":{}}`, `{"overrides":{}}`, "application", "app-1", Arguments{AppID: "app-1"}},
		{"app_health_set", "PUT", "/api/v1/apps/app-1/health-policy", `{"checks":[]}`, `{"checks":[]}`, "application", "app-1", Arguments{AppID: "app-1"}},
		{"deployment_cancel", "POST", "/api/v1/deployments/deployment-1/cancel", "", "", "deployment", "deployment-1", Arguments{DeploymentID: "deployment-1"}},
		{"server_adopt", "POST", "/api/v1/infrastructure/servers/server-1/adopt", `{"id":"server-1","revision":3}`, `{"resourceId":"machine","revision":2,"confirmName":"review-machine"}`, "managed_server", "server-1", Arguments{ServerID: "server-1"}},
		{"server_enrollment", "POST", "/api/v1/infrastructure/servers/server-1/enrollment", `{"nodeId":"node-1","token":"one-use-enrollment","expiresAt":"2026-10-03T14:00:00Z"}`, "", "managed_server", "server-1", Arguments{ServerID: "server-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method || r.URL.Path != tc.path || r.Header.Get("Idempotency-Key") != "" {
					t.Error("wrong route or manufactured retry identity", r.Method, r.URL.Path)
				}
				if tc.response == "" {
					w.WriteHeader(204)
				} else {
					io.WriteString(w, tc.response)
				}
			})
			tc.args.Input = json.RawMessage(tc.input)
			r := c.Call(context.Background(), tc.name, tc.args)
			if !r.OK || r.Continuation == nil || r.Continuation.Kind != tc.kind || r.Continuation.ID != tc.id {
				t.Fatal("lost original continuation", r)
			}
			if tc.name == "server_enrollment" && !strings.Contains(string(r.Data), "one-use-enrollment") {
				t.Fatal("explicit issuance did not return installation credential")
			}
		})
	}
}

func TestInfrastructureRecoveryBindsOperationToSelectedServer(t *testing.T) {
	for _, action := range []string{"server_retry", "server_cancel"} {
		t.Run(action, func(t *testing.T) {
			var requests, mutations atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method == "GET" {
					if r.URL.Path != "/api/v1/infrastructure/servers/server-1/operations" {
						t.Error("wrong inspection")
					}
					io.WriteString(w, `[{"id":"operation-1","serverId":"server-1","state":"paused"}]`)
					return
				}
				mutations.Add(1)
				if r.URL.Path != "/api/v1/infrastructure/operations/operation-1/"+strings.TrimPrefix(action, "server_") {
					t.Error("wrong recovery identity")
				}
				w.WriteHeader(204)
			})
			r := c.Call(context.Background(), action, Arguments{ServerID: "server-1", OperationID: "operation-1"})
			if !r.OK || requests.Load() != 2 || mutations.Load() != 1 || r.Continuation.OperationID != "operation-1" {
				t.Fatal("recovery did not preserve original operation", r)
			}
			r = c.Call(context.Background(), action, Arguments{ServerID: "server-1", OperationID: "foreign-operation"})
			if r.ExitCode() != 2 || mutations.Load() != 1 {
				t.Fatal("foreign operation was submitted", r)
			}
		})
	}
}

func TestWorkflowLostReplyNeverReplaysMutation(t *testing.T) {
	for _, tc := range []struct {
		name string
		args Arguments
	}{
		{"app_create", Arguments{Key: "create-app-once", Input: json.RawMessage(`{"projectId":"project-1","serverId":"server-1","name":"web","composeContent":"services: {}"}`)}},
		{"server_enrollment", Arguments{ServerID: "server-1"}},
		{"deployment_cancel", Arguments{DeploymentID: "deployment-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Fatal(err)
				}
				conn.Close()
			})
			r := c.Call(context.Background(), tc.name, tc.args)
			if r.OK || calls.Load() != 1 || r.Continuation == nil || r.Continuation.Key != tc.args.Key {
				t.Fatal("replayed lost mutation or lost retry identity", r)
			}
			if tc.args.Key == "" && !strings.Contains(r.Error.Title, "No automatic retry") {
				t.Fatal("non-keyed uncertainty missing inspection guidance", r)
			}
		})
	}
}

func TestWorkflowRejectsInventedReceiptAndMismatchedRecoveryRecords(t *testing.T) {
	for _, tc := range []struct {
		name, response, location string
		args                     Arguments
	}{
		{"app_create", `{"id":"application-1","operationId":"application-1","resourceId":"application-1","operationKind":"application"}`, "", Arguments{Key: "create-app-once", Input: json.RawMessage(`{"projectId":"project-1","serverId":"server-1","name":"web","composeContent":"services: {}"}`)}},
		{"app_create", `{"id":"receipt-1","operationId":"application-1","resourceId":"application-1","operationKind":"application"}`, "/api/v1/mutation-receipts/different-receipt", Arguments{Key: "create-app-once", Input: json.RawMessage(`{"projectId":"project-1","serverId":"server-1","name":"web","composeContent":"services: {}"}`)}},
		{"server_adopt", `{"id":"foreign-server"}`, "", Arguments{ServerID: "server-1", Input: json.RawMessage(`{"resourceId":"original-machine","revision":1,"confirmName":"review-machine"}`)}},
		{"server_enrollment", `{"nodeId":"node-1","token":"credential"}`, "", Arguments{ServerID: "server-1"}},
	} {
		t.Run(tc.name+tc.location, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				io.WriteString(w, tc.response)
			})
			r := c.Call(context.Background(), tc.name, tc.args)
			if r.OK || r.Error == nil || r.Error.Code != "invalid_response" || r.Continuation == nil || r.Continuation.Key != tc.args.Key {
				t.Fatal("invented successful receipt or lost original continuation", r)
			}
		})
	}
}

func TestWorkflowMCPSchemasExposeExplicitInputsAndMutationHints(t *testing.T) {
	for _, name := range []string{"app_create", "app_bindings_set", "deployment_rollback", "server_adopt", "server_enrollment"} {
		op, _ := Find(name)
		tool := toolDescription(op)
		hints := tool["annotations"].(map[string]any)
		if hints["readOnlyHint"] != false || hints["idempotentHint"] != !op.NonIdempotent {
			t.Fatal(name, "misleading mutation hint")
		}
		props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if name == "app_bindings_set" && props["input"].(map[string]any)["type"] != "array" {
			t.Fatal("binding input must be an explicit array")
		}
		if name == "deployment_rollback" {
			schema := props["input"].(map[string]any)["properties"].(map[string]any)
			if schema["confirmDatabaseNotReverted"].(map[string]any)["const"] != true {
				t.Fatal("database acknowledgment not explicit")
			}
		}
	}
}

func TestWorkflowInputPropertiesAreInOpenAPI(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	for _, op := range workflowOperations {
		if op.InputSchema == "" || op.InputSchema == "ServiceBindings" || op.InputSchema == "HealthPolicy" {
			continue
		}
		var properties map[string]any
		for path, raw := range spec["paths"].(map[string]any) {
			entry := raw.(map[string]any)
			if contractPathMatches(path, op, entry) {
				body := entry[strings.ToLower(op.Method)].(map[string]any)["requestBody"].(map[string]any)
				schema := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
				if ref, ok := schema["$ref"].(string); ok {
					schema = spec["components"].(map[string]any)["schemas"].(map[string]any)[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
				}
				properties = schema["properties"].(map[string]any)
				break
			}
		}
		if properties == nil {
			t.Errorf("missing input contract %s", op.Name)
			continue
		}
		for key := range workflowSchema(op.InputSchema)["properties"].(map[string]any) {
			if _, ok := properties[key]; !ok {
				t.Errorf("%s field absent from OpenAPI: %s", op.Name, key)
			}
		}
	}
}
