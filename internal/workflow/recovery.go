package workflow

import (
	"context"
	"errors"
	"time"
)

// RecoverInterrupted must run before the API serves requests or starts pollers.
// It never retries a possibly completed external operation automatically.
func (s *Service) RecoverInterrupted(ctx context.Context) error {
	data, ok := s.Store.(interface {
		RecoverInterruptedWorkflows(context.Context, time.Time) (int64, error)
	})
	if !ok {
		return errors.New("workflow recovery storage is unavailable")
	}
	count, err := data.RecoverInterruptedWorkflows(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	if count > 0 && s.Logger != nil {
		s.Logger.Info("interrupted workflow work marked failed; explicit retry required", "records", count)
	}
	return nil
}
