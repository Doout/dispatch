package remoteruntime

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
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/store"
)

func brokerFixture(t *testing.T) (*Broker, Request) {
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
	project := core.Project{ID: "project", Name: "Project", CreatedAt: now}
	server := core.Server{ID: "server", Name: "Server", Address: "agent:node", Runtime: core.ServerRuntimeDocker, AgentNodeID: "node", CreatedAt: now}
	app := core.App{ID: "app", ProjectID: project.ID, ServerID: server.ID, Name: "App", BuildType: core.BuildTypeCompose, ComposeContent: "services: {}", CreatedAt: now}
	for _, err := range []error{
		data.CreateProject(ctx, project), data.CreateServer(ctx, server), data.CreateApp(ctx, app),
		data.CreatePrivateNetwork(ctx, core.PrivateNetwork{ID: "node", Name: "Node", Driver: "dispatch_agent", State: "ready", CreatedAt: now, UpdatedAt: now}),
		data.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: "node", EnrollmentHash: "enroll", EnrollmentExpiresAt: now.Add(time.Hour), UpdatedAt: now}),
		data.EnrollEdgeCredential(ctx, "node", "enroll", "public-key", "session", now, now.Add(time.Hour)),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	key := filepath.Join(t.TempDir(), "key")
	if err = os.WriteFile(key, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(key)
	if err != nil {
		t.Fatal(err)
	}
	request := NewRequest(runtimecontract.Deploy, core.Deployment{ID: "deployment", AppID: app.ID, SpecDigest: "spec", CommitSHA: strings.Repeat("a", 40)}, app, server)
	request.Inputs.SourceCredential = "private-source-value"
	return &Broker{Store: data, Vault: vault}, request
}

