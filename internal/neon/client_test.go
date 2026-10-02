package neon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	t               *testing.T
	spec            Spec
	scope           Scope
	branch          *Branch
	marks           map[string]string
	endpoint        *Endpoint
	hasRole         bool
	parentRole      bool
	lostCreate      bool
	branchCreates   int
	endpointCreates int
	roleCreates     int
	deletes         int
	connectionReads int
}

func newFixture(t *testing.T) (*Client, *fixture) {
	t.Helper()
	f := &fixture{t: t, spec: Spec{ProjectID: "neon-project", ParentBranchID: "br-production", CredentialRef: "scoped-secret", Database: "database", DataMode: "schema-only"}, scope: Scope{ProjectID: "dispatch-project", RunID: "run", PreviewID: "preview-12", Generation: 1, ConfigDigest: strings.Repeat("a", 64)}}
	server := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	f.spec.Endpoint = server.URL + "/api/v2"
	client, err := New(f.spec, "private-provider-key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, f
}
func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer private-provider-key" {
		f.t.Error("provider credential missing")
		w.WriteHeader(401)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v2/projects/neon-project")
	send := func(value any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(value) }
	fail := func(status int) {
		w.WriteHeader(status)
		fmt.Fprint(w, `{"error":"private-provider-key must never escape"}`)
	}
	switch {
	case path == "/branches" && r.Method == "GET":
		branches := []Branch{}
		marks := map[string]annotation{}
		if f.branch != nil {
			branches = append(branches, *f.branch)
			marks[f.branch.ID] = annotation{f.marks}
		}
		send(map[string]any{"branches": branches, "annotations": marks, "pagination": map[string]any{}})
	case path == "/branches/br-production" && r.Method == "GET":
		send(branchResponse{Branch: Branch{ID: "br-production", ProjectID: f.spec.ProjectID, Name: "production", State: "ready", Default: true, Protected: true, InitSource: "parent-data"}})
	case strings.HasPrefix(path, "/branches/br-production/roles/") && r.Method == "GET":
		if f.parentRole {
			send(map[string]any{"role": role{BranchID: "br-production", Name: Role(f.scope)}})
		} else {
			fail(404)
		}
	case path == "/branches" && r.Method == "POST":
		f.branchCreates++
		var body struct {
			Branch struct {
				ParentID   string `json:"parent_id"`
				Name       string `json:"name"`
				InitSource string `json:"init_source"`
			} `json:"branch"`
			Annotations map[string]string `json:"annotation_value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Fatal(err)
		}
		if body.Branch.ParentID != f.spec.ParentBranchID {
			f.t.Error("wrong parent")
		}
		f.branch = &Branch{ID: "br-owned", ProjectID: f.spec.ProjectID, Name: body.Branch.Name, InitSource: body.Branch.InitSource, State: "ready"}
		f.marks = body.Annotations
		f.endpoint = &Endpoint{ID: "ep-owned", ProjectID: f.spec.ProjectID, BranchID: "br-owned", Type: "read_write", Host: "ep-owned.example.test", State: "idle"}
		if f.lostCreate {
			f.lostCreate = false
			fail(502)
			return
		}
		w.WriteHeader(201)
		send(branchResponse{Branch: *f.branch})
	case path == "/branches/br-owned" && r.Method == "GET":
		if f.branch == nil {
			fail(404)
		} else {
			send(branchResponse{Branch: *f.branch, Annotation: annotation{f.marks}})
		}
	case path == "/branches/br-owned" && r.Method == "DELETE":
		f.deletes++
		f.branch = nil
		w.WriteHeader(204)
	case path == "/branches/br-owned/endpoints" && r.Method == "GET":
		endpoints := []Endpoint{}
		if f.endpoint != nil {
			endpoints = append(endpoints, *f.endpoint)
		}
		send(map[string]any{"endpoints": endpoints})
	case path == "/endpoints" && r.Method == "POST":
		f.endpointCreates++
		f.endpoint = &Endpoint{ID: "ep-owned", ProjectID: f.spec.ProjectID, BranchID: "br-owned", Type: "read_write", Host: "ep-owned.example.test", State: "active"}
		send(map[string]any{"endpoint": f.endpoint})
	case path == "/branches/br-owned/roles/"+Role(f.scope) && r.Method == "GET":
		if f.hasRole {
			send(map[string]any{"role": role{BranchID: "br-owned", Name: Role(f.scope)}})
		} else {
			fail(404)
		}
	case path == "/branches/br-owned/roles" && r.Method == "POST":
		f.roleCreates++
		f.hasRole = true
		send(map[string]any{"role": role{BranchID: "br-owned", Name: Role(f.scope)}})
	case path == "/connection_uri" && r.Method == "GET":
		f.connectionReads++
		if r.URL.Query().Get("branch_id") != "br-owned" || r.URL.Query().Get("endpoint_id") != "ep-owned" || r.URL.Query().Get("role_name") != Role(f.scope) {
			f.t.Error("connection request fell back to provider defaults")
		}
		uri := url.URL{Scheme: "postgresql", Host: "ep-owned.example.test", Path: "/database", User: url.UserPassword(Role(f.scope), "branch-only-password"), RawQuery: "sslmode=require"}
		send(map[string]any{"uri": uri.String()})
	default:
		f.t.Errorf("unexpected provider request %s %s", r.Method, path)
		fail(404)
	}
}
func TestNeonOwnedSchemaBranchReuseAndExplicitDeletion(t *testing.T) {
	client, f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := client.Ensure(ctx, f.spec, f.scope, true, nil)
	if err != nil || got.State != "ready" {
		t.Fatal(got, err)
	}
	if f.branch.InitSource != "schema-only" {
		t.Fatal("default copied production rows")
	}
	reused, err := client.Ensure(ctx, f.spec, f.scope, false, nil)
	if err != nil || reused.Branch.ID != got.Branch.ID || f.branchCreates != 1 || f.roleCreates != 1 {
		t.Fatal("redeploy duplicated database", reused, err)
	}
	connection, err := client.Connection(ctx, f.spec, f.scope, got.Branch.ID)
	if err != nil || connection["username"] != Role(f.scope) || connection["password"] != "branch-only-password" {
		t.Fatal("isolated credential unavailable", err)
	}
	if err = client.Delete(ctx, f.spec, f.scope, got.Branch.ID); err != nil {
		t.Fatal(err)
	}
	if err = client.Delete(ctx, f.spec, f.scope, got.Branch.ID); err != nil || f.deletes != 1 {
		t.Fatal("cleanup did not converge", err)
	}
}
func TestNeonLostCreateResponseAdoptsWithoutDuplicate(t *testing.T) {
	client, f := newFixture(t)
	f.lostCreate = true
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := client.Ensure(ctx, f.spec, f.scope, true, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.Uncertain || strings.Contains(err.Error(), "private-provider-key") {
		t.Fatal("unknown create was not safely reported", err)
	}
	// The branch exists despite the lost response. Recovery may complete missing
	// compute and role setup without asking Neon to create another branch.
	f.endpoint = nil
	got, err := client.Ensure(ctx, f.spec, f.scope, false, nil)
	if err != nil || got.State != "ready" || f.branchCreates != 1 || f.endpointCreates != 1 || f.roleCreates != 1 {
		t.Fatal("partial create did not converge", got, err)
	}
}
func TestNeonRefusesForeignProtectedAndDataPolicyChangedBranches(t *testing.T) {
	for _, kind := range []string{"foreign", "protected", "default", "data"} {
		t.Run(kind, func(t *testing.T) {
			client, f := newFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got, err := client.Ensure(ctx, f.spec, f.scope, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "foreign":
				f.marks["dispatch.run"] = "another-run"
			case "protected":
				f.branch.Protected = true
			case "default":
				f.branch.Default = true
			case "data":
				f.branch.InitSource = "parent-data"
			}
			if _, err = client.Connection(ctx, f.spec, f.scope, got.Branch.ID); err == nil || f.connectionReads != 0 {
				t.Fatal("unsafe branch exposed credentials", err)
			}
			if err = client.Delete(ctx, f.spec, f.scope, got.Branch.ID); err == nil || f.deletes != 0 {
				t.Fatal("unsafe branch was deleted", err)
			}
		})
	}
}
func TestNeonRejectsUnapprovedRowsAndInheritedCredentialRole(t *testing.T) {
	client, f := newFixture(t)
	copied := f.spec
	copied.DataMode = "parent-data"
	if _, err := client.Ensure(context.Background(), copied, f.scope, true, nil); err == nil || f.branchCreates != 0 {
		t.Fatal("unapproved production data copied")
	}
	f.parentRole = true
	if _, err := client.Ensure(context.Background(), f.spec, f.scope, true, nil); err == nil || f.branchCreates != 0 {
		t.Fatal("production database role reused")
	}
}
func TestNeonProgressFailureStopsBeforeProviderMutation(t *testing.T) {
	client, f := newFixture(t)
	_, err := client.Ensure(context.Background(), f.spec, f.scope, true, func(context.Context, string, Resource) error { return errors.New("cannot persist acceptance") })
	if err == nil || f.branchCreates != 0 {
		t.Fatal("provider mutation preceded durable phase", err)
	}
}
