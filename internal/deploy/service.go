package deploy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

var ErrDeploymentActive = errors.New("an active deployment already exists for this app")

type Service struct {
	store    store.Store
	executor Executor
	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
}

func NewService(data store.Store, executor Executor) *Service {
	return &Service{store: data, executor: executor, cancels: map[string]context.CancelFunc{}}
}

func (s *Service) Start(ctx context.Context, appID, commitSHA string) (core.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active, err := s.store.ActiveDeploymentForApp(ctx, appID)
	if err != nil {
		return core.Deployment{}, err
	}
	if active != nil {
		return core.Deployment{}, ErrDeploymentActive
	}
	app, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return core.Deployment{}, err
	}
	if commitSHA == "" {
		commitSHA = "HEAD"
	}
	now := time.Now().UTC()
	deployment := core.Deployment{
		ID: ulid.Make().String(), AppID: app.ID, CommitSHA: commitSHA, SpecDigest: app.SpecDigest(),
		State: core.DeploymentQueued, Message: "Deployment accepted", CreatedAt: now,
	}
	if err := s.store.CreateDeployment(ctx, deployment); err != nil {
		return core.Deployment{}, err
	}
	if err := s.log(ctx, deployment.ID, "info", "Deployment accepted for "+app.Name); err != nil {
		return core.Deployment{}, err
	}
	jobCtx, cancel := context.WithCancel(context.Background())
	s.cancels[deployment.ID] = cancel
	go s.run(jobCtx, deployment)
	return deployment, nil
}

func (s *Service) Cancel(ctx context.Context, id string) error {
	s.mu.Lock()
	cancel := s.cancels[id]
	s.mu.Unlock()
	if cancel == nil {
		return errors.New("deployment is not active")
	}
	cancel()
	return nil
}

func (s *Service) run(ctx context.Context, deployment core.Deployment) {
	defer func() {
		s.mu.Lock()
		delete(s.cancels, deployment.ID)
		s.mu.Unlock()
	}()
	app, err := s.store.GetApp(ctx, deployment.AppID)
	if err != nil {
		s.fail(deployment, err)
		return
	}
	server, err := s.store.GetServer(ctx, app.ServerID)
	if err != nil {
		s.fail(deployment, err)
		return
	}
	now := time.Now().UTC()
	lease := now.Add(10 * time.Minute)
	deployment.StartedAt, deployment.LeaseUntil = &now, &lease
	if err := s.transition(ctx, &deployment, core.DeploymentFetching, "Preparing source acquisition"); err != nil {
		return
	}
	err = s.executor.Deploy(ctx, deployment, app, server, func(state core.DeploymentState, message string) error {
		return s.transition(ctx, &deployment, state, message)
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			s.finish(&deployment, core.DeploymentCancelled, "Deployment cancelled by operator")
			return
		}
		s.fail(deployment, err)
		return
	}
	s.finish(&deployment, core.DeploymentSucceeded, "Deployment is live")
}

func (s *Service) transition(ctx context.Context, deployment *core.Deployment, state core.DeploymentState, message string) error {
	deployment.State, deployment.Message = state, message
	lease := time.Now().UTC().Add(10 * time.Minute)
	deployment.LeaseUntil = &lease
	if err := s.store.UpdateDeployment(context.Background(), *deployment); err != nil {
		return err
	}
	return s.log(context.Background(), deployment.ID, "info", message)
}

func (s *Service) finish(deployment *core.Deployment, state core.DeploymentState, message string) {
	now := time.Now().UTC()
	deployment.State, deployment.Message, deployment.FinishedAt, deployment.LeaseUntil = state, message, &now, nil
	_ = s.store.UpdateDeployment(context.Background(), *deployment)
	level := "info"
	if state == core.DeploymentFailed {
		level = "error"
	}
	_ = s.log(context.Background(), deployment.ID, level, message)
}

func (s *Service) fail(deployment core.Deployment, err error) {
	s.finish(&deployment, core.DeploymentFailed, fmt.Sprintf("Deployment failed: %v", err))
}

func (s *Service) log(ctx context.Context, deploymentID, level, message string) error {
	return s.store.AppendDeploymentLog(ctx, core.DeploymentLog{DeploymentID: deploymentID, Level: level, Message: message, CreatedAt: time.Now().UTC()})
}
