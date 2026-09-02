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

func TestLoadAcceptsPublicHTTPSOrigin(t *testing.T) {
	t.Setenv("DISPATCH_PUBLIC_URL", "https://dispatch.example.com/")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.PublicURL != "https://dispatch.example.com" {
		t.Fatalf("unexpected public URL %q", config.PublicURL)
	}
}

func TestLoadRejectsUnsafePublicURL(t *testing.T) {
	for _, value := range []string{
		"http://dispatch.example.com",
		"https://user@dispatch.example.com",
		"https://dispatch.example.com/path",
		"https://dispatch.example.com?next=other",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("DISPATCH_PUBLIC_URL", value)
			if _, err := Load(); err == nil {
				t.Fatalf("expected %q to fail", value)
			}
		})
	}
}
