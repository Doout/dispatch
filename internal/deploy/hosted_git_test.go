package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestHostedGitIgnoresControllerCredentialsAndConfiguration(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required")
	}
	directory := t.TempDir()
	config := filepath.Join(directory, "controller.gitconfig")
	if err = os.WriteFile(config, []byte("[credential]\nhelper = controller-private-helper\n[url \"file:///controller/private/\"]\ninsteadOf = https://tenant.example/\n[http]\nextraHeader = Authorization: Bearer controller-private-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"GIT_CONFIG_GLOBAL": config, "GIT_CONFIG_SYSTEM": config, "GIT_SSH_COMMAND": "controller-private-command",
		"SSH_AUTH_SOCK": "controller-private-agent", "DISPATCH_GIT_TOKEN": "controller-private-token", "HTTPS_PROXY": "http://controller-private-proxy",
		"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "credential.helper", "GIT_CONFIG_VALUE_0": "controller-private-helper",
	} {
		t.Setenv(key, value)
	}
	environment, cleanup, err := PrepareIsolatedGitEnvironment(core.App{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, value := range environment {
		if strings.Contains(value, "controller-private") || strings.Contains(value, directory) {
			t.Fatalf("inherited controller setting: %q", value)
		}
	}
	command := exec.Command(git, "config", "--list", "--show-origin")
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated git config: %v %s", err, output)
	}
	if strings.Contains(string(output), "controller-private") || strings.Contains(string(output), "insteadof") || strings.Contains(string(output), "extraheader") {
		t.Fatalf("controller configuration leaked: %s", output)
	}
	command = exec.Command(git, "ls-remote", "file://"+directory)
	command.Env = environment
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "transport 'file' not allowed") {
		t.Fatalf("local Git transport enabled: %v %s", err, output)
	}
}

func TestHostedGitUsesOnlyExplicitTenantCredential(t *testing.T) {
	environment, cleanup, err := PrepareIsolatedGitEnvironment(core.App{SourceAuthType: SourceAuthGitHubToken, SourceCredential: "tenant-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !strings.Contains(strings.Join(environment, "\n"), "Authorization: Bearer tenant-token") {
		t.Fatal("tenant token missing")
	}
	_, clean, err := PrepareIsolatedGitEnvironment(core.App{SourceAuthType: SourceAuthGitHubToken})
	clean()
	if err == nil {
		t.Fatal("empty explicit credential accepted")
	}
	environment, cleanupKey, err := PrepareIsolatedGitEnvironment(core.App{SourceAuthType: SourceAuthSSHKey, SourceCredential: "PRIVATE TENANT KEY"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupKey()
	var home, ssh string
	for _, value := range environment {
		if strings.HasPrefix(value, "HOME=") {
			home = strings.TrimPrefix(value, "HOME=")
		}
		if strings.HasPrefix(value, "GIT_SSH_COMMAND=") {
			ssh = strings.TrimPrefix(value, "GIT_SSH_COMMAND=")
		}
	}
	keyPath := filepath.Join(home, "identity")
	key, err := os.ReadFile(keyPath)
	if err != nil || string(key) != "PRIVATE TENANT KEY\n" {
		t.Fatal("tenant key not materialized", err)
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("tenant key permissions", err)
	}
	for _, flag := range []string{"-F /dev/null", "IdentityAgent=none", "IdentitiesOnly=yes", "GlobalKnownHostsFile=/dev/null", "ProxyCommand=none", "ProxyJump=none", "PermitLocalCommand=no"} {
		if !strings.Contains(ssh, flag) {
			t.Fatalf("SSH isolation missing %s", flag)
		}
	}
	cleanupKey()
	if _, err = os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("tenant key persisted after cleanup", err)
	}
}
