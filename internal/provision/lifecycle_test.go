package provision

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/doout/dispatch/internal/store"
)

type lifecycleFixture struct {
	m          *Manager
	data       *store.SQLStore
	path       string
	mockPath   string
	input      CreateInput
	adapter    *mock.Mock
	now        time.Time
	requests   []provider.CreateServerRequest
	loseReply  bool
	wrongOwner bool
	endpoint   *httptest.Server
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	m, path := managerFixture(t)
	m.Vault = m.Secrets.(*secretvalue.Resolver).Vault
	f := &lifecycleFixture{m: m, data: m.Store.(*store.SQLStore), path: path, now: time.Now().UTC()}
	m.Now = func() time.Time { return f.now }
	var err error
	f.mockPath = filepath.Join(t.TempDir(), "mock.json")
	f.adapter, err = mock.New(mock.Options{StateFile: f.mockPath})
	if err != nil {
		t.Fatal(err)
	}
	f.endpoint = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v1/servers" {
			var request provider.CreateServerRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			f.requests = append(f.requests, request)
			raw, _ := json.Marshal(request)
			r.Body = http.NoBody
			r.Body = readCloser{strings.NewReader(string(raw))}
			if f.loseReply {
				f.loseReply = false
				rec := httptest.NewRecorder()
				provider.Handler(f.adapter, "").ServeHTTP(rec, r)
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
		}
		if f.wrongOwner && r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/servers/") {
			rec := httptest.NewRecorder()
			provider.Handler(f.adapter, "").ServeHTTP(rec, r)
			if rec.Code == 200 {
				var resource provider.Server
				_ = json.Unmarshal(rec.Body.Bytes(), &resource)
				resource.Labels["dispatch.project"] = "foreign"
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resource)
				return
			}
		}
		provider.Handler(f.adapter, "").ServeHTTP(w, r)
	}))
	t.Cleanup(f.endpoint.Close)
	p, err := m.Register(context.Background(), Registration{Name: "Mock", Endpoint: f.endpoint.URL, Enabled: true, Capabilities: []string{provider.CapabilityCreate, provider.CapabilityInspect, provider.CapabilityDelete}})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.data.CreateProject(context.Background(), core.Project{ID: "project", Name: "Project", CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	if err = f.data.CreateSecret(context.Background(), core.Secret{ID: "ssh-key", Name: "SSH", Type: core.SecretTypeSSHPrivateKey, Source: core.SecretSourceLocal, PublicValue: "ssh-ed25519 public-fixture", CreatedAt: f.now, UpdatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	f.input = CreateInput{ProjectID: "project", ProviderID: p.ID, Name: "machine", Region: "mock-region", Size: "mock-small", Image: "mock-linux", Network: "mock-private", SSHKeySecretID: "ssh-key", Config: map[string]any{}}
	return f
}

type readCloser struct{ *strings.Reader }

func (readCloser) Close() error { return nil }
func (f *lifecycleFixture) accept(t *testing.T) (core.InfrastructureReview, Accepted) {
	t.Helper()
	r, err := f.m.ReviewCreate(context.Background(), f.input)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := f.m.AcceptCreate(context.Background(), "actor", Acceptance{ReviewID: r.ID, Digest: r.Digest, ConfirmName: r.Name, RequestKey: "request-key"})
	if err != nil {
		t.Fatal(err)
	}
	return r, accepted
}
func (f *lifecycleFixture) tick(t *testing.T) {
	t.Helper()
	f.now = f.now.Add(5 * time.Second)
	if _, err := f.m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func (f *lifecycleFixture) finish(t *testing.T, id string) core.InfrastructureOperation {
	t.Helper()
	for i := 0; i < 12; i++ {
		f.tick(t)
		op, err := f.data.GetInfrastructureOperation(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if op.State == "succeeded" || op.State == "unknown" || op.State == "paused" || op.State == "cancelled" {
			return op
		}
	}
	t.Fatal("operation did not finish")
	return core.InfrastructureOperation{}
}

func TestLifecycleLostReplyRestartFrozenSecretsAndIdempotentAcceptance(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	secretFixture(t, f.m, "field-secret", "frozen-secret")
	f.input.SecretRefs = map[string]string{"testLabel": "field-secret"}
	r, accepted := f.accept(t)
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "frozen-secret") || strings.Contains(string(raw), "EncryptedRequest") {
		t.Fatal("review leaked resolved secret")
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			duplicate, err := f.m.AcceptCreate(ctx, "actor", Acceptance{ReviewID: r.ID, Digest: r.Digest, ConfirmName: r.Name, RequestKey: "request-key"})
			if err != nil || duplicate.Operation.ID != accepted.Operation.ID {
				t.Errorf("duplicate acceptance: %v", err)
			}
		}()
	}
	wg.Wait()
	if _, err := f.m.AcceptCreate(ctx, "actor", Acceptance{ReviewID: r.ID, Digest: r.Digest, ConfirmName: r.Name, RequestKey: "different-key"}); !errors.Is(err, store.ErrInfrastructureChanged) {
		t.Fatal("review reused with a second key", err)
	}
	f.loseReply = true
	f.tick(t)
	secretFixture(t, f.m, "field-secret", "rotated-secret")
	reopened, err := store.Open(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	f.data = reopened
	f.m.Store = reopened
	f.m.Secrets = secretvalue.New(reopened, f.m.Vault)
	f.adapter, err = mock.New(mock.Options{StateFile: f.mockPath})
	if err != nil {
		t.Fatal(err)
	}
	op := f.finish(t, accepted.Operation.ID)
	if op.State != "succeeded" || len(f.requests) != 2 {
		t.Fatalf("restart: %#v, requests %d", op, len(f.requests))
	}
	for _, req := range f.requests {
		if req.ProviderConfig["testLabel"] != "frozen-secret" {
			t.Fatal("rotation changed reviewed input")
		}
	}
	server, _ := f.data.GetManagedServer(ctx, accepted.Server.ID)
	if server.AllocationState != "allocated" || server.RuntimeState == "ready" {
		t.Fatalf("readiness: %#v", server)
	}
	if _, err = f.data.GetServer(ctx, server.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("provider address became a deployment target before enrollment")
	}
}
func TestLifecycleCancelUnknownOwnershipAndAdoption(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	_, accepted := f.accept(t)
	f.loseReply = true
	f.tick(t)
	if err := f.m.ChangeOperation(ctx, accepted.Operation.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	op := f.finish(t, accepted.Operation.ID)
	if op.State != "unknown" || len(f.requests) != 1 {
		t.Fatalf("cancel resumed create: %#v %d", op, len(f.requests))
	}
	// Complete the provider's original asynchronous operation independently.
	remote, err := f.adapter.CreateServer(ctx, op.ID, f.requests[0])
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		remote, err = f.adapter.Operation(ctx, remote.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	server, _ := f.data.GetManagedServer(ctx, accepted.Server.ID)
	in := Adoption{ResourceID: remote.ResourceID, Revision: server.Revision, ConfirmName: server.Name}
	f.wrongOwner = true
	if _, err = f.m.Adopt(ctx, server.ID, in); err == nil {
		t.Fatal("adopted foreign resource")
	}
	f.wrongOwner = false
	server, err = f.m.Adopt(ctx, server.ID, in)
	if err != nil || server.AllocationState != "allocated" {
		t.Fatalf("adopt: %#v %v", server, err)
	}
	f.now = f.now.Add(time.Hour)
	readiness, err := f.m.GetManaged(ctx, server.ID)
	if err != nil || readiness.WaitState != "waiting" || readiness.LatestOperation.State != "adopted" || readiness.Deployable {
		t.Fatal("adopted allocation retained the original operation deadline", readiness, err)
	}
	f.tick(t)
	if len(f.requests) != 1 {
		t.Fatal("adopted cancelled creation resumed")
	}
}
func TestLifecycleCancellationBeforeSubmitAndExpiredNeverCreates(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "expired"}[expired], func(t *testing.T) {
			f := newLifecycleFixture(t)
			_, accepted := f.accept(t)
			if expired {
				f.now = f.now.Add(time.Hour)
			} else if err := f.m.ChangeOperation(context.Background(), accepted.Operation.ID, "cancel"); err != nil {
				t.Fatal(err)
			}
			f.tick(t)
			if len(f.requests) != 0 {
				t.Fatal("cancelled/expired intent created a machine")
			}
			if err := f.m.ChangeOperation(context.Background(), accepted.Operation.ID, "retry"); err == nil {
				t.Fatal("cancelled/expired operation retried")
			}
		})
	}
}
func TestLifecycleAgentReadinessThenProtectedDeletion(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	_, accepted := f.accept(t)
	if op := f.finish(t, accepted.Operation.ID); op.State != "succeeded" {
		t.Fatal(op)
	}
	enrollment, err := f.m.Enrollment(ctx, accepted.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err = edge.Enroll(ctx, f.data, enrollment.NodeID, enrollment.Token, base64.RawURLEncoding.EncodeToString(pub), f.now); err != nil {
		t.Fatal(err)
	}
	if err = f.m.RefreshReadiness(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.data.GetServer(ctx, accepted.Server.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("enrollment without typed runtime published target")
	}
	node, err := f.data.GetPrivateNetwork(ctx, enrollment.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	now := f.now
	node.LastVerifiedAt = &now
	node.Details = map[string]string{"runtimeVersion": remoteruntime.APIVersion, "runtimeCapabilities": "deploy,inspect", "runtimeCheckedAt": f.now.Format(time.RFC3339Nano)}
	if err = f.data.UpdatePrivateNetwork(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err = f.m.RefreshReadiness(ctx); err != nil {
		t.Fatal(err)
	}
	target, err := f.data.GetServer(ctx, accepted.Server.ID)
	if err != nil || target.AgentNodeID != enrollment.NodeID {
		t.Fatalf("ready: %#v %v", target, err)
	}
	target.Address = "other"
	if err = f.data.UpdateServer(ctx, target); err == nil {
		t.Fatal("managed identity changed outside provider lifecycle")
	}
	if err = f.data.DeleteServer(ctx, target.ID); err == nil {
		t.Fatal("managed target deleted without its provider resource")
	}
	if err = f.data.CreateApp(ctx, core.App{ID: "application", ProjectID: "project", ServerID: target.ID, Name: "App", CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.ReviewDelete(ctx, target.ID); !errors.Is(err, store.ErrInfrastructureProtected) {
		t.Fatal("deleted machine with an application", err)
	}
	if err = f.data.DeleteApp(ctx, "application"); err != nil {
		t.Fatal(err)
	}
	review, err := f.m.ReviewDelete(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	deletion, err := f.m.Delete(ctx, target.ID, "actor", Acceptance{Digest: review.Digest, ConfirmName: target.Name, RequestKey: "delete-key"})
	if err != nil {
		t.Fatal(err)
	}
	readiness, err := f.m.GetManaged(ctx, target.ID)
	if err != nil || readiness.WaitState != "deleting" || readiness.Deployable || readiness.LatestOperation.ID != deletion.Operation.ID {
		t.Fatal("deletion remained deployable", readiness, err)
	}
	if err = f.data.CreateApp(ctx, core.App{ID: "late-app", ProjectID: "project", ServerID: target.ID, Name: "Late", CreatedAt: f.now}); err == nil {
		t.Fatal("application admitted after deletion")
	}
	op := f.finish(t, deletion.Operation.ID)
	if op.State != "succeeded" {
		t.Fatalf("delete: %#v", op)
	}
	if err = f.m.RefreshReadiness(ctx); err != nil {
		t.Fatal(err)
	}
	credential, _ := f.data.GetEdgeCredential(ctx, enrollment.NodeID)
	if !credential.Revoked {
		t.Fatal("deleted agent still enrolled")
	}
	readiness, err = f.m.GetManaged(ctx, target.ID)
	if err != nil || readiness.WaitState != "deleted" || readiness.Deployable {
		t.Fatal("deleted server remained ready", readiness, err)
	}
	unchanged, err := f.data.GetEdgeCredential(ctx, enrollment.NodeID)
	if err != nil || unchanged.Generation != credential.Generation {
		t.Fatal("status read changed a deleted node's credential generation", unchanged, err)
	}
	if _, err = f.data.GetServer(ctx, target.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("deleted target remains")
	}
}

func TestLifecycleInconsistentProviderEvidenceStopsMutation(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	_, accepted := f.accept(t)
	f.wrongOwner = true
	op := f.finish(t, accepted.Operation.ID)
	if op.State != "unknown" || op.ErrorCode != "ownership" {
		t.Fatalf("inconsistent evidence accepted: %#v", op)
	}
	server, err := f.data.GetManagedServer(ctx, accepted.Server.ID)
	if err != nil || server.ResourceID != "" || server.AllocationState != "unknown" {
		t.Fatal("unverified resource became owned", server, err)
	}
	if err = f.m.ChangeOperation(ctx, op.ID, "retry"); err == nil {
		t.Fatal("unknown ownership permitted blind retry")
	}
	f.tick(t)
	if len(f.requests) != 1 {
		t.Fatal("inconsistent provider created duplicate resource")
	}
}
