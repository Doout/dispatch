package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/remoteruntime"
)

func runtimeAPIFixture(t *testing.T) (*API, core.PrivateNetwork, edge.Session, core.App) {
	t.Helper()
	a := serviceTestAPI(t)
	a.deploy = deploy.NewService(a.store, deploy.RemoteExecutor{Local: deploy.SimulationExecutor{}, Broker: a.runtimeBroker()})
	ctx := context.Background()
	var node core.PrivateNetwork
	raw := serviceRequestTest(t, a, "POST", "/api/v1/private-networks", map[string]string{"name": "runtime", "driver": "dispatch_agent"}, 201)
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatal(err)
	}
	session := enrollNodeTest(t, a, node)
	var server core.Server
	raw = serviceRequestTest(t, a, "POST", "/api/v1/servers", map[string]string{"name": "remote-runtime", "runtime": "docker", "agentNodeId": node.ID}, 201)
	if err := json.Unmarshal(raw, &server); err != nil {
		t.Fatal(err)
	}
	if server.AgentNodeID != node.ID || server.AgentMode != "outbound-runtime" {
		t.Fatalf("binding missing: %#v", server)
	}
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := core.App{ID: "runtime-app", ProjectID: projects[0].ID, ServerID: server.ID, Name: "Remote app", BuildType: core.BuildTypeCompose, ComposeContent: "services: {}", CreatedAt: time.Now()}
	if err = a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	return a, node, session, app
}

func runtimeNodeRequest(t *testing.T, a *API, method, path, token string, input any, want int) []byte {
	t.Helper()
	raw, _ := json.Marshal(input)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Dispatch-Runtime-Version", remoteruntime.APIVersion)
	req.Header.Set("X-Dispatch-Runtime-Capabilities", "deploy,inspect,logs,start,stop,rollback,destroy,provision_service")
	rr := httptest.NewRecorder()
	a.ServeHTTP(rr, req)
	if rr.Code != want {
		t.Fatalf("%s %s: %d %s", method, path, rr.Code, rr.Body.String())
	}
	return rr.Body.Bytes()
}
func submitRuntimeTest(t *testing.T, a *API, app core.App, op, key string, want int) core.RuntimeJob {
	t.Helper()
	req := tokenRequest("POST", "/api/v1/apps/"+app.ID+"/runtime/"+op, bytes.NewBufferString(`{}`))
	req.Header.Set("Idempotency-Key", key)
	rr := httptest.NewRecorder()
	a.ServeHTTP(rr, req)
	if rr.Code != want {
		t.Fatalf("submit %s: %d %s", op, rr.Code, rr.Body.String())
	}
	var job core.RuntimeJob
	if want == 202 && json.Unmarshal(rr.Body.Bytes(), &job) != nil {
		t.Fatal("invalid receipt")
	}
	return job
}
func TestRemoteRuntimeAPIEnrollmentReceiptsAndRevocation(t *testing.T) {
	a, node, session, app := runtimeAPIFixture(t)
	serviceRequestTest(t, a, "POST", "/api/v1/servers", map[string]string{"name": "duplicate", "runtime": "docker", "agentNodeId": node.ID}, 409)
	job := submitRuntimeTest(t, a, app, "inspect", "inspection-key", 202)
	replay := submitRuntimeTest(t, a, app, "inspect", "inspection-key", 202)
	if replay.ID != job.ID {
		t.Fatal("request replay created another job")
	}
	submitRuntimeTest(t, a, app, "start", "inspection-key", 409)
	path := "/api/v1/edge/nodes/" + node.ID + "/runtime/jobs/"
	runtimeNodeRequest(t, a, "GET", path+"next", node.EnrollmentToken, nil, 401)
	var leased remoteruntime.LeasedJob
	raw := runtimeNodeRequest(t, a, "GET", path+"next", session.Token, nil, 200)
	if json.Unmarshal(raw, &leased) != nil || leased.ID != job.ID {
		t.Fatal("wrong job leased")
	}
	runtimeNodeRequest(t, a, "POST", path+job.ID+"/complete", session.Token, remoteruntime.Completion{LeaseToken: "stale", Result: remoteruntime.Result{State: "succeeded"}}, 409)
	runtimeNodeRequest(t, a, "POST", path+job.ID+"/complete", session.Token, remoteruntime.Completion{LeaseToken: leased.LeaseToken, Result: remoteruntime.Result{State: "succeeded"}}, 204)
	serviceRequestTest(t, a, "GET", "/api/v1/apps/"+app.ID+"/runtime/jobs/"+job.ID, nil, 200)
	serviceRequestTest(t, a, "POST", "/api/v1/private-networks/"+node.ID+"/revoke", nil, 200)
	runtimeNodeRequest(t, a, "GET", path+"next", session.Token, nil, 401)
}
func TestRemoteRuntimeAPIRejectsLegacyAndUnknownProtocol(t *testing.T) {
	a, node, session, _ := runtimeAPIFixture(t)
	path := "/api/v1/edge/nodes/" + node.ID + "/runtime/jobs/next"
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	req.Header.Set("X-Dispatch-Runtime-Version", "unknown")
	rr := httptest.NewRecorder()
	a.ServeHTTP(rr, req)
	if rr.Code != 422 {
		t.Fatalf("unknown version: %d", rr.Code)
	}
	token, hash, err := newEdgeToken()
	if err != nil {
		t.Fatal(err)
	}
	legacy := core.PrivateNetwork{ID: "legacy", Name: "Legacy", Driver: edge.DriverAgent, TokenHash: hash, State: "ready", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = a.store.CreatePrivateNetwork(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	runtimeNodeRequest(t, a, http.MethodGet, "/api/v1/edge/nodes/legacy/runtime/jobs/next", token, nil, 401)
}
