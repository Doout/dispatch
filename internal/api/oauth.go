package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type oauthState struct {
	ProviderID string
	Verifier   string
	LinkUserID string
	ReturnTo   string
	ExpiresAt  time.Time
}

type oauthCode struct {
	Token     string
	ExpiresAt time.Time
}

type authDiscovery struct {
	Method    string               `json:"method"`
	Provider  *publicAuthProvider  `json:"provider,omitempty"`
	Providers []publicAuthProvider `json:"providers,omitempty"`
	Password  bool                 `json:"password"`
}

func (a *API) discoverAuth(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Identifier string `json:"identifier"`
	}
	if !decode(w, r, &input) {
		return
	}
	identifier := strings.TrimSpace(input.Identifier)
	providers, err := a.store.ListAuthProviders(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	enabled := map[string]core.AuthProvider{}
	public := []publicAuthProvider{}
	for _, item := range providers {
		if item.State == core.AuthProviderStateReady {
			enabled[item.ID] = item
			public = append(public, publicProvider(item))
		}
	}
	local := false
	if user, err := a.store.GetUserByUsername(r.Context(), identifier); err == nil && user.PasswordHash != "" && user.State == core.UserStateActive {
		local = true
	}
	if strings.Contains(identifier, "@") {
		if user, err := a.store.GetUserByEmail(r.Context(), identifier); err == nil && user.PasswordHash != "" && user.State == core.UserStateActive {
			local = true
		}
	}
	identities, err := a.store.FindExternalIdentitiesByLogin(r.Context(), identifier)
	if err != nil {
		a.internal(w, err)
		return
	}
	matches := []publicAuthProvider{}
	seen := map[string]bool{}
	for _, identity := range identities {
		if item, ok := enabled[identity.ProviderID]; ok && !seen[item.ID] {
			matches = append(matches, publicProvider(item))
			seen[item.ID] = true
		}
	}
	if len(matches) == 1 && !local {
		writeJSON(w, http.StatusOK, authDiscovery{Method: "provider", Provider: &matches[0]})
		return
	}
	if len(matches) > 0 || !local {
		if len(matches) == 0 {
			matches = public
		}
		writeJSON(w, http.StatusOK, authDiscovery{Method: "choose", Providers: matches, Password: local || len(matches) == 0})
		return
	}
	writeJSON(w, http.StatusOK, authDiscovery{Method: "password", Password: true})
}

func (a *API) startOAuth(w http.ResponseWriter, r *http.Request) {
	a.beginOAuth(w, r, "", "/")
}

