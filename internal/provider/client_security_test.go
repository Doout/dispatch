package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/provider"
)

func TestClientRejectsEscapedCredentialInSuccessfulMetadata(t *testing.T) {
	token := `private"credential`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []provider.Option{{ID: "region", Name: "Region", Metadata: map[string]any{"accidentalCredential": token}}}})
	}))
	defer server.Close()
	client, err := provider.NewClient(server.URL, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Options(context.Background(), provider.OptionRequest{Kind: "regions"})
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("credential metadata accepted: %v", err)
	}
}
