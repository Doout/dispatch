package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestEdgeNodeHealthSQLite(t *testing.T) {
	testEdgeNodeHealth(t, filepath.Join(t.TempDir(), "health.db"))
}

func TestEdgeNodeHealthPostgres(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL to exercise PostgreSQL")
	}
	testEdgeNodeHealth(t, dsn)
}

func testEdgeNodeHealth(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	node := core.PrivateNetwork{ID: "health-node", Name: "Owner name", Driver: "dispatch_agent", Config: map[string]string{"workflowMode": "tenant", "workflowProjectId": "new-project"}, Details: map[string]string{"ownerNote": "keep", "enrollmentExpiresAt": "old", "runtimeVersion": "new-runtime"}, TokenHash: "legacy-hash", EncryptedCredentials: "new-ciphertext", State: "waiting", CreatedAt: now, UpdatedAt: now}
	if err = s.CreatePrivateNetwork(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateEdgeNodeHealth(ctx, node.ID, node.TokenHash, map[string]string{"version": "legacy"}, now); err != nil {
		t.Fatalf("legacy health: %v", err)
	}
	if err = s.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: node.ID, EnrollmentHash: "enrollment", EnrollmentExpiresAt: now.Add(time.Minute), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateEdgeNodeHealth(ctx, node.ID, node.TokenHash, nil, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("managed node accepted legacy health: %v", err)
	}
	if err = s.EnrollEdgeCredential(ctx, node.ID, "enrollment", "public-key", "session", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	patch := map[string]string{"credentialMode": "short_session", "workerVersion": "worker-v1", "workerDocker": "false"}
	if err = s.UpdateEdgeNodeHealth(ctx, node.ID, "session", patch, now); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetPrivateNetwork(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != node.Name || !reflect.DeepEqual(saved.Config, node.Config) || saved.EncryptedCredentials != node.EncryptedCredentials || saved.TokenHash != node.TokenHash {
		t.Fatal("health update changed owner-controlled fields")
	}
	if saved.State != "ready" || saved.LastVerifiedAt == nil || saved.Details["workerVersion"] != "worker-v1" || saved.Details["ownerNote"] != "keep" || saved.Details["runtimeVersion"] != "new-runtime" || saved.Details["enrollmentExpiresAt"] != "" {
		t.Fatalf("incorrect merged health: %#v", saved.Details)
	}
	for _, transition := range []string{"expired", "rotated", "revoked"} {
		t.Run(transition, func(t *testing.T) {
			stamp := now
			if transition == "expired" {
				stamp = now.Add(2 * time.Minute)
			} else if transition == "rotated" {
				if err := s.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: node.ID, EnrollmentHash: "rotated", EnrollmentExpiresAt: now.Add(time.Minute), UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
			} else if err := s.RevokeEdgeCredential(ctx, node.ID, now); err != nil {
				t.Fatal(err)
			}
			before, _ := s.GetPrivateNetwork(ctx, node.ID)
			if err := s.UpdateEdgeNodeHealth(ctx, node.ID, "session", map[string]string{"version": "stale"}, stamp); !errors.Is(err, ErrNotFound) {
				t.Fatalf("stale session updated health: %v", err)
			}
			after, _ := s.GetPrivateNetwork(ctx, node.ID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("stale heartbeat changed the node")
			}
		})
	}
}
