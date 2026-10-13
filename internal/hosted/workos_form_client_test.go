package hosted

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	workos "github.com/workos/workos-go/v10"
)

func TestWorkOSFormSDKGrantsContinueWithoutBrowserProviderTokens(t *testing.T) {
	ctx := context.Background()
	grant := ""
	verified := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer server-key" {
			t.Error("server key missing")
		}
		switch r.URL.Path {
		case "/user_management/authenticate":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["client_id"] != "client_test" || in["client_secret"] != "server-key" || in["ip_address"] != "192.0.2.1" {
				t.Error("wrong client or browser context")
			}
			grant = in["grant_type"].(string)
			if grant == "password" {
				if in["password"] != "provider-password" || in["invitation_token"] != "valid-invite" {
					t.Error("password or invitation lost")
				}
				w.WriteHeader(403)
				_, _ = w.Write([]byte(`{"code":"email_verification_required","pending_authentication_token":"private-pending","email":"member@example.test","email_verification_id":"ev_test"}`))
				return
			}
			if in["pending_authentication_token"] != "private-pending" || in["code"] != "123456" {
				t.Error("verification continuation lost")
			}
			claims := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user_test","sid":"session_test"}`))
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": "user_test", "email": "member@example.test", "email_verified": verified}, "access_token": "header." + claims + ".signature", "refresh_token": "not-saved"})
		case "/user_management/users/user_test/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "session_test", "user_id": "user_test", "status": "active", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}}, "list_metadata": map[string]any{}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := &workOSClient{workos.NewClient("server-key", workos.WithClientID("client_test"), workos.WithBaseURL(server.URL), workos.WithMaxRetries(0))}
	result, err := c.Authenticate(ctx, managedAuthentication{Step: "password", Email: "member@example.test", Invitation: "valid-invite", Deadline: time.Now().Add(time.Minute)}, managedCredentials{Password: "provider-password", IP: "192.0.2.1"})
	if err != nil || result.Next == nil || result.Next.Step != "verify-email" || result.Next.PendingToken != "private-pending" || result.Identity != nil {
		t.Fatal("missing verification", err)
	}
	result, err = c.Authenticate(ctx, *result.Next, managedCredentials{Code: "123456", IP: "192.0.2.1"})
	if err != nil || result.Identity == nil || result.Identity.SessionID != "session_test" || grant != "urn:workos:oauth:grant-type:email-verification:code" {
		t.Fatal("verification failed", err)
	}
	verified = false
	if _, err = c.Authenticate(ctx, managedAuthentication{Step: "verify-email", PendingToken: "private-pending"}, managedCredentials{Code: "123456", IP: "192.0.2.1"}); err == nil {
		t.Fatal("unverified identity accepted")
	}
}

func TestWorkOSFormSDKInvitationCodeAndWaitlistNeverReturnSecretCodes(t *testing.T) {
	created, sent, waitlisted := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer server-key" {
			t.Error("server key missing")
		}
		switch r.URL.Path {
		case "/user_management/invitations/by_token/invite":
			_ = json.NewEncoder(w).Encode(map[string]any{"email": "member@example.test", "state": "pending", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
		case "/user_management/users":
			if r.Method == "GET" {
				if r.URL.Query().Get("email") != "member@example.test" {
					t.Error("lookup email")
				}
				_, _ = w.Write([]byte(`{"data":[],"list_metadata":{}}`))
				return
			}
			created++
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["email"] != "member@example.test" || in["email_verified"] != nil || in["password"] != nil {
				t.Error("user creation bypassed email ownership")
			}
			_, _ = w.Write([]byte(`{"id":"user_test","email":"member@example.test","email_verified":false}`))
		case "/user_management/magic_auth":
			sent++
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["invitation_token"] != "invite" {
				t.Error("invitation missing")
			}
			_, _ = w.Write([]byte(`{"id":"magic_test","code":"private-code"}`))
		case "/user_management/waitlists/default/entries":
			waitlisted++
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["send_confirmation_email"] != true {
				t.Error("confirmation disabled")
			}
			_, _ = w.Write([]byte(`{"id":"entry_test","state":"pending"}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := &workOSClient{workos.NewClient("server-key", workos.WithClientID("client_test"), workos.WithBaseURL(server.URL), workos.WithMaxRetries(0))}
	ctx := context.Background()
	email, err := c.InvitationEmail(ctx, "invite")
	if err != nil || email != "member@example.test" {
		t.Fatal("invitation", err)
	}
	if err = c.SendCode(ctx, email, "invite", "192.0.2.1", "browser", true); err != nil {
		t.Fatal(err)
	}
	if created != 1 || sent != 1 {
		t.Fatal("invited user was not prepared")
	}
	if err = c.JoinWaitlist(ctx, email); err != nil || waitlisted != 1 {
		t.Fatal("waitlist", err)
	}
	for method, want := range map[string]string{"github": "provider=GitHubOAuth", "google": "provider=GoogleOAuth", "sso": "domain_hint=example.test"} {
		address := c.AuthorizationURLFor("https://doodship.com/callback", "state", "challenge", "", method, email)
		if !strings.Contains(address, want) || strings.Contains(address, "provider=authkit") || !strings.Contains(address, "code_challenge_method=S256") {
			t.Fatal("hosted UI or PKCE regression", address)
		}
	}
}
