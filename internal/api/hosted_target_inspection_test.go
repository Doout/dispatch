package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowrunner"
)

type targetInspectionRunner func(context.Context, workflowrunner.Request, func(string)) (workflowrunner.Result, error)

func (run targetInspectionRunner) Run(ctx context.Context, request workflowrunner.Request, progress func(string)) (workflowrunner.Result, error) {
	return run(ctx, request, progress)
}

func TestHostedTargetInspectionChecksPendingRequestAndMembership(t *testing.T) {
	for _, scenario := range []string{"success", "changed target", "revoked before lease", "revoked before save", "incomplete evidence"} {
		t.Run(scenario, func(t *testing.T) {
			revoked := false
			inspector := &hostedTargetInspector{pending: map[string]pendingTargetInspection{}, auth: &HostedAuth{TenantID: "tenant", Authenticate: func(r *http.Request) (core.Identity, error) {
				if r.Header.Get("Authorization") != "Bearer browser-session" || revoked {
					return core.Identity{}, errors.New("membership revoked")
				}
				return core.Identity{ID: "owner", SystemRole: core.UserRoleOwner}, nil
			}}}
			var saved workflowrunner.Request
			inspector.runner = targetInspectionRunner(func(ctx context.Context, request workflowrunner.Request, _ func(string)) (workflowrunner.Result, error) {
				// Exercise the serialized worker payload, including private fields.
				raw, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &saved); err != nil {
					t.Fatal(err)
				}
				if saved.ProjectID != "project" || saved.TargetInspection.Kubeconfig != "private-config" {
					t.Fatal("target request lost its project or stored credentials")
				}
				if err := inspector.authorize(ctx, saved); err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "changed target":
					saved.TargetInspection.Config.Namespace = "another-namespace"
				case "revoked before lease":
					revoked = true
				}
				if err := inspector.authorize(ctx, saved); err != nil {
					return workflowrunner.Result{}, err
				}
				if scenario == "revoked before save" {
					revoked = true
				}
				evidence := &core.KubernetesTargetEvidence{ClusterUID: "cluster", NamespaceUID: "namespace", Version: "v1.36.0", CheckedAt: time.Now()}
				if scenario == "incomplete evidence" {
					evidence.NamespaceUID = ""
				}
				return workflowrunner.Result{State: "succeeded", TargetEvidence: evidence}, nil
			})
			a := API{kubernetesTargetValidator: inspector.inspect}
			r := httptest.NewRequest(http.MethodPost, "/api/v1/servers", nil)
			r.Header.Set("Authorization", "Bearer browser-session")
			config := &core.KubernetesServerConfig{KubeconfigData: "private-config", Context: "test", Namespace: "app"}
			w := httptest.NewRecorder()
			accepted := a.inspectKubernetesTarget(w, r, config, nil, "project")
			if accepted != (scenario == "success") {
				t.Fatalf("accepted=%v, status=%d body=%s", accepted, w.Code, w.Body)
			}
			if accepted && config.Validation.ClusterUID != "cluster" {
				t.Fatal("evidence not saved")
			}
			if err := inspector.authorize(context.Background(), saved); err == nil {
				t.Fatal("completed request remained authorized")
			}
		})
	}
}

func TestHostedTargetInspectionNeverFallsBackToControlPlane(t *testing.T) {
	a := API{}
	a.ConfigureHostedTargetInspection(nil)
	if a.kubernetesTargetValidator == nil {
		t.Fatal("missing hosted validator permits local discovery")
	}
	w := httptest.NewRecorder()
	if a.inspectKubernetesTarget(w, httptest.NewRequest(http.MethodPost, "/", nil), &core.KubernetesServerConfig{}, nil, "project") || w.Code != http.StatusUnprocessableEntity {
		t.Fatal("incomplete hosted runtime accepted a target")
	}
}
