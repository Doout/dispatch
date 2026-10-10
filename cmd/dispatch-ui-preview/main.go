package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/uipreview"
)

func main() {
	handler, err := uipreview.New(uipreview.Config{
		Origin:      os.Getenv("DISPATCH_UI_PREVIEW_ORIGIN"),
		Development: os.Getenv("DISPATCH_UI_PREVIEW_DEVELOPMENT"),
		Production:  os.Getenv("DISPATCH_UI_PREVIEW_PRODUCTION"),
	})
	if err != nil {
		slog.Error("UI preview configuration is invalid", "error", err)
		os.Exit(1)
	}
	address := os.Getenv("DISPATCH_UI_PREVIEW_ADDR")
	if address == "" {
		address = "127.0.0.1:8082"
	}
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("UI preview listening", "address", address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("UI preview stopped")
		os.Exit(1)
	}
}
