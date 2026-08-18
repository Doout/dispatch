package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestRelayPersistsAndReplaysUnacknowledgedDelivery(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "relay.db")
	store, err := OpenStore(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(store, "this-is-a-long-relay-token", "https://relay.example.test"))
	client := Client{BaseURL: server.URL, Token: "this-is-a-long-relay-token", HTTP: server.Client()}
	hook, err := client.CreateHook(ctx, "GitHub events")
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/hooks/"+hook.ID, bytes.NewBufferString(`{"action":"created"}`))
	request.Header.Set("X-Provider-Delivery", "delivery-1")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("receive status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	first, err := client.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || first.HookID != hook.ID || first.Attempt != 1 {
		t.Fatalf("unexpected first delivery: %#v", first)
	}
	if string(first.Body) != `{"action":"created"}` || len(first.Headers["X-Provider-Delivery"]) == 0 || first.Headers["X-Provider-Delivery"][0] != "delivery-1" {
		t.Fatalf("payload was not preserved: %#v", first)
	}

	server.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Expire the lease to simulate a controller that disconnected before ACK.
	if _, err := store.db.Exec(`UPDATE relay_deliveries SET lease_until=?`, stamp(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(NewServer(store, "this-is-a-long-relay-token", "https://relay.example.test"))
	defer server.Close()
	client = Client{BaseURL: server.URL, Token: "this-is-a-long-relay-token", HTTP: server.Client()}
	replayed, err := client.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if replayed == nil || replayed.ID != first.ID || replayed.Attempt != 2 {
		t.Fatalf("delivery was not replayed: %#v", replayed)
	}
	if err := client.Acknowledge(ctx, *replayed, "ack", ""); err != nil {
		t.Fatal(err)
	}
	status, err := store.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Pending != 0 {
		t.Fatalf("pending = %d, want 0", status.Pending)
	}
}

func TestRelayRejectsManagementWithoutToken(t *testing.T) {
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := httptest.NewServer(NewServer(store, "this-is-a-long-relay-token", serverURLPlaceholder))
	defer server.Close()
	response, err := http.Post(server.URL+"/api/v1/hooks", "application/json", bytes.NewBufferString(`{"name":"events"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(response.Body).Decode(&body)
	if body["error"] == "" {
		t.Fatal("missing authentication error")
	}
}

const serverURLPlaceholder = "https://relay.example.test"
