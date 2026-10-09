package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/doout/dispatch/internal/authoritativedns"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("DNS server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	path := strings.TrimSpace(os.Getenv("DISPATCH_DNS_SNAPSHOT"))
	server, err := authoritativedns.NewServer(path)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	syncURL := strings.TrimSpace(os.Getenv("DISPATCH_DNS_SYNC_URL"))
	tokenFile := strings.TrimSpace(os.Getenv("DISPATCH_DNS_TOKEN_FILE"))
	if (syncURL == "") != (tokenFile == "") {
		return errors.New("set both DNS sync URL and token file")
	}
	if syncURL != "" {
		info, err := os.Stat(tokenFile)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("DNS token file must be a private regular file")
		}
		raw, err := os.ReadFile(tokenFile)
		if err != nil {
			return err
		}
		poller := authoritativedns.Poller{URL: syncURL, Token: strings.TrimSpace(string(raw)), Server: server, OnError: func(err error) { logger.Warn("DNS snapshot refresh failed", "error", err) }}
		// A failed refresh preserves the durable last accepted zone. A fresh
		// replica without one returns SERVFAIL rather than inventing records.
		if err = poller.Sync(ctx); err != nil {
			logger.Warn("Initial DNS snapshot refresh failed", "error", err)
		}
		go poller.Run(ctx)
	}
	address := strings.TrimSpace(os.Getenv("DISPATCH_DNS_ADDR"))
	if address == "" {
		address = "127.0.0.1:5353"
	}
	logger.Info("Starting authoritative DNS", "address", address)
	return server.Serve(ctx, address)
}
