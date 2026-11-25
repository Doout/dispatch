package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/doout/dispatch/internal/automationcli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(automationcli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
