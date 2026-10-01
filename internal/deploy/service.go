package deploy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/serviceconn"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

var (
	ErrDeploymentActive    = errors.New("an active deployment already exists for this app")
	ErrApplicationTemplate = errors.New("application templates cannot be deployed directly")
)

type Service struct {
	Storage *StorageManager
	// CheckExecution verifies preview provenance before accepting or dispatching work.
	CheckExecution func(context.Context, core.App, string) error
	// OnFinished queues follow-up observations after the terminal state is saved.
	OnFinished      func(core.Deployment)
	services        serviceconn.Resolver
	store           store.Store
	executor        Executor
	runtimeRollback RuntimeRollbackExecutor
	mu              sync.Mutex
	cancels         map[string]context.CancelFunc
	appLocks        map[string]*sync.Mutex
	helmComparison  *SourceAuthExecutor
	compareHelm     func(context.Context, core.App, core.Server, string, core.Deployment) (bool, error)
	resolveRevision func(context.Context, core.App, string) (string, error)
}

func NewService(data store.Store, executor Executor) *Service {
	runtimeRollback, _ := executor.(RuntimeRollbackExecutor)
	return &Service{Storage: NewStorageManager(data), store: data, executor: executor, runtimeRollback: runtimeRollback, cancels: map[string]context.CancelFunc{}, appLocks: map[string]*sync.Mutex{}}
}

func (s *Service) ConfigureRuntimeRollback(executor RuntimeRollbackExecutor) {
	s.runtimeRollback = executor
}

func (s *Service) Start(ctx context.Context, appID, commitSHA string) (core.Deployment, error) {
	return s.start(ctx, appID, commitSHA, nil)
}
func (s *Service) StartReviewed(ctx context.Context, appID, commitSHA string, review core.DeploymentReview) (core.Deployment, error) {
	if review.ExpectedAppName == "" || review.ProjectID == "" || review.AppSpecDigest == "" || review.BindingsDigest == "" || review.ServiceRevisions == nil {
		return core.Deployment{}, store.ErrDeploymentReviewChanged
	}
	return s.start(ctx, appID, commitSHA, &review)
}
func (s *Service) start(ctx context.Context, appID, commitSHA string, review *core.DeploymentReview) (core.Deployment, error) {
	unlock := s.lockApp(appID)
	defer unlock()
	return s.startLocked(ctx, appID, commitSHA, review)
}

