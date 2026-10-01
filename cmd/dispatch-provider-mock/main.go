package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8091", "HTTP listen address; expose only on an isolated test network")
	state := flag.String("state", "", "private state file for restart tests; empty keeps state in memory")
	polls := flag.Int("polls", 2, "operation polls before completion, from 1 to 1000")
	fault := flag.String("fault", "", "transport fault: unavailable, malformed or incompatible")
	failCreate := flag.Bool("fail-create", false, "fail accepted create operations")
	failDelete := flag.Bool("fail-delete", false, "fail accepted delete operations")
	flag.Parse()
	adapter, err := mock.New(mock.Options{StateFile: *state, Polls: *polls, FailCreate: *failCreate, FailDelete: *failDelete})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	handler, err := mock.FaultHandler(provider.Handler(adapter, os.Getenv("DISPATCH_PROVIDER_TOKEN")), *fault)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Fprintln(os.Stderr, "Mock provider listening; no infrastructure resources will be created")
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "Mock provider listener failed")
		os.Exit(1)
	}
}
