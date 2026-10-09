package hosted

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/tenancy"
)

func TestHostedRuntimeInitializationDoesNotBlockOtherTenants(t *testing.T) {
	f := newHostedFixture(t)
	a := f.tenant(t, "alpha", "owner-a")
	b := f.tenant(t, "bravo", "owner-b")
	if _, err := f.server.runtime(a); err != nil {
		t.Fatal(err)
	}
	token := f.session(t, "owner-a", tenancy.TenantAudience(a.ID))
	entered, release := make(chan struct{}), make(chan struct{})
	var opens atomic.Int32
	original := f.server.OpenRuntime
	f.server.OpenRuntime = func(ctx context.Context, tenant tenancy.Tenant, auth *api.HostedAuth) (*TenantRuntime, error) {
		if tenant.ID == b.ID {
			if opens.Add(1) == 1 {
				close(entered)
			}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return original(ctx, tenant, auth)
	}
	t.Cleanup(func() { close(release) })
	type result struct {
		runtime *TenantRuntime
		err     error
	}
	results := make(chan result, 8)
	for range 8 {
		go func() {
			runtime, err := f.server.runtime(b)
			results <- result{runtime, err}
		}()
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("tenant initialization did not start")
	}

	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodGet, "https://alpha."+f.server.Config.RootDomain+"/api/v1/private", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		f.server.ServeHTTP(recorder, request)
		response <- recorder
	}()
	select {
	case w := <-response:
		if w.Code != http.StatusOK {
			t.Fatalf("cached tenant request: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening another tenant blocked an existing tenant request")
	}

	// A membership change made during initialization must appear in the local
	// account store before the membership endpoint reports success.
	if err := f.catalog.SetMembership(context.Background(), "owner-b", tenancy.Membership{TenantID: b.ID, UserID: "member", Role: tenancy.RoleMember}); err != nil {
		t.Fatal(err)
	}
	synced := make(chan error, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "https://"+f.server.Config.RootDomain+"/", nil)
		synced <- f.server.syncMember(request, b.ID, "member")
	}()
	release <- struct{}{}
	// All callers use the same initialization, so one release is sufficient.
	var initialized *TenantRuntime
	for range 8 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if initialized != nil && result.runtime != initialized {
				t.Fatal("concurrent callers received different tenant runtimes")
			}
			initialized = result.runtime
		case <-time.After(30 * time.Second):
			t.Fatal("tenant initialization did not finish")
		}
	}
	if opens.Load() != 1 {
		t.Fatalf("opened the same tenant %d times", opens.Load())
	}
	select {
	case err := <-synced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("membership synchronization did not finish")
	}
	member, err := initialized.Store.GetUser(context.Background(), "member")
	if err != nil || member.State != core.UserStateActive || member.SystemRole != core.UserRoleMember {
		t.Fatalf("membership was not synchronized: %+v, %v", member, err)
	}
}

func TestHostedCloseCancelsRuntimeInitialization(t *testing.T) {
	f := newHostedFixture(t)
	tenant := f.tenant(t, "closing", "owner-a")
	entered := make(chan struct{})
	var closed atomic.Int32
	f.server.OpenRuntime = func(ctx context.Context, _ tenancy.Tenant, _ *api.HostedAuth) (*TenantRuntime, error) {
		close(entered)
		<-ctx.Done()
		return &TenantRuntime{Close: func() { closed.Add(1) }}, nil
	}
	result := make(chan error, 1)
	go func() { _, err := f.server.runtime(tenant); result <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("tenant initialization did not start")
	}
	done := make(chan struct{}, 2)
	for range 2 {
		go func() { f.server.Close(); done <- struct{}{} }()
	}
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("shutdown did not cancel tenant initialization")
		}
	}
	if err := <-result; !errors.Is(err, errServerClosed) {
		t.Fatalf("initialization returned %v after shutdown", err)
	}
	if closed.Load() != 1 {
		t.Fatalf("partially initialized runtime closed %d times", closed.Load())
	}
	if _, err := f.server.runtime(tenant); !errors.Is(err, errServerClosed) {
		t.Fatalf("closed server accepted runtime request: %v", err)
	}
}

func TestHostedRuntimeInitializationFailureCanRetry(t *testing.T) {
	f := newHostedFixture(t)
	tenant := f.tenant(t, "retry", "owner-a")
	original := f.server.OpenRuntime
	f.server.OpenRuntime = func(context.Context, tenancy.Tenant, *api.HostedAuth) (*TenantRuntime, error) {
		return nil, errors.New("database unavailable")
	}
	if _, err := f.server.runtime(tenant); err == nil {
		t.Fatal("expected initialization failure")
	}
	f.server.OpenRuntime = original
	if _, err := f.server.runtime(tenant); err != nil {
		t.Fatalf("failed initialization prevented retry: %v", err)
	}
}
