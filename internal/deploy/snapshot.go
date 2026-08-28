package deploy

import (
	"context"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

type DeploymentSnapshotStore interface {
	UpdateDeploymentSnapshot(context.Context, string, core.DeploymentSnapshot) error
}

type SnapshotExecutor struct {
	Next  Executor
	Store DeploymentSnapshotStore
}

func (e SnapshotExecutor) Deploy(ctx context.Context, deployment core.Deployment, app core.App, server core.Server, progress Progress) error {
	values, err := helmValues(app)
	if err != nil {
		return err
	}
	namespace := app.HelmNamespace
	if namespace == "" && server.Kubernetes != nil {
		namespace = server.Kubernetes.Namespace
	}
	if namespace == "" {
		namespace = "default"
	}
	release := app.HelmRelease
	if release == "" {
		release = app.Name
	}
	snapshot := core.DeploymentSnapshot{TargetID: server.ID, TargetName: server.Name, Runtime: server.Runtime, Namespace: namespace, Release: release, Chart: app.HelmChart, Values: redactSnapshotValues("", values).(map[string]any)}
	if e.Store != nil {
		if err := e.Store.UpdateDeploymentSnapshot(ctx, deployment.ID, snapshot); err != nil {
			return err
		}
	}
	return e.Next.Deploy(ctx, deployment, app, server, progress)
}

func (e SnapshotExecutor) Cleanup(ctx context.Context, app core.App, server core.Server, progress Progress) error {
	cleaner, ok := e.Next.(CleanupExecutor)
	if !ok {
		return ErrCleanupUnsupported
	}
	return cleaner.Cleanup(ctx, app, server, progress)
}

func redactSnapshotValues(path string, value any) any {
	if object, ok := value.(map[string]interface{}); ok {
		result := make(map[string]any, len(object))
		for key, item := range object {
			next := key
			if path != "" {
				next = path + "." + key
			}
			if snapshotSensitive(next) {
				result[key] = "••••••••"
			} else {
				result[key] = redactSnapshotValues(next, item)
			}
		}
		return result
	}
	if items, ok := value.([]interface{}); ok {
		result := make([]any, len(items))
		for index, item := range items {
			result[index] = redactSnapshotValues(path, item)
		}
		return result
	}
	return value
}

func snapshotSensitive(path string) bool {
	lower := strings.ToLower(path)
	for _, part := range []string{"password", "passwd", "secret", "token", "apikey", "api_key", "privatekey", "private_key", "credential"} {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}
