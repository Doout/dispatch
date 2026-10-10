package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/tenancy"
)

func TestOfflineAccountCommands(t *testing.T) {
	configFixture(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	initial := "initial-admin-password"
	recovered := "recovered-admin-password"
	ownerPassword := "initial-owner-password"
	password := privateTestFile(t, "initial-password", initial)
	ownerFile := privateTestFile(t, "owner-password", ownerPassword)
	createArgs := []string{"create-user", "--email", "owner@example.test", "--name", "Tenant owner", "--password-file", ownerFile}
	if err := run(ctx, createArgs, io.Discard, logger); err == nil {
		t.Fatal("created user before initial administrator")
	}
	if err := run(ctx, []string{"bootstrap-user", "--email", "admin@example.test", "--name", "Admin", "--password-file", password}, io.Discard, logger); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(ctx, createArgs, &output, logger); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, createArgs, io.Discard, logger); err == nil {
		t.Fatal("replaced existing account")
	}
	root, database, err := catalogConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := tenancy.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	owner, err := catalog.AuthenticatePassword(ctx, "owner@example.test", ownerPassword)
	if err != nil || !owner.EmailVerified || owner.PlatformAdmin {
		t.Fatal("invalid provisioned identity", owner, err)
	}
	if memberships, err := catalog.ListMemberships(ctx, owner.ID); err != nil || len(memberships) != 0 {
		t.Fatal("provisioning granted membership", err)
	}
	admin, err := catalog.AuthenticatePassword(ctx, "admin@example.test", initial)
	if err != nil {
		t.Fatal(err)
	}
	token, err := catalog.CreateSession(ctx, admin.ID, tenancy.AudiencePlatform, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	recoveryFile := privateTestFile(t, "recovery-password", recovered)
	recoverArgs := []string{"recover-admin", "--email", "admin@example.test", "--password-file", recoveryFile}
	unlock, err := lockDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, recoverArgs, io.Discard, logger); err == nil {
		t.Error("recovery ran with active controller lock")
	}
	if err := run(ctx, createArgs, io.Discard, logger); err == nil {
		t.Error("provisioning ran with active controller lock")
	}
	unlock()
	if err := run(ctx, recoverArgs, &output, logger); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.AuthenticatePassword(ctx, "admin@example.test", initial); !errors.Is(err, tenancy.ErrDenied) {
		t.Fatal("old password valid", err)
	}
	if _, err := catalog.AuthenticatePassword(ctx, "admin@example.test", recovered); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.AuthenticateSession(ctx, token, tenancy.AudiencePlatform); !errors.Is(err, tenancy.ErrExpired) {
		t.Fatal("old session valid", err)
	}
	if err := run(ctx, []string{"recover-admin", "--email", owner.Email, "--password-file", recoveryFile}, io.Discard, logger); err == nil {
		t.Fatal("recovered non-admin")
	}
	for _, secret := range []string{initial, recovered, ownerPassword} {
		if bytes.Contains(output.Bytes(), []byte(secret)) {
			t.Fatal("password printed")
		}
	}
	if err := os.Chmod(recoveryFile, 0644); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, recoverArgs, io.Discard, logger); err == nil {
		t.Fatal("public password file accepted")
	}
	if err := os.Chmod(ownerFile, 0644); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, createArgs, io.Discard, logger); err == nil {
		t.Fatal("public password file accepted")
	}
}
