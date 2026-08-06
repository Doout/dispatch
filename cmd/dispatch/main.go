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
	if _, err := secretcrypto.OpenFile(cfg.MasterKeyFile); err != nil {
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
		executor = deploy.HookExecutor{Next: runtime, Outputs: data}
	}
	deployments := deploy.NewService(data, executor)
	server := &http.Server{
		Addr: cfg.Addr, Handler: api.New(data, deployments, cfg.Demo, api.AuthConfig{
			AdminToken: cfg.AdminToken, Username: cfg.AdminUsername, Password: cfg.AdminPassword,
		}, logger, api.EventConfig{WebhookSecret: cfg.WebhookSecret, DefaultCommand: cfg.PreviewCommand,
			GitHubAPIURL: cfg.GitHubAPIURL, GitHubToken: cfg.GitHubToken}),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second,
	}
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
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
