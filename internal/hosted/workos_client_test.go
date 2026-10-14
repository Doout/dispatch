package hosted

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/tenancy"
	workos "github.com/workos/workos-go/v10"
)

func TestWorkOSOrganizationDeletionRetriesAndAcceptsMissingOrganizations(t *testing.T) {
	status := 204
	lookup := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing API key")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/organizations/external_id/") {
			lookup = true
			if status == 404 {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"message":"Not found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"org_example","name":"Example"}`))
			return
		}
		if r.Method != "DELETE" || r.URL.Path != "/organizations/org_example" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
		if status != 204 {
			_, _ = w.Write([]byte(`{"message":"Unavailable"}`))
		}
	}))
	defer server.Close()
	client := &workOSClient{client: workos.NewClient("test-key", workos.WithBaseURL(server.URL), workos.WithMaxRetries(0))}
	ctx := context.Background()
	if err := client.DeleteOrganization(ctx, "org_example", ""); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteOrganization(ctx, "", "doodship.test:instance"); err != nil || !lookup {
		t.Fatal("external recovery failed", err)
	}
	status = 503
	if err := client.DeleteOrganization(ctx, "org_example", ""); err == nil {
		t.Fatal("provider error acknowledged")
	}
	status = 404
	if err := client.DeleteOrganization(ctx, "org_example", ""); err != nil {
		t.Fatal("deleted org not idempotent", err)
	}
	if err := client.DeleteOrganization(ctx, "", "doodship.test:instance"); err != nil {
		t.Fatal("missing external org not idempotent", err)
	}
}

func TestWorkOSSDKCodeExchangeAndSessionValidation(t *testing.T) {
	verified := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing server API credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user_management/authenticate":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["code"] != "one-time-code" || body["code_verifier"] != "verifier" || body["client_id"] != "client_test" {
				t.Errorf("exchange request: %+v", body)
			}
			claims := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user_test","sid":"session_test"}`))
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": "user_test", "email": "user@example.test", "email_verified": verified, "first_name": "Test", "last_name": "User"}, "access_token": "header." + claims + ".signature", "refresh_token": "not-persisted"})
		case "/user_management/users/user_test/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "session_test", "user_id": "user_test", "status": "active", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}}, "list_metadata": map[string]any{}})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := &workOSClient{client: workos.NewClient("test-key", workos.WithClientID("client_test"), workos.WithBaseURL(server.URL), workos.WithMaxRetries(0))}
	u, err := client.Exchange(context.Background(), "one-time-code", "verifier")
	if err != nil || u.Subject != "user_test" || u.SessionID != "session_test" {
		t.Fatalf("exchange: %+v %v", u, err)
	}
	verified = false
	if _, err = client.Exchange(context.Background(), "one-time-code", "verifier"); err == nil {
		t.Fatal("unverified user accepted")
	}
	if err = client.SessionActive(context.Background(), "user_test", "wrong-session"); err == nil {
		t.Fatal("wrong session accepted")
	}
	address := client.AuthorizationURL("https://dispatch.example.test/callback", "state", "challenge", "org_test")
	for _, want := range []string{"client_id=client_test", "code_challenge=challenge", "code_challenge_method=S256", "organization_id=org_test", "provider=authkit"} {
		if !strings.Contains(address, want) {
			t.Fatalf("missing %s in URL %s", want, address)
		}
	}
}

func TestWorkOSAdmissionRequiresAcceptedInvitationForSubject(t *testing.T) {
	state, subject, status := "pending", "user_test", 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user_management/invitations" || r.URL.Query().Get("email") != "user@example.test" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("invalid invitation lookup")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "inv_test", "email": "user@example.test", "state": state, "accepted_user_id": subject}}, "list_metadata": map[string]any{}})
	}))
	defer server.Close()
	client := &workOSClient{client: workos.NewClient("test-key", workos.WithBaseURL(server.URL), workos.WithMaxRetries(0))}
	identity := workOSIdentity{Subject: "user_test", Email: "user@example.test"}
	if err := client.AdmissionAllowed(context.Background(), identity); !errors.Is(err, tenancy.ErrDenied) {
		t.Fatal("pending invitation admitted user", err)
	}
	state = "accepted"
	if err := client.AdmissionAllowed(context.Background(), identity); err != nil {
		t.Fatal("accepted invitation denied", err)
	}
	subject = "someone_else"
	if err := client.AdmissionAllowed(context.Background(), identity); !errors.Is(err, tenancy.ErrDenied) {
		t.Fatal("email matched a different subject", err)
	}
	status = 503
	if err := client.AdmissionAllowed(context.Background(), identity); err == nil {
		t.Fatal("API failure admitted user")
	}
}
