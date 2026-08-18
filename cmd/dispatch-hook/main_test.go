package main

import (
	"path/filepath"
	"testing"

	"github.com/doout/dispatch/internal/hookresult"
)

func TestRunSetsOutputAndHelmValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	t.Setenv("DISPATCH_RESULT_FILE", path)
	if err := run([]string{"output", "set", "backendImage", "registry/service:preview-847"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"helm", "set", "images.backend.tag", "preview-847"}); err != nil {
		t.Fatal(err)
	}
	result, err := hookresult.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["backendImage"] != "registry/service:preview-847" {
		t.Fatalf("unexpected result: %#v", result)
	}
}