func TestBrokerEncryptedReceiptOwnershipAndLeaseReplay(t *testing.T) {
	b, r := brokerFixture(t)
	ctx := context.Background()
	j, err := b.Submit(ctx, "operation", r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(j.EncryptedRequest, r.Inputs.SourceCredential) {
		t.Fatal("plaintext credential stored")
	}
	if _, err = b.Submit(ctx, "operation", r); err != nil {
		t.Fatal(err)
	}
	changed := r
	changed.LogLimit++
	if _, err = b.Submit(ctx, "operation", changed); !errors.Is(err, store.ErrRuntimeJobConflict) {
		t.Fatalf("changed operation reused: %v", err)
	}
	if job, err := b.Lease(ctx, "other-node"); err != nil || job != nil {
		t.Fatalf("cross-node lease: %v %v", job, err)
	}
	lease, err := b.Lease(ctx, "node")
	if err != nil || lease == nil {
		t.Fatalf("lease: %v", err)
	}
	if lease.Request.Inputs.SourceCredential != r.Inputs.SourceCredential {
		t.Fatal("agent input lost")
	}
	if err = b.Complete(ctx, "other-node", j.ID, Completion{LeaseToken: lease.LeaseToken, Result: Result{State: "succeeded"}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other node completed job: %v", err)
	}
	if _, err = b.Renew(ctx, "node", j.ID, Heartbeat{LeaseToken: "stale"}); !errors.Is(err, store.ErrRuntimeJobConflict) {
		t.Fatalf("stale heartbeat: %v", err)
	}
	now := time.Now().UTC()
	replay, err := b.Store.LeaseRuntimeJob(ctx, "node", now.Add(LeaseDuration+time.Second), LeaseDuration)
	if err != nil || replay == nil || replay.ID != lease.ID || replay.LeaseToken == lease.LeaseToken {
		t.Fatalf("lost lease did not reoffer same operation: %v %v", replay, err)
	}
	if err = b.Complete(ctx, "node", j.ID, Completion{LeaseToken: lease.LeaseToken, Result: Result{State: "succeeded"}}); !errors.Is(err, store.ErrRuntimeJobConflict) {
		t.Fatalf("stale completion: %v", err)
	}
	if err = b.Complete(ctx, "node", j.ID, Completion{LeaseToken: replay.LeaseToken, Result: Result{State: "succeeded", Logs: "log " + r.Inputs.SourceCredential, Resources: []Resource{{ApplicationID: r.Application.ID}}}}); err != nil {
		t.Fatal(err)
	}
	result, err := b.Wait(ctx, j.ID, nil)
	if err != nil || strings.Contains(result.Logs, r.Inputs.SourceCredential) {
		t.Fatalf("result: %#v %v", result, err)
	}
	saved, _ := b.Store.GetRuntimeJob(ctx, j.ID)
	if saved.EncryptedRequest != "" || saved.EncryptedResult == "" {
		t.Fatal("completed operation retained request credentials")
	}
}

func TestBrokerCancellationExpirationAndEnrollmentRotation(t *testing.T) {
	for _, kind := range []string{"cancel", "expire", "rotate", "move"} {
		t.Run(kind, func(t *testing.T) {
			b, r := brokerFixture(t)
			ctx := context.Background()
			j, err := b.Submit(ctx, "operation", r)
			if err != nil {
				t.Fatal(err)
			}
			leased, err := b.Lease(ctx, "node")
			if err != nil || leased == nil {
				t.Fatal(err)
			}
			switch kind {
			case "cancel":
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				_, err = b.Wait(cancelCtx, j.ID, nil)
				// Wait must persist cancellation even if its first store read is cancelled.
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
				saved, _ := b.Store.GetRuntimeJob(ctx, j.ID)
				if !saved.CancelRequested {
					t.Fatal("cancellation was not persisted")
				}
			case "expire":
				if err = b.Store.ExpireRuntimeJobs(ctx, j.ExpiresAt.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				saved, _ := b.Store.GetRuntimeJob(ctx, j.ID)
				if saved.State != "unknown" || saved.EncryptedRequest != "" {
					t.Fatal("disconnected credentials were not expired")
				}
			case "rotate":
				data := b.Store.(*store.SQLStore)
				if err = data.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: "node", EnrollmentHash: "next", EnrollmentExpiresAt: time.Now().Add(time.Hour), UpdatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
				if err = data.EnrollEdgeCredential(ctx, "node", "next", "new-public", "new-session", time.Now(), time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				job, err := b.Store.LeaseRuntimeJob(ctx, "node", time.Now().Add(LeaseDuration+time.Second), LeaseDuration)
				if err != nil || job != nil {
					t.Fatalf("new enrollment received old credentials: %v %v", job, err)
				}
				if _, err = b.Renew(ctx, "node", j.ID, Heartbeat{LeaseToken: leased.LeaseToken}); !errors.Is(err, store.ErrRuntimeJobConflict) {
					t.Fatalf("rotated job renewed: %v", err)
				}
			case "move":
				data := b.Store.(*store.SQLStore)
				app := r.App()
				if err = data.CreateServer(ctx, core.Server{ID: "different", Name: "Different", Address: "local", Runtime: core.ServerRuntimeDocker, CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
				app.ServerID = "different"
				if err = data.UpdateApp(ctx, app); err != nil {
					t.Fatal(err)
				}
				if _, err = b.Renew(ctx, "node", j.ID, Heartbeat{LeaseToken: leased.LeaseToken}); !errors.Is(err, store.ErrRuntimeJobConflict) {
					t.Fatalf("moved app renewed: %v", err)
				}
			}
		})
	}
}

func TestBrokerUnknownOutcomeBlocksNewMutationUntilInspected(t *testing.T) {
	b, r := brokerFixture(t)
	ctx := context.Background()
	job, err := b.Submit(ctx, "original", r)
	if err != nil {
		t.Fatal(err)
	}
	leased, err := b.Lease(ctx, "node")
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Complete(ctx, "node", job.ID, Completion{LeaseToken: leased.LeaseToken, Result: Result{State: "unknown", Code: runtimecontract.Uncertain}}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Submit(ctx, "replacement", r); !errors.Is(err, store.ErrRuntimeJobConflict) {
		t.Fatalf("mutation accepted after uncertain result: %v", err)
	}
	if err = b.Store.AcknowledgeRuntimeJob(ctx, r.Application.ID, job.ID, "missing-inspection", time.Now().Add(time.Minute)); !errors.Is(err, store.ErrRuntimeJobConflict) {
		t.Fatalf("unknown result acknowledged without inspection: %v", err)
	}
	inspect := r
	inspect.Operation = runtimecontract.Inspect
	inspect.Deployment = core.Deployment{}
	inspection, err := b.Submit(ctx, "inspection", inspect)
	if err != nil {
		t.Fatal(err)
	}
	leased, err = b.Lease(ctx, "node")
	if err != nil || leased == nil || leased.ID != inspection.ID {
		t.Fatalf("inspection not available: %v %v", leased, err)
	}
	if err = b.Complete(ctx, "node", inspection.ID, Completion{LeaseToken: leased.LeaseToken, Result: Result{State: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if err = b.Store.AcknowledgeRuntimeJob(ctx, r.Application.ID, job.ID, inspection.ID, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Submit(ctx, "replacement", r); err != nil {
		t.Fatalf("reviewed recovery blocked: %v", err)
	}
}

func TestBrokerServiceProvisionOwnershipAndEncryptedOutputs(t *testing.T) {
	b, r := brokerFixture(t)
	ctx := context.Background()
	data := b.Store.(*store.SQLStore)
	run := core.ServiceProvisionRun{ID: "service-run", TemplateID: "template", ProjectID: r.Application.ProjectID, ServiceName: "database", State: "running", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: r.Server.ID}, CreatedAt: time.Now()}
	if err := data.CreateServiceProvisionRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	request := NewServiceRequest(core.ServiceProvisionRequest{Run: run, ServiceType: "postgresql", Inputs: map[string]string{"password": "secret-input"}}, core.DockerServiceProvision{ServerRef: r.Server.ID}, r.Server)
	job, err := b.Submit(ctx, "service-operation", request)
	if err != nil {
		t.Fatal(err)
	}
	leased, err := b.Lease(ctx, r.Server.AgentNodeID)
	if err != nil || leased == nil {
		t.Fatalf("service lease %v", err)
	}
	if err = b.Complete(ctx, r.Server.AgentNodeID, job.ID, Completion{LeaseToken: leased.LeaseToken, Result: Result{State: "succeeded", ServiceOutputs: map[string]string{"password": "generated-password"}}}); err != nil {
		t.Fatal(err)
	}
	result, err := b.Wait(ctx, job.ID, nil)
	if err != nil || result.ServiceOutputs["password"] != "generated-password" {
		t.Fatalf("service outputs lost: %v", err)
	}
	saved, _ := b.Store.GetRuntimeJob(ctx, job.ID)
	if strings.Contains(saved.EncryptedResult, "generated-password") || saved.EncryptedRequest != "" {
		t.Fatal("service credentials were persisted as plaintext")
	}
	request.Service.Request.Run.ProjectID = "another-project"
	if _, err = b.Submit(ctx, "wrong-service", request); err == nil {
		t.Fatal("cross-project service accepted")
	}
}
