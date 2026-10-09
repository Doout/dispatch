package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/hosted"
	"github.com/doout/dispatch/internal/hostedruntime"
	"github.com/doout/dispatch/internal/installation"
	"github.com/doout/dispatch/internal/publichttp"
	"github.com/doout/dispatch/internal/tenancy"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, logger); err != nil {
		logger.Error("Hosted controller stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer, logger *slog.Logger) error {
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version") {
		_, err := fmt.Fprintln(out, "Dispatch platform", installation.Version)
		return err
	}
	if len(args) > 0 && args[0] == "bootstrap-user" {
		return bootstrap(ctx, args[1:], out)
	}
	if len(args) > 0 && args[0] != "serve" {
		return errors.New("usage: dispatch-platform [serve|bootstrap-user|version]")
	}
	config, err := readConfiguration()
	if err != nil {
		return err
	}
	http.DefaultTransport = publichttp.Transport()
	unlock, err := lockDirectory(config.Hosted.DataDirectory)
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	catalog, err := tenancy.Open(ctx, config.CatalogURL)
	if err != nil {
		return errors.New("cannot open hosted catalog")
	}
	defer catalog.Close()
	var leaseLost atomic.Bool
	release, err := catalog.AcquireControllerLease(ctx, func() { leaseLost.Store(true); cancel() })
	if err != nil {
		return err
	}
	defer release()
	if err = catalog.Migrate(ctx); err != nil {
		return errors.New("cannot migrate hosted catalog")
	}
	factory, err := tenancy.NewFactory(tenancy.FactoryConfig{RootDir: filepath.Join(config.Hosted.DataDirectory, "tenants"), PostgresAdminURL: config.PostgresAdminURL})
	if err != nil {
		return err
	}
	defer factory.Close()
	server, err := hosted.New(ctx, config.Hosted, catalog, func(ctx context.Context, tenant tenancy.Tenant, auth *api.HostedAuth) (*hosted.TenantRuntime, error) {
		runtime, err := factory.Open(ctx, tenant.ID)
		if err != nil {
			return nil, err
		}
		return hostedruntime.New(ctx, runtime, tenant, auth, config.Hosted.TenantOrigin(tenant.Slug), logger)
	}, logger)
	if err != nil {
		return err
	}
	defer server.Close()
	if err = server.Prepare(ctx); err != nil {
		return errors.New("cannot prepare authoritative DNS zone")
	}
	loopsDone := make(chan struct{})
	go func() { defer close(loopsDone); server.Run(ctx) }()
	defer func() { cancel(); <-loopsDone }()
	httpServer := &http.Server{Addr: config.Address, Handler: server, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	switch config.TLSMode {
	case "acme":
		var fallback *tls.Certificate
		if config.CertificateFile != "" {
			certificate, loadErr := tls.LoadX509KeyPair(config.CertificateFile, config.KeyFile)
			if loadErr != nil {
				return errors.New("cannot load bootstrap TLS certificate")
			}
			fallback = &certificate
		}
		httpServer.TLSConfig.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			certificate, err := server.GetCertificate(hello)
			if err != nil && fallback != nil && hello.ServerName == config.Hosted.RootDomain {
				return fallback, nil
			}
			return certificate, err
		}
	}
	failures := make(chan error, 1)
	go func() {
		if config.TLSMode == "proxy" {
			failures <- httpServer.ListenAndServe()
			return
		}
		if config.TLSMode == "acme" {
			failures <- httpServer.ListenAndServeTLS("", "")
			return
		}
		failures <- httpServer.ListenAndServeTLS(config.CertificateFile, config.KeyFile)
	}()
	logger.Info("Hosted controller listening", "address", config.Address, "domain", config.Hosted.RootDomain)
	select {
	case err = <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if err = httpServer.Shutdown(shutdown); err != nil {
			_ = httpServer.Close()
			return err
		}
	}
	if leaseLost.Load() {
		return errors.New("hosted catalog controller lease was lost")
	}
	return nil
}

func lockDirectory(root string) (func(), error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("hosted data directory must be private and must not be a symlink")
	}
	f, err := os.OpenFile(filepath.Join(root, "controller.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another hosted controller is using this directory")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func bootstrap(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("bootstrap-user", flag.ContinueOnError)
	flags.SetOutput(out)
	email := flags.String("email", "", "initial administrator email")
	name := flags.String("name", "", "initial administrator name")
	passwordFile := flags.String("password-file", "", "private file containing the initial password")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *email == "" || *name == "" || *passwordFile == "" {
		return errors.New("bootstrap-user requires --email, --name and --password-file")
	}
	password, err := privateFile(*passwordFile)
	if err != nil {
		return err
	}
	if len(password) < 12 || len(password) > 72 {
		return errors.New("initial password must contain 12 to 72 bytes")
	}
	root, url, err := catalogConfiguration()
	if err != nil {
		return err
	}
	unlock, err := lockDirectory(root)
	if err != nil {
		return err
	}
	defer unlock()
	catalog, err := tenancy.Open(ctx, url)
	if err != nil {
		return errors.New("cannot open hosted catalog")
	}
	defer catalog.Close()
	release, err := catalog.AcquireControllerLease(ctx, nil)
	if err != nil {
		return err
	}
	defer release()
	if err = catalog.Migrate(ctx); err != nil {
		return errors.New("cannot migrate hosted catalog")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err = catalog.BootstrapUser(ctx, tenancy.User{Email: *email, Name: *name, PasswordHash: string(hash)}); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Initial platform administrator created. No tenant membership was granted.")
	return err
}
