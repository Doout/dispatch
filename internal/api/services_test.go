package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/go-chi/chi/v5"
)

func serviceTestAPI(t *testing.T) *API {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, true, EventConfig{Vault: vault})
	t.Cleanup(cleanup)
	return h.(*API)
}
func serviceRequestTest(t *testing.T, a *API, method, path string, body any, want int) []byte {
	t.Helper()
	encoded, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	a.ServeHTTP(rr, tokenRequest(method, path, bytes.NewReader(encoded)))
	if rr.Code != want {
		t.Fatalf("%s %s: %d %s", method, path, rr.Code, rr.Body.String())
	}
	return rr.Body.Bytes()
}
func TestServiceAPIWriteOnlyRevisionAndBindings(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	project := projects[0]
	body := map[string]any{"projectId": project.ID, "name": "orders-db", "type": "postgresql", "connectionUrl": "postgresql://user:p%40ss@localhost:5432/orders?sslmode=disable"}
	response := serviceRequestTest(t, a, "POST", "/api/v1/services", body, 201)
	if strings.Contains(string(response), "p@ss") || strings.Contains(string(response), "p%40ss") || strings.Contains(string(response), "postgresql://") {
		t.Fatal("credential leaked")
	}
	var item serviceResponse
	if err := json.Unmarshal(response, &item); err != nil {
		t.Fatal(err)
	}
	stored, err := a.store.GetService(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	original := stored.Fields["password"].EncryptedValue
	if original == "" || stored.Fields["password"].Value != "" {
		t.Fatal("password not encrypted")
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/services/"+item.ID, map[string]any{"description": "updated"}, 200)
	stored, _ = a.store.GetService(ctx, item.ID)
	if stored.Revision != 1 || stored.Fields["password"].EncryptedValue != original {
		t.Fatal("metadata edit changed credentials or revision")
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/services/"+item.ID, map[string]any{"fields": map[string]any{"password": map[string]any{"value": "new-password"}}}, 200)
	stored, _ = a.store.GetService(ctx, item.ID)
	if stored.Revision != 2 || stored.Check != nil {
		t.Fatal("credential edit did not increment revision")
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/services/"+item.ID, map[string]any{"revision": 1}, 409)
	servers, _ := a.store.ListServers(ctx)
	app := core.App{ID: "service-api-app", ProjectID: project.ID, ServerID: servers[0].ID, Name: "api", BuildType: core.BuildTypeDockerfile, CreatedAt: time.Now()}
	if err = a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	binding := []core.ServiceBinding{{Alias: "db", ServiceRef: item.ID, Environment: map[string]string{"DATABASE_URL": "connectionUrl"}}}
	serviceRequestTest(t, a, "PUT", "/api/v1/apps/"+app.ID+"/service-bindings", binding, 200)
	serviceRequestTest(t, a, "DELETE", "/api/v1/services/"+item.ID, nil, 409)
	overview := serviceRequestTest(t, a, "GET", "/api/v1/overview", nil, 200)
	if strings.Contains(string(overview), "new-password") || strings.Contains(string(overview), original) {
		t.Fatal("overview exposed service secret")
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/apps/"+app.ID+"/service-bindings", []core.ServiceBinding{}, 200)
	serviceRequestTest(t, a, "DELETE", "/api/v1/services/"+item.ID, nil, 204)
}
func TestServiceProjectPermissionsAndGlobalReferences(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	project := projects[0]
	now := time.Now().UTC()
	user := core.User{ID: "service-operator", Username: "service-operator", SystemRole: "member", State: "active", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	grant := core.RoleAssignment{ID: "service-grant", PrincipalType: "user", PrincipalID: user.ID, ScopeType: "project", ScopeID: project.ID, Role: "operator", CreatedAt: now, UpdatedAt: now}
	if err := a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	call := func(handler http.HandlerFunc, id string, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req = req.WithContext(withIdentity(req.Context(), core.Identity{ID: user.ID, SystemRole: "member"}))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		rr := httptest.NewRecorder()
		handler(rr, req)
		return rr
	}
	response := call(a.createService, "", `{"projectId":"`+project.ID+`","name":"team-db","type":"generic","fields":{"token":{"value":"local-value","sensitive":true}}}`)
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	var item serviceResponse
	json.Unmarshal(response.Body.Bytes(), &item)
	response = call(a.updateService, item.ID, `{"fields":{"token":{"secretRef":"global-secret"}}}`)
	if response.Code != 400 {
		t.Fatal("operator attached a global secret", response.Code)
	}
	response = call(a.createService, "", `{"projectId":"inaccessible","name":"forbidden","type":"generic"}`)
	if response.Code != 403 {
		t.Fatal("project boundary bypass", response.Code)
	}
	grant.Role = "viewer"
	if err := a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	response = call(a.verifyService, item.ID, `{}`)
	if response.Code != 403 {
		t.Fatal("viewer ran connection test", response.Code)
	}
	response = call(a.updateService, item.ID, `{"description":"change"}`)
	if response.Code != 403 {
		t.Fatal("viewer edited service", response.Code)
	}
}
func TestSecretManifestsHideDataAndLastAppliedAnnotation(t *testing.T) {
	docs := parseReleaseManifests(`apiVersion: v1
kind: Secret
metadata:
  name: database
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: '{"data":{"password":"secret-copy"}}'
data:
  password: c2VjcmV0
stringData:
  token: secret-token
`)
	if len(docs) != 1 || strings.Contains(docs[0].Document, "c2VjcmV0") || strings.Contains(docs[0].Document, "secret-token") || strings.Contains(docs[0].Document, "secret-copy") {
		t.Fatalf("Secret leaked: %#v", docs)
	}
}
