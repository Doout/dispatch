package privateaccess

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckLanewayReadsStatusAndRoutes(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "lanewayd.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/status":
			_, _ = w.Write([]byte(`{"running":true,"network_id":"network-1","name":"dispatch","selected_path":"wireguard-relay-quic","product_version":"1.2.3","controller":{"configuration_lease_expired":false}}`))
		case "/v1/routes":
			_, _ = w.Write([]byte(`[{"prefix":"10.20.0.0/16","via_node":"connector-1","kind":"private"}]`))
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = os.Remove(socketPath)
	})

	snapshot, err := CheckLaneway(context.Background(), socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status.NetworkID != "network-1" || !RouteCovers(snapshot.Routes, netip.MustParseAddr("10.20.4.8")) {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}

func TestValidateSocketPathRejectsRelativePaths(t *testing.T) {
	if _, err := ValidateSocketPath("lanewayd.sock"); err == nil {
		t.Fatal("expected a relative path to be rejected")
	}
}
