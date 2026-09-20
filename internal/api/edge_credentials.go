package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/edge"
	"github.com/go-chi/chi/v5"
)

func (a *API) edgeCredentials() (edge.CredentialStore, bool) {
	data, ok := a.store.(edge.CredentialStore)
	return data, ok
}
func (a *API) edgeCredentialRequest(w http.ResponseWriter, r *http.Request) (edge.CredentialStore, string, bool) {
	data, ok := a.edgeCredentials()
	id := chi.URLParam(r, "id")
	item, err := a.store.GetPrivateNetwork(r.Context(), id)
	if !ok || err != nil || item.Driver != edge.DriverAgent {
		problem(w, http.StatusUnauthorized, "Authentication required", "The edge node credential is invalid, expired or revoked.")
		return nil, "", false
	}
	return data, id, true
}
func (a *API) enrollEdgeNode(w http.ResponseWriter, r *http.Request) {
	data, id, ok := a.edgeCredentialRequest(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !ok {
		return
	}
	var input struct {
		PublicKey string `json:"publicKey"`
	}
	if !decode(w, r, &input) {
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	session, err := edge.Enroll(r.Context(), data, id, token, input.PublicKey, time.Now().UTC())
	if err != nil {
		problem(w, http.StatusUnauthorized, "Enrollment refused", "The enrollment token is invalid, expired or already used.")
		return
	}
	item, _ := a.store.GetPrivateNetwork(r.Context(), id)
	if item.Details == nil {
		item.Details = map[string]string{}
	}
	item.Details["credentialMode"] = "short_session"
	item.Details["keyFingerprint"] = edge.KeyFingerprint(input.PublicKey)
	item.Details["sessionExpiresAt"] = session.ExpiresAt.Format(time.RFC3339)
	delete(item.Details, "enrollmentExpiresAt")
	item.TokenHash = ""
	a.touchEdgeNode(r.Context(), item, r)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, session)
}
func (a *API) challengeEdgeNode(w http.ResponseWriter, r *http.Request) {
	data, id, ok := a.edgeCredentialRequest(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !ok {
		return
	}
	var input struct {
		PublicKey string `json:"publicKey"`
	}
	if !decode(w, r, &input) {
		return
	}
	challenge, err := edge.NewChallenge(r.Context(), data, id, input.PublicKey, time.Now().UTC())
	if err != nil {
		problem(w, http.StatusUnauthorized, "Authentication required", "The node identity is unavailable or has too many pending challenges.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, challenge)
}
func (a *API) createEdgeSession(w http.ResponseWriter, r *http.Request) {
	data, id, ok := a.edgeCredentialRequest(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !ok {
		return
	}
	var input struct {
		Challenge  string `json:"challenge"`
		Signature  string `json:"signature"`
		Generation int64  `json:"generation"`
	}
	if !decode(w, r, &input) {
		return
	}
	session, err := edge.Exchange(r.Context(), data, id, input.Challenge, input.Signature, input.Generation, time.Now().UTC())
	if err != nil {
		problem(w, http.StatusUnauthorized, "Authentication required", "The signed challenge is invalid, expired or already used.")
		return
	}
	item, _ := a.store.GetPrivateNetwork(r.Context(), id)
	if item.Details == nil {
		item.Details = map[string]string{}
	}
	item.Details["sessionExpiresAt"] = session.ExpiresAt.Format(time.RFC3339)
	a.touchEdgeNode(r.Context(), item, r)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, session)
}
func (a *API) revokeEdgeNode(w http.ResponseWriter, r *http.Request) {
	data, id, ok := a.edgeCredentialRequest(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !ok {
		return
	}
	now := time.Now().UTC()
	if err := data.RevokeEdgeCredential(r.Context(), id, now); err != nil {
		a.internal(w, err)
		return
	}
	item, err := a.store.GetPrivateNetwork(r.Context(), id)
	if err != nil {
		a.internal(w, err)
		return
	}
	if item.Details == nil {
		item.Details = map[string]string{}
	}
	item.State = "revoked"
	item.TokenHash = ""
	item.UpdatedAt = now
	item.Details["credentialMode"] = "revoked"
	item.Details["revokedAt"] = now.Format(time.RFC3339)
	if err = a.store.UpdatePrivateNetwork(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
