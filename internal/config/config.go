package config

import (
	"errors"
	"os"
	"strings"
)

type Config struct {
	Addr            string
	DatabaseURL     string
	Executor        string
	Demo            bool
	AdminToken      string
	AdminUsername   string
	AdminPassword   string
	MasterKeyFile   string
	DockerSocket    string
	WebhookSecret   string
	PreviewCommand  string
	GitHubAPIURL    string
	GitHubToken     string
	RepositoryCache string
}

func Load() (Config, error) {
	cfg := Config{
		Addr:            env("DISPATCH_ADDR", "127.0.0.1:8080"),
		DatabaseURL:     env("DATABASE_URL", "dispatch.db"),
		Executor:        env("DISPATCH_EXECUTOR", "simulation"),
		Demo:            strings.EqualFold(os.Getenv("DISPATCH_DEMO"), "true"),
		AdminToken:      os.Getenv("DISPATCH_ADMIN_TOKEN"),
		AdminUsername:   strings.TrimSpace(os.Getenv("DISPATCH_ADMIN_USERNAME")),
		AdminPassword:   os.Getenv("DISPATCH_ADMIN_PASSWORD"),
		MasterKeyFile:   os.Getenv("DISPATCH_MASTER_KEY_FILE"),
		DockerSocket:    env("DISPATCH_DOCKER_SOCKET", "/var/run/docker.sock"),
		WebhookSecret:   os.Getenv("DISPATCH_GITHUB_WEBHOOK_SECRET"),
		PreviewCommand:  env("DISPATCH_PREVIEW_COMMAND", "/preview"),
		GitHubAPIURL:    env("DISPATCH_GITHUB_API_URL", "https://api.github.com"),
		GitHubToken:     os.Getenv("DISPATCH_GITHUB_TOKEN"),
		RepositoryCache: env("DISPATCH_REPOSITORY_CACHE", "repository-cache"),
	}
	if cfg.Executor != "simulation" && cfg.Executor != "docker" {
		return Config{}, errors.New("DISPATCH_EXECUTOR must be simulation or docker")
	}
	if (cfg.AdminUsername == "") != (cfg.AdminPassword == "") {
		return Config{}, errors.New("DISPATCH_ADMIN_USERNAME and DISPATCH_ADMIN_PASSWORD must be set together")
	}
	if cfg.AdminUsername != "" && (len(cfg.AdminUsername) < 3 || len(cfg.AdminUsername) > 64 || strings.Contains(cfg.AdminUsername, ":")) {
		return Config{}, errors.New("DISPATCH_ADMIN_USERNAME must be 3 to 64 characters and cannot contain a colon")
	}
	if cfg.AdminPassword != "" && (len([]byte(cfg.AdminPassword)) < 12 || len([]byte(cfg.AdminPassword)) > 72) {
		return Config{}, errors.New("DISPATCH_ADMIN_PASSWORD must be 12 to 72 bytes")
	}
	if !strings.HasPrefix(cfg.PreviewCommand, "/") || strings.ContainsAny(cfg.PreviewCommand, " \t\r\n") || len(cfg.PreviewCommand) > 64 {
		return Config{}, errors.New("DISPATCH_PREVIEW_COMMAND must start with / and contain no whitespace")
	}
	return cfg, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
