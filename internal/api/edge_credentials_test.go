package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/edge"
	"github.com/go-chi/chi/v5"
)

func enrollNodeTest(t *testing.T, handler http.Handler, node core.PrivateNetwork) edge.Session {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"publicKey": base64.RawURLEncoding.EncodeToString(public)})
	req := httptest.NewRequest("POST", "/api/v1/edge/nodes/"+node.ID+"/enroll", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+node.EnrollmentToken)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("enroll returned HTTP %d", rr.Code)
	}
	var session edge.Session
	if err = json.Unmarshal(rr.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	return session
}
func TestEdgeEnrollmentAndRevocationAPI(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	raw := serviceRequestTest(t, a, "POST", "/api/v1/private-networks", map[string]string{"name": "secure-node", "driver": "dispatch_agent"}, 201)
	var node core.PrivateNetwork
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatal(err)
	}
	if node.Details["credentialMode"] != "short_session" || node.Details["enrollmentExpiresAt"] == "" {
		t.Fatal("missing secure credential metadata")
	}
	session := enrollNodeTest(t, a, node)
	// Authentication uses the session, never the enrollment token.
	authenticate := func(token string) bool {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", node.ID)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
		_, ok := a.authenticateEdge(r)
		return ok
	}
	if !authenticate(session.Token) || authenticate(node.EnrollmentToken) {
		t.Fatal("session or enrollment isolation failed")
	}
	raw = serviceRequestTest(t, a, "GET", "/api/v1/private-networks", nil, 200)
	if strings.Contains(string(raw), session.Token) || strings.Contains(string(raw), node.EnrollmentToken) {
		t.Fatal("credential leaked")
	}
	serviceRequestTest(t, a, "POST", "/api/v1/private-networks/"+node.ID+"/revoke", nil, 200)
	if authenticate(session.Token) {
		t.Fatal("revocation did not invalidate session")
	}
	serviceRequestTest(t, a, "POST", "/api/v1/private-networks/"+node.ID+"/verify", nil, 409)
	// Legacy installed nodes remain usable until explicitly rotated or revoked.
	legacyToken, hash, err := newEdgeToken()
	if err != nil {
		t.Fatal(err)
	}
	legacy := core.PrivateNetwork{ID: "legacy-edge", Name: "legacy-edge", Driver: edge.DriverAgent, TokenHash: hash, State: "ready", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = a.store.CreatePrivateNetwork(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	node = legacy
	if !authenticate(legacyToken) {
		t.Fatal("legacy node unexpectedly broken")
	}
	serviceRequestTest(t, a, "POST", "/api/v1/private-networks/"+node.ID+"/rotate-token", nil, 200)
	if authenticate(legacyToken) {
		t.Fatal("legacy token survived explicit rotation")
	}
}