func (a *API) beginOAuth(w http.ResponseWriter, r *http.Request, linkUserID, returnTo string) {
	provider, err := a.store.GetAuthProvider(r.Context(), chi.URLParam(r, "id"))
	if err != nil || provider.State != core.AuthProviderStateReady {
		problem(w, http.StatusNotFound, "Sign-in method not found", "The requested sign-in method is unavailable.")
		return
	}
	state, err := randomURLToken(32)
	if err != nil {
		a.internal(w, err)
		return
	}
	verifier, err := randomURLToken(48)
	if err != nil {
		a.internal(w, err)
		return
	}
	challenge := sha256.Sum256([]byte(verifier))
	now := time.Now().UTC()
	a.oauthMu.Lock()
	a.cleanOAuthLocked(now)
	a.oauthStates[state] = oauthState{ProviderID: provider.ID, Verifier: verifier, LinkUserID: linkUserID, ReturnTo: safeOAuthReturnTo(returnTo), ExpiresAt: now.Add(10 * time.Minute)}
	a.oauthMu.Unlock()
	values := url.Values{
		"client_id":             {provider.ClientID},
		"redirect_uri":          {a.oauthCallbackURL(r)},
		"scope":                 {"read:user user:email"},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	writeJSON(w, http.StatusOK, map[string]string{"authorizationUrl": provider.BaseURL + "/login/oauth/authorize?" + values.Encode()})
}

func (a *API) completeOAuth(w http.ResponseWriter, r *http.Request) {
	stateValue := r.URL.Query().Get("state")
	a.oauthMu.Lock()
	state, ok := a.oauthStates[stateValue]
	delete(a.oauthStates, stateValue)
	a.oauthMu.Unlock()
	if !ok || time.Now().After(state.ExpiresAt) || r.URL.Query().Get("code") == "" {
		a.redirectOAuthError(w, r, "Sign-in request expired. Try again.")
		return
	}
	provider, err := a.store.GetAuthProvider(r.Context(), state.ProviderID)
	if err != nil || provider.State != core.AuthProviderStateReady {
		a.redirectOAuthError(w, r, "This sign-in method is unavailable.")
		return
	}
	profile, err := a.exchangeGitHubIdentity(r.Context(), r, provider, r.URL.Query().Get("code"), state.Verifier)
	if err != nil {
		a.logger.Warn("OAuth sign-in failed", "provider", provider.ID, "error", err)
		if state.LinkUserID != "" {
			a.redirectOAuthLink(w, r, state.ReturnTo, "error", "GitHub could not verify this account.")
			return
		}
		a.redirectOAuthError(w, r, "GitHub sign-in failed. Try again or use another method.")
		return
	}
	if state.LinkUserID != "" {
		if err := a.linkExternalIdentity(r.Context(), provider, profile, state.LinkUserID); err != nil {
			message := "Dispatch could not link this account."
			switch {
			case errors.Is(err, errExternalIdentityOwned):
				message = "This GitHub account belongs to another Dispatch user. Ask an owner to merge the accounts."
			case errors.Is(err, errExternalProviderUsed):
				message = "Another account from this sign-in method is already linked."
			case errors.Is(err, store.ErrNotFound):
				message = "Your Dispatch user no longer exists."
			default:
				a.logger.Error("link external identity", "provider", provider.ID, "user", state.LinkUserID, "error", err)
			}
			a.redirectOAuthLink(w, r, state.ReturnTo, "error", message)
			return
		}
		a.redirectOAuthLink(w, r, state.ReturnTo, "linked", provider.Name+" linked.")
		return
	}
	user, err := a.resolveExternalUser(r.Context(), provider, profile)
	if err != nil {
		if errors.Is(err, errExternalApprovalPending) {
			a.redirectOAuthError(w, r, "Your account is waiting for owner approval.")
			return
		}
		if errors.Is(err, errExternalAccessDenied) {
			a.redirectOAuthError(w, r, "Your account does not have access to this controller.")
			return
		}
		a.logger.Error("resolve OAuth user", "provider", provider.ID, "error", err)
		a.redirectOAuthError(w, r, "Dispatch could not finish signing you in.")
		return
	}
	token, err := a.createSession(r.Context(), user.ID, identityForUser(user))
	if err != nil {
		a.internal(w, err)
		return
	}
	handoff, err := randomURLToken(32)
	if err != nil {
		a.internal(w, err)
		return
	}
	now := time.Now().UTC()
	a.oauthMu.Lock()
	a.cleanOAuthLocked(now)
	a.oauthCodes[handoff] = oauthCode{Token: token, ExpiresAt: now.Add(time.Minute)}
	a.oauthMu.Unlock()
	home := a.publicOrigin(r) + "/?auth_code=" + url.QueryEscape(handoff)
	http.Redirect(w, r, home, http.StatusSeeOther)
}

func (a *API) exchangeOAuthCode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &input) {
		return
	}
	a.oauthMu.Lock()
	item, ok := a.oauthCodes[input.Code]
	delete(a.oauthCodes, input.Code)
	a.oauthMu.Unlock()
	if !ok || time.Now().After(item.ExpiresAt) {
		problem(w, http.StatusUnauthorized, "Sign-in expired", "Start the sign-in flow again.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"token": item.Token})
}

func (a *API) cleanOAuthLocked(now time.Time) {
	for key, item := range a.oauthStates {
		if now.After(item.ExpiresAt) {
			delete(a.oauthStates, key)
		}
	}
	for key, item := range a.oauthCodes {
		if now.After(item.ExpiresAt) {
			delete(a.oauthCodes, key)
		}
	}
}

func (a *API) publicOrigin(r *http.Request) string {
	if a.auth.PublicURL != "" {
		return strings.TrimRight(a.auth.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host
}

func (a *API) oauthCallbackURL(r *http.Request) string {
	return a.publicOrigin(r) + "/api/v1/auth/callback"
}

func (a *API) redirectOAuthError(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, a.publicOrigin(r)+"/?auth_error="+url.QueryEscape(message), http.StatusSeeOther)
}

func safeOAuthReturnTo(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(value, "//") {
		return "/"
	}
	return parsed.String()
}

func (a *API) redirectOAuthLink(w http.ResponseWriter, r *http.Request, returnTo, status, detail string) {
	target, err := url.Parse(safeOAuthReturnTo(returnTo))
	if err != nil {
		target = &url.URL{Path: "/"}
	}
	query := target.Query()
	query.Set("accountLink", status)
	query.Set("detail", detail)
	target.RawQuery = query.Encode()
	http.Redirect(w, r, a.publicOrigin(r)+target.String(), http.StatusSeeOther)
}

func randomURLToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

type githubOAuthProfile struct {
	Subject     string
	Login       string
	DisplayName string
	Email       string
}

func (a *API) exchangeGitHubIdentity(ctx context.Context, request *http.Request, provider core.AuthProvider, code, verifier string) (githubOAuthProfile, error) {
	if a.eventConfig.Vault == nil {
		return githubOAuthProfile{}, errors.New("secret vault unavailable")
	}
	secret, err := a.eventConfig.Vault.Decrypt("auth-provider:"+provider.ID, provider.EncryptedClientSecret)
	if err != nil {
		return githubOAuthProfile{}, err
	}
	values := url.Values{
		"client_id":     {provider.ClientID},
		"client_secret": {string(secret)},
		"code":          {code},
		"redirect_uri":  {a.oauthCallbackURL(request)},
		"code_verifier": {verifier},
	}
	tokenRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.BaseURL+"/login/oauth/access_token", strings.NewReader(values.Encode()))
	if err != nil {
		return githubOAuthProfile{}, err
	}
	tokenRequest.Header.Set("Accept", "application/json")
	tokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(tokenRequest)
	if err != nil {
		return githubOAuthProfile{}, err
	}
	defer response.Body.Close()
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&tokenResponse); err != nil || tokenResponse.AccessToken == "" {
		return githubOAuthProfile{}, fmt.Errorf("token exchange failed: %s", tokenResponse.Error)
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := githubOAuthGet(ctx, client, provider.APIURL+"/user", tokenResponse.AccessToken, &user); err != nil {
		return githubOAuthProfile{}, err
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := githubOAuthGet(ctx, client, provider.APIURL+"/user/emails", tokenResponse.AccessToken, &emails); err != nil {
		return githubOAuthProfile{}, err
	}
	email := ""
	for _, item := range emails {
		if item.Primary && item.Verified {
			email = strings.ToLower(strings.TrimSpace(item.Email))
			break
		}
	}
	if user.ID == 0 || user.Login == "" || email == "" {
		return githubOAuthProfile{}, errors.New("verified primary email required")
	}
	displayName := strings.TrimSpace(user.Name)
	if displayName == "" {
		displayName = user.Login
	}
	return githubOAuthProfile{Subject: strconv.FormatInt(user.ID, 10), Login: user.Login, DisplayName: displayName, Email: email}, nil
}

func githubOAuthGet(ctx context.Context, client *http.Client, endpoint, token string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("GitHub API returned %d: %s", response.StatusCode, bytes.TrimSpace(body))
	}
	return json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(target)
}

var (
	errExternalAccessDenied    = errors.New("external account has no access")
	errExternalApprovalPending = errors.New("external account is waiting for approval")
	errExternalIdentityOwned   = errors.New("external identity belongs to another user")
	errExternalProviderUsed    = errors.New("external provider already linked")
)

func (a *API) linkExternalIdentity(ctx context.Context, provider core.AuthProvider, profile githubOAuthProfile, userID string) error {
	if _, err := a.store.GetUser(ctx, userID); err != nil {
		return err
	}
	current, err := a.store.GetExternalIdentity(ctx, provider.ID, profile.Subject)
	if err == nil {
		if current.UserID != userID {
			return errExternalIdentityOwned
		}
		current.Login, current.Email, current.LastLogin = profile.Login, profile.Email, time.Now().UTC()
		return a.store.UpsertExternalIdentity(ctx, current)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	identities, err := a.store.ListExternalIdentities(ctx)
	if err != nil {
		return err
	}
	for _, identity := range identities {
		if identity.ProviderID == provider.ID && identity.UserID == userID {
			return errExternalProviderUsed
		}
	}
	now := time.Now().UTC()
	return a.store.UpsertExternalIdentity(ctx, core.ExternalIdentity{ProviderID: provider.ID, Subject: profile.Subject, UserID: userID, Login: profile.Login, Email: profile.Email, LastLogin: now, CreatedAt: now})
}

func (a *API) resolveExternalUser(ctx context.Context, provider core.AuthProvider, profile githubOAuthProfile) (core.User, error) {
	now := time.Now().UTC()
	identity, err := a.store.GetExternalIdentity(ctx, provider.ID, profile.Subject)
	if err == nil {
		user, err := a.store.GetUser(ctx, identity.UserID)
		if err != nil {
			return core.User{}, errExternalAccessDenied
		}
		identity.Login, identity.Email, identity.LastLogin = profile.Login, profile.Email, now
		if err := a.store.UpsertExternalIdentity(ctx, identity); err != nil {
			return core.User{}, err
		}
		return externalUserResult(user)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return core.User{}, err
	}
	users, err := a.store.ListUsers(ctx)
	if err != nil {
		return core.User{}, err
	}
	matches := []core.User{}
	for _, item := range users {
		if item.Email != "" && strings.EqualFold(item.Email, profile.Email) {
			matches = append(matches, item)
		}
	}
	var user core.User
	if len(matches) == 1 && (matches[0].State == core.UserStateActive || matches[0].State == core.UserStatePending) {
		user = matches[0]
	} else if len(matches) == 0 && provider.Provisioning == core.AuthProvisionApproval {
		username, err := a.availableExternalUsername(ctx, profile.Login)
		if err != nil {
			return core.User{}, err
		}
		user = core.User{ID: ulid.Make().String(), Username: username, DisplayName: profile.DisplayName, Email: profile.Email, PasswordHash: "", SystemRole: core.UserRoleMember, State: core.UserStatePending, CreatedAt: now, UpdatedAt: now}
		if err := a.store.CreateUser(ctx, user); err != nil {
			return core.User{}, err
		}
	} else {
		return core.User{}, errExternalAccessDenied
	}
	identity = core.ExternalIdentity{ProviderID: provider.ID, Subject: profile.Subject, UserID: user.ID, Login: profile.Login, Email: profile.Email, LastLogin: now, CreatedAt: now}
	if err := a.store.UpsertExternalIdentity(ctx, identity); err != nil {
		return core.User{}, err
	}
	return externalUserResult(user)
}

func externalUserResult(user core.User) (core.User, error) {
	switch user.State {
	case core.UserStateActive:
		return user, nil
	case core.UserStatePending:
		return core.User{}, errExternalApprovalPending
	default:
		return core.User{}, errExternalAccessDenied
	}
}

func (a *API) availableExternalUsername(ctx context.Context, preferred string) (string, error) {
	base := strings.ToLower(strings.TrimSpace(preferred))
	base = strings.Map(func(value rune) rune {
		if value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '-' || value == '_' || value == '.' {
			return value
		}
		return '-'
	}, base)
	base = strings.Trim(base, "-._")
	if len(base) < 3 {
		base = "user-" + strings.ToLower(ulid.Make().String()[:6])
	}
	for attempt := 0; attempt < 20; attempt++ {
		candidate := base
		if attempt > 0 {
			candidate = base + "-" + strconv.Itoa(attempt+1)
		}
		_, err := a.store.GetUserByUsername(ctx, candidate)
		if errors.Is(err, store.ErrNotFound) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate username")
}
