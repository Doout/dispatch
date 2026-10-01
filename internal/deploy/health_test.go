package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestHealthPolicyFailureEvidenceAndRouteGate(t *testing.T) {
	policy := core.HealthPolicy{TimeoutSeconds: 2, IntervalSeconds: 1, FailureThreshold: 1, Checks: []core.HealthCheck{{ID: "route", Kind: "http", Scope: "route", Path: "/ready"}}}
	result, err := RunHealthPolicy(context.Background(), policy, func(_ context.Context, c core.HealthCheck) HealthObservation {
		return HealthObservation{Passed: c.Kind == "container", HTTPStatus: 503}
	})
	if err == nil || result.State != "failed" || len(result.Checks) != 2 || result.Checks[0].State != "passed" || result.Checks[1].HTTPStatus != 503 || result.Checks[1].Failures != 1 || result.FinishedAt == nil {
		t.Fatalf("wrong gate evidence: %+v %v", result, err)
	}
}
func TestHealthPolicyCancellationTimeoutAndUnavailable(t *testing.T) {
	for _, state := range []string{"cancelled", "timeout", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if state == "cancelled" {
				cancel()
			}
			result, err := RunHealthPolicy(ctx, core.HealthPolicy{TimeoutSeconds: 1, CheckTimeoutSeconds: 1, FailureThreshold: 20}, func(ctx context.Context, _ core.HealthCheck) HealthObservation {
				if state == "unavailable" {
					return HealthObservation{Unavailable: true}
				}
				<-ctx.Done()
				return HealthObservation{}
			})
			if err == nil || result.State != state {
				t.Fatalf("%s: %+v %v", state, result, err)
			}
		})
	}
}
func TestHealthDockerAndSimulationShareFailedPromotion(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "app.example.test" {
			t.Errorf("missing candidate host: %s", r.Host)
		}
		w.Header().Set("X-Private", "token-secret")
		w.WriteHeader(503)
		_, _ = io.WriteString(w, "database-password-secret")
	}))
	defer endpoint.Close()
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(endpoint.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	policy := core.HealthPolicy{TimeoutSeconds: 2, FailureThreshold: 1, Checks: []core.HealthCheck{{ID: "candidate-http", Kind: "http", Scope: "route", Path: "/ready"}}}
	app := core.App{Domain: "app.example.test", HealthPolicy: policy}
	d := core.Deployment{ID: "candidate", Health: core.DeploymentHealth{Policy: policy}}
	for _, runtime := range []string{"docker", "simulation"} {
		t.Run(runtime, func(t *testing.T) {
			var saved core.DeploymentHealth
			ctx := WithHealthReporter(context.Background(), func(_ context.Context, _ string, result core.DeploymentHealth) error { saved = result; return nil })
			routed := false
			progress := func(state core.DeploymentState, _ string) error {
				if state == core.DeploymentRouting {
					routed = true
				}
				return nil
			}
			var err error
			if runtime == "docker" {
				e := DockerExecutor{run: func(_ context.Context, _ io.Reader, out io.Writer, name string, args ...string) error {
					if name != "docker" || args[0] != "inspect" {
						t.Fatal("health check mutated runtime")
					}
					_, _ = io.WriteString(out, `{"running":true,"health":"healthy"}`)
					return nil
				}}
				err = e.CheckCandidateHealth(ctx, d, app, core.Server{}, []CandidateTarget{{Container: "candidate", Host: host, Port: port}}, progress)
			} else {
				e := SimulationExecutor{Delay: time.Nanosecond, HealthProbe: func(_ context.Context, c core.HealthCheck) HealthObservation {
					return HealthObservation{Passed: c.Kind == "container", HTTPStatus: 503}
				}}
				err = e.Deploy(ctx, d, app, core.Server{}, progress)
			}
			raw, _ := json.Marshal(saved)
			if err == nil || routed || saved.State != "failed" || strings.Contains(string(raw), "secret") {
				t.Fatalf("failed policy reached routing or leaked evidence: %v %s", err, raw)
			}
		})
	}
}
func TestHealthEvidencePersistenceFailurePreventsSuccess(t *testing.T) {
	ctx := WithHealthReporter(context.Background(), func(context.Context, string, core.DeploymentHealth) error { return errors.New("database unavailable") })
	if err := evaluateDeploymentHealth(ctx, core.Deployment{}, core.App{}, func(context.Context, core.HealthCheck) HealthObservation { return HealthObservation{Passed: true} }, func(core.DeploymentState, string) error { return nil }); err == nil {
		t.Fatal("unpersisted health allowed promotion")
	}
}
func TestHealthHelmNativeProbesAndConflicts(t *testing.T) {
	policy, _ := core.NormalizeHealthPolicy(core.HealthPolicy{TimeoutSeconds: 30, IntervalSeconds: 4, CheckTimeoutSeconds: 2, FailureThreshold: 3, Checks: []core.HealthCheck{{ID: "readiness", Kind: "http", Service: "web", Port: 8080, Path: "/ready"}}})
	container := map[string]any{"name": "web"}
	document := map[string]any{"kind": "Deployment", "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{container}}}}}
	matched, err := configureHelmHealth(document, policy, 0, "")
	if err != nil || !matched["readiness"] {
		t.Fatalf("probe unavailable %v", err)
	}
	probe := container["readinessProbe"].(map[string]any)
	if probe["failureThreshold"] != 3 || probe["periodSeconds"] != 4 || probe["timeoutSeconds"] != 2 {
		t.Fatal("policy bounds lost")
	}
	policy.Checks = append(policy.Checks, core.HealthCheck{ID: "other", Kind: "tcp", Service: "web", Port: 8080})
	if _, err = configureHelmHealth(document, policy, 0, ""); err == nil {
		t.Fatal("conflicting native checks were silently omitted")
	}
}

func TestHealthRechecksEarlierSuccessBeforePromotion(t *testing.T) {
	policy := core.HealthPolicy{TimeoutSeconds: 4, IntervalSeconds: 1, FailureThreshold: 2, Checks: []core.HealthCheck{{ID: "http", Kind: "http", Path: "/ready"}}}
	rounds := 0
	result, err := RunHealthPolicy(context.Background(), policy, func(_ context.Context, c core.HealthCheck) HealthObservation {
		if c.Kind == "container" {
			rounds++
			return HealthObservation{Passed: rounds == 1}
		}
		return HealthObservation{Passed: rounds > 1}
	})
	if err == nil || result.State != "failed" || result.Checks[0].Failures != 2 || rounds != 3 {
		t.Fatalf("old passing check reused: %+v %v", result, err)
	}
}

func TestHealthHelmUnmatchedContainerRejectsRenderedCandidate(t *testing.T) {
	policy, _ := core.NormalizeHealthPolicy(core.HealthPolicy{Checks: []core.HealthCheck{{ID: "process", Kind: "container", Service: "missing"}}})
	renderer := helmDeploymentMetadata{HealthPolicy: policy}
	_, err := renderer.Run(bytes.NewBufferString("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app\nspec:\n  template:\n    spec:\n      containers:\n      - name: web\n        image: example/web\n"))
	if err == nil || !strings.Contains(err.Error(), "process") {
		t.Fatal("unknown container became a passing health check")
	}
}
