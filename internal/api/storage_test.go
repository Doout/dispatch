package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
)

type storageTestBackend struct {
	observation core.StorageObservation
	absent      bool
	fail        bool
	deletes     int
}

func (b *storageTestBackend) Inspect(context.Context, core.Server) ([]core.StorageObservation, error) {
	if b.absent {
		return []core.StorageObservation{}, nil
	}
	return []core.StorageObservation{b.observation}, nil
}
func (b *storageTestBackend) Delete(context.Context, core.Server, core.StorageResource) error {
	b.deletes++
	if b.fail {
		return errors.New("interrupted runtime deletion")
	}
	b.absent = true
	return nil
}

func storageAPIFixture(t *testing.T) (*API, *storageTestBackend, core.StorageResource) {
	t.Helper()
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, err := a.store.ListApps(ctx)
	if err != nil || len(apps) == 0 {
		t.Fatal(err)
	}
	app := apps[0]
	b := &storageTestBackend{observation: core.StorageObservation{Resource: core.StorageResource{Kind: "docker_volume", Name: "orders-data", Identity: "first", Evidence: "owned-labels"}, Labels: map[string]string{"dispatch.app": app.ID}}}
	a.deploy.Storage.Backend = b
	server, err := a.store.GetServer(ctx, app.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.deploy.Storage.Refresh(ctx, server); err != nil {
		t.Fatal(err)
	}
	item, err := a.store.GetStorage(ctx, deploy.StorageID(server.ID, "docker_volume", "", "orders-data"))
	if err != nil {
		t.Fatal(err)
	}
	return a, b, item
}

func TestStorageReviewedDeletionAndPolicyAudit(t *testing.T) {
	a, b, item := storageAPIFixture(t)
	ctx := context.Background()
	path := "/api/v1/storage/" + item.ID
	call := func(r *http.Request, want int) {
		t.Helper()
		rr := httptest.NewRecorder()
		a.ServeHTTP(rr, r)
		if rr.Code != want {
			t.Fatalf("got %d want %d: %s", rr.Code, want, rr.Body.String())
		}
	}
	call(tokenRequest("DELETE", path, strings.NewReader(`{}`)), 422)
	call(confirmedTokenRequest(t, a, "DELETE", path, nil), 409)
	if b.deletes != 0 {
		t.Fatal("retained storage reached runtime mutation")
	}
	serviceRequestTest(t, a, "PUT", path+"/policy", map[string]any{"revision": item.Revision, "policy": "destroy"}, 200)
	stale := confirmedTokenRequest(t, a, "DELETE", path, nil)
	b.observation.Resource.Consumers = []core.StorageConsumer{{ID: "new-consumer", Active: true}}
	call(stale, 409)
	if b.deletes != 0 {
		t.Fatal("new consumer bypassed current review")
	}
	b.observation.Resource.Consumers = nil
	call(confirmedTokenRequest(t, a, "DELETE", path, nil), 204)
	got, _ := a.store.GetStorage(ctx, item.ID)
	if got.State != "absent" || got.OwnerID != item.OwnerID || b.deletes != 1 {
		t.Fatalf("deletion lost inventory: %+v", got)
	}
	call(confirmedTokenRequest(t, a, "DELETE", path, nil), 204)
	if b.deletes != 1 {
		t.Fatal("absent storage was deleted twice")
	}
	audits, err := a.store.(operationsStore).ListAuditEvents(ctx, core.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		if audit.ResourceID == item.ID && audit.ConfirmedAction == "delete" && audit.Outcome == "succeeded" {
			found = audit.ActorID != "" && audit.ConfirmedPolicy == "destroy" && audit.ConfirmedVersion != ""
		}
	}
	if !found {
		t.Fatal("data deletion did not record actor, policy, confirmation and outcome")
	}
}

func TestStorageInterruptedDeletionRequiresReinspection(t *testing.T) {
	a, b, item := storageAPIFixture(t)
	ctx := context.Background()
	path := "/api/v1/storage/" + item.ID
	if err := a.store.SetStoragePolicy(ctx, item.ID, item.Revision, "destroy"); err != nil {
		t.Fatal(err)
	}
	b.fail = true
	rr := httptest.NewRecorder()
	a.ServeHTTP(rr, confirmedTokenRequest(t, a, "DELETE", path, nil))
	if rr.Code != 409 {
		t.Fatalf("failure %d %s", rr.Code, rr.Body.String())
	}
	got, _ := a.store.GetStorage(ctx, item.ID)
	if got.State != "inaccessible" {
		t.Fatal("uncertain deletion became absent")
	}
	b.fail = false
	rr = httptest.NewRecorder()
	a.ServeHTTP(rr, confirmedTokenRequest(t, a, "DELETE", path, nil))
	if rr.Code != 204 || b.deletes != 2 {
		t.Fatalf("retry %d %s", rr.Code, rr.Body.String())
	}
}

func TestStorageInventoryAndMutationProjectScope(t *testing.T) {
	a, _, item := storageAPIFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	item.Consumers = []core.StorageConsumer{{ID: "other-secret-consumer", Mount: "/other-project/private", Active: true}}
	item.Mounts = []string{"/other-project/private"}
	if err := a.store.ObserveStorage(ctx, item); err != nil {
		t.Fatal(err)
	}
	user := core.User{ID: "storage-viewer", Username: "storage-viewer", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: "storage-viewer-role", PrincipalType: core.PrincipalUser, PrincipalID: user.ID, ScopeType: core.ScopeProject, ScopeID: item.ProjectID, Role: core.RoleViewer, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	other := item
	other.ID = "other-project-storage"
	other.ProjectID = "invisible-project"
	other.Name = "other-secret-name"
	if err := a.store.ObserveStorage(ctx, other); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{{"GET", "/api/v1/storage", "", 200}, {"GET", "/api/v1/storage/" + other.ID, "", 403}, {"PUT", "/api/v1/storage/" + item.ID + "/policy", `{"revision":1,"policy":"destroy"}`, 403}, {"POST", "/api/v1/storage/" + item.ID + "/delete-preview", "{}", 403}} {
		rr := httptest.NewRecorder()
		req := tokenRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Impersonate-User", user.ID)
		a.ServeHTTP(rr, req)
		if rr.Code != tc.want {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, rr.Code, rr.Body.String())
		}
		if tc.want == 200 {
			var rows []core.StorageResource
			if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].ID != item.ID || strings.Contains(rr.Body.String(), "other-secret") || strings.Contains(rr.Body.String(), "/other-project") {
				t.Fatal("cross-project inventory leak")
			}
			if len(rows[0].Consumers) != 1 || !rows[0].Consumers[0].Active {
				t.Fatal("consumer protection information was lost")
			}
		}
	}
}
