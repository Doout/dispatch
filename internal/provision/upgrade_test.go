package provision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/provider"
)

type upgradeTransport func(*http.Request) (*http.Response, error)

func (f upgradeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestProviderUpgradeRollbackRetainsOwnedResources(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	_, accepted := f.accept(t)
	if op := f.finish(t, accepted.Operation.ID); op.State != "succeeded" {
		t.Fatal(op)
	}
	original, err := f.data.GetInfrastructureProvider(ctx, f.input.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	mode := "incompatible"
	f.m.HTTPClient = &http.Client{Transport: upgradeTransport(func(r *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(r)
		if err != nil || r.URL.Path != "/v1/manifest" {
			return response, err
		}
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if mode == "unavailable" {
			response.StatusCode = 503
			response.Header.Set("Content-Type", "application/problem+json")
			raw, _ = json.Marshal(provider.NewProblem(503, "Unavailable", "public fixture"))
		} else {
			var manifest provider.Manifest
			_ = json.Unmarshal(raw, &manifest)
			if mode == "incompatible" {
				manifest.APIVersion = "dispatch.provider/v999"
			}
			if mode == "upgrade" {
				manifest.Version += "-upgrade"
			}
			raw, _ = json.Marshal(manifest)
		}
		response.Body = io.NopCloser(strings.NewReader(string(raw)))
		response.ContentLength = int64(len(raw))
		return response, nil
	})}
	p, err := f.m.Verify(ctx, original.ID, original.Revision)
	if err != nil || p.Enabled || p.State != "failed" || !strings.Contains(p.LastError, provider.APIVersion) {
		t.Fatalf("incompatible upgrade: %#v %v", p, err)
	}
	if p.ManifestDigest != original.ManifestDigest {
		t.Fatal("failed upgrade erased original manifest")
	}
	if _, _, err = f.m.Adapter(ctx, p.ID, provider.CapabilityCreate, ""); !errors.Is(err, ErrDisabled) {
		t.Fatal("incompatible provider enabled mutations", err)
	}
	server, err := f.data.GetManagedServer(ctx, accepted.Server.ID)
	if err != nil || server.ResourceID == "" {
		t.Fatal("upgrade lost owned resource", err)
	}
	history, err := f.data.ListInfrastructureOperations(ctx, server.ID)
	if err != nil || len(history) != 1 || history[0].ID != accepted.Operation.ID {
		t.Fatal("upgrade lost history", err)
	}
	mode = "unavailable"
	p, err = f.m.Verify(ctx, p.ID, p.Revision)
	if err != nil || p.State != "failed" || p.ManifestDigest != original.ManifestDigest {
		t.Fatal("unavailable upgrade erased registration", err)
	}
	mode = ""
	p, err = f.m.Verify(ctx, p.ID, p.Revision)
	if err != nil || p.State != "disabled" || p.ManifestDigest != original.ManifestDigest {
		t.Fatal("rollback did not recover preserved registration", err)
	}
	p, err = f.m.Update(ctx, p.ID, Registration{Name: p.Name, Endpoint: p.Endpoint, Enabled: true, Capabilities: p.Capabilities, Revision: p.Revision})
	if err != nil || p.State != "ready" {
		t.Fatal("rollback re-enable failed", err)
	}
	// A new reviewed create must stay pinned across a later schema/version upgrade.
	pendingReview, err := f.m.ReviewCreate(ctx, f.input)
	if err != nil {
		t.Fatal(err)
	}
	mode = "upgrade"
	p, err = f.m.Verify(ctx, p.ID, p.Revision)
	if err != nil || p.ManifestDigest == original.ManifestDigest {
		t.Fatal("compatible upgrade was not pinned", err)
	}
	if _, err = f.m.AcceptCreate(ctx, "actor", Acceptance{ReviewID: pendingReview.ID, Digest: pendingReview.Digest, ConfirmName: pendingReview.Name, RequestKey: "old-review-key"}); err == nil {
		t.Fatal("stale creation review accepted after upgrade")
	}
	// Already-owned resources remain operable under the approved v1 contract.
	review, err := f.m.ReviewDelete(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := f.m.Delete(ctx, server.ID, "actor", Acceptance{Digest: review.Digest, ConfirmName: server.Name, RequestKey: "upgrade-delete"})
	if err != nil {
		t.Fatal(err)
	}
	if op := f.finish(t, deleted.Operation.ID); op.State != "succeeded" {
		t.Fatalf("compatible upgrade broke owned deletion: %#v", op)
	}
}

func TestProviderUnavailablePausesOriginalIntent(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	_, accepted := f.accept(t)
	f.m.HTTPClient = &http.Client{Transport: upgradeTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection lost") })}
	for i := 0; i < 8; i++ {
		f.now = f.now.Add(time.Minute)
		f.tick(t)
	}
	op, err := f.data.GetInfrastructureOperation(ctx, accepted.Operation.ID)
	if err != nil || op.State != "paused" || op.Attempts != 8 || len(f.requests) != 0 {
		t.Fatalf("unbounded or mutating retry: %#v %v", op, err)
	}
	if err = f.m.ChangeOperation(ctx, op.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	f.tick(t)
	op, err = f.data.GetInfrastructureOperation(ctx, op.ID)
	if err != nil || op.State != "cancelled" || len(f.requests) != 0 {
		t.Fatal("paused cancellation resumed allocation", op, err)
	}
}
