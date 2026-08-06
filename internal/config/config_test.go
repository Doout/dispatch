package config

import "testing"

func TestLoadAllowsFirstRunSetupOnNetworkAddress(t *testing.T) {
	t.Setenv("DISPATCH_ADDR", "0.0.0.0:8080")
	t.Setenv("DISPATCH_ADMIN_TOKEN", "")
	t.Setenv("DISPATCH_ADMIN_USERNAME", "")
	t.Setenv("DISPATCH_ADMIN_PASSWORD", "")
	if _, err := Load(); err != nil {
		t.Fatalf("expected first-run configuration to load: %v", err)
	}
}

func TestLoadRequiresCompleteEnvironmentCredentials(t *testing.T) {
	t.Setenv("DISPATCH_ADMIN_USERNAME", "admin")
	t.Setenv("DISPATCH_ADMIN_PASSWORD", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected incomplete environment credentials to fail")
	}
}

func TestLoadRejectsWeakEnvironmentPassword(t *testing.T) {
	t.Setenv("DISPATCH_ADMIN_USERNAME", "admin")
	t.Setenv("DISPATCH_ADMIN_PASSWORD", "short")
	if _, err := Load(); err == nil {
		t.Fatal("expected weak environment password to fail")
	}
}
