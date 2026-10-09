package hosted

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/tenancy"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !s.throttle.allow(r, input.Email) {
		problem(w, http.StatusTooManyRequests, "Wait before trying again.")
		return
	}
	user, err := s.Catalog.AuthenticatePassword(r.Context(), input.Email, input.Password)
	if err != nil {
		problem(w, http.StatusUnauthorized, "Email or password was not accepted.")
		return
	}
	token, err := s.Catalog.CreateSession(r.Context(), user.ID, tenancy.AudiencePlatform, s.Config.SessionDuration)
	if err != nil {
		catalogError(w, err)
		return
	}
	setSession(w, platformCookie, token, int(s.Config.SessionDuration.Seconds()))
	respond(w, http.StatusOK, user)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if s.Config.SMTP.Address == "" {
		problem(w, http.StatusServiceUnavailable, "Registration is not configured.")
		return
	}
	var input struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !s.throttle.allow(r, input.Email) {
		problem(w, http.StatusTooManyRequests, "Wait before trying again.")
		return
	}
	user := tenancy.User{ID: ulid.Make().String(), Email: input.Email, Name: input.Name, State: tenancy.StatePending}
	err := s.Catalog.CreateUser(r.Context(), user)
	if err != nil && !errors.Is(err, tenancy.ErrConflict) {
		catalogError(w, err)
		return
	}
	if errors.Is(err, tenancy.ErrConflict) {
		user, err = s.Catalog.UserByEmail(r.Context(), input.Email)
		if err != nil {
			catalogError(w, err)
			return
		}
	}
	if !user.EmailVerified && user.State == tenancy.StatePending && user.PasswordHash == "" {
		token, issueErr := s.Catalog.CreateEmailVerification(r.Context(), user.ID)
		if issueErr != nil {
			catalogError(w, issueErr)
			return
		}
		link := s.Config.Origin() + "/#verify_email=" + url.QueryEscape(token)
		if mailErr := s.Config.SMTP.SendVerification(r.Context(), input.Email, link); mailErr != nil {
			s.Logger.Error("verification email delivery failed", "user", user.ID)
			problem(w, http.StatusServiceUnavailable, "Email could not be delivered. Try again later.")
			return
		}
	}
	respond(w, http.StatusAccepted, map[string]string{"message": "If this address can register, a verification link has been sent."})
}

