package core

import "testing"

func TestHealthPolicyAlwaysIncludesWorkloadReadiness(t *testing.T) {
	policy, err := NormalizeHealthPolicy(HealthPolicy{Checks: []HealthCheck{{ID: "certificate", Kind: "tls", Scope: "certificate"}}})
	if err != nil || len(policy.Checks) != 2 || policy.Checks[0].Kind != "container" {
		t.Fatalf("required readiness missing %+v %v", policy, err)
	}
	repeated, err := NormalizeHealthPolicy(policy)
	if err != nil || len(repeated.Checks) != 2 {
		t.Fatal("normalized policy cannot be saved again")
	}
	for _, input := range []HealthPolicy{{TimeoutSeconds: -1}, {IntervalSeconds: 61}, {FailureThreshold: 21}, {Checks: []HealthCheck{{ID: "secret\nvalue", Kind: "container"}}}, {Checks: []HealthCheck{{ID: "route", Kind: "http", Path: "//other.test"}}}, {Checks: []HealthCheck{{ID: "tls", Kind: "tls", Scope: "workload"}}}} {
		if _, err := NormalizeHealthPolicy(input); err == nil {
			t.Fatalf("invalid policy accepted: %+v", input)
		}
	}
}
