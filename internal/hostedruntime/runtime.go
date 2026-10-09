// Package hostedruntime builds operational services for one isolated tenant.
package hostedruntime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/doout/dispatch/internal/analytics"
	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/bootstrap"
	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/hosted"
	"github.com/doout/dispatch/internal/provision"
	"github.com/doout/dispatch/internal/publichttp"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/doout/dispatch/internal/tenancy"
	"github.com/doout/dispatch/internal/workflowrunner"
	"github.com/go-chi/chi/v5"
)

func New(ctx context.Context, runtime *tenancy.Runtime, tenant tenancy.Tenant, auth *api.HostedAuth, publicURL string, logger *slog.Logger) (*hosted.TenantRuntime, error) {
	if runtime == nil || runtime.Store == nil || auth == nil || auth.TenantID != tenant.ID || auth.Authenticate == nil {
		return nil, errors.New("tenant runtime requires its own store and membership authenticator")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := ensureKey(runtime.Paths.VaultPath); err != nil {
		return nil, err
	}
	vault, err := secretcrypto.OpenFile(runtime.Paths.VaultPath)
	if err != nil {
		return nil, err
	}
	data := runtime.Store
	github := githubapp.New(data, vault)
	github.Client = publichttp.Client()
	secrets := secretvalue.New(data, vault)
	secrets.Client = publichttp.Client()
	edgeBroker := edge.New(data, vault)
	github.Edge = edgeBroker
	secrets.Edge = edgeBroker
	runtimeBroker := &remoteruntime.Broker{Store: data, Vault: vault}
	workerBroker := &workflowrunner.Broker{Store: data, Vault: vault}
	hostedExecutor := &deploy.HostedExecutor{Runner: workerBroker, Store: data, Vault: vault, Resolver: secrets}
	remote := deploy.RemoteExecutor{Broker: runtimeBroker}
	execution := tenantExecutor{hosted: hostedExecutor, remote: remote}
	sourceAuth := deploy.SourceAuthExecutor{Next: execution, Secrets: data, Vault: vault, Resolver: secrets, GitHubApps: github}
	deployments := deploy.NewService(data, sourceAuth)
	storageBackend := deploy.HostedStorageBackend{Store: data, Runner: workerBroker, Docker: deploy.RemoteStorageBackend{Broker: runtimeBroker}}
	deployments.Storage.Backend = storageBackend
	deployments.Retention = deploy.RemoteRetentionBackend{Broker: runtimeBroker}
	deployments.ConfigureRuntimeRollback(remote)
	deployments.ConfigureHostedSourceResolution(sourceAuth)
	bootstraps := bootstrap.Configured(data, vault, publicURL)
	bootstraps.SSHInstall = func(context.Context, core.TargetBootstrap, bootstrap.SSHCredentials, string) error {
		return errors.New("hosted targets require outbound enrollment; install the worker on the target host")
	}
	infrastructure := &provision.Manager{RequireRelay: true, Bootstrap: bootstraps, Store: data, Secrets: secrets, Edge: edgeBroker, Vault: vault, Admission: data.InfrastructureQuotaAdmission}
	history := analytics.New(data, filepath.Join(runtime.Paths.DataDir, "analytics"), logger)
	controller := api.New(data, deployments, false, api.AuthConfig{PublicURL: publicURL, Hosted: auth}, logger, api.EventConfig{Bootstrap: bootstraps, Vault: vault, GitHubApps: github, SecretResolver: secrets, Edge: edgeBroker, RepositoryCache: runtime.Paths.CacheDir, Analytics: history, MasterKeyFile: runtime.Paths.VaultPath, DatabaseURL: runtime.DatabaseURL, BackupDirectory: filepath.Join(runtime.Paths.DataDir, "backups"), WorkloadBackupDirectory: filepath.Join(runtime.Paths.DataDir, "workload-backups")})
	if err = controller.ConfigureHostedExecution(hostedExecutor, workerBroker); err != nil {
		return nil, err
	}
	controller.ConfigureHostedTargetInspection(workerBroker)
	storageBackend.ConfigureBroker(workerBroker)
	if err = controller.RecoverWorkflowWork(ctx); err != nil {
		return nil, err
	}
	workerRoutes := chi.NewRouter()
	workerRoutes.Route("/api/v1", func(r chi.Router) { controller.MountWorkflowRunnerRoutes(r, workerBroker) })
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/edge/nodes/") && strings.Contains(r.URL.Path, "/workflow/jobs/") {
			workerRoutes.ServeHTTP(w, r)
			return
		}
		controller.ServeHTTP(w, r)
	})
	lifetime, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	start := func(run func(context.Context)) { workers.Add(1); go func() { defer workers.Done(); run(lifetime) }() }
	start(func(c context.Context) { runtimeBroker.RunExpiration(c, logger) })
	start(func(c context.Context) { bootstraps.Run(c, logger) })
	start(func(c context.Context) { infrastructure.Run(c, logger) })
	start(history.Run)
	start(func(c context.Context) { deployments.RunRecovery(c, logger) })
	start(deployments.RunRouteReconciliation)
	start(controller.RunObservations)
	start(controller.RunRelayConsumers)
	start(controller.RunWorkflowPoller)
	start(controller.RunPreviewPoller)
	start(controller.RunWebhookProcessor)
	start(controller.RunWorkflowChecks)
	start(controller.RunPreviewExpirer)
	start(controller.RunTemporaryEnvironments)
	start(controller.RunWorkloadBackupVerification)
	var closeOnce sync.Once
	return &hosted.TenantRuntime{Handler: handler, Store: data, Close: func() { closeOnce.Do(func() { cancel(); workers.Wait() }) }}, nil
}

// Docker applications without hooks use the existing durable target runtime so
// retained rollback, storage and routing keep their established receipts.
type tenantExecutor struct {
	hosted *deploy.HostedExecutor
	remote deploy.RemoteExecutor
}

func (e tenantExecutor) targetRuntime(app core.App, server core.Server) bool {
	return server.Runtime == core.ServerRuntimeDocker && server.AgentNodeID != "" && strings.TrimSpace(app.PreDeployHook) == "" && strings.TrimSpace(app.PostDeployHook) == ""
}
func (e tenantExecutor) Deploy(ctx context.Context, d core.Deployment, app core.App, server core.Server, p deploy.Progress) error {
	if e.targetRuntime(app, server) {
		return (deploy.SnapshotExecutor{Next: e.remote, Store: e.hosted.Store}).Deploy(ctx, d, app, server, p)
	}
	return e.hosted.Deploy(ctx, d, app, server, p)
}
func (e tenantExecutor) Cleanup(ctx context.Context, app core.App, server core.Server, p deploy.Progress) error {
	if e.targetRuntime(app, server) {
		return e.remote.Cleanup(ctx, app, server, p)
	}
	return e.hosted.Cleanup(ctx, app, server, p)
}
func (e tenantExecutor) RuntimeCapabilities(app core.App, server core.Server) runtimecontract.Manifest {
	if e.targetRuntime(app, server) {
		return e.remote.RuntimeCapabilities(app, server)
	}
	return e.hosted.RuntimeCapabilities(app, server)
}

func ensureKey(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("tenant vault requires an absolute key path")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("tenant vault key must be a private regular file")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	defer clear(key)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return ensureKey(path)
	}
	if err != nil {
		return err
	}
	if _, err = file.Write([]byte(base64.StdEncoding.EncodeToString(key))); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
