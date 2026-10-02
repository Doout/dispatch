package api

import (
	"context"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

var errTemporaryAuthority = errors.New("temporary environment authority was revoked")

func temporaryAuthorityRevoked(err error) bool {
	return errors.Is(err, errTemporaryAuthority) || errors.Is(err, store.ErrTemporaryEnvironmentChanged) || errors.Is(err, store.ErrAutomationCredential)
}
func (a *API) temporaryAuthority(ctx context.Context, e core.TemporaryEnvironment) error {
	target, err := a.store.GetServer(ctx, e.ServerID)
	if err != nil {
		return err
	}
	data, ok := a.store.(*store.SQLStore)
	if !ok {
		return store.ErrTemporaryEnvironmentChanged
	}
	credential, err := data.GetEdgeCredential(ctx, e.TargetNodeID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err != nil || target.AgentNodeID != e.TargetNodeID || credential.Generation != e.TargetGeneration || credential.Revoked || credential.PublicKey == "" {
		return store.ErrTemporaryEnvironmentChanged
	}
	actor := e.Actor
	if actor.Kind == core.PrincipalServiceAccount {
		data, ok := a.store.(store.AutomationStore)
		if !ok {
			return store.ErrAutomationCredential
		}
		account, err := data.GetServiceAccount(ctx, actor.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err != nil || account.State != "active" {
			return store.ErrAutomationCredential
		}
		credentials, err := data.ListAutomationCredentials(ctx, actor.ID)
		if err != nil {
			return err
		}
		valid := false
		for _, c := range credentials {
			if c.ID == actor.CredentialID && c.RevokedAt == nil && c.ExpiresAt.After(time.Now()) {
				valid = true
			}
		}
		if !valid {
			return store.ErrAutomationCredential
		}
		actor.SystemRole = core.UserRoleMember
	} else if actor.ID != "controller-owner" {
		user, err := a.store.GetUser(ctx, actor.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err != nil || user.State != core.UserStateActive {
			return store.ErrAutomationCredential
		}
		actor = identityForUser(user)
	}
	ctx = withIdentity(ctx, actor)
	for _, p := range []core.Permission{core.PermissionProjectView, core.PermissionProjectConfigure, core.PermissionDeploymentRun} {
		allowed, err := a.canProject(ctx, p, e.ProjectID)
		if err != nil {
			return err
		}
		if !allowed {
			return errTemporaryAuthority
		}
	}
	assigned, err := a.assignedInfrastructure(ctx, e.ProjectID, "target", e.ServerID)
	if err != nil {
		return err
	}
	if !assigned {
		return errTemporaryAuthority
	}
	return nil
}
func (a *API) checkTemporaryExecution(ctx context.Context, app core.App, sha string) error {
	data, ok := a.store.(*store.SQLStore)
	if !ok {
		return nil
	}
	e, err := data.TemporaryEnvironmentForApp(ctx, app.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if e.State == "closing" || e.State == "cleanup_blocked" || e.State == "closed" || !e.ExpiresAt.After(time.Now()) || sha != e.SourceSHA {
		return store.ErrTemporaryEnvironmentChanged
	}
	d, err := data.GetDeployment(ctx, e.DeploymentID)
	if err != nil {
		return err
	}
	if d.SpecDigest != app.SpecDigest() {
		return store.ErrTemporaryEnvironmentChanged
	}
	if err = a.temporaryAuthority(ctx, e); err != nil {
		if temporaryAuthorityRevoked(err) {
			_ = data.StopTemporaryEnvironment(ctx, e.ID, e.Revision, time.Now().UTC())
		}
		return err
	}
	return nil
}
func (a *API) checkTemporaryRuntimeAuthority(ctx context.Context, id string) error {
	data, ok := a.store.(*store.SQLStore)
	if !ok {
		return nil
	}
	job, err := data.GetRuntimeJob(ctx, id)
	if err != nil {
		return err
	}
	if job.Operation == "inspect" || job.Operation == "logs" || job.Operation == "destroy" || job.Operation == "retention_inspect" || job.Operation == "retention_prune" {
		return nil
	}
	app, err := a.store.GetApp(ctx, job.AppID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := data.TemporaryEnvironmentForApp(ctx, app.ID); errors.Is(err, store.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	d, err := data.GetDeployment(ctx, job.DeploymentID)
	if err != nil {
		return err
	}
	return a.checkTemporaryExecution(ctx, app, d.CommitSHA)
}
func (a *API) RunTemporaryEnvironments(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		a.reconcileTemporaryEnvironments(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (a *API) reconcileTemporaryEnvironments(ctx context.Context) {
	data, ok := a.store.(*store.SQLStore)
	if !ok {
		return
	}
	items, err := data.ListTemporaryEnvironments(ctx, "")
	if err != nil {
		a.logger.Warn("temporary environment reconciliation unavailable", "error", err)
		return
	}
	for _, e := range items {
		if ctx.Err() != nil {
			return
		}
		if e.State == "closed" {
			continue
		}
		if e.State != "closing" && e.State != "cleanup_blocked" && (!e.ExpiresAt.After(time.Now()) || temporaryAuthorityRevoked(a.temporaryAuthority(ctx, e))) {
			_ = data.StopTemporaryEnvironment(ctx, e.ID, e.Revision, time.Now().UTC())
			_ = a.deploy.Cancel(ctx, e.DeploymentID)
		}
		claimed, err := data.ClaimTemporaryEnvironment(ctx, e.ID, time.Now().UTC())
		if err != nil {
			continue
		}
		work, cancel := context.WithTimeout(ctx, 90*time.Second)
		a.reconcileTemporaryEnvironment(work, data, claimed)
		cancel()
	}
}
func (a *API) reconcileTemporaryEnvironment(ctx context.Context, data *store.SQLStore, e core.TemporaryEnvironment) {
	defer func() {
		save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := data.SaveTemporaryEnvironment(save, e, time.Now().UTC()); err != nil && !errors.Is(err, store.ErrTemporaryEnvironmentChanged) {
			a.logger.Warn("temporary environment checkpoint unavailable", "environment", e.ID, "error", err)
		}
	}()
	app, err := a.store.GetApp(ctx, e.AppID)
	if err != nil {
		e.Message = "Owned application record is unavailable; new work is paused until it can be read."
		return
	}
	if volumes, err := data.ListStorage(ctx, e.ServerID); err == nil {
		seen := map[string]bool{}
		for _, r := range e.Resources {
			seen[r.Kind+":"+r.ID] = true
		}
		for _, v := range volumes {
			if v.OwnerID == e.AppID && !seen[v.Kind+":"+v.ID] {
				e.Resources = append(e.Resources, core.TemporaryResource{Kind: v.Kind, ID: v.ID, Ownership: "retained"})
			}
		}
	}
	if e.State == "closing" || e.State == "cleanup_blocked" {
		_ = a.deploy.Cancel(ctx, e.DeploymentID)
		active, err := data.ActiveRuntimeMutation(ctx, e.AppID)
		if err != nil {
			e.State, e.Message = "cleanup_blocked", "Runtime operation state is unavailable."
			return
		}
		if active != nil && (active.ID != e.CleanupJobID || active.State == "unknown") {
			e.State, e.Message = "cleanup_blocked", "Inspect and acknowledge the interrupted runtime operation before cleanup."
			return
		}
		if err = a.deploy.CleanupOwnedApplication(ctx, app, e.CleanupJobID); err != nil {
			e.State, e.Message = "cleanup_blocked", "Cleanup is incomplete. Inspect runtime and retained storage; retry the reviewed cleanup when the target recovers."
			return
		}
		e.State, e.Message = "closed", "Owned workload and route removed. Shared target, retained storage, backups and execution history remain."
		return
	}
	if err = a.checkTemporaryExecution(ctx, app, e.SourceSHA); err != nil {
		e.Message = "New work is blocked by ownership, source or authorization changes."
		return
	}
	d, err := data.GetDeployment(ctx, e.DeploymentID)
	if err != nil {
		e.State, e.Message = "failed", "Accepted deployment is unavailable."
		return
	}
	job, jobErr := data.GetRuntimeJob(ctx, "deploy-"+d.ID)
	if jobErr != nil && !errors.Is(jobErr, store.ErrNotFound) {
		e.Message = "Runtime operation state is temporarily unavailable."
		return
	}
	if jobErr == nil && job.Terminal() {
		broker := a.runtimeBroker()
		if broker == nil {
			e.Message = "Runtime broker is unavailable."
			return
		}
		result, waitErr := broker.Wait(ctx, job.ID, nil)
		originalState, originalFinished, originalHealth := d.State, d.FinishedAt, d.Health.State
		now := time.Now().UTC()
		d.FinishedAt, d.LeaseUntil = &now, nil
		d.State = core.DeploymentFailed
		if job.State == "cancelled" {
			d.State = core.DeploymentCancelled
		}
		d.Message = "Inspect the saved remote runtime outcome before recovery."
		if waitErr == nil && result.Health != nil && result.Health.State == "passed" && !result.Health.Simulated {
			d.State = core.DeploymentSucceeded
			d.Message = "Reviewed source deployed and required workload health checks passed."
			d.Health = *result.Health
		}
		if originalState != d.State || originalFinished == nil || originalHealth != d.Health.State {
			if result.Health != nil {
				_ = data.UpdateDeploymentHealth(ctx, d.ID, d.Health)
			}
			_ = data.UpdateDeployment(ctx, d)
		}
	}
	switch d.State {
	case core.DeploymentSucceeded:
		if d.Health.State != "passed" || d.Health.Simulated {
			e.State, e.Message = "failed", "Real workload health evidence is required before readiness."
			return
		}
		e.State, e.Message = "ready", "Reviewed source is running and workload health checks passed. No production route was created."
	case core.DeploymentFailed, core.DeploymentCancelled:
		e.State, e.Message = "failed", "Deployment did not finish successfully. Inspect the deployment and runtime; the finite cleanup deadline still applies."
	default:
		e.State, e.Message = "deploying", "Accepted deployment is waiting for the assigned runtime."
		if errors.Is(jobErr, store.ErrNotFound) {
			if err = a.deploy.DispatchAccepted(ctx, d.ID); err != nil {
				e.Message = "Dispatch is unavailable; the accepted operation will be reconciled."
			}
		}
	}
}
