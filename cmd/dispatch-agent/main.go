package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type status struct {
	APIVersion    string `json:"apiVersion"`
	AgentVersion  string `json:"agentVersion"`
	Architecture  string `json:"architecture"`
	DockerReady   bool   `json:"dockerReady"`
	DockerVersion string `json:"dockerVersion,omitempty"`
	CheckedAt     string `json:"checkedAt"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	addr := env("DISPATCH_AGENT_ADDR", "127.0.0.1:9090")
	token := os.Getenv("DISPATCH_AGENT_TOKEN")
	if token == "" && !strings.HasPrefix(addr, "127.0.0.1:") {
		logger.Error("DISPATCH_AGENT_TOKEN is required when binding beyond loopback")
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /v1/status", authorize(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		output, err := exec.CommandContext(r.Context(), "docker", "version", "--format", "{{.Server.Version}}").Output()
		item := status{APIVersion: "dispatch.agent/v1", AgentVersion: "dev", Architecture: runtime.GOOS + "/" + runtime.GOARCH, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
		if err == nil {
			item.DockerReady, item.DockerVersion = true, strings.TrimSpace(string(output))
		}
		write(w, http.StatusOK, item)
	})))
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	logger.Info("dispatch agent listening", "addr", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func authorize(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
				write(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func write(w http.ResponseWriter, statusCode int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(value)
}
func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
