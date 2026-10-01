package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/doout/dispatch/internal/core"
)

// HealthObservation deliberately excludes raw errors, command output, response
// bodies and headers. A probe can only emit bounded, non-secret evidence.
type HealthObservation struct {
	Passed      bool
	Unavailable bool
	HTTPStatus  int
}
type HealthProbe func(context.Context, core.HealthCheck) HealthObservation
type HealthReporter func(context.Context, string, core.DeploymentHealth) error
type healthReporterKey struct{}

func WithHealthReporter(ctx context.Context, report HealthReporter) context.Context {
	return context.WithValue(ctx, healthReporterKey{}, report)
}

// RunHealthPolicy uses the same bounded failure and cancellation semantics for
// every runtime. A successful earlier check cannot skip a later route/TLS gate.
func RunHealthPolicy(ctx context.Context, policy core.HealthPolicy, probe HealthProbe) (core.DeploymentHealth, error) {
	policy, err := core.NormalizeHealthPolicy(policy)
	result := core.DeploymentHealth{Policy: policy, State: "pending", Checks: []core.HealthCheckResult{}}
	if err != nil {
		result.State = "unavailable"
		return result, err
	}
	started := time.Now().UTC()
	result.StartedAt = &started
	for _, c := range policy.Checks {
		result.Checks = append(result.Checks, core.HealthCheckResult{Check: c, State: "pending", Message: "Check has not run."})
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(policy.TimeoutSeconds)*time.Second)
	defer cancel()
	finish := func(state string, err error) (core.DeploymentHealth, error) {
		now := time.Now().UTC()
		result.FinishedAt = &now
		result.State = state
		return result, err
	}
	for {
		allPassed := true
		for i := range result.Checks {
			check := &result.Checks[i]
			if err := ctx.Err(); err != nil {
				state := "cancelled"
				message := "Health evaluation cancelled."
				if errors.Is(err, context.DeadlineExceeded) {
					state = "timeout"
					message = fmt.Sprintf("Health policy exceeded its %d second timeout.", policy.TimeoutSeconds)
				}
				check.State, check.Message = state, message
				return finish(state, err)
			}
			attempt, stop := context.WithTimeout(ctx, time.Duration(policy.CheckTimeoutSeconds)*time.Second)
			observation := HealthObservation{Unavailable: true}
			if probe != nil {
				observation = probe(attempt, check.Check)
			}
			attemptErr := attempt.Err()
			stop()
			check.Attempts++
			if ctx.Err() != nil {
				state := "cancelled"
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					state = "timeout"
				}
				check.State, check.Message = state, "Health evaluation ended before this check completed."
				return finish(state, ctx.Err())
			}
			if attemptErr != nil {
				observation = HealthObservation{}
				check.Message = fmt.Sprintf("Check exceeded its %d second timeout.", policy.CheckTimeoutSeconds)
			}
			if observation.Unavailable {
				check.State, check.Message = "unavailable", "Check is unavailable on this target; promotion is blocked."
				return finish("unavailable", fmt.Errorf("health check %s is unavailable", check.Check.ID))
			}
			if observation.HTTPStatus >= 100 && observation.HTTPStatus <= 599 {
				check.HTTPStatus = observation.HTTPStatus
			}
			if observation.Passed {
				check.State, check.Message, check.Failures = "passed", "Check passed.", 0
				continue
			}
			allPassed = false
			check.State = "checking"
			check.Failures++
			if attemptErr == nil {
				check.Message = fmt.Sprintf("Check failed %d of %d allowed attempts.", check.Failures, policy.FailureThreshold)
			}
			if check.Failures >= policy.FailureThreshold {
				check.State = "failed"
				return finish("failed", fmt.Errorf("health check %s reached its failure threshold of %d", check.Check.ID, policy.FailureThreshold))
			}
		}
		if allPassed {
			return finish("passed", nil)
		}
		timer := time.NewTimer(time.Duration(policy.IntervalSeconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

// ReportDeploymentHealth persists final evidence before a runtime may promote.
// Reporting survives execution cancellation with its own bounded write context.
func ReportDeploymentHealth(ctx context.Context, d core.Deployment, result core.DeploymentHealth) error {
	report, _ := ctx.Value(healthReporterKey{}).(HealthReporter)
	if report == nil {
		return nil
	}
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return report(save, d.ID, result)
}

func evaluateDeploymentHealth(ctx context.Context, d core.Deployment, app core.App, probe HealthProbe, progress Progress) error {
	if err := progress(core.DeploymentChecking, "Evaluating workload, route and certificate health policy"); err != nil {
		return err
	}
	policy := d.Health.Policy
	if policy.TimeoutSeconds == 0 {
		policy = app.HealthPolicy
	}
	result, err := RunHealthPolicy(ctx, policy, probe)
	if saveErr := reportDeploymentHealth(ctx, d, result); saveErr != nil {
		return errors.New("cannot persist deployment health evidence; promotion is blocked")
	}
	if err != nil {
		return err
	}
	return progress(core.DeploymentChecking, "Required health checks passed; optional QA remains separate")
}

func reportDeploymentHealth(ctx context.Context, d core.Deployment, result core.DeploymentHealth) error {
	return ReportDeploymentHealth(ctx, d, result)
}
