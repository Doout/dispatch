package deploy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
)

type staticSecretReader struct{ secret core.Secret }

func (r staticSecretReader) GetSecret(context.Context, string) (core.Secret, error) {
	return r.secret, nil
}

type sourceCaptureExecutor struct{ credential string }

func (e *sourceCaptureExecutor) Deploy(_ context.Context, _ core.Deployment, app core.App, _ core.Server, _ Progress) error {
	e.credential = app.SourceCredential
	return nil
}

func TestSourceAuthExecutorDecryptsCredentialOnlyForExecution(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "master-key")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	secret := core.Secret{ID: "github", EncryptedValue: ""}
	secret.EncryptedValue, err = vault.Encrypt("secret:"+secret.ID, []byte("private-token"))
	if err != nil {
		t.Fatal(err)
	}
	next := &sourceCaptureExecutor{}
	executor := SourceAuthExecutor{Next: next, Secrets: staticSecretReader{secret: secret}, Vault: vault}
	app := core.App{SourceAuthType: SourceAuthGitHubToken, SourceCredentialID: secret.ID}
	if err := executor.Deploy(context.Background(), core.Deployment{}, app, core.Server{}, func(core.DeploymentState, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if next.credential != "private-token" {
		t.Fatalf("executor did not receive decrypted credential: %q", next.credential)
	}
	if app.SourceCredential != "" {
		t.Fatal("caller-owned application was mutated with plaintext")
	}
}

func TestSSHCredentialIsMaterializedPrivatelyAndRemoved(t *testing.T) {
	environment, cleanup, err := PrepareGitEnvironment(core.App{SourceAuthType: SourceAuthSSHKey, SourceCredential: "PRIVATE KEY"})
	if err != nil {
		t.Fatal(err)
	}
	command := ""
	for _, entry := range environment {
		if strings.HasPrefix(entry, "GIT_SSH_COMMAND=") {
			command = strings.TrimPrefix(entry, "GIT_SSH_COMMAND=")
		}
	}
	fields := strings.Fields(command)
	if len(fields) < 3 || fields[1] != "-i" {
		t.Fatalf("unexpected SSH command %q", command)
	}
	keyPath := fields[2]
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode is %o", info.Mode().Perm())
	}
	cleanup()
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("temporary private key was not removed: %v", err)
	}
}

func TestSourceAuthenticationRejectsIncompatibleTypedSecret(t *testing.T) {
	err := ValidateSourceCredentialType(SourceAuthSSHKey, core.SecretTypeGitHubToken)
	if err == nil || !strings.Contains(err.Error(), "SSH private key") {
		t.Fatalf("expected incompatible credential error, got %v", err)
	}
	if err := ValidateSourceCredentialType(SourceAuthSSHKey, core.SecretTypeText); err != nil {
		t.Fatalf("legacy text secrets should remain compatible: %v", err)
	}
}

func TestGitBackedHelmChartValidationRejectsTraversal(t *testing.T) {
	server := core.Server{Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/config"}}
	app := core.App{SourceRepo: "https://github.com/example/charts.git", HelmChart: "../private", BuildType: core.BuildTypeHelm}
	if err := ValidateHelmTarget(app, server); err == nil || !strings.Contains(err.Error(), "escapes repository") {
		t.Fatalf("expected chart traversal rejection, got %v", err)
	}
}
