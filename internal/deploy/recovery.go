package deploy

import (
	"context"
	"github.com/doout/dispatch/internal/core"
	"log/slog"
	"time"
)

type recoveryStore interface {
	RenewDeploymentLease(context.Context, string, time.Time) error
	RecoverInterruptedDeployments(context.Context, time.Time, time.Time) ([]core.Deployment, error)
}

func (s *Service) RunRecovery(ctx context.Context, logger *slog.Logger) {
	data, ok := s.store.(recoveryStore)
	if !ok {
		return
	}
	startup := time.Now().UTC()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		now := time.Now().UTC()
		// Renew owned executions first, including long image builds with no progress messages.
		s.mu.Lock()
		ids := make([]string, 0, len(s.cancels))
		for id := range s.cancels {
			ids = append(ids, id)
		}
		s.mu.Unlock()
		for _, id := range ids {
			if err := data.RenewDeploymentLease(ctx, id, now.Add(10*time.Minute)); err != nil && ctx.Err() == nil {
				logger.Warn("cannot renew execution lease", "deployment", id)
			}
		}
		recovered, err := data.RecoverInterruptedDeployments(ctx, now, startup)
		if err != nil && ctx.Err() == nil {
			logger.Error("execution recovery failed", "error", err)
		}
		for _, d := range recovered {
			logger.Warn("interrupted deployment recovered", "deployment", d.ID, "application", d.AppID)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
