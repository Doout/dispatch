package workflow

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"golang.org/x/crypto/ssh"
)

func TestDockerBuilderPoolCapacityAndCancellation(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "builders.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		if err := data.CreateServer(ctx, core.Server{ID: id, Name: id, Runtime: core.ServerRuntimeBuilder, Address: "ssh://build@example.com", State: "ready", Builder: &core.BuilderServerConfig{MaxConcurrent: 1}, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{Store: data}
	first, releaseFirst, err := service.acquireDockerBuilder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, releaseSecond, err := service.acquireDockerBuilder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("both jobs acquired %s", first.ID)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if _, _, err := service.acquireDockerBuilder(waitCtx); err != context.DeadlineExceeded {
		t.Fatalf("full pool should wait for capacity: %v", err)
	}
	releaseFirst()
	third, releaseThird, err := service.acquireDockerBuilder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if third.ID != first.ID {
		t.Fatalf("released builder not reused: %s", third.ID)
	}
	releaseThird()
	releaseSecond()
}

func TestDockerBuilderPinsHostAndUsesSavedKey(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	fakeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeDir, "ssh"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	builder := core.Server{Address: "ssh://build@example.com:2222", Builder: &core.BuilderServerConfig{HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public)))}}
	env, cleanup, err := dockerBuilderEnvironmentForKey(builder, []byte("private-test-key"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var path string
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			path = strings.TrimPrefix(entry, "PATH=")
		}
	}
	directory := strings.Split(path, string(os.PathListSeparator))[0]
	known, err := os.ReadFile(filepath.Join(directory, "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(known), "[example.com]:2222 ssh-ed25519 ") {
		t.Fatalf("host key was not pinned: %s", known)
	}
	cmd := exec.Command(filepath.Join(directory, "ssh"), "-l", "build", "example.com", "docker", "system", "dial-stdio")
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "StrictHostKeyChecking=yes") || !strings.Contains(string(output), filepath.Join(directory, "identity")) {
		t.Fatalf("Docker SSH missed pinned credentials: %s", output)
	}
	cmd = exec.Command(filepath.Join(directory, "ssh"), "git@example.com")
	output, err = cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(output), "identity") {
		t.Fatalf("builder key leaked to unrelated SSH command: %s", output)
	}
}
