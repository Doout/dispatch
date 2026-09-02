package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestLanewayApplicationAndAuthorizationTransaction(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	application := core.LanewayApplication{
		ID: "application-1", Name: "Deployment controller", Authority: "https://lane.example.com",
		RemoteApplicationID: "remote-1", ClientID: "client-1", EncryptedClientSecret: "encrypted",
		State: "active", CreatedAt: now, UpdatedAt: now,
	}
	if err := data.CreateLanewayApplication(ctx, application); err != nil {
		t.Fatal(err)
	}
	loaded, err := data.GetActiveLanewayApplicationByAuthority(ctx, application.Authority)
	if err != nil || loaded.ID != application.ID || loaded.EncryptedClientSecret != "encrypted" {
		t.Fatalf("unexpected application: %#v %v", loaded, err)
	}

	transaction := core.LanewayAuthorizationTransaction{
		StateHash: "hashed-state", Kind: "installation", ConnectionName: "Private services",
		Authority: application.Authority, RedirectURI: "https://dispatch.example.com/callback",
		ApplicationID: application.ID, EncryptedCodeVerifier: "encrypted-verifier",
		ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now,
	}
	if err := data.CreateLanewayAuthorizationTransaction(ctx, transaction); err != nil {
		t.Fatal(err)
	}
	consumed, err := data.ConsumeLanewayAuthorizationTransaction(ctx, transaction.StateHash, now.Add(time.Minute))
	if err != nil || consumed.ApplicationID != application.ID || consumed.ConsumedAt == nil {
		t.Fatalf("unexpected transaction: %#v %v", consumed, err)
	}
	if _, err := data.ConsumeLanewayAuthorizationTransaction(ctx, transaction.StateHash, now.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected replay to fail, got %v", err)
	}
}

func TestLanewayRefreshLeaseIsExclusive(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	network := core.PrivateNetwork{ID: "network-1", Name: "Private services", Driver: "laneway_network", Config: map[string]string{}, Details: map[string]string{}, State: "ready", CreatedAt: now, UpdatedAt: now}
	if err := data.CreatePrivateNetwork(ctx, network); err != nil {
		t.Fatal(err)
	}
	if err := data.AcquireLanewayRefreshLease(ctx, network.ID, "lease-1", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := data.AcquireLanewayRefreshLease(ctx, network.ID, "lease-2", now, now.Add(time.Minute)); !errors.Is(err, ErrLanewayRefreshBusy) {
		t.Fatalf("expected active lease to block refresh, got %v", err)
	}
	if err := data.ReleaseLanewayRefreshLease(ctx, network.ID, "lease-1"); err != nil {
		t.Fatal(err)
	}
	if err := data.AcquireLanewayRefreshLease(ctx, network.ID, "lease-2", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}
