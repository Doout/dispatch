package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func saveResourceComparisonDeployment(t *testing.T, a *API, app core.App, id string, replicas int, evidence string) core.Deployment {
	t.Helper()
	d := core.Deployment{ID: id, AppID: app.ID, State: core.DeploymentSucceeded, CommitSHA: id + "-sha", CreatedAt: time.Now().UTC(), Snapshot: core.DeploymentSnapshot{
		TargetID: app.ServerID, Runtime: core.ServerRuntimeOpenShift, Namespace: "default", Release: "example", Chart: "./chart",
		Values: map[string]any{"replicas": replicas, "images": map[string]any{"agent-gateway": map[string]any{"wxo-agent-gateway": map[string]any{"digest_amd64": id + "-unused-digest"}}}},
	}}
	if err := a.store.CreateDeployment(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if evidence != "" {
		ciphertext, err := a.eventConfig.Vault.Encrypt("deployment-drift:"+d.ID, []byte(evidence))
		if err != nil {
			t.Fatal(err)
		}
		if err := a.store.SaveDriftBaseline(context.Background(), core.DriftBaseline{DeploymentID: d.ID, AppID: d.AppID, ServerID: app.ServerID, Namespace: "default", Release: "example", Ciphertext: ciphertext}); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func comparisonResource(replicas int, image string) string {
	return fmt.Sprintf(`[{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"default","uid":"api-uid"},"spec":{"replicas":%d,"template":{"spec":{"containers":[{"name":"api","image":%q,"env":[{"name":"ORDINARY_NAME","value":"private-environment-value"}]}]}}}},{"apiVersion":"v1","kind":"Secret","metadata":{"name":"credentials","namespace":"default","uid":"secret-uid"},"data":{"password":"private-secret-value"}}]`, replicas, image)
}

func readComparison(t *testing.T, raw []byte) deploymentComparison {
	t.Helper()
	var result deploymentComparison
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRecordedResourceComparisonEndpoints(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, err := a.store.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := apps[0]
	from := saveResourceComparisonDeployment(t, a, app, "resources-old", 2, comparisonResource(2, "example/api:v1"))
	to := saveResourceComparisonDeployment(t, a, app, "resources-new", 3, comparisonResource(3, "example/api:v2"))
	unusedOnly := saveResourceComparisonDeployment(t, a, app, "resources-unused-only", 3, comparisonResource(3, "example/api:v2"))
	other := app
	other.ID, other.Name = "resources-stage-app", "resources-stage-app"
	if err := a.store.CreateApp(ctx, other); err != nil {
		t.Fatal(err)
	}
	stage := saveResourceComparisonDeployment(t, a, other, "resources-stage", 3, comparisonResource(3, "example/api:v2"))
	beforeBaseline, err := a.store.GetDriftBaseline(ctx, to.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeHistory, err := a.store.ListApplicationHistory(ctx, app.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	// Comparison must need neither a live drift service nor target access.
	a.drift = nil
	for _, path := range []string{
		"/api/v1/deployments/" + to.ID + "/compare?from=" + from.ID,
		"/api/v1/deployments/" + to.ID + "/compare?basis=resources&from=" + from.ID,
		"/api/v1/deployments/" + stage.ID + "/compare-environment?from=" + from.ID,
	} {
		raw := serviceRequestTest(t, a, "GET", path, nil, 200)
		result := readComparison(t, raw)
		if !result.Available || result.Basis != "resources" || len(result.Changes) != 2 {
			t.Fatalf("unexpected resource comparison: %s", raw)
		}
		if strings.Contains(string(raw), "digest_amd64") || strings.Contains(string(raw), "/values/") || strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "ciphertext") {
			t.Fatalf("unused inputs or private evidence exposed: %s", raw)
		}
		paths := map[string]deploymentChange{}
		for _, change := range result.Changes {
			paths[change.Path] = change
		}
		if change, ok := paths["/resources/apps~1v1/Deployment/default/api/spec/replicas"]; !ok || change.Before != float64(2) || change.After != float64(3) {
			t.Fatalf("replica change missing: %+v", result.Changes)
		}
		foundImage := false
		for _, change := range result.Changes {
			if strings.HasSuffix(change.Path, "/image") && change.Before == "example/api:v1" && change.After == "example/api:v2" {
				foundImage = true
			}
		}
		if !foundImage {
			t.Fatal("rendered image change missing")
		}
	}
	result := readComparison(t, serviceRequestTest(t, a, "GET", "/api/v1/deployments/"+unusedOnly.ID+"/compare?from="+to.ID, nil, 200))
	if !result.Available || result.Basis != "resources" || len(result.Changes) != 0 {
		t.Fatalf("unused inputs or code revisions appeared as resource changes: %+v", result)
	}
	afterBaseline, err := a.store.GetDriftBaseline(ctx, to.ID)
	if err != nil || afterBaseline != beforeBaseline {
		t.Fatal("comparison changed encrypted evidence", err)
	}
	afterHistory, err := a.store.ListApplicationHistory(ctx, app.ID, "", 100)
	if err != nil || !reflect.DeepEqual(beforeHistory, afterHistory) {
		t.Fatal("comparison changed deployment records", err)
	}
	serviceRequestTest(t, a, "GET", "/api/v1/deployments/"+stage.ID+"/compare?from="+from.ID+"&basis=resources", nil, 404)
}

func TestRecordedResourceComparisonUnavailableAndExplicitInputs(t *testing.T) {
	a := serviceTestAPI(t)
	apps, err := a.store.ListApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	from := saveResourceComparisonDeployment(t, a, apps[0], "fallback-old", 2, comparisonResource(2, "example/api:v1"))
	for _, tc := range []struct{ name, evidence string }{{"missing", ""}, {"invalid", "not-json"}} {
		to := saveResourceComparisonDeployment(t, a, apps[0], "fallback-"+tc.name, 3, tc.evidence)
		for _, endpoint := range []string{"compare", "compare-environment"} {
			path := "/api/v1/deployments/" + to.ID + "/" + endpoint + "?from=" + from.ID
			result := readComparison(t, serviceRequestTest(t, a, "GET", path, nil, 200))
			if result.Available || result.Basis != "resources" || len(result.Changes) != 0 || result.Message == "" {
				t.Fatalf("missing evidence silently fell back to inputs: %+v", result)
			}
			raw := serviceRequestTest(t, a, "GET", path+"&basis=inputs", nil, 200)
			result = readComparison(t, raw)
			if !result.Available || result.Basis != "inputs" || !strings.Contains(string(raw), "digest_amd64") {
				t.Fatalf("explicit input comparison unavailable: %s", raw)
			}
			for _, query := range []string{"basis=", "basis=other", "basis=resources&basis=inputs", "basis=inputs&basis=inputs", "basis=%ZZ", "basis=resources;inputs"} {
				serviceRequestTest(t, a, "GET", path+"&"+query, nil, 400)
			}
		}
	}
}

func TestDeploymentComparisonBasisUsesHistoricalRuntime(t *testing.T) {
	a := serviceTestAPI(t)
	for _, tc := range []struct {
		name     string
		snapshot core.DeploymentSnapshot
		want     string
	}{
		{"chart", core.DeploymentSnapshot{Chart: "./chart"}, "resources"},
		{"kubernetes", core.DeploymentSnapshot{Runtime: core.ServerRuntimeKubernetes}, "resources"},
		{"openshift", core.DeploymentSnapshot{Runtime: core.ServerRuntimeOpenShift}, "resources"},
		{"docker", core.DeploymentSnapshot{Runtime: core.ServerRuntimeDocker}, "inputs"},
		{"legacy", core.DeploymentSnapshot{}, "inputs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from := core.Deployment{ID: "old", Snapshot: tc.snapshot}
			to := core.Deployment{ID: "new", App: &core.App{BuildType: core.BuildTypeHelm, HelmChart: "./current-chart"}}
			for _, pair := range [][2]core.Deployment{{from, to}, {to, from}} {
				rr := httptest.NewRecorder()
				a.writeDeploymentComparison(rr, httptest.NewRequest("GET", "/", nil), pair[0], pair[1])
				result := readComparison(t, rr.Body.Bytes())
				if rr.Code != 200 || result.Basis != tc.want {
					t.Fatalf("historical basis = %s, want %s", result.Basis, tc.want)
				}
			}
		})
	}
}

func TestRecordedResourceComparisonAuthenticationAndProjectIsolation(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, err := a.store.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := apps[0]
	from := saveResourceComparisonDeployment(t, a, app, "access-old", 2, comparisonResource(2, "example/api:v1"))
	to := saveResourceComparisonDeployment(t, a, app, "access-new", 3, comparisonResource(3, "example/api:v2"))
	privateProject := core.Project{ID: "resource-private", Name: "resource-private", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateProject(ctx, privateProject); err != nil {
		t.Fatal(err)
	}
	other := app
	other.ID, other.Name, other.ProjectID = "resource-private-app", "resource-private-app", privateProject.ID
	if err := a.store.CreateApp(ctx, other); err != nil {
		t.Fatal(err)
	}
	private := saveResourceComparisonDeployment(t, a, other, "access-private", 3, comparisonResource(3, "private-image:v1"))
	for _, endpoint := range []string{"compare", "compare-environment"} {
		path := "/api/v1/deployments/" + to.ID + "/" + endpoint + "?basis=resources&from=" + from.ID
		rr := httptest.NewRecorder()
		a.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("comparison did not require authentication: %d", rr.Code)
		}
		serviceRequestTest(t, a, "GET", "/api/v1/deployments/"+to.ID+"/"+endpoint+"?basis=resources&from="+private.ID, nil, 404)
	}
	raw := serviceRequestTest(t, a, "POST", "/api/v1/users", map[string]any{"username": "comparison-viewer", "password": "comparison-password-123", "displayName": "Comparison viewer", "systemRole": "member", "state": "active"}, http.StatusCreated)
	var user core.User
	if err := json.Unmarshal(raw, &user); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	a.ServeHTTP(login, httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewBufferString(`{"username":"comparison-viewer","password":"comparison-password-123"}`)))
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil || login.Code != 200 || session.Token == "" {
		t.Fatal("viewer login failed", err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+session.Token)
		rr := httptest.NewRecorder()
		a.ServeHTTP(rr, req)
		return rr
	}
	path := "/api/v1/deployments/" + to.ID + "/compare?basis=resources&from=" + from.ID
	if rr := request(path); rr.Code != 403 {
		t.Fatalf("unassigned member read comparison: %d", rr.Code)
	}
	if err := a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: "comparison-grant", PrincipalType: "user", PrincipalID: user.ID, ScopeType: "project", ScopeID: app.ProjectID, Role: "viewer", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if rr := request(path); rr.Code != 200 || readComparison(t, rr.Body.Bytes()).Basis != "resources" {
		t.Fatalf("project viewer could not compare: %d", rr.Code)
	}
	if rr := request("/api/v1/deployments/" + to.ID + "/compare-environment?basis=resources&from=" + private.ID); rr.Code != 404 || strings.Contains(rr.Body.String(), "private-image") {
		t.Fatalf("cross-project environment comparison exposed data: %d", rr.Code)
	}
}
