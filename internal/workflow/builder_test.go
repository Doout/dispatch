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

func TestLocalDockerBuilderSharesPoolAndCapacity(t *testing.T) {
	runtime, _ := schedulerFixture(t)
	ctx := context.Background()
	local, err := runtime.service.Store.ReconcileLocalDockerServer(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	first, release, err := runtime.service.acquireDockerBuilder(ctx)
	if err != nil || first.ID != local.ID {
		t.Fatalf("local builder was not selected: %+v %v", first, err)
	}
	defer release()
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, _, err := runtime.service.acquireDockerBuilder(waitCtx); err != context.DeadlineExceeded {
		t.Fatalf("local capacity was not enforced: %v", err)
	}
	remote := core.Server{ID: "remote", Name: "remote", Runtime: core.ServerRuntimeBuilder, Address: "ssh://build@example.com", State: "ready", Builder: &core.BuilderServerConfig{MaxConcurrent: 1}, CreatedAt: time.Now()}
	if err := runtime.service.Store.CreateServer(ctx, remote); err != nil {
		t.Fatal(err)
	}
	next, releaseNext, err := runtime.service.acquireDockerBuilder(ctx)
	if err != nil || next.ID != remote.ID {
		t.Fatalf("busy local builder blocked remote capacity: %+v %v", next, err)
	}
	releaseNext()
	if _, err := runtime.service.Store.ReconcileLocalDockerServer(ctx, false); err != nil {
		t.Fatal(err)
	}
	next, releaseNext, err = runtime.service.acquireDockerBuilder(ctx)
	if err != nil || next.ID != remote.ID {
		t.Fatalf("unavailable local builder was selected: %+v %v", next, err)
	}
	releaseNext()
	remote.State = "unavailable"
	if err := runtime.service.Store.UpdateServer(ctx, remote); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.service.acquireDockerBuilder(ctx); err == nil || !strings.Contains(err.Error(), "no Docker builders") {
		t.Fatalf("unavailable builders must not receive work: %v", err)
	}
}

func TestLocalDockerBuilderJobUsesSocketWithoutSSH(t *testing.T) {
	runtime, resource := schedulerFixture(t)
	ctx := context.Background()
	if _, err := runtime.service.Store.ReconcileLocalDockerServer(ctx, true); err != nil {
		t.Fatal(err)
	}
	// Neither inherited Docker settings nor a job secret may redirect this builder.
	t.Setenv("DOCKER_HOST", "ssh://wrong-host")
	runtime.service.LocalDockerSocket = "/run/custom docker.sock"
	job := JobSpec{Builder: "docker", Run: `printf '%s\n' "$DOCKER_HOST" "$DOCKER_CONTEXT" "$DOCKER_TLS_VERIFY" "$DOCKER_CERT_PATH"`}
	_, log, err := runtime.runJobCommand(ctx, job, map[string]string{"DOCKER_CONTEXT": "remote", "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": "/wrong/certs"}, func(string) {})
	if err != nil || !strings.Contains(log, "unix:///run/custom docker.sock\n\n\n\n") || !strings.Contains(log, "Using Docker builder local-docker") {
		t.Fatalf("job did not use the local socket: %s %v", log, err)
	}
	// Preview review must include local builder identity without an SSH secret.
	doc := Document{Spec: &ApplicationSpec{Jobs: map[string]JobSpec{"build": job}}}
	scopes, _, err := runtime.service.previewCredentialScope(ctx, runtime.source, doc)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, scope := range scopes {
		if strings.HasPrefix(scope, "builder:") && scope != "builder:docker" {
			found = true
		}
	}
	if !found {
		t.Fatalf("local builder missing from preview review for %s: %v", resource.ID, scopes)
	}
}

func TestDockerBuilderCapacityExcludesOtherTargets(t *testing.T) {
	for _, server := range []core.Server{
		{Runtime: core.ServerRuntimeDocker, Address: "remote", AgentMode: "ssh"},
		{Runtime: core.ServerRuntimeDocker, Address: "local", AgentMode: "local", AgentNodeID: "worker"},
		{Runtime: core.ServerRuntimeKubernetes, Address: "local", AgentMode: "local"},
		{Runtime: core.ServerRuntimeBuilder},
	} {
		if server.DockerBuilderCapacity() != 0 {
			t.Fatalf("non-builder entered pool: %+v", server)
		}
	}
}

func TestLocalDockerBuilderIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_TEST_LOCAL_BUILDER") != "1" {
		t.Skip("set DISPATCH_TEST_LOCAL_BUILDER=1 to build an image with local Docker")
	}
	runtime, resource := schedulerFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := runtime.service.Store.ReconcileLocalDockerServer(ctx, true); err != nil {
		t.Fatal(err)
	}
	tag := "dispatch-local-builder-test:" + strings.ToLower(resource.ID)
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", tag).Run() })
	if err := os.WriteFile(filepath.Join(runtime.root, "Dockerfile"), []byte("FROM scratch\nLABEL dispatch.local-builder-test=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, log, err := runtime.runJobCommand(ctx, JobSpec{Builder: "docker", Run: "docker build -t " + shellEscape(tag) + " . && docker image inspect " + shellEscape(tag)}, nil, func(string) {})
	if err != nil || !strings.Contains(log, "Using Docker builder local-docker") || !strings.Contains(log, "true") {
		t.Fatalf("local image build failed: %s %v", log, err)
	}
}
