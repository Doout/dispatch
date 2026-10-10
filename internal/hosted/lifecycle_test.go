package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/tenancy"
)

func TestHostedProvisioningRetriesAndRehydratesDNS(t *testing.T) {
	f := newHostedFixture(t)
	ctx := context.Background()
	tenant, err := f.catalog.CreateTenant(ctx, tenancy.CreateTenantInput{Slug: "retry", Name: "Retry", CreatorID: "platform", InitialOwnerID: "owner-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.server.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	original := f.server.OpenRuntime
	fail := true
	f.server.OpenRuntime = func(ctx context.Context, tenant tenancy.Tenant, auth *api.HostedAuth) (*TenantRuntime, error) {
		if fail {
			fail = false
			return nil, errors.New("private-connection-details")
		}
		return original(ctx, tenant, auth)
	}
	if err = f.server.ReconcileTenants(ctx); err == nil {
		t.Fatal("first provisioning unexpectedly succeeded")
	}
	failed, err := f.catalog.Tenant(ctx, tenant.ID)
	if err != nil || failed.State != "failed" {
		t.Fatal(failed, err)
	}
	job, err := f.catalog.ProvisioningJob(ctx, tenant.ID)
	if err != nil || job.State != "failed" || strings.Contains(job.Phase, "private") {
		t.Fatal("unsafe failure metadata", job, err)
	}
	if err = f.server.ReconcileTenants(ctx); err != nil {
		t.Fatal(err)
	}
	active, err := f.catalog.Tenant(ctx, tenant.ID)
	if err != nil || active.State != "active" {
		t.Fatal(active, err)
	}
	generation, records, err := f.catalog.ZoneRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var console, workload bool
	for _, record := range records {
		if record.TenantID != tenant.ID {
			continue
		}
		console = console || record.Name == "retry."+f.server.Config.RootDomain
		workload = workload || record.Name == "*.retry."+f.server.Config.RootDomain
	}
	if !console || !workload {
		t.Fatal("missing tenant records", records)
	}
	if err = f.server.ReconcileTenants(ctx); err != nil {
		t.Fatal(err)
	}
	next, _, err := f.catalog.ZoneRecords(ctx)
	if err != nil || next != generation {
		t.Fatal("unchanged reconciliation churned DNS generation", generation, next, err)
	}
	if members, err := f.catalog.ListMemberships(ctx, "platform"); err != nil || len(members) != 0 {
		t.Fatal("provisioning enrolled creator", members, err)
	}
	// A new controller instance reads the persisted tenant and DNS records.
	restarted, err := New(ctx, f.server.Config, f.catalog, original, f.server.Logger)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err = restarted.ReconcileTenants(ctx); err != nil {
		t.Fatal(err)
	}
	after, _, err := restarted.Catalog.ZoneRecords(ctx)
	if err != nil || after != generation {
		t.Fatal("restart changed desired DNS", after, err)
	}
	if pending, err := restarted.Catalog.PendingDNSChanges(ctx, 100); err != nil || len(pending) != 0 {
		t.Fatal("restart lost DNS publication state", pending, err)
	}
}

func TestHostedUsageDailyUpsertAndRestart(t *testing.T) {
	f := newHostedFixture(t)
	tenant := f.tenant(t, "usage", "owner-a")
	ctx := context.Background()
	runtime, err := f.server.runtime(tenant)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = runtime.Store.CreateProject(ctx, core.Project{ID: "private-project", Name: "Private project name", Description: "private-description", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err = f.server.CollectUsage(ctx, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := f.catalog.TenantUsage(ctx, tenant.ID, now.Add(-48*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 2 {
		t.Fatal("repeated snapshots created overlapping daily usage", len(usage))
	}
	if usage[1].Projects != 1 || usage[1].Members != 1 {
		t.Fatal("wrong aggregate gauges", usage[1])
	}
	admin := f.session(t, "platform", tenancy.AudiencePlatform)
	w := f.request(t, f.server.Config.RootDomain, http.MethodGet, "/api/v1/platform/tenants/"+tenant.ID+"/usage?days=7", admin, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Private project") || strings.Contains(w.Body.String(), "private-project") || strings.Contains(w.Body.String(), "owner-a") {
		t.Fatal("raw operational data exported in usage", w.Body.String())
	}
	var response []tenancy.Usage
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response) != 2 {
		t.Fatal("today's usage missing", response, err)
	}
	restarted, err := New(ctx, f.server.Config, f.catalog, f.server.OpenRuntime, f.server.Logger)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err = restarted.CollectUsage(ctx, now); err != nil {
		t.Fatal(err)
	}
	after, err := f.catalog.TenantUsage(ctx, tenant.ID, now.Add(-48*time.Hour), now)
	if err != nil || len(after) != 2 || after[1].Projects != 1 {
		t.Fatal("restart changed or duplicated usage", after, err)
	}
}
