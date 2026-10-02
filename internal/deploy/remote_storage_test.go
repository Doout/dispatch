package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/store"
)

type forbiddenLocalStorage struct{ t *testing.T }

func (b forbiddenLocalStorage) Inspect(context.Context, core.Server) ([]core.StorageObservation, error) {
	b.t.Fatal("enrolled target used controller inventory")
	return nil, nil
}
func (b forbiddenLocalStorage) Delete(context.Context, core.Server, core.StorageResource) error {
	b.t.Fatal("enrolled target deleted controller data")
	return nil
}

func remoteStorageFixture(t *testing.T) (*remoteruntime.Broker, core.App, core.Server) {
	t.Helper()
	ctx := context.Background()
	data, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	server := core.Server{ID: "server", Name: "Target", Address: "local", Runtime: core.ServerRuntimeDocker, AgentNodeID: "node", CreatedAt: now}
	app := core.App{ID: "app", ProjectID: "project", ServerID: server.ID, Name: "App", BuildType: core.BuildTypeCompose, ComposeContent: "services: {}", CreatedAt: now}
	for _, err := range []error{
		data.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: now}), data.CreateServer(ctx, server), data.CreateApp(ctx, app),
		data.CreatePrivateNetwork(ctx, core.PrivateNetwork{ID: "node", Name: "Node", Driver: "dispatch_agent", State: "ready", CreatedAt: now, UpdatedAt: now}),
		data.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: "node", EnrollmentHash: "enroll", EnrollmentExpiresAt: now.Add(time.Hour), UpdatedAt: now}),
		data.EnrollEdgeCredential(ctx, "node", "enroll", "public-key", "session", now, now.Add(time.Hour)),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "key")
	if err = os.WriteFile(path, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return &remoteruntime.Broker{Store: data, Vault: vault}, app, server
}

func completeRemoteJob(ctx context.Context, b *remoteruntime.Broker, result remoteruntime.Result) <-chan error {
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			lease, err := b.Lease(ctx, "node")
			if err != nil {
				done <- err
				return
			}
			if lease != nil {
				done <- b.Complete(ctx, "node", lease.ID, remoteruntime.Completion{LeaseToken: lease.LeaseToken, Result: result})
				return
			}
			select {
			case <-ctx.Done():
				done <- ctx.Err()
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}

func TestRemoteStorageBackendFailsClosedWithoutBroker(t *testing.T) {
	backend := RemoteStorageBackend{Local: forbiddenLocalStorage{t}}
	server := core.Server{ID: "server", Address: "local", AgentNodeID: "node", Runtime: core.ServerRuntimeDocker}
	if _, err := backend.Inspect(context.Background(), server); err == nil {
		t.Fatal("missing broker accepted")
	}
	item := core.StorageResource{Ownership: "verified", Policy: "destroy", State: "present"}
	if err := backend.Delete(context.Background(), server, item); err == nil {
		t.Fatal("missing remote deletion accepted")
	}
}

func TestRemoteStorageBackendReturnsOnlyAgentInventory(t *testing.T) {
	b, _, server := remoteStorageFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	evidence := []core.StorageObservation{{Resource: core.StorageResource{Kind: "docker_volume", Name: "agent-data", Identity: "created:local", Evidence: "labels"}, Labels: map[string]string{"dispatch.app": "app"}}}
	done := completeRemoteJob(ctx, b, remoteruntime.Result{State: "succeeded", Storage: evidence})
	backend := RemoteStorageBackend{Local: forbiddenLocalStorage{t}, Broker: b}
	result, err := backend.Inspect(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Resource.Name != "agent-data" {
		t.Fatalf("wrong inventory: %#v", result)
	}
}

func TestRemoteFailedDeploymentRetainsHealthEvidence(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "deploy", true: "rollback"}[rollback], func(t *testing.T) {
			b, app, server := remoteStorageFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			policy, _ := core.NormalizeHealthPolicy(core.HealthPolicy{})
			health, _ := RunHealthPolicy(ctx, policy, func(context.Context, core.HealthCheck) HealthObservation { return HealthObservation{Passed: true} })
			deployment := core.Deployment{ID: "deployment", AppID: app.ID, SpecDigest: "spec", RuntimeReviewDigest: "reviewed", Health: core.DeploymentHealth{Policy: policy, State: "pending"}}
			done := completeRemoteJob(ctx, b, remoteruntime.Result{State: "failed", Code: runtimecontract.Failed, Message: "publication failed", Health: &health})
			saved := false
			ctx = WithHealthReporter(ctx, func(_ context.Context, id string, result core.DeploymentHealth) error {
				if id != deployment.ID || result.State != "passed" {
					return errors.New("incorrect health forwarded")
				}
				saved = true
				return nil
			})
			executor := RemoteExecutor{Broker: b}
			var err error
			if rollback {
				err = executor.RollbackRuntime(ctx, deployment, core.Deployment{ID: "source"}, app, server, nil)
			} else {
				err = executor.Deploy(ctx, deployment, app, server, nil)
			}
			if err == nil || !saved {
				t.Fatalf("failure lost health evidence: saved=%t err=%v", saved, err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeStorageRejectsEnrolledBindingWithoutAdapter(t *testing.T) {
	_, err := (RuntimeStorage{}).Inspect(context.Background(), core.Server{ID: "server", Runtime: core.ServerRuntimeDocker, Address: "local", AgentNodeID: "node"})
	if err == nil {
		t.Fatal("direct controller storage accepted an enrolled target")
	}
}
