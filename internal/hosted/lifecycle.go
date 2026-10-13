package hosted

import (
	"context"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/tenancy"
)

// Prepare binds installation settings in the hosted catalog. DNS publication
// runs in background reconciliation, so provider outages do not prevent startup.
func (s *Server) Prepare(ctx context.Context) error {
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	return s.prepareZone(ctx)
}

func (s *Server) ReconcileTenants(ctx context.Context) error {
	s.dnsMu.Lock()
	joined := s.syncTenantDNS(ctx, platformOwner)
	s.dnsMu.Unlock()
	tenants, err := s.Catalog.ListTenants(ctx)
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		if err := s.syncWorkOSOrganization(ctx, tenant); err != nil {
			joined = errors.Join(joined, err)
			s.Logger.Error("WorkOS organization sync failed", "tenant", tenant.ID)
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if tenant.State != tenancy.StatePending && tenant.State != "provisioning" && tenant.State != "failed" && tenant.State != tenancy.StateActive {
			continue
		}

		if err = s.provisionTenant(ctx, tenant); err != nil {
			// Catalog metadata carries a fixed phase, never connection strings or
			// tenant content from a backend error.
			if tenant.State != tenancy.StateActive {
				_ = s.Catalog.SetTenantState(ctx, tenant.ID, "failed")
				phase := "preparing tenant"
				if job, err := s.Catalog.ProvisioningJob(ctx, tenant.ID); err == nil && job.Phase == "registering address" {
					phase = job.Phase
				}
				_ = s.Catalog.SaveProvisioningJob(ctx, tenancy.ProvisioningJob{TenantID: tenant.ID, State: "failed", Phase: phase})
			}
			s.Logger.Error("tenant preparation failed", "tenant", tenant.ID)
			joined = errors.Join(joined, err)
		}
		s.dnsMu.Lock()
		dnsErr := s.syncTenantDNS(ctx, tenant.ID)
		s.dnsMu.Unlock()
		joined = errors.Join(joined, dnsErr)
	}
	return joined
}

func (s *Server) provisionTenant(ctx context.Context, tenant tenancy.Tenant) error {
	if tenant.State != tenancy.StateActive {
		if err := s.Catalog.SetTenantState(ctx, tenant.ID, "provisioning"); err != nil {
			return err
		}
		if err := s.Catalog.SaveProvisioningJob(ctx, tenancy.ProvisioningJob{TenantID: tenant.ID, State: "running", Phase: "preparing tenant"}); err != nil {
			return err
		}
	}
	connection, err := s.Catalog.InstanceConnection(ctx, tenant.ID)
	if err != nil {
		return err
	}
	if connection.BackendMode == tenancy.BackendConnected {
		if err = s.Catalog.SetTenantState(ctx, tenant.ID, tenancy.StateActive); err != nil {
			return err
		}
		return s.Catalog.SaveProvisioningJob(ctx, tenancy.ProvisioningJob{TenantID: tenant.ID, State: "succeeded", Phase: "awaiting connection"})
	}
	if s.Config.BackendMode == tenancy.BackendConnected {
		return nil
	}
	runtime, err := s.runtime(tenant)
	if err != nil {
		return err
	}
	routes, err := runtime.Store.ListApplicationRoutes(ctx)
	if err != nil {
		return err
	}
	reserve := s.tenantAuthenticator(tenant).ReserveHostname
	for _, route := range routes {
		// Older nested or custom routes need an explicit redeployment. They must
		// never become aliases for a tenant console or another tenant's workload.
		if err := reserve(ctx, route); err != nil && !errors.Is(err, tenancy.ErrDenied) {
			return err
		}
	}

	if tenant.State != tenancy.StateActive {
		if err := s.Catalog.SaveProvisioningJob(ctx, tenancy.ProvisioningJob{TenantID: tenant.ID, State: "running", Phase: "registering address"}); err != nil {
			return err
		}
	}
	s.dnsMu.Lock()
	err = s.prepareTenantDNS(ctx, tenant)
	s.dnsMu.Unlock()
	if err != nil {
		return err
	}
	if tenant.State == tenancy.StateActive {
		return nil
	}
	if err = s.Catalog.SetTenantState(ctx, tenant.ID, tenancy.StateActive); err != nil {
		return err
	}
	return s.Catalog.SaveProvisioningJob(ctx, tenancy.ProvisioningJob{TenantID: tenant.ID, State: "succeeded", Phase: "ready"})
}

// Run keeps certificate renewal separate from deployment jobs and retries
// interrupted tenant creation from its durable catalog state.
func (s *Server) Run(ctx context.Context) {
	certDone := make(chan struct{})
	go func() {
		defer close(certDone)
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			s.reconcileCertificates(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() { <-certDone }()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		_ = s.ReconcileTenants(ctx)
		if err := s.CollectUsage(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			s.Logger.Error("tenant usage collection failed")
		}
		if err := s.Catalog.PruneCredentials(ctx); err != nil && ctx.Err() == nil {
			s.Logger.Error("expired session cleanup failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) reconcileCertificates(ctx context.Context) {
	if s.Config.Certificates.DirectoryURL == "" || ctx.Err() != nil {
		return
	}
	if err := s.reconcileCertificate(ctx); err != nil && ctx.Err() == nil {
		s.Logger.Error("console certificate renewal failed")
	}

}

func (s *Server) CollectUsage(ctx context.Context, now time.Time) error {
	tenants, err := s.Catalog.ListTenants(ctx)
	if err != nil {
		return err
	}
	start := now.UTC().Truncate(24 * time.Hour)
	for _, tenant := range tenants {
		if tenant.State != tenancy.StateActive {
			continue
		}
		if connection, e := s.Catalog.InstanceConnection(ctx, tenant.ID); e != nil {
			return e
		} else if connection.BackendMode == tenancy.BackendConnected || s.Config.BackendMode == tenancy.BackendConnected {
			continue
		}
		runtime, err := s.runtime(tenant)
		if err != nil {
			return err
		}
		members, err := s.Catalog.TenantMembers(ctx, tenant.ID)
		if err != nil {
			return err
		}
		var count int64
		for _, member := range members {
			if member.State == tenancy.StateActive {
				count++
			}
		}
		// Refresh yesterday too, so a restart around midnight cannot lose the
		// last minute of completed builds. Upserts use stable daily boundaries.
		for _, day := range []time.Time{start.Add(-24 * time.Hour), start} {
			end := day.Add(24 * time.Hour)
			totals, err := runtime.Store.TenantUsage(ctx, day, end)
			if err != nil {
				return err
			}
			u := tenancy.Usage{TenantID: tenant.ID, PeriodStart: day, PeriodEnd: end, Projects: totals.Projects, Applications: totals.Applications, Members: count, Builds: totals.Builds, Deployments: totals.Deployments, BuildSeconds: totals.BuildSeconds, Measured: append(totals.Measured, "members")}
			if err = s.Catalog.SaveUsage(ctx, u); err != nil {
				return err
			}
		}
	}
	return nil
}