func (s *Server) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token    string `json:"token"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !s.throttle.allow(r, "verify:"+input.Token) {
		problem(w, http.StatusTooManyRequests, "Wait before trying again.")
		return
	}
	user, err := s.Catalog.CompleteRegistration(r.Context(), input.Token, input.Name, input.Password)
	if err != nil {
		catalogError(w, err)
		return
	}
	token, err := s.Catalog.CreateSession(r.Context(), user.ID, tenancy.AudiencePlatform, s.Config.SessionDuration)
	if err != nil {
		catalogError(w, err)
		return
	}
	setSession(w, platformCookie, token, int(s.Config.SessionDuration.Seconds()))
	respond(w, http.StatusOK, user)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.Catalog.RevokeSession(r.Context(), requestToken(r, platformCookie)); err != nil {
		catalogError(w, err)
		return
	}
	setSession(w, platformCookie, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	if user, ok := s.user(w, r); ok {
		respond(w, http.StatusOK, user)
	}
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var input struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !s.throttle.allow(r, user.Email) {
		problem(w, http.StatusTooManyRequests, "Wait before trying again.")
		return
	}
	if err := s.Catalog.UpdateOwnPassword(r.Context(), user.ID, input.OldPassword, input.NewPassword); err != nil {
		catalogError(w, err)
		return
	}
	setSession(w, platformCookie, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) memberships(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	memberships, err := s.Catalog.ListMemberships(r.Context(), user.ID)
	if err != nil {
		catalogError(w, err)
		return
	}
	type entry struct {
		Tenant tenancy.Tenant `json:"tenant"`
		Role   string         `json:"role"`
		URL    string         `json:"url"`
	}
	items := []entry{}
	for _, membership := range memberships {
		tenant, err := s.Catalog.Tenant(r.Context(), membership.TenantID)
		if err != nil {
			catalogError(w, err)
			return
		}
		items = append(items, entry{tenant, membership.Role, s.Config.TenantOrigin(tenant.Slug) + "/api/v1/hosted/auth/start"})
	}
	invitations, err := s.Catalog.PendingInvitations(r.Context(), user.ID)
	if err != nil {
		catalogError(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"memberships": items, "invitations": invitations})
}

func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	if err := s.Catalog.AcceptInvitation(r.Context(), user.ID, chi.URLParam(r, "id")); err != nil {
		catalogError(w, err)
		return
	}
	if err := s.syncMember(r, chi.URLParam(r, "id"), user.ID); err != nil {
		catalogError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handoff(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var input struct {
		TenantID      string `json:"tenantId"`
		CodeChallenge string `json:"codeChallenge"`
	}
	if !decode(w, r, &input) {
		return
	}
	tenant, err := s.Catalog.Tenant(r.Context(), input.TenantID)
	if err != nil {
		catalogError(w, err)
		return
	}
	origin := s.Config.TenantOrigin(tenant.Slug)
	code, err := s.Catalog.CreateHandoff(r.Context(), tenancy.HandoffRequest{UserID: user.ID, TenantID: tenant.ID, Origin: origin, CodeChallenge: input.CodeChallenge})
	if err != nil {
		catalogError(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]string{"url": origin + "/#tenant_code=" + url.QueryEscape(code)})
}

func (s *Server) tenantAuth(w http.ResponseWriter, r *http.Request, tenant tenancy.Tenant) {
	switch {
	case r.URL.Path == "/api/v1/hosted/auth/start" && r.Method == http.MethodGet:
		var entropy [32]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			problem(w, http.StatusInternalServerError, "Sign-in could not start.")
			return
		}
		verifier := base64.RawURLEncoding.EncodeToString(entropy[:])
		challenge := sha256.Sum256([]byte(verifier))
		setSession(w, "__Host-dispatch-handoff", verifier, int((5 * time.Minute).Seconds()))
		params := url.Values{"tenant": []string{tenant.ID}, "challenge": []string{base64.RawURLEncoding.EncodeToString(challenge[:])}}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, s.Config.Origin()+"/?"+params.Encode(), http.StatusSeeOther)
	case r.URL.Path == "/api/v1/hosted/auth/exchange" && r.Method == http.MethodPost:
		var input struct {
			Code string `json:"code"`
		}
		if !decode(w, r, &input) {
			return
		}
		cookie, err := r.Cookie("__Host-dispatch-handoff")
		if err != nil {
			problem(w, http.StatusForbidden, "Start sign-in again.")
			return
		}
		token, err := s.Catalog.ExchangeHandoff(r.Context(), tenancy.HandoffExchange{Code: input.Code, TenantID: tenant.ID, Origin: s.Config.TenantOrigin(tenant.Slug), CodeVerifier: cookie.Value, SessionDuration: s.Config.SessionDuration})
		if err != nil {
			catalogError(w, err)
			return
		}
		setSession(w, "__Host-dispatch-handoff", "", -1)
		setSession(w, tenantCookie, token, int(s.Config.SessionDuration.Seconds()))
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/api/v1/hosted/auth/logout" && r.Method == http.MethodPost:
		token := requestToken(r, tenantCookie)
		if _, err := s.Catalog.AuthenticateSession(r.Context(), token, tenancy.TenantAudience(tenant.ID)); err == nil {
			if err = s.Catalog.RevokeSession(r.Context(), token); err != nil {
				catalogError(w, err)
				return
			}
		}
		setSession(w, tenantCookie, "", -1)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func cleanEmail(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
