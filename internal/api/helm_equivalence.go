package api

import (
	"context"
	"errors"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

// Only the guarded store read can turn an input mismatch into a current release.
// Successful deployments, snapshots, credentials, and observations stay intact.
func (a *API) currentHelmEquivalence(ctx context.Context, app core.App, server core.Server, deployment core.Deployment) (core.HelmEquivalence, bool, error) {
	data, ok := a.store.(interface {
		GetHelmEquivalence(context.Context, string) (core.HelmEquivalence, error)
	})
	if !ok || app.BuildType != core.BuildTypeHelm {
		return core.HelmEquivalence{}, false, nil
	}
	proof, err := data.GetHelmEquivalence(ctx, app.ID)
	if errors.Is(err, store.ErrNotFound) {
		return proof, false, nil
	}
	if err != nil {
		return proof, false, err
	}
	valid := proof.AppID == app.ID && proof.ProjectID == app.ProjectID && proof.AppName == app.Name && proof.AppSpecDigest == app.SpecDigest() && proof.ServerID == server.ID && proof.TargetDigest == core.HelmTargetDigest(server) && proof.DeploymentID == deployment.ID
	return proof, valid, nil
}

func evaluatedChartCommit(revision core.WorkflowRevision, commit string) bool {
	for _, source := range revision.Sources {
		if source.CommitSHA == commit {
			return true
		}
	}
	return false
}
