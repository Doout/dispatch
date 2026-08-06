package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateKubeconfigResolvesCurrentOrSelectedContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	contents := `apiVersion: v1
kind: Config
current-context: current
contexts:
  - name: current
    context: {cluster: one, user: one}
  - name: selected
    context: {cluster: two, user: two}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if contextName, err := validateKubeconfig(path, ""); err != nil || contextName != "current" {
		t.Fatalf("expected current context, got context=%q err=%v", contextName, err)
	}
	if contextName, err := validateKubeconfig(path, "selected"); err != nil || contextName != "selected" {
		t.Fatalf("expected selected context, got context=%q err=%v", contextName, err)
	}
}

func TestValidateKubeconfigRejectsUnsafeOrIncompleteInput(t *testing.T) {
	directory := t.TempDir()
	malformed := filepath.Join(directory, "malformed")
	if err := os.WriteFile(malformed, []byte("contexts: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	missingContext := filepath.Join(directory, "missing-context")
	if err := os.WriteFile(missingContext, []byte("current-context: absent\ncontexts:\n  - name: other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		path, context, message string
	}{
		"relative path":   {"config", "", "absolute"},
		"unreadable path": {filepath.Join(directory, "missing"), "", "not readable"},
		"malformed":       {malformed, "", "parse kubeconfig"},
		"missing context": {missingContext, "", "does not exist"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateKubeconfig(test.path, test.context)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("expected %q error, got %v", test.message, err)
			}
		})
	}
}
