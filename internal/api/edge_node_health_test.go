package api

import (
	"context"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestEdgeHeartbeatPreservesOwnerChangesAfterAuthentication(t *testing.T) {
	for _, mode := range []string{"", "managed"} {
		t.Run("mode_"+mode, func(t *testing.T) {
			a, snapshot, session, _ := runtimeAPIFixture(t)
			ctx := context.Background()
			snapshot.Config = map[string]string{"workflowMode": "tenant", "workflowProjectId": "old-project"}
			if err := a.store.UpdatePrivateNetwork(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
			// Authentication has already loaded snapshot when the owner edits the
			// node. The delayed heartbeat must not restore that older assignment.
			current, err := a.store.GetPrivateNetwork(ctx, snapshot.ID)
			if err != nil {
				t.Fatal(err)
			}
			current.Name = "Renamed worker"
			current.Config = map[string]string{"workflowMode": mode, "workflowProjectId": "new-project"}
			current.TokenHash = "new-owner-token-hash"
			current.EncryptedCredentials = "new-owner-ciphertext"
			current.Details = map[string]string{"ownerNote": "keep", "runtimeVersion": "newer-runtime"}
			if err := a.store.UpdatePrivateNetwork(ctx, current); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("GET", "/", nil)
			request.Header.Set("Authorization", "Bearer "+session.Token)
			a.touchEdgeNode(ctx, snapshot, request, map[string]string{"workerVersion": "test-worker-version"})
			saved, err := a.store.GetPrivateNetwork(ctx, snapshot.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Name != current.Name || !reflect.DeepEqual(saved.Config, current.Config) || saved.TokenHash != current.TokenHash || saved.EncryptedCredentials != current.EncryptedCredentials {
				t.Fatal("stale heartbeat restored owner-controlled configuration or credentials")
			}
			if saved.Details["ownerNote"] != "keep" || saved.Details["runtimeVersion"] != "newer-runtime" || saved.Details["workerVersion"] != "test-worker-version" || saved.LastVerifiedAt == nil {
				t.Fatalf("health update lost current details: %#v", saved.Details)
			}
		})
	}
}