// startLocked requires the application lock, also used by automatic comparison.
func (s *Service) startLocked(ctx context.Context, appID, commitSHA string, review *core.DeploymentReview) (core.Deployment, error) {
	if err := s.checkRemoteMutation(ctx, appID); err != nil {
		return core.Deployment{}, err
	}
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
	if app.Template {
		return core.Deployment{}, ErrApplicationTemplate
	}
	server, err := s.store.GetServer(ctx, app.ServerID)
	if err != nil {
		return core.Deployment{}, err
	}
	if err := s.RuntimeCapabilities(app, server).Check(ctx, runtimecontract.Deploy); err != nil {
		return core.Deployment{}, err
	}
	if review == nil {
		review = &core.DeploymentReview{ExpectedAppName: app.Name, ProjectID: app.ProjectID, AppSpecDigest: app.SpecDigest()}
	}
	if review.ExpectedAppName != app.Name || review.ProjectID != app.ProjectID || review.AppSpecDigest != app.SpecDigest() {
		return core.Deployment{}, store.ErrDeploymentReviewChanged
	}
	if commitSHA == "" {
		if app.BuildType == core.BuildTypeHelm {
			commitSHA = "chart"
		} else if app.ComposeContent != "" {
			commitSHA = "inline"
		} else {
			commitSHA = "HEAD"
		}
	}
	if s.CheckExecution != nil {
		if err := s.CheckExecution(ctx, app, commitSHA); err != nil {
			return core.Deployment{}, err
		}
	}
	if s.resolveRevision != nil {
		commitSHA, err = s.resolveRevision(ctx, app, commitSHA)
		if err != nil {
			return core.Deployment{}, err
		}
	}
	policy, err := core.NormalizeHealthPolicy(app.HealthPolicy)
	if err != nil {
		return core.Deployment{}, err
	}
	now := time.Now().UTC()
	deployment := core.Deployment{
		ID: ulid.Make().String(), AppID: app.ID, CommitSHA: commitSHA, SpecDigest: app.SpecDigest(),
		Health: core.DeploymentHealth{Policy: policy, State: "pending", Checks: []core.HealthCheckResult{}},
		State:  core.DeploymentQueued, Message: "Deployment accepted", CreatedAt: now,
		Acceptance: review, ExecutionAppName: app.Name, ExecutionTemplate: app.Template, ExecutionGenerated: app.Generated,
	}
	if claim, ok := core.MutationAcceptanceFromContext(ctx); ok {
		if claim.OperationKind != "deployment" {
			return core.Deployment{}, store.ErrMutationClaimLost
		}
		deployment.ID = claim.OperationID
	}
	if err := s.store.CreateDeployment(ctx, deployment); err != nil {
		return core.Deployment{}, err
	}
	deployment, err = s.store.GetDeployment(ctx, deployment.ID)
	if err != nil {
		return core.Deployment{}, err
	}
	if err := s.log(ctx, deployment.ID, "info", "Deployment accepted for "+app.Name); err != nil {
		return core.Deployment{}, err
	}
	jobCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancels[deployment.ID] = cancel
	s.mu.Unlock()
	go s.run(jobCtx, deployment, app)
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

func (s *Service) Cleanup(ctx context.Context, appID string, progress Progress) error {
	return s.CleanupReviewed(ctx, appID, nil, false, progress)
}

// CleanupReviewed keeps validation, runtime cleanup and optional deletion under
// one application lock so a deployment cannot start between those steps.
func (s *Service) CleanupReviewed(ctx context.Context, appID string, review func() error, remove bool, progress Progress) error {
	unlock := s.lockApp(appID)
	defer unlock()
	if err := s.checkRemoteMutation(ctx, appID); err != nil {
		return err
	}
	active, err := s.store.ActiveDeploymentForApp(ctx, appID)
	if err != nil {
		return err
	}
	if active != nil {
		return ErrDeploymentActive
	}
	app, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return err
	}
	server, err := s.store.GetServer(ctx, app.ServerID)
	if err != nil {
		return err
	}
	return s.Storage.WithTarget(ctx, server.ID, func() error {
		if err := s.Storage.RefreshLocked(ctx, server); err != nil {
			return err
		}
		if review != nil {
			if err := review(); err != nil {
				return err
			}
		}
		cleanup := true
		if remove {
			cleanup, err = s.store.AppHasDeployments(ctx, appID)
			if err != nil {
				return err
			}
		}
		if progress == nil {
			progress = func(core.DeploymentState, string) error { return nil }
		}
		if cleanup {
			if err := s.Storage.CheckApplicationCleanup(ctx, app, server); err != nil {
				return err
			}
			cleaner, ok := s.executor.(CleanupExecutor)
			if !ok {
				return ErrCleanupUnsupported
			}
			if err := cleaner.Cleanup(ctx, app, server, progress); err != nil {
				return err
			}
		}
		if err := s.Storage.RefreshLocked(ctx, server); err != nil {
			return err
		}
		if remove {
			return s.store.DeleteApp(ctx, appID)
		}
		return nil
	})
}

