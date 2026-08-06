package main

import (
	"net"
	"path/filepath"
	"testing"
)

func TestDockerSocketAvailable(t *testing.T) {
	if dockerSocketAvailable(filepath.Join(t.TempDir(), "missing.sock")) {
		t.Fatal("missing socket must not be available")
	}

	path := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if !dockerSocketAvailable(path) {
		t.Fatal("Unix socket must be discovered")
	}
}
