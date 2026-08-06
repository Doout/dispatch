package kubeconfig

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

const storedConfig = `apiVersion: v1
kind: Config
current-context: preview
contexts:
  - name: preview
    context:
      cluster: preview-cluster
      user: preview-user
clusters:
  - name: preview-cluster
    cluster:
      server: https://cluster.example.test
      certificate-authority: /outside/ca.crt
users:
  - name: preview-user
    user:
      token: test-token
`

func TestStoredKubeconfigRequiresAndMaterializesExternalCA(t *testing.T) {
	if _, err := ValidateStored([]byte(storedConfig), nil, ""); err == nil || !strings.Contains(err.Error(), "paste the CA") {
		t.Fatalf("expected external CA error, got %v", err)
	}
	ca := certificatePEM(t)
	contextName, err := ValidateStored([]byte(storedConfig), ca, "")
	if err != nil || contextName != "preview" {
		t.Fatalf("validate stored kubeconfig: context=%q err=%v", contextName, err)
	}
	prepared, cleanup, err := Prepare(core.KubernetesServerConfig{KubeconfigData: storedConfig, CertificateAuthorityData: string(ca), Context: contextName})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(prepared.KubeconfigPath)
	contents, err := os.ReadFile(prepared.KubeconfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "/outside/ca.crt") || !strings.Contains(string(contents), filepath.Join(directory, "ca.crt")) {
		t.Fatalf("CA path was not materialized: %s", contents)
	}
	for _, path := range []string{prepared.KubeconfigPath, filepath.Join(directory, "ca.crt")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("expected private file %s: info=%v err=%v", path, info, err)
		}
	}
	cleanup()
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("temporary kubeconfig directory was not removed: %v", err)
	}
}

func TestStoredKubeconfigRejectsExternalClientCredentials(t *testing.T) {
	config := strings.Replace(storedConfig, "token: test-token", "client-key: /outside/key.pem", 1)
	if _, err := ValidateStored([]byte(config), certificatePEM(t), ""); err == nil || !strings.Contains(err.Error(), "client key") {
		t.Fatalf("expected external client key error, got %v", err)
	}
}

func TestStoredKubeconfigUsesEmbeddedCAWithoutSeparateCertificate(t *testing.T) {
	embedded := strings.Replace(storedConfig, "certificate-authority: /outside/ca.crt", "certificate-authority-data: "+base64.StdEncoding.EncodeToString(certificatePEM(t)), 1)
	if contextName, err := ValidateStored([]byte(embedded), nil, ""); err != nil || contextName != "preview" {
		t.Fatalf("expected embedded CA to validate: context=%q err=%v", contextName, err)
	}
}

func certificatePEM(t *testing.T) []byte {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Dispatch test CA"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
