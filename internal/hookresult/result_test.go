package hookresult

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentWritersPreserveNamedOutputsAndHelmValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	var group sync.WaitGroup
	for index := 0; index < 16; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			if err := SetOutput(path, fmt.Sprintf("image%d", index), fmt.Sprintf("registry/app:%d", index)); err != nil {
				t.Errorf("set output: %v", err)
			}
		}(index)
	}
	group.Add(1)
	go func() {
		defer group.Done()
		if err := SetHelmValue(path, "images.backend.tag", "preview-847"); err != nil {
			t.Errorf("set Helm value: %v", err)
		}
	}()
	group.Wait()

	result, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outputs) != 16 {
		t.Fatalf("outputs were lost during concurrent updates: %#v", result.Outputs)
	}
	images := result.Deployment.HelmValues["images"].(map[string]any)
	backend := images["backend"].(map[string]any)
	if backend["tag"] != "preview-847" {
		t.Fatalf("unexpected Helm result: %#v", result.Deployment.HelmValues)
	}
}

func TestOutputEnvironmentNormalizesNamesAndRejectsCollisions(t *testing.T) {
	environment, err := OutputEnvironment(map[string]string{"backendImage": "registry/service:tag", "image.digest": "sha256:123"})
	if err != nil {
		t.Fatal(err)
	}
	joined := "\n" + strings.Join(environment, "\n") + "\n"
	for _, expected := range []string{"DISPATCH_OUTPUT_BACKEND_IMAGE=registry/service:tag", "DISPATCH_OUTPUT_IMAGE_DIGEST=sha256:123"} {
		if !strings.Contains(joined, "\n"+expected+"\n") {
			t.Fatalf("missing %q in %#v", expected, environment)
		}
	}
	if _, err := OutputEnvironment(map[string]string{"image-tag": "one", "image.tag": "two"}); err == nil {
		t.Fatal("expected normalized environment collision")
	}
}

func TestResultRejectsUnknownVersionAndImmutableOutputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	if err := SetOutput(path, "namespace", "unsafe"); err == nil {
		t.Fatal("expected immutable output rejection")
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"outputs":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil || !strings.Contains(err.Error(), "unsupported hook result version") {
		t.Fatalf("unexpected version error: %v", err)
	}
}
