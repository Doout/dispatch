package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	catalog "github.com/doout/dispatch/internal/tenancy/roles"
	workos "github.com/workos/workos-go/v10"
)

type workOSFixture struct {
	roles       map[string]workos.Role
	permissions map[string]workos.AuthorizationPermission
	writes      []string
	fail        string
	client      *workos.Client
}

func newFixture(t *testing.T) *workOSFixture {
	t.Helper()
	f := &workOSFixture{roles: map[string]workos.Role{}, permissions: map[string]workos.AuthorizationPermission{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("API key missing from authenticated request")
		}
		if r.URL.Path == f.fail {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"sensitive upstream error: test-key"}`)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/authorization/"), "/")
		if len(parts) == 0 || (parts[0] != "roles" && parts[0] != "permissions") {
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		if r.Method != "GET" {
			f.writes = append(f.writes, r.Method+" "+r.URL.Path)
		}
		if parts[0] == "permissions" {
			switch r.Method {
			case "GET":
				p, ok := f.permissions[parts[1]]
				if !ok {
					w.WriteHeader(404)
					fmt.Fprint(w, `{}`)
					return
				}
				json.NewEncoder(w).Encode(p)
			case "POST", "PATCH":
				var p workos.AuthorizationPermission
				json.NewDecoder(r.Body).Decode(&p)
				if r.Method == "PATCH" {
					p.Slug = parts[1]
				}
				f.permissions[p.Slug] = p
				json.NewEncoder(w).Encode(p)
			default:
				t.Errorf("unexpected permission operation: %s", r.Method)
			}
			return
		}
		switch r.Method {
		case "GET":
			role, ok := f.roles[parts[1]]
			if !ok {
				w.WriteHeader(404)
				fmt.Fprint(w, `{}`)
				return
			}
			json.NewEncoder(w).Encode(role)
		case "POST":
			var role workos.Role
			json.NewDecoder(r.Body).Decode(&role)
			f.roles[role.Slug] = role
			json.NewEncoder(w).Encode(role)
		case "PATCH":
			role := f.roles[parts[1]]
			json.NewDecoder(r.Body).Decode(&role)
			f.roles[role.Slug] = role
			json.NewEncoder(w).Encode(role)
		case "PUT":
			if len(parts) != 3 || parts[2] != "permissions" {
				t.Errorf("unexpected role update: %s", r.URL.Path)
			}
			role := f.roles[parts[1]]
			var input struct{ Permissions []string }
			json.NewDecoder(r.Body).Decode(&input)
			for _, slug := range input.Permissions {
				if _, ok := f.permissions[slug]; !ok {
					t.Errorf("assigned permission before creating it: %s", slug)
				}
			}
			role.Permissions = input.Permissions
			f.roles[role.Slug] = role
			json.NewEncoder(w).Encode(role)
		default:
			t.Errorf("unexpected role operation: %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)
	f.client = workos.NewClient("test-key", workos.WithBaseURL(server.URL), workos.WithMaxRetries(0))
	return f
}

func TestSyncCreatesCatalogAndSecondRunDoesNotWrite(t *testing.T) {
	f := newFixture(t)
	f.roles["custom-team"] = workos.Role{Slug: "custom-team", Permissions: []string{"unrelated:permission"}}
	f.permissions["unrelated:permission"] = workos.AuthorizationPermission{Slug: "unrelated:permission"}
	var out bytes.Buffer
	roles, permissions := catalog.Definitions(), catalog.Permissions()
	if err := syncRoles(context.Background(), f.client, roles, permissions, true, &out); err != nil {
		t.Fatal(err)
	}
	for _, role := range roles {
		if !samePermissions(f.roles[role.Slug].Permissions, role.Permissions) {
			t.Fatalf("role %s was not synchronized", role.Slug)
		}
	}
	if f.roles["custom-team"].Permissions[0] != "unrelated:permission" {
		t.Fatal("unrelated role modified")
	}
	writes := len(f.writes)
	out.Reset()
	if err := syncRoles(context.Background(), f.client, roles, permissions, true, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != writes || !strings.Contains(out.String(), "up to date") {
		t.Fatal("second run wrote to an unchanged environment")
	}
}

func TestDryRunAndFailedPreflightNeverWrite(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	roles, permissions := catalog.Definitions(), catalog.Permissions()
	if err := syncRoles(context.Background(), f.client, roles, permissions, false, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 0 || !strings.Contains(out.String(), "Dry run") {
		t.Fatal("dry run mutated WorkOS")
	}
	f.fail = "/authorization/roles/" + catalog.PlatformAdmin
	err := syncRoles(context.Background(), f.client, roles, permissions, true, &out)
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || strings.Contains(err.Error(), "test-key") || len(f.writes) != 0 {
		t.Fatalf("failed preflight must not write or leak upstream errors: %v", err)
	}
}

func TestSyncUpdatesMetadataRemovesOldGrantsAndAddsNewRoles(t *testing.T) {
	f := newFixture(t)
	roles, permissions := catalog.Definitions(), catalog.Permissions()
	if err := syncRoles(context.Background(), f.client, roles, permissions, true, io.Discard); err != nil {
		t.Fatal(err)
	}
	owner := f.roles[catalog.Owner]
	owner.Name = "Old name"
	owner.Permissions = append(owner.Permissions, "platform:tenants:create")
	owner.Permissions = append(owner.Permissions, "tenant:obsolete", "widgets:users-table:manage")
	f.permissions["widgets:users-table:manage"] = workos.AuthorizationPermission{Slug: "widgets:users-table:manage", System: true}
	f.roles[owner.Slug] = owner
	p := f.permissions["tenant:access"]
	p.Name = "Old permission name"
	f.permissions[p.Slug] = p
	roles = append(roles, catalog.Definition{Slug: "auditor", Name: "Auditor", Description: "Read tenant membership.", Scope: "tenant", Permissions: []string{"tenant:members:read"}})
	if err := syncRoles(context.Background(), f.client, roles, permissions, true, io.Discard); err != nil {
		t.Fatal(err)
	}
	expected := append(roles[0].Permissions, "widgets:users-table:manage")
	if !samePermissions(f.roles[catalog.Owner].Permissions, expected) || f.roles[catalog.Owner].Name != roles[0].Name || f.permissions[p.Slug].Name != "Open tenant" || f.roles["auditor"].Slug != "auditor" {
		t.Fatal("changed definitions were not applied")
	}
}

func TestCatalogRejectsPlatformPermissionInTenantRole(t *testing.T) {
	roles, permissions := catalog.Definitions(), catalog.Permissions()
	if err := validateCatalog(roles, permissions); err != nil {
		t.Fatal(err)
	}
	roles[0].Permissions = append(roles[0].Permissions, "platform:tenants:create")
	if err := validateCatalog(roles, permissions); err == nil {
		t.Fatal("tenant role accepted a platform permission")
	}
	if catalog.IsTenantRole(catalog.PlatformAdmin) {
		t.Fatal("platform administration accepted as a tenant role")
	}
}

func TestFailedApplyCanBeResumed(t *testing.T) {
	f := newFixture(t)
	roles, permissions := catalog.Definitions(), catalog.Permissions()
	f.fail = "/authorization/roles/owner/permissions"
	err := syncRoles(context.Background(), f.client, roles, permissions, true, io.Discard)
	if err == nil || strings.Contains(err.Error(), "test-key") || len(f.permissions) != len(permissions) {
		t.Fatalf("failed apply did not retain completed steps safely: %v", err)
	}
	f.fail = ""
	if err := syncRoles(context.Background(), f.client, roles, permissions, true, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, role := range roles {
		if !samePermissions(f.roles[role.Slug].Permissions, role.Permissions) {
			t.Fatalf("role %s was not recovered", role.Slug)
		}
	}
}

func TestConflictingResourceTypePreventsWrites(t *testing.T) {
	f := newFixture(t)
	f.roles[catalog.Owner] = workos.Role{Slug: catalog.Owner, ResourceTypeSlug: "project"}
	err := syncRoles(context.Background(), f.client, catalog.Definitions(), catalog.Permissions(), true, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "different resource type") || len(f.writes) != 0 {
		t.Fatalf("conflicting role was overwritten: %v", err)
	}
}

func TestKeyFilePrivacyAndCLIArguments(t *testing.T) {
	p := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(p, []byte("test-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if key, err := readKey(p); err != nil || key != "test-key" {
		t.Fatalf("private key rejected: %v", err)
	}
	link := p + "-link"
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readKey(link); err == nil {
		t.Fatal("symlink key accepted")
	}
	os.Chmod(p, 0644)
	if _, err := readKey(p); err == nil {
		t.Fatal("public key file accepted")
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"--print-definitions"}, &out, &out); err != nil || !json.Valid(out.Bytes()) {
		t.Fatalf("cannot print definitions without credentials: %v", err)
	}
	if err := run(context.Background(), []string{"--apply", "--dry-run"}, &out, &out); err == nil {
		t.Fatal("conflicting flags accepted")
	}
}
