package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEdgeIdentityPersistsAndBindsControllerAndNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	first, err := loadIdentity(path, "https://controller.example", "node")
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadIdentity(path, "https://controller.example", "node")
	if err != nil || first.PrivateKey != second.PrivateKey {
		t.Fatal("identity changed across restart")
	}
	if _, err = loadIdentity(path, "https://other.example", "node"); err == nil {
		t.Fatal("controller binding bypass")
	}
	if _, err = loadIdentity(path, "https://controller.example", "other"); err == nil {
		t.Fatal("node binding bypass")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = loadIdentity(path, "https://controller.example", "node"); err == nil {
		t.Fatal("world-readable key accepted")
	}
}
func TestEdgeSessionRenewalSignsPersistedKeyWithoutEnrollment(t *testing.T) {
	var public ed25519.PublicKey
	challenge := "abcdefgh0123456789abcdefgh0123456789"
	sessionToken := strings.Repeat("s", 32)
	enrollments := 0
	renewals := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/edge/nodes/node/challenge":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			if public == nil {
				w.WriteHeader(401)
				return
			}
			write(w, 200, edgeChallenge{Challenge: challenge, Generation: 1, ExpiresAt: time.Now().Add(time.Minute)})
		case "/api/v1/edge/nodes/node/enroll":
			enrollments++
			if r.Header.Get("Authorization") != "Bearer enrollment" {
				w.WriteHeader(401)
				return
			}
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			public, _ = base64.RawURLEncoding.DecodeString(in["publicKey"])
			write(w, 200, edgeSession{Token: sessionToken, ExpiresAt: time.Now().Add(10 * time.Minute)})
		case "/api/v1/edge/nodes/node/session":
			renewals++
			var in struct {
				Challenge string `json:"challenge"`
				Signature string `json:"signature"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			sig, _ := base64.RawURLEncoding.DecodeString(in.Signature)
			if !ed25519.Verify(public, []byte("dispatch-edge-session-v1\nnode\n1\n"+challenge), sig) {
				t.Error("signature mismatch")
				w.WriteHeader(401)
				return
			}
			write(w, 200, edgeSession{Token: sessionToken, ExpiresAt: time.Now().Add(10 * time.Minute)})
		default:
			t.Error("unexpected request")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "identity.json")
	identity, err := loadIdentity(path, server.URL, "node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = obtainSession(context.Background(), server.Client(), identity, "enrollment"); err != nil {
		t.Fatal(err)
	}
	restarted, err := loadIdentity(path, server.URL, "node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = obtainSession(context.Background(), server.Client(), restarted, ""); err != nil {
		t.Fatal(err)
	}
	if enrollments != 1 || renewals != 1 {
		t.Fatal("renewal reused enrollment")
	}
}
func TestEdgeOperationsRejectUnboundedOrUntypedRequests(t *testing.T) {
	for _, in := range []edgeRequest{{Method: "CONNECT", URL: "https://example.com"}, {Method: "TRACE", URL: "https://example.com"}, {Method: "GET", URL: "http://example.com"}, {Method: "GET", URL: "https://user:secret@example.com"}, {Method: "POST", URL: "https://example.com", Body: make([]byte, (1<<20)+1)}} {
		if _, err := executeEdgeRequest(context.Background(), http.DefaultClient, in); err == nil {
			t.Fatal("unsafe operation accepted")
		}
	}
	if responseError("poll", &http.Response{StatusCode: 401}) != errEdgeUnauthorized {
		t.Fatal("session refresh signal lost")
	}
}
