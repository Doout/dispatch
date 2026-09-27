package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/bcrypt"
)

type sessionState struct {
	expires  time.Time
	identity core.Identity
}

func (a *API) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if identity, valid := a.bearerIdentity(r); valid {
			ctx, ok := a.authorizedContext(w, r, identity)
			if !ok {
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		username, password, basic := r.BasicAuth()
		identity, _, valid, setupRequired, err := a.passwordIdentity(r.Context(), username, password)
		if err != nil {
			a.internal(w, err)
			return
		}
		if basic && valid {
			ctx, ok := a.authorizedContext(w, r, identity)
			if !ok {
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		unauthorized(w, setupRequired)
	})
}

const impersonateUserHeader = "Impersonate-User"

func (a *API) authorizedContext(w http.ResponseWriter, r *http.Request, actor core.Identity) (context.Context, bool) {
	ctx := withIdentity(r.Context(), actor)
	targetID := strings.TrimSpace(r.Header.Get(impersonateUserHeader))
	if targetID == "" || targetID == actor.ID {
		return ctx, true
	}
	if actor.SystemRole != core.UserRoleOwner {
		problem(w, http.StatusForbidden, "Impersonation denied", "Controller owner access is required to view Dispatch as another user.")
		return nil, false
	}
	target, err := a.store.GetUser(r.Context(), targetID)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "User not found", "The selected user no longer exists.")
		return nil, false
	}
	if err != nil {
		a.internal(w, err)
		return nil, false
	}
	if target.State != core.UserStateActive {
		problem(w, http.StatusConflict, "User is not active", "Only active users can be impersonated.")
		return nil, false
	}
	ctx = withImpersonator(ctx, actor)
	return withIdentity(ctx, identityForUser(target)), true
}

func (a *API) bearerIdentity(r *http.Request) (core.Identity, bool) {
	provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return core.Identity{}, false
	}
	if a.auth.AdminToken != "" && secureEqual(provided, a.auth.AdminToken) {
		return controllerIdentity("token"), true
	}
	a.sessionMu.RLock()
	session, found := a.sessions[provided]
	a.sessionMu.RUnlock()
	if found && time.Now().Before(session.expires) {
		if session.identity.ID == "controller-owner" {
			return session.identity, true
		}
		user, err := a.store.GetUser(r.Context(), session.identity.ID)
		if err == nil && user.State == core.UserStateActive {
			return identityForUser(user), true
		}
		return core.Identity{}, false
	}
	now := time.Now().UTC()
	user, err := a.store.SessionUser(r.Context(), sessionHash(provided), now)
	if err == nil && user.State == core.UserStateActive {
		return identityForUser(user), true
	}
	valid, err := a.store.AdminSessionValid(r.Context(), sessionHash(provided), now)
	return controllerIdentity("administrator"), err == nil && valid
}

func secureEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func unauthorized(w http.ResponseWriter, setupRequired bool) {
	if setupRequired {
		problem(w, http.StatusUnauthorized, "Administrator setup required", "Create the first administrator account before using Dispatch.")
		return
	}
	problem(w, http.StatusUnauthorized, "Authentication required", "Enter the administrator username and password.")
}

func (a *API) validPassword(ctx context.Context, username, password string) (valid, setupRequired bool, err error) {
	_, _, valid, setupRequired, err = a.passwordIdentity(ctx, username, password)
	return valid, setupRequired, err
}

