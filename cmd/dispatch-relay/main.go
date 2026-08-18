package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/relay"
	"golang.org/x/crypto/acme/autocert"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("relay stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	token := strings.TrimSpace(os.Getenv("DISPATCH_RELAY_TOKEN"))
	publicURL := strings.TrimRight(strings.TrimSpace(os.Getenv("DISPATCH_RELAY_PUBLIC_URL")), "/")
	if len(token) < 24 {
		return errors.New("DISPATCH_RELAY_TOKEN must contain at least 24 characters")
	}
	if publicURL == "" {
		return errors.New("DISPATCH_RELAY_PUBLIC_URL is required")
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = "relay.db"
	}
	addr := strings.TrimSpace(os.Getenv("DISPATCH_RELAY_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:8090"
	}
	store, err := relay.OpenStore(context.Background(), databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	server := &http.Server{Addr: addr, Handler: relay.NewServer(store, token, publicURL), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	var challengeServer *http.Server
	tlsMode := strings.ToLower(strings.TrimSpace(os.Getenv("DISPATCH_RELAY_TLS_MODE")))
	if tlsMode == "auto" {
		parsed, parseErr := url.Parse(publicURL)
		if parseErr != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
			return errors.New("automatic TLS requires an https DISPATCH_RELAY_PUBLIC_URL")
		}
		cache := strings.TrimSpace(os.Getenv("DISPATCH_RELAY_ACME_CACHE"))
		if cache == "" {
			cache = "/data/acme"
		}
		manager := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(parsed.Hostname()), Cache: autocert.DirCache(cache)}
		server.TLSConfig = manager.TLSConfig()
		challengeServer = &http.Server{Addr: ":80", Handler: manager.HTTPHandler(nil), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
		go func() {
			if challengeErr := challengeServer.ListenAndServe(); challengeErr != nil && !errors.Is(challengeErr, http.ErrServerClosed) {
				logger.Error("ACME challenge server stopped", "error", challengeErr)
			}
		}()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		if challengeServer != nil {
			_ = challengeServer.Shutdown(shutdown)
		}
	}()
	logger.Info("webhook relay listening", "addr", addr, "public_url", publicURL, "tls_mode", tlsMode)
	if tlsMode == "auto" {
		err = server.ListenAndServeTLS("", "")
	} else {
		err = server.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
