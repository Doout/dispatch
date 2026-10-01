package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
)

func fixture() provider.CreateServerRequest {
	return provider.CreateServerRequest{Name: "Public mock", Region: "mock-region", Size: "mock-small", Image: "mock-linux", Network: "mock-private", SSHKey: "public-mock-key", ProviderConfig: map[string]any{}}
}

func mockClient(t *testing.T, handler func(http.Handler) http.Handler) *provider.Client {
	t.Helper()
	adapter, err := mock.New(mock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	serverHandler := provider.Handler(adapter, "")
	if handler != nil {
		serverHandler = handler(serverHandler)
	}
	server := httptest.NewServer(serverHandler)
	t.Cleanup(server.Close)
	client, err := provider.NewClient(server.URL, "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestHTTPProviderConformance(t *testing.T) {
	client := mockClient(t, nil)
	report, err := provider.RunConformance(context.Background(), client, provider.ConformanceOptions{Request: fixture(), PollInterval: time.Millisecond, Timeout: time.Second})
	if err != nil {
		t.Fatalf("conformance failed: %+v %v", report, err)
	}
	if len(report.Checks) != 10 {
		t.Fatalf("missing lifecycle checks: %+v", report)
	}
	for _, check := range report.Checks {
		if !check.Passed {
			t.Fatalf("failed check: %+v", check)
		}
	}
}

func TestConformanceDetectsBrokenAdapters(t *testing.T) {
	for _, fault := range []string{"incompatible", "malformed", "unavailable", "invalid-schema", "unstable-create", "bad-state", "wrong-problem", "bad-options"} {
		t.Run(fault, func(t *testing.T) {
			client := mockClient(t, func(next http.Handler) http.Handler {
				if fault == "incompatible" || fault == "malformed" || fault == "unavailable" {
					result, err := mock.FaultHandler(next, fault)
					if err != nil {
						t.Fatal(err)
					}
					return result
				}
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if fault == "invalid-schema" && r.URL.Path == "/v1/manifest" {
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(provider.Manifest{APIVersion: provider.APIVersion, Name: "bad", DisplayName: "Bad", Version: "1", Capabilities: []string{provider.CapabilityCreate}, ConfigurationSchema: json.RawMessage(`{"type":"string"}`)})
						return
					}
					if fault == "unstable-create" && r.Method == "POST" && r.URL.Path == "/v1/servers" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(202)
						_ = json.NewEncoder(w).Encode(provider.Operation{ID: fmt.Sprintf("different-%d", time.Now().UnixNano()), ResourceID: "missing", State: provider.StatePending})
						return
					}
					if fault == "bad-state" && r.Method == "POST" && r.URL.Path == "/v1/servers" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(202)
						_ = json.NewEncoder(w).Encode(provider.Operation{ID: "operation", State: "invented"})
						return
					}
					if fault == "wrong-problem" && r.URL.Path == "/v1/validate" {
						http.Error(w, "internal credentials must not be printed", 500)
						return
					}
					if fault == "bad-options" && r.URL.Path == "/v1/options" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"items":[{"id":"duplicate","name":"First"},{"id":"duplicate","name":"Second"}]}`))
						return
					}
					next.ServeHTTP(w, r)
				})
			})
			report, err := provider.RunConformance(context.Background(), client, provider.ConformanceOptions{Request: fixture(), PollInterval: time.Millisecond, Timeout: 100 * time.Millisecond})
			if err == nil {
				t.Fatalf("broken adapter passed: %+v", report)
			}
			encoded, _ := json.Marshal(report)
			if strings.Contains(string(encoded), "internal credentials") {
				t.Fatal("conformance exposed upstream response content")
			}
		})
	}
}

func TestClientRejectsRedirectsOversizedResponsesAndIdentityChanges(t *testing.T) {
	for _, kind := range []string{"redirect", "oversized", "identity", "problem-status"} {
		t.Run(kind, func(t *testing.T) {
			visited := false
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { visited = true }))
			defer destination.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "redirect":
					http.Redirect(w, r, destination.URL, 302)
				case "oversized":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(strings.Repeat(" ", provider.MaxMessageBytes+1)))
				case "identity":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(provider.Operation{ID: "other", State: provider.StatePending})
				case "problem-status":
					w.Header().Set("Content-Type", "application/problem+json")
					w.WriteHeader(500)
					_ = json.NewEncoder(w).Encode(provider.NewProblem(404, "Wrong status", ""))
				}
			}))
			defer server.Close()
			client, err := provider.NewClient(server.URL, "test-credential", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.Operation(context.Background(), "expected"); err == nil {
				t.Fatal("invalid response accepted")
			}
			if visited {
				t.Fatal("credential-bearing request followed a redirect")
			}
		})
	}
}

func TestHTTPProviderRejectsInvalidRequestsAndCredentials(t *testing.T) {
	adapter, _ := mock.New(mock.Options{})
	handler := provider.Handler(adapter, "test-credential")
	for _, test := range []struct {
		name, path, body, key, token string
		want                         int
	}{
		{"unauthenticated", "/v1/validate", `{"config":{}}`, "", "", 401},
		{"missing-key", "/v1/servers", `{}`, "", "test-credential", 400},
		{"bad-config", "/v1/validate", `{"config":{"unexpected":"private-value"}}`, "", "test-credential", 422},
		{"null-body", "/v1/validate", `null`, "", "test-credential", 400},
		{"trailing-json", "/v1/validate", `{"config":{}} {}`, "", "test-credential", 400},
		{"oversized", "/v1/validate", `{"config":{"testLabel":"` + strings.Repeat("x", provider.MaxMessageBytes) + `"}}`, "", "test-credential", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			if test.key != "" {
				request.Header.Set("Idempotency-Key", test.key)
			}
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			var problem provider.Problem
			if response.Code != test.want || response.Header().Get("Content-Type") != "application/problem+json" || json.Unmarshal(response.Body.Bytes(), &problem) != nil || problem.Status != test.want || problem.Type == "" {
				t.Fatalf("invalid problem response: %d %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private-value") || strings.Contains(response.Body.String(), "test-credential") {
				t.Fatal("error exposed request credentials")
			}
		})
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client, _ := provider.NewClient(server.URL, "test-credential", server.Client())
	if _, err := client.Manifest(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := client.Server(context.Background(), "absent")
	var problem *provider.Problem
	if !errors.As(err, &problem) || problem.Status != 404 {
		t.Fatalf("lost provider problem status: %v", err)
	}
}
