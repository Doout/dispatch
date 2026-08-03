package config

import (
	"errors"
	"net"
	"os"
	"strings"
)

type Config struct {
	Addr          string
	DatabaseURL   string
	Executor      string
	Demo          bool
	AdminToken    string
	MasterKeyFile string
}

func Load() (Config, error) {
	cfg := Config{
		Addr:          env("DISPATCH_ADDR", "127.0.0.1:8080"),
		DatabaseURL:   env("DATABASE_URL", "dispatch.db"),
		Executor:      env("DISPATCH_EXECUTOR", "simulation"),
		Demo:          strings.EqualFold(os.Getenv("DISPATCH_DEMO"), "true"),
		AdminToken:    os.Getenv("DISPATCH_ADMIN_TOKEN"),
		MasterKeyFile: os.Getenv("DISPATCH_MASTER_KEY_FILE"),
	}
	if cfg.Executor != "simulation" && cfg.Executor != "docker" {
		return Config{}, errors.New("DISPATCH_EXECUTOR must be simulation or docker")
	}
	if !isLoopback(cfg.Addr) && cfg.AdminToken == "" {
		return Config{}, errors.New("DISPATCH_ADMIN_TOKEN is required when binding beyond loopback")
	}
	return cfg, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
