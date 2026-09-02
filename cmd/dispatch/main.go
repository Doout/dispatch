package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/config"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/doout/dispatch/internal/store"
)

func main() {
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
	var executor deploy.Executor = deploy.SimulationExecutor{}
	if cfg.Executor == "docker" {
		runtime := deploy.RuntimeExecutor{Default: deploy.DockerExecutor{}, Helm: deploy.HelmExecutor{}}
		snapshots := deploy.SnapshotExecutor{Next: runtime, Store: data}
		executor = deploy.HookExecutor{Next: snapshots, Outputs: data, Vault: vault, Resolver: secretResolver}
	}
	executor = deploy.SourceAuthExecutor{Next: executor, Secrets: data, Vault: vault, Resolver: secretResolver, GitHubApps: githubApps}
	deployments := deploy.NewService(data, executor)
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	controller := api.New(data, deployments, cfg.Demo, api.AuthConfig{
		AdminToken: cfg.AdminToken, Username: cfg.AdminUsername, Password: cfg.AdminPassword, PublicURL: cfg.PublicURL,
	}, logger, api.EventConfig{WebhookSecret: cfg.WebhookSecret, DefaultCommand: cfg.PreviewCommand,
		GitHubAPIURL: cfg.GitHubAPIURL, GitHubToken: cfg.GitHubToken, Vault: vault, GitHubApps: githubApps, SecretResolver: secretResolver,
		Edge: edgeBroker, RepositoryCache: cfg.RepositoryCache})
	go controller.RunRelayConsumers(shutdownCtx)
	go controller.RunWorkflowPoller(shutdownCtx)
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
