package main

import (
	"context"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/doout/dispatch/internal/certificatesync"
	"github.com/doout/dispatch/internal/installation"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		_, err := fmt.Fprintln(out, "Dispatch certificate sync", installation.Version)
		return err
	}
	flags := flag.NewFlagSet("dispatch-certificate-sync", flag.ContinueOnError)
	flags.SetOutput(out)
	config := certificatesync.Config{}
	flags.StringVar(&config.Origin, "origin", "", "tenant console HTTPS origin")
	flags.StringVar(&config.TokenFile, "token-file", "", "private file with the tenant certificate credential")
	flags.StringVar(&config.Directory, "directory", "", "private directory containing the current certificate bundle")
	issuerCA := flags.String("issuer-ca-file", "", "additional certificate issuer root PEM for testing; HTTPS verification is unchanged")
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := flags.Args()
	if len(command) == 0 {
		return errors.New("provide an ingress validation and reload command after --")
	}
	if *issuerCA != "" {
		raw, err := os.ReadFile(*issuerCA)
		if err != nil {
			return err
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return err
		}
		if !roots.AppendCertsFromPEM(raw) {
			return errors.New("issuer CA file contains no certificates")
		}
		config.Roots = roots
	}
	config.Reload = func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, command[0], command[1:]...)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		return cmd.Run()
	}
	changed, err := certificatesync.Sync(ctx, config)
	if err != nil {
		return err
	}
	if changed {
		_, err = fmt.Fprintln(out, "Workload certificate installed.")
	} else {
		_, err = fmt.Fprintln(out, "Workload certificate unchanged.")
	}
	return err
}
