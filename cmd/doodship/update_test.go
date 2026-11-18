package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpdateExecutable(t *testing.T) {
	fixtures := t.TempDir()
	asset := fmt.Sprintf("doodship-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	put(t, filepath.Join(fixtures, "doodship"), "new binary", 0755)
	if output, err := exec.Command("tar", "-czf", filepath.Join(fixtures, asset), "-C", fixtures, "doodship").CombinedOutput(); err != nil {
		t.Fatalf("create release fixture: %s: %v", output, err)
	}
	archive, err := os.ReadFile(filepath.Join(fixtures, asset))
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(fixtures, "SHA256SUMS"), fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), asset), 0644)
	put(t, filepath.Join(fixtures, "curl"), `#!/bin/sh
for url do :; done
case "$url" in
  https://github.com/Doout/dispatch/releases/latest) printf 'https://github.com/Doout/dispatch/releases/tag/v9.0.0' ;;
  https://github.com/Doout/dispatch/releases/download/v9.0.0/*) cat "$FIXTURES/${url##*/}" ;;
  *) exit 1 ;;
esac
`, 0755)
	t.Setenv("PATH", fixtures+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FIXTURES", fixtures)
	otherInstall := filepath.Join(t.TempDir(), "other")
	t.Setenv("DOODSHIP_BIN_DIR", otherInstall)

	dir := t.TempDir()
	target := filepath.Join(dir, "custom name")
	link := filepath.Join(t.TempDir(), "doodship")
	put(t, target, "old binary", 0755)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	update := func() error {
		output.Reset()
		return updateExecutable(t.Context(), link, &output, &output)
	}
	checkTarget := func(want string) {
		t.Helper()
		data, err := os.ReadFile(target)
		if err != nil || string(data) != want {
			t.Fatalf("installed binary = %q, %v", data, err)
		}
	}

	put(t, filepath.Join(fixtures, asset), "broken", 0644)
	if err := update(); err == nil {
		t.Fatal("update accepted a corrupt release")
	}
	checkTarget("old binary")

	put(t, filepath.Join(fixtures, asset), string(archive), 0644)
	if err := update(); err != nil {
		t.Fatalf("update: %v: %s", err, &output)
	}
	checkTarget("new binary")
	if _, err := os.Readlink(link); err != nil {
		t.Fatal("update replaced the symlink", err)
	}
	if _, err := os.Stat(otherInstall); !os.IsNotExist(err) {
		t.Fatal("update used the bootstrap install directory", err)
	}

	// With the current version installed, only the release tag lookup is needed.
	original := releaseVersion
	releaseVersion = "v9.0.0"
	t.Cleanup(func() { releaseVersion = original })
	if err := os.Remove(filepath.Join(fixtures, asset)); err != nil {
		t.Fatal(err)
	}
	if err := update(); err != nil || !strings.Contains(output.String(), "already up to date") {
		t.Fatalf("repeat update: %v: %s", err, &output)
	}
}
