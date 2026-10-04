package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/provider"
)

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:8091", "provider HTTP(S) endpoint")
	fixture := flag.String("request", "", "path to a create-server JSON fixture; omitted uses public mock options")
	serverActions := flag.Bool("server-actions", false, "also exercise optional VM power controls and clone promotion with disposable resources")
	snapshots := flag.Bool("snapshots", false, "also capture and delete a retained snapshot and create and delete an isolated clone")
	allow := flag.Bool("allow-mutations", false, "allow the suite to create and delete a disposable server")
	timeout := flag.Duration("timeout", 2*time.Minute, "lifecycle timeout; failure cleanup gets a separate timeout")
	interval := flag.Duration("poll-interval", 100*time.Millisecond, "operation polling interval")
	flag.Parse()
	if !*allow {
		fmt.Fprintln(os.Stderr, "Conformance creates and deletes test servers; --snapshots also captures and deletes a retained snapshot and isolated clone; --server-actions changes test machine power and promotes a disposable clone. Use an isolated test account and pass --allow-mutations.")
		os.Exit(2)
	}
	if *timeout <= 0 || *interval <= 0 {
		fmt.Fprintln(os.Stderr, "Timeout and poll interval must be positive")
		os.Exit(2)
	}
	request := provider.CreateServerRequest{Name: "conformance", Region: "mock-region", Size: "mock-small", Image: "mock-linux", Network: "mock-private", SSHKey: "public-mock-key", ProviderConfig: map[string]any{}}
	if *fixture != "" {
		request = provider.CreateServerRequest{}
		file, err := os.Open(*fixture)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Cannot open the conformance request file")
			os.Exit(2)
		}
		decoder := json.NewDecoder(io.LimitReader(file, provider.MaxMessageBytes+1))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&request)
		if err == nil && decoder.Decode(new(any)) != io.EOF {
			err = fmt.Errorf("trailing request data")
		}
		_ = file.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Invalid conformance request file")
			os.Exit(2)
		}
	}
	client, err := provider.NewClient(*endpoint, os.Getenv("DISPATCH_PROVIDER_TOKEN"), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	report, err := provider.RunConformance(ctx, client, provider.ConformanceOptions{Request: request, Timeout: *timeout, PollInterval: *interval})
	if err == nil && *snapshots {
		snapshotReport, snapshotErr := provider.RunSnapshotConformance(ctx, client, provider.ConformanceOptions{Request: request, Timeout: *timeout, PollInterval: *interval})
		report.Checks = append(report.Checks, snapshotReport.Checks...)
		err = snapshotErr
	}
	if err == nil && *serverActions {
		actionReport, actionErr := provider.RunServerActionConformance(ctx, client, provider.ConformanceOptions{Request: request, Timeout: *timeout, PollInterval: *interval})
		report.Checks = append(report.Checks, actionReport.Checks...)
		err = actionErr
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
