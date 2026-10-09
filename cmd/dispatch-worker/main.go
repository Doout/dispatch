package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/agentruntime"
	"github.com/doout/dispatch/internal/edgeclient"
	"github.com/doout/dispatch/internal/runtimeclient"
	"github.com/doout/dispatch/internal/workflowrunner"
	"github.com/doout/dispatch/internal/workflowrunnerexec"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == "execute" {
		return execute(ctx, os.Stdin, os.Stdout)
	}
	controller := strings.TrimRight(os.Getenv("DISPATCH_EDGE_CONTROLLER_URL"), "/")
	node := os.Getenv("DISPATCH_EDGE_NODE_ID")
	parsed, err := url.Parse(controller)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return errors.New("DISPATCH_EDGE_CONTROLLER_URL must be an HTTPS origin")
	}
	state := env("DISPATCH_WORKER_STATE", "/var/lib/dispatch-worker")
	identity, err := edgeclient.LoadIdentity(filepath.Join(state, "identity.json"), controller, node)
	if err != nil {
		return err
	}
	executor := workflowrunner.ContainerExecutor{Image: os.Getenv("DISPATCH_WORKER_IMAGE"), Mode: env("DISPATCH_WORKER_MODE", "tenant"), Network: os.Getenv("DISPATCH_WORKER_NETWORK"), AllowDocker: os.Getenv("DISPATCH_WORKER_DOCKER") == "true", MemoryMiB: integer("DISPATCH_WORKER_MEMORY_MIB", 1024), DiskMiB: integer("DISPATCH_WORKER_DISK_MIB", 2048), PIDs: integer("DISPATCH_WORKER_PIDS", 256), CPUs: 1}
	if value := os.Getenv("DISPATCH_WORKER_CPUS"); value != "" {
		executor.CPUs, err = strconv.ParseFloat(value, 64)
		if err != nil {
			return errors.New("DISPATCH_WORKER_CPUS must be a number")
		}
	}
	if err = executor.Validate(); err != nil {
		return err
	}
	worker, err := workflowrunner.OpenWorker(filepath.Join(state, "operations"), node, executor.Execute)
	if err != nil {
		return err
	}
	defer worker.Close()
	client := &workflowrunner.Client{Identity: identity, Enrollment: os.Getenv("DISPATCH_EDGE_TOKEN"), Mode: executor.Mode, Docker: executor.AllowDocker, HTTP: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	var runtimeWorker *agentruntime.Worker
	if executor.AllowDocker {
		runtimeWorker, err = agentruntime.Open(filepath.Join(state, "runtime"), node, agentruntime.Options{RoutingDirectory: os.Getenv("DISPATCH_AGENT_ROUTING_DIRECTORY")})
		if err != nil {
			return err
		}
		defer runtimeWorker.Close()
	}
	typed := runtimeclient.Client{HTTP: client.HTTP, Controller: controller, Node: node, Token: client.Token, ResponseError: func(_ string, response *http.Response) error {
		if response.StatusCode == 401 {
			client.InvalidateSession()
		}
		return fmt.Errorf("runtime worker request returned HTTP %d", response.StatusCode)
	}}
	for ctx.Err() == nil {
		busy, err := client.Poll(ctx, worker)
		if err == nil && !busy && runtimeWorker != nil {
			job, leaseErr := typed.Lease(ctx)
			err = leaseErr
			if err == nil && job != nil {
				busy = true
				err = typed.Execute(ctx, runtimeWorker, *job)
			}
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("worker connection unavailable", "error", err)
		}
		if busy && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}
func execute(ctx context.Context, input io.Reader, output io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(input, workflowrunner.MaxPayload+1))
	if err != nil {
		return err
	}
	defer clear(raw)
	if len(raw) > workflowrunner.MaxPayload {
		return errors.New("worker request exceeds the payload limit")
	}
	var request workflowrunner.Request
	if err = json.Unmarshal(raw, &request); err != nil {
		return err
	}
	if err = request.Validate(); err != nil {
		return err
	}
	workspace, err := os.MkdirTemp("/work", "execution-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)
	var mu sync.Mutex
	encoder := json.NewEncoder(output)
	var writeErr error
	progress := func(log string) {
		mu.Lock()
		defer mu.Unlock()
		if writeErr == nil {
			writeErr = encoder.Encode(workflowrunner.ExecutionEvent{Log: &log})
		}
	}
	result := workflowrunnerexec.Execute(ctx, request, workspace, progress)
	mu.Lock()
	defer mu.Unlock()
	if writeErr != nil {
		return writeErr
	}
	return encoder.Encode(workflowrunner.ExecutionEvent{Result: &result})
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func integer(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		fmt.Fprintln(os.Stderr, key+" must be an integer")
		return 0
	}
	return n
}
