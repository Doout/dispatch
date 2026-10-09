package api

import (
	"testing"
)

func TestDisablingWorkflowWorkerAlsoStopsTypedRuntimeDispatch(t *testing.T) {
	a, node, session, app := runtimeAPIFixture(t)
	path := "/api/v1/private-networks/" + node.ID
	input := map[string]string{"name": node.Name, "driver": node.Driver, "workflowMode": "tenant", "workflowProjectId": app.ProjectID}
	serviceRequestTest(t, a, "PUT", path, input, 200)
	submitRuntimeTest(t, a, app, "inspect", "accepted-before-disable", 202)
	input["workflowMode"] = "disabled"
	serviceRequestTest(t, a, "PUT", path, input, 200)
	// A later name-only edit must not turn a disabled worker into a legacy agent.
	serviceRequestTest(t, a, "PUT", path, map[string]string{"name": "Renamed worker", "driver": node.Driver}, 200)
	saved, err := a.store.GetPrivateNetwork(t.Context(), node.ID)
	if err != nil || saved.Config["workflowMode"] != "disabled" {
		t.Fatal("disabled worker state was lost", err)
	}
	runtimeNodeRequest(t, a, "GET", "/api/v1/edge/nodes/"+node.ID+"/runtime/jobs/next", session.Token, nil, 422)
}
