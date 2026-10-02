package api

import (
	"context"
	"encoding/json"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"testing"
	"time"
)

func TestRuntimeRetentionAPIRequiresPolicyReviewAndProjectScope(t *testing.T) {
	a := serviceTestAPI(t)
	enableOperationsForTest(t, a)
	a.deploy.Retention = deploy.SimulationRetention{Store: a.store}
	if err := a.store.CreateProject(context.Background(), core.Project{ID: "retention-other-project", Name: "Other retention project", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	projects, err := a.store.ListProjects(context.Background())
	if err != nil || len(projects) < 2 {
		t.Fatalf("fixture projects %v %v", projects, err)
	}
	p := core.RetentionPolicy{ProjectID: projects[0].ID, LogDays: 30, RunDays: 90, KeepRuns: 5, ImageDays: 7, StoppedRevisionDays: 7, KeepRollbackRevisions: 2}
	path := "/api/v1/projects/" + p.ProjectID + "/retention"
	serviceRequestTest(t, a, "PUT", path, p, 200)
	serviceRequestTest(t, a, "POST", path+"/preview", map[string]any{"scope": "runtime"}, 400)
	raw := serviceRequestTest(t, a, "POST", path+"/preview", map[string]any{"scope": "runtime", "expectedPolicy": p}, 200)
	var result core.RetentionResult
	if err = json.Unmarshal(raw, &result); err != nil || result.Runtime == nil {
		t.Fatalf("missing runtime review %s %v", raw, err)
	}
	r := result.Runtime
	serviceRequestTest(t, a, "GET", path+"/runtime-reviews/"+r.ID, nil, 200)
	serviceRequestTest(t, a, "GET", "/api/v1/projects/"+projects[1].ID+"/retention/runtime-reviews/"+r.ID, nil, 404)
	input := map[string]any{"scope": "runtime", "confirm": p.ProjectID, "expectedPolicy": p, "runtimeReviewId": r.ID, "runtimeReviewDigest": "changed"}
	serviceRequestTest(t, a, "POST", path+"/apply", input, 409)
	input["runtimeReviewDigest"] = r.Digest
	serviceRequestTest(t, a, "POST", path+"/apply", input, 200)
	invalid := p
	invalid.KeepRollbackRevisions = 1
	serviceRequestTest(t, a, "PUT", path, invalid, 400)
}
