package deploy

import (
	"context"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

type cleanupOperationKey struct{}

// WithCleanupOperation pins the remote job identity across controller retries.
func WithCleanupOperation(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, cleanupOperationKey{}, id)
}

// DispatchAccepted starts a deployment already inserted atomically with its
// owning environment. Only outbound execution supports this recovery path.
func (s *Service) DispatchAccepted(ctx context.Context, id string) error {
	d, err := s.store.GetDeployment(ctx, id)
	if err != nil {
		return err
	}
	app, err := s.store.GetApp(ctx, d.AppID)
	if err != nil {
		return err
	}
	server, err := s.store.GetServer(ctx, app.ServerID)
	if err != nil {
		return err
	}
	if server.AgentNodeID == "" || app.BuildType != core.BuildTypeDockerfile {
		return errors.New("temporary environments require outbound Dockerfile execution")
	}
	data, ok := s.store.(interface {
		TemporaryEnvironmentForApp(context.Context, string) (core.TemporaryEnvironment, error)
	})
	if !ok {
		return errors.New("temporary environment storage unavailable")
	}
	e, err := data.TemporaryEnvironmentForApp(ctx, app.ID)
	expected := e.AppSpecDigest
	if expected == "" {
		expected = d.SpecDigest
	}
	if err != nil || e.DeploymentID != id || e.SourceSHA != d.CommitSHA || app.SpecDigest() != expected {
		return store.ErrTemporaryEnvironmentChanged
	}
	if d.State.Terminal() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancels[id] != nil {
		return nil
	}
	claim, ok := s.store.(interface {
		ClaimTemporaryDeployment(context.Context, string, string, time.Time) (bool, error)
	})
	if !ok {
		return store.ErrTemporaryEnvironmentChanged
	}
	claimed, err := claim.ClaimTemporaryDeployment(ctx, e.ID, d.ID, time.Now().UTC())
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	jobCtx, cancel := context.WithDeadline(context.Background(), e.ExpiresAt)
	s.cancels[id] = cancel
	go s.run(jobCtx, d, app)
	return nil
}

// CleanupOwnedApplication is shared by PR previews and temporary environments.
// Cleanup applies storage ownership guards, retains volumes and removes routing
// before closing the application without erasing deployment history.
func (s *Service) CleanupOwnedApplication(ctx context.Context, app core.App, operation string) error {
	if operation != "" {
		ctx = WithCleanupOperation(ctx, operation)
	}
	if err := s.Cleanup(ctx, app.ID, nil); err != nil {
		return err
	}
	app.State = "closed"
	return s.store.UpdateApp(ctx, app)
}

// Cleanup waits for this controller's cancelled executor to settle. The store
// separately fences new acceptance and remote leases before this is called.
func (s *Service) CancelAndWaitOwnedApplication(ctx context.Context, appID string) error {
	s.mu.Lock()
	ids := []string{}
	for id := range s.cancels {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	deployments := []core.Deployment{}
	for _, id := range ids {
		d, err := s.store.GetDeployment(ctx, id)
		if err != nil {
			return err
		}
		if d.AppID == appID {
			deployments = append(deployments, d)
		}
	}
	for {
		waiting := false
		s.mu.Lock()
		for _, deployment := range deployments {
			if cancel := s.cancels[deployment.ID]; cancel != nil {
				waiting = true
				cancel()
			}
		}
		s.mu.Unlock()
		if !waiting {
			return nil
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
