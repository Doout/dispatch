package remoteruntime

import (
	"encoding/json"
	"errors"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
)

func (r Request) ValidateHealth(health *core.DeploymentHealth) error {
	if health == nil {
		return nil
	}
	if r.Operation != runtimecontract.Deploy && r.Operation != runtimecontract.Rollback {
		return errors.New("health evidence is only accepted for deployment operations")
	}
	expected := r.Deployment.Health.Policy
	if expected.TimeoutSeconds == 0 {
		expected = r.Application.HealthPolicy
	}
	expected, err := core.NormalizeHealthPolicy(expected)
	if err != nil {
		return err
	}
	a, _ := json.Marshal(expected)
	b, _ := json.Marshal(health.Policy)
	if string(a) != string(b) || health.Simulated || len(health.Checks) > len(expected.Checks) {
		return errors.New("remote health evidence does not match the captured policy")
	}
	switch health.State {
	case "passed", "failed", "cancelled", "timeout", "unavailable":
	default:
		return errors.New("remote health result is incomplete")
	}
	checks := map[string]core.HealthCheck{}
	for _, check := range expected.Checks {
		checks[check.ID] = check
	}
	seen := map[string]bool{}
	for _, result := range health.Checks {
		check, ok := checks[result.Check.ID]
		if !ok || seen[result.Check.ID] || check != result.Check || result.Attempts < 0 || result.Attempts > 10000 || result.Failures < 0 || result.Failures > expected.FailureThreshold || result.HTTPStatus != 0 && (result.HTTPStatus < 100 || result.HTTPStatus > 599) {
			return errors.New("remote check evidence is invalid")
		}
		seen[result.Check.ID] = true
		switch result.State {
		case "pending", "checking", "passed", "failed", "cancelled", "timeout", "unavailable":
		default:
			return errors.New("remote check state is invalid")
		}
		if health.State == "passed" && result.State != "passed" {
			return errors.New("remote health passed with an incomplete check")
		}
	}
	if health.State == "passed" && len(seen) != len(expected.Checks) {
		return errors.New("remote health passed without all required checks")
	}
	return nil
}
