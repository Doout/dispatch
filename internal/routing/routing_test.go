package routing

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func routeFixture(t *testing.T) (*FilePublisher, core.ApplicationRoute) {
	t.Helper()
	app := core.App{ID: "app", ProjectID: "project", ServerID: "server", BuildType: core.BuildTypeDockerfile, ContainerPort: 8080}
	server := core.Server{ID: "server", Routing: &core.RoutingConfig{BaseDomain: "apps.example.com", RequireTLS: true, TLSResolver: "letsencrypt"}}
	plan, err := Plan(core.Deployment{ID: "first"}, app, server)
	if err != nil {
		t.Fatal(err)
	}
	return &FilePublisher{Directory: t.TempDir()}, *plan
}
func TestRoutePreparationDoesNotExposeCandidateAndRetryIsStable(t *testing.T) {
	publisher, plan := routeFixture(t)
	ctx := context.Background()
	prepared, err := publisher.Prepare(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Destination != "" || prepared.DeploymentID != "" {
		t.Fatal("unready candidate exposed")
	}
	raw, err := os.ReadFile(filepath.Join(publisher.Directory, routeFile(plan.AppID)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "url:") || !strings.Contains(string(raw), "servers: []") {
		t.Fatal("placeholder has a live backend", string(raw))
	}
	if _, err = publisher.Prepare(ctx, plan); err != nil {
		t.Fatal(err)
	}
	ready, err := publisher.Promote(ctx, plan, LoopbackDestination(32123))
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != "published" || ready.Certificate.State != "pending" {
		t.Fatal("route claimed verified TLS", ready)
	}
	retry, err := publisher.Promote(ctx, plan, LoopbackDestination(32123))
	if err != nil || retry.PreviousDeploymentID != "" {
		t.Fatal("retry duplicated publication", retry, err)
	}
	other := plan
	other.AppID = "other"
	if _, err = publisher.Prepare(ctx, other); !errors.Is(err, ErrConflict) {
		t.Fatal("hostname takeover accepted", err)
	}
}
func TestCandidateFailureRetainsPreviousRouteAndRollback(t *testing.T) {
	publisher, plan := routeFixture(t)
	ctx := context.Background()
	if _, err := publisher.Prepare(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Promote(ctx, plan, LoopbackDestination(32001)); err != nil {
		t.Fatal(err)
	}
	second := plan
	second.RequestedDeploymentID = "second"
	prepared, err := publisher.Prepare(ctx, second)
	if err != nil || prepared.DeploymentID != "first" || prepared.Destination != LoopbackDestination(32001) {
		t.Fatal("preparing replaced live route", prepared, err)
	}
	// A failed readiness check never calls Promote.
	current, err := publisher.Read(ctx, plan.AppID)
	if err != nil || current.DeploymentID != "first" {
		t.Fatal("failed candidate lost healthy route", err)
	}
	if _, err = publisher.Promote(ctx, plan, LoopbackDestination(32002)); !errors.Is(err, ErrConflict) {
		t.Fatal("stale publication accepted", err)
	}
	published, err := publisher.Promote(ctx, second, LoopbackDestination(32002))
	if err != nil || published.PreviousDeploymentID != "first" {
		t.Fatal("old destination not retained", err)
	}
	rollback := plan
	rollback.RequestedDeploymentID = "rollback"
	if _, err = publisher.Prepare(ctx, rollback); err != nil {
		t.Fatal(err)
	}
	restored, err := publisher.Promote(ctx, rollback, published.PreviousDestination)
	if err != nil || restored.Destination != LoopbackDestination(32001) {
		t.Fatal("route rollback failed", err)
	}
}
func TestRouteRejectsTamperingAndInvalidScope(t *testing.T) {
	publisher, plan := routeFixture(t)
	ctx := context.Background()
	if _, err := publisher.Prepare(ctx, plan); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(publisher.Directory, routeFile(plan.AppID))
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("# operator edit\n")
	_ = file.Close()
	if _, err = publisher.Prepare(ctx, plan); !errors.Is(err, ErrConflict) {
		t.Fatal("edited proxy config silently overwritten", err)
	}
	for _, hostname := range []string{"example.com`)", "https://apps.example.com", "*.apps.example.com", "127.0.0.1", "bad..example.com"} {
		if _, err = NormalizeHostname(hostname); err == nil {
			t.Fatalf("invalid hostname accepted %q", hostname)
		}
	}
	if _, err = Plan(core.Deployment{ID: "d"}, core.App{ID: "a", Domain: "apps.example.com", ContainerPort: 80}, core.Server{}); err == nil {
		t.Fatal("public hostname accepted without proxy")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPublicEvidenceDistinguishesCertificateDelayRenewalAndRouteMismatch(t *testing.T) {
	_, route := routeFixture(t)
	route.DeploymentID = "first"
	now := time.Now().UTC()
	expiry := now.Add(3 * 24 * time.Hour)
	probe := Probe{Now: func() time.Time { return now }, LookupHost: func(context.Context, string) ([]string, error) { return []string{"192.0.2.10"}, nil }, Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &tls.CertificateVerificationError{Err: errors.New("issuer pending")}
	})}}
	checked := probe.Check(context.Background(), route)
	if checked.State == "active" || checked.Certificate.State != "invalid" || checked.DNS != "resolved" {
		t.Fatal("certificate delay reported live", checked)
	}
	probe.Client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("body must never be persisted")), Header: http.Header{"X-Dispatch-Deployment": []string{"first"}}, TLS: &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}, PeerCertificates: []*x509.Certificate{{NotAfter: expiry}}}}, nil
	})
	checked = probe.Check(context.Background(), route)
	if checked.State != "active" || checked.Certificate.State != "renewal_due" || checked.Certificate.ExpiresAt == nil {
		t.Fatal("valid renewal evidence missing", checked)
	}
	route.DeploymentID = "second"
	checked = probe.Check(context.Background(), route)
	if checked.State == "active" {
		t.Fatal("old route header accepted for new destination")
	}
}

func TestPreparationCannotChangeServingListenerOrTLSPolicy(t *testing.T) {
	publisher, plan := routeFixture(t)
	ctx := context.Background()
	if _, err := publisher.Prepare(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Promote(ctx, plan, LoopbackDestination(32001)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(publisher.Directory, routeFile(plan.AppID)))
	for _, change := range []func(*core.ApplicationRoute){
		func(r *core.ApplicationRoute) { r.EntryPoint = "other" },
		func(r *core.ApplicationRoute) { r.RequireTLS = false },
		func(r *core.ApplicationRoute) { r.TLSResolver = "other" },
	} {
		candidate := plan
		candidate.RequestedDeploymentID = "next"
		change(&candidate)
		if _, err := publisher.Prepare(ctx, candidate); !errors.Is(err, ErrConflict) {
			t.Fatal("serving policy changed before candidate readiness", err)
		}
		after, _ := os.ReadFile(filepath.Join(publisher.Directory, routeFile(plan.AppID)))
		if string(before) != string(after) {
			t.Fatal("serving route was rewritten")
		}
	}
}
func TestPublicProxyFailureCannotBeLiveEvenWithDeploymentHeader(t *testing.T) {
	_, route := routeFixture(t)
	route.RequireTLS = false
	route.DeploymentID = "first"
	probe := Probe{LookupHost: func(context.Context, string) ([]string, error) { return []string{"192.0.2.10"}, nil }, Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 502, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"X-Dispatch-Deployment": []string{"first"}}}, nil
	})}}
	checked := probe.Check(context.Background(), route)
	if checked.State != "degraded" {
		t.Fatal("broken proxy claimed live", checked)
	}
}
