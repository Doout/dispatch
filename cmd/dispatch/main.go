package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/analytics"
	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/bootstrap"
	"github.com/doout/dispatch/internal/config"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/drift"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/installation"
	"github.com/doout/dispatch/internal/provision"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/doout/dispatch/internal/store"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if handled, err := installation.Run(ctx, os.Args[1:], os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("dispatch stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	vault, err := secretcrypto.OpenFile(cfg.MasterKeyFile)
	if err != nil {
		return err
	}
	ctx := context.Background()
	data, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer data.Close()
	if err := data.Migrate(ctx); err != nil {
		return err
	}
	if cfg.Demo {
		if err := data.SeedDemo(ctx); err != nil {
			return err
		}
	}
	githubApps := githubapp.New(data, vault)
	secretResolver := secretvalue.New(data, vault)
	edgeBroker := edge.New(data, vault)
	githubApps.Edge = edgeBroker
	secretResolver.Edge = edgeBroker
	local, err := data.ReconcileLocalDockerServer(ctx, dockerSocketAvailable(cfg.DockerSocket))
	if err != nil {
		return err
	}
	if local != nil {
		logger.Info("local Docker server reconciled", "server", local.Name, "state", local.State, "socket", cfg.DockerSocket)
	}
	runtimeBroker := &remoteruntime.Broker{Store: data, Vault: vault}
	var executor deploy.Executor = deploy.SimulationExecutor{}
	dockerExecutor := deploy.DockerExecutor{Artifacts: data, Vault: vault, ArtifactDirectory: filepath.Join(filepath.Dir(cfg.MasterKeyFile), "runtime-artifacts")}
	if cfg.RoutingDirectory != "" {
		dockerExecutor.Routes = &routing.FilePublisher{Directory: cfg.RoutingDirectory}
	}
	if cfg.Executor == "docker" {
		helmExecutor := deploy.HelmExecutor{}
		if vault != nil {
			helmExecutor.Capture = drift.New(data, vault).Capture
		}
		remote := deploy.RemoteExecutor{Local: dockerExecutor, Broker: runtimeBroker}
		runtime := deploy.RuntimeExecutor{Default: remote, Helm: helmExecutor}
		snapshots := deploy.SnapshotExecutor{Next: runtime, Store: data}
		executor = deploy.HookExecutor{Next: snapshots, Outputs: data, Vault: vault, Resolver: secretResolver}
	}
	sourceAuth := deploy.SourceAuthExecutor{Next: executor, Secrets: data, Vault: vault, Resolver: secretResolver, GitHubApps: githubApps}
	executor = sourceAuth
	deployments := deploy.NewService(data, executor)
	deployments.Retention = deploy.SimulationRetention{Store: data}
	if cfg.Executor == "docker" {
		deployments.Storage.Backend = deploy.RemoteStorageBackend{Local: deploy.RuntimeStorage{}, Broker: runtimeBroker}
		deployments.Retention = deploy.RemoteRetentionBackend{Local: dockerExecutor, Broker: runtimeBroker}
	}
	if cfg.Executor == "docker" {
		deployments.ConfigureHelmComparison(sourceAuth)
		deployments.ConfigureSourceResolution(sourceAuth)
		deployments.ConfigureRuntimeRollback(deploy.RemoteExecutor{Local: dockerExecutor, Broker: runtimeBroker})
	}
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go runtimeBroker.RunExpiration(shutdownCtx, logger)
	bootstraps := bootstrap.Configured(data, vault, cfg.PublicURL)
	go bootstraps.Run(shutdownCtx, logger)
	infrastructure := &provision.Manager{Bootstrap: bootstraps, Store: data, Secrets: secretResolver, Edge: edgeBroker, Vault: vault, Admission: data.InfrastructureQuotaAdmission}
	go infrastructure.Run(shutdownCtx, logger)
	var history analytics.Reader
	if cfg.AnalyticsEnabled {
		worker := analytics.New(data, cfg.AnalyticsDirectory, logger)
		history = worker
		workerCtx, workerCancel := context.WithCancel(shutdownCtx)
		workerDone := make(chan struct{})
		go func() { defer close(workerDone); worker.Run(workerCtx) }()
		defer func() { workerCancel(); <-workerDone }()
	}
	controller := api.New(data, deployments, cfg.Demo, api.AuthConfig{
		AdminToken: cfg.AdminToken, Username: cfg.AdminUsername, Password: cfg.AdminPassword, PublicURL: cfg.PublicURL, TrustedProxyCIDRs: cfg.TrustedProxyCIDRs,
	}, logger, api.EventConfig{Bootstrap: bootstraps, WebhookSecret: cfg.WebhookSecret, DefaultCommand: cfg.PreviewCommand,
		GitHubAPIURL: cfg.GitHubAPIURL, GitHubToken: cfg.GitHubToken, Vault: vault, GitHubApps: githubApps, SecretResolver: secretResolver,
		WorkloadBackupDirectory: filepath.Join(filepath.Dir(cfg.MasterKeyFile), "workload-backups"), BackupDirectory: filepath.Join(filepath.Dir(cfg.MasterKeyFile), "backups"), MasterKeyFile: cfg.MasterKeyFile, DatabaseURL: cfg.DatabaseURL,
		Edge: edgeBroker, RepositoryCache: cfg.RepositoryCache, Analytics: history})
	if err := controller.RecoverWorkflowWork(ctx); err != nil {
		return err
	}
	go deployments.RunRecovery(shutdownCtx, logger)
	go deployments.RunRouteReconciliation(shutdownCtx)
	if cfg.Executor == "docker" {
		go controller.RunObservations(shutdownCtx)
	}
	go controller.RunRelayConsumers(shutdownCtx)
	go controller.RunWorkflowPoller(shutdownCtx)
	go controller.RunPreviewPoller(shutdownCtx)
	go controller.RunWebhookProcessor(shutdownCtx)
	go controller.RunWorkflowChecks(shutdownCtx)
	go controller.RunPreviewExpirer(shutdownCtx)
	go controller.RunTemporaryEnvironments(shutdownCtx)
	go controller.RunWorkloadBackupVerification(shutdownCtx)
	server := &http.Server{
		Addr: cfg.Addr, Handler: controller,
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second,
	}
	go func() {
		<-shutdownCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = api.Shutdown(ctx, server)
	}()
	logger.Info("dispatch controller listening", "addr", cfg.Addr, "executor", cfg.Executor, "demo", cfg.Demo)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func dockerSocketAvailable(path string) bool {
	info, err := os.Stat(filepath.Clean(path))
	return err == nil && info.Mode()&os.ModeSocket != 0
}
