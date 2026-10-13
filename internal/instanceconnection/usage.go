package instanceconnection

import (
	"context"
	"net/http"
	"time"
)

func (m *Manager) reportUsage(ctx context.Context, s saved) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for ctx.Err() == nil {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		for _, start := range []time.Time{today.Add(-24 * time.Hour), today} {
			end := start.Add(24 * time.Hour)
			totals, err := m.config.Usage(ctx, start, end)
			if err == nil {
				// Only aggregate counts leave the installation. Memberships are owned by
				// the catalog, so local account identities are never included here.
				_ = m.call(ctx, http.MethodPost, s.ControlPlaneURL+"/api/v1/instance-connections/usage", s.Credential, map[string]any{"periodStart": start, "periodEnd": end, "measured": totals.Measured, "projects": totals.Projects, "applications": totals.Applications, "builds": totals.Builds, "deployments": totals.Deployments, "buildSeconds": totals.BuildSeconds}, nil)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