func (a *API) passwordIdentity(ctx context.Context, username, password string) (identity core.Identity, userID string, valid, setupRequired bool, err error) {
	if a.auth.Username != "" {
		valid = secureEqual(username, a.auth.Username) && secureEqual(password, a.auth.Password)
		return controllerIdentity(a.auth.Username), "", valid, false, nil
	}
	user, userErr := a.store.GetUserByUsername(ctx, username)
	if errors.Is(userErr, store.ErrNotFound) && strings.Contains(username, "@") {
		user, userErr = a.store.GetUserByEmail(ctx, username)
	}
	if userErr == nil {
		valid = user.State == core.UserStateActive && bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil
		return identityForUser(user), user.ID, valid, false, nil
	}
	if !errors.Is(userErr, store.ErrNotFound) {
		return identity, "", false, false, userErr
	}
	users, listErr := a.store.ListUsers(ctx)
	if listErr != nil {
		return identity, "", false, false, listErr
	}
	if len(users) > 0 {
		return identity, "", false, false, nil
	}
	credential, err := a.store.GetAdminCredential(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return identity, "", false, true, nil
	}
	if err != nil {
		return identity, "", false, false, err
	}
	valid = secureEqual(username, credential.Username) && bcrypt.CompareHashAndPassword([]byte(credential.PasswordHash), []byte(password)) == nil
	return controllerIdentity(credential.Username), "", valid, false, nil
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var input setupAdminRequest
	if !decode(w, r, &input) {
		return
	}
	identity, userID, valid, setupRequired, err := a.passwordIdentity(r.Context(), strings.TrimSpace(input.Username), input.Password)
	if err != nil {
		a.internal(w, err)
		return
	}
	if !valid {
		unauthorized(w, setupRequired)
		return
	}
	token, err := a.createSession(r.Context(), userID, identity)
	if err != nil {
		a.internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if ok && provided != "" {
		if err := a.store.DeleteSession(r.Context(), sessionHash(provided)); err != nil {
			a.internal(w, err)
			return
		}
		a.sessionMu.Lock()
		delete(a.sessions, provided)
		a.sessionMu.Unlock()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) createSession(ctx context.Context, userID string, identity core.Identity) (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buffer)
	now := time.Now().UTC()
	expires := now.Add(12 * time.Hour)
	var err error
	if userID == "" {
		err = a.store.CreateAdminSession(ctx, sessionHash(token), expires, now)
	} else {
		err = a.store.CreateUserSession(ctx, sessionHash(token), userID, expires, now)
	}
	if err != nil {
		return "", err
	}
	a.sessionMu.Lock()
	for existing, session := range a.sessions {
		if now.After(session.expires) {
			delete(a.sessions, existing)
		}
	}
	a.sessions[token] = sessionState{expires: expires, identity: identity}
	a.sessionMu.Unlock()
	return token, nil
}

func sessionHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (a *API) authStatus(w http.ResponseWriter, r *http.Request) {
	configured := a.auth.Username != ""
	if !configured {
		_, err := a.store.GetAdminCredential(r.Context())
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			a.internal(w, err)
			return
		}
		configured = err == nil
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setupRequired":       !configured,
		"tokenLoginAvailable": a.auth.AdminToken != "",
	})
}

type setupAdminRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *API) setupAdmin(w http.ResponseWriter, r *http.Request) {
	if a.auth.Username != "" {
		problem(w, http.StatusConflict, "Administrator already configured", "Environment-provided credentials are active on this controller.")
		return
	}
	if _, err := a.store.GetAdminCredential(r.Context()); err == nil {
		problem(w, http.StatusConflict, "Administrator already configured", "The first administrator account has already been created.")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		a.internal(w, err)
		return
	}

	var input setupAdminRequest
	if !decode(w, r, &input) {
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if detail := validateCredentials(input.Username, input.Password); detail != "" {
		problem(w, http.StatusBadRequest, "Invalid administrator credentials", detail)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		a.internal(w, err)
		return
	}
	now := time.Now().UTC()
	owner := core.User{ID: ulid.Make().String(), Username: input.Username, DisplayName: input.Username, PasswordHash: string(hash), SystemRole: core.UserRoleOwner, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	err = a.store.CreateInitialOwner(r.Context(), store.AdminCredential{Username: input.Username, PasswordHash: string(hash), CreatedAt: now}, owner)
	if errors.Is(err, store.ErrAlreadyExists) {
		problem(w, http.StatusConflict, "Administrator already configured", "The first administrator account has already been created.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"username": input.Username})
}

func validateCredentials(username, password string) string {
	if len(username) < 3 || len(username) > 64 || strings.Contains(username, ":") {
		return "Use a username between 3 and 64 characters without a colon."
	}
	if len([]byte(password)) < 12 || len([]byte(password)) > 72 {
		return "Use a password between 12 and 72 bytes."
	}
	return ""
}