func (s *Service) lockApp(appID string) func() {
	s.mu.Lock()
	lock := s.appLocks[appID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.appLocks[appID] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (s *Service) run(ctx context.Context, deployment core.Deployment, app core.App) {
	defer func() {
		s.mu.Lock()
		delete(s.cancels, deployment.ID)
		s.mu.Unlock()
	}()
	if s.CheckExecution != nil {
		if err := s.CheckExecution(ctx, app, deployment.CommitSHA); err != nil {
			s.fail(deployment, err)
			return
		}
	}
	server, err := s.store.GetServer(ctx, app.ServerID)
	if err != nil {
		s.fail(deployment, err)
		return
	}
	applied, err := s.resolveServices(ctx, deployment, &app)
	if err != nil {
		s.fail(deployment, err)
		return
	}
	deployment.Snapshot.ServiceBindings = applied
	if err := s.store.UpdateDeploymentSnapshot(ctx, deployment.ID, deployment.Snapshot); err != nil {
		s.fail(deployment, err)
		return
	}
	now := time.Now().UTC()
	lease := now.Add(10 * time.Minute)
	deployment.StartedAt, deployment.LeaseUntil = &now, &lease
	preparing := "Preparing source acquisition"
	if app.BuildType == core.BuildTypeHelm {
		preparing = "Preparing Helm release"
	} else if app.ComposeContent != "" {
		preparing = "Preparing saved Compose definition"
	}
	if err := s.transition(ctx, &deployment, core.DeploymentFetching, preparing); err != nil {
		return
	}
	ctx = WithHealthReporter(ctx, func(save context.Context, id string, result core.DeploymentHealth) error {
		deployment.Health = result
		return s.store.UpdateDeploymentHealth(save, id, result)
	})
	err = s.Storage.WithTarget(ctx, server.ID, func() error {
		executionErr := s.executor.Deploy(ctx, deployment, app, server, func(state core.DeploymentState, message string) error {
			return s.transition(ctx, &deployment, state, redactServiceMessage(message, app.ServiceRuntime))
		})
		return errors.Join(executionErr, s.Storage.RefreshLocked(ctx, server))
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			s.finish(&deployment, core.DeploymentCancelled, "Deployment cancelled by operator")
			return
		}
		s.fail(deployment, errors.New(redactServiceMessage(err.Error(), app.ServiceRuntime)))
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
	saved := s.store.UpdateDeployment(context.Background(), *deployment) == nil
	level := "info"
	if state == core.DeploymentFailed {
		level = "error"
	}
	_ = s.log(context.Background(), deployment.ID, level, message)
	if saved && s.OnFinished != nil {
		s.OnFinished(*deployment)
	}
}

func (s *Service) fail(deployment core.Deployment, err error) {
	s.finish(&deployment, core.DeploymentFailed, fmt.Sprintf("Deployment failed: %v", err))
}

func (s *Service) log(ctx context.Context, deploymentID, level, message string) error {
	return s.store.AppendDeploymentLog(ctx, core.DeploymentLog{DeploymentID: deploymentID, Level: level, Message: message, CreatedAt: time.Now().UTC()})
}

// WithIdleApplication excludes checks and repairs from deployment/cleanup changes.
func (s *Service) WithIdleApplication(ctx context.Context, appID string, fn func() error) error {
	s.mu.Lock()
	lock := s.appLocks[appID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.appLocks[appID] = lock
	}
	s.mu.Unlock()
	if !lock.TryLock() {
		return ErrDeploymentActive
	}
	defer lock.Unlock()
	if err := s.checkRemoteMutation(ctx, appID); err != nil {
		return err
	}
	active, err := s.store.ActiveDeploymentForApp(ctx, appID)
	if err != nil {
		return err
	}
	if active != nil {
		return ErrDeploymentActive
	}
	return fn()
}

// Cleanup review uses the same live identity as restore where the runtime supports it.
func (s *Service) CleanupRuntimeIdentity(ctx context.Context, app core.App, server core.Server) (string, error) {
	if app.BuildType == core.BuildTypeHelm {
		return "", nil
	}
	inspector, ok := s.runtimeRollback.(interface {
		CurrentRuntimeIdentity(context.Context, core.App, core.Server) (string, error)
	})
	if !ok {
		return "", nil
	}
	return inspector.CurrentRuntimeIdentity(ctx, app, server)
}

func (s *Service) checkRemoteMutation(ctx context.Context, app string) error {
	if data, ok := s.store.(interface {
		ActiveRuntimeMutation(context.Context, string) (*core.RuntimeJob, error)
	}); ok {
		job, err := data.ActiveRuntimeMutation(ctx, app)
		if err != nil {
			return err
		}
		if job != nil {
			return ErrDeploymentActive
		}
	}
	return nil
}
