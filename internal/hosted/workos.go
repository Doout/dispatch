package hosted

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/doout/dispatch/internal/tenancy"
)

const workOSBrowserCookie = "__Host-dispatch-workos"

func loginEntropy() (string, error) {
	var b [32]byte
	_, err := rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:]), err
}

func (s *Server) startWorkOS(w http.ResponseWriter, r *http.Request) {
	if s.workos == nil {
		http.NotFound(w, r)
		return
	}
	if !s.throttle.allow(r, "workos") {
		problem(w, 429, "Wait before trying again.")
		return
	}
	var input struct {
		TenantID        string `json:"tenantId"`
		CodeChallenge   string `json:"codeChallenge"`
		Link            bool   `json:"link"`
		Waitlist        bool   `json:"waitlist"`
		InvitationToken string `json:"invitationToken"`
		Provider        string `json:"provider"`
		Email           string `json:"email"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Provider != "" && ((input.Provider != "github" && input.Provider != "google" && input.Provider != "sso") || !s.Config.WorkOS.hasLoginMethod(input.Provider) || input.Waitlist || input.Link) {
		problem(w, 400, "This sign-in method is not enabled.")
		return
	}
	if input.Provider == "sso" && !validManagedEmail(cleanEmail(input.Email)) {
		problem(w, 400, "Enter your email to use SSO.")
		return
	}
	if len(input.InvitationToken) > 512 || (input.InvitationToken != "" && (input.Link || input.Waitlist)) {
		problem(w, http.StatusBadRequest, "Use your invitation from the main sign-in page.")
		return
	}
	if input.Waitlist && (input.Link || input.TenantID != "" || input.CodeChallenge != "") {
		problem(w, http.StatusBadRequest, "Start the waitlist from the main sign-in page.")
		return
	}
	attempt := tenancy.LoginAttempt{ReturnTo: "/", InvitationToken: input.InvitationToken}
	if input.Link {
		u, ok := s.user(w, r)
		if !ok {
			return
		}
		attempt.LinkUser = u.ID
		attempt.LinkVersion = u.Version
		attempt.ReturnTo = "/?view=account"
	}
	if input.TenantID != "" {
		tenant, err := s.Catalog.Tenant(r.Context(), input.TenantID)
		if err != nil {
			catalogError(w, err)
			return
		}
		challenge, err := base64.RawURLEncoding.DecodeString(input.CodeChallenge)
		if err != nil || len(challenge) != 32 {
			problem(w, 400, "Start sign-in from the instance address.")
			return
		}
		if err = s.syncWorkOSOrganization(r.Context(), tenant); err != nil {
			problem(w, http.StatusServiceUnavailable, "Instance sign-in is being prepared. Try again shortly.")
			return
		}
		attempt.OrganizationID, err = s.Catalog.WorkOSOrganization(r.Context(), tenant.ID)
		if err != nil {
			catalogError(w, err)
			return
		}
		attempt.ReturnTo = "/?" + url.Values{"tenant": {tenant.ID}, "challenge": {input.CodeChallenge}}.Encode()
	}
	browser, err := loginEntropy()
	if err != nil {
		catalogError(w, err)
		return
	}
	attempt.Verifier, err = loginEntropy()
	if err != nil {
		catalogError(w, err)
		return
	}
	state, err := s.Catalog.CreateLoginAttempt(r.Context(), browser, attempt)
	if err != nil {
		catalogError(w, err)
		return
	}
	challenge := sha256.Sum256([]byte(attempt.Verifier))
	setSession(w, workOSBrowserCookie, browser, 600)
	authorization := s.workos.AuthorizationURL(s.Config.Origin()+"/api/v1/account/workos/callback", state, base64.RawURLEncoding.EncodeToString(challenge[:]), attempt.OrganizationID)
	if input.Provider != "" {
		client, ok := s.workos.(interface {
			AuthorizationURLFor(string, string, string, string, string, string) string
		})
		if !ok {
			problem(w, 503, "This sign-in method is unavailable.")
			return
		}
		authorization = client.AuthorizationURLFor(s.Config.Origin()+"/api/v1/account/workos/callback", state, base64.RawURLEncoding.EncodeToString(challenge[:]), attempt.OrganizationID, input.Provider, cleanEmail(input.Email))
	}
	if input.Waitlist || input.InvitationToken != "" {
		u, err := url.Parse(authorization)
		if err != nil {
			catalogError(w, err)
			return
		}
		query := u.Query()
		if input.Waitlist {
			query.Set("screen_hint", "sign-up")
		}
		if input.InvitationToken != "" {
			query.Set("invitation_token", input.InvitationToken)
		}
		u.RawQuery = query.Encode()
		authorization = u.String()
	}
	respond(w, 200, map[string]string{"authorizationUrl": authorization})
}

func (s *Server) completeWorkOS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	fail := func(message string) {
		http.Redirect(w, r, s.Config.Origin()+"/?view=login&auth_error="+url.QueryEscape(message), http.StatusSeeOther)
	}
	if s.workos == nil {
		http.NotFound(w, r)
		return
	}
	// Invitations opened directly in AuthKit have no local state or PKCE
	// verifier. Start a fresh login rather than exchanging an unbound code.
	if r.URL.Query().Get("state") == "" && r.URL.Query().Get("code") != "" && r.URL.Query().Get("error") == "" {
		http.Redirect(w, r, s.Config.Origin()+"/?view=login&workos_return=1", http.StatusSeeOther)
		return
	}
	cookie, err := r.Cookie(workOSBrowserCookie)
	if err != nil {
		fail("Your sign-in session expired. Enter your email to try again.")
		return
	}
	attempt, err := s.Catalog.ConsumeLoginAttempt(r.Context(), r.URL.Query().Get("state"), cookie.Value)
	setSession(w, workOSBrowserCookie, "", -1)
	if err != nil || r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		fail("Sign-in expired or was cancelled. Try again.")
		return
	}
	var external workOSIdentity
	if client, ok := s.workos.(interface {
		ExchangeInvitation(context.Context, string, string, string) (workOSIdentity, error)
	}); ok {
		external, err = client.ExchangeInvitation(r.Context(), r.URL.Query().Get("code"), attempt.Verifier, attempt.InvitationToken)
	} else {
		external, err = s.workos.Exchange(r.Context(), r.URL.Query().Get("code"), attempt.Verifier)
	}
	if err != nil {
		if client, ok := s.workos.(interface {
			authenticationContinuation(context.Context, managedAuthentication, error) (managedResult, error)
		}); ok {
			result, continueErr := client.authenticationContinuation(r.Context(), managedAuthentication{Invitation: attempt.InvitationToken, Deadline: time.Now().Add(10 * time.Minute)}, err)
			if continueErr == nil && result.Next != nil {
				progress, e := s.createManagedStep(w, r, attempt, *result.Next, "")
				if e == nil {
					http.Redirect(w, r, s.Config.Origin()+"/?view=login&managed_state="+url.QueryEscape(progress.State), http.StatusSeeOther)
					return
				}
			}
		}
		fail("Sign-in could not be verified. Try again.")
		return
	}
	if err := s.issueWorkOSSession(w, r, attempt, external); err != nil {
		fail(err.Error())
		return
	}
	http.Redirect(w, r, s.Config.Origin()+attempt.ReturnTo, http.StatusSeeOther)
}

// All managed login methods share admission, explicit identity linking and
// session creation. WorkOS organization membership never grants local access.
func (s *Server) issueWorkOSSession(w http.ResponseWriter, r *http.Request, attempt tenancy.LoginAttempt, external workOSIdentity) error {
	if external.Subject == "" || external.SessionID == "" || external.Email == "" {
		return errors.New("Sign-in could not be verified. Try again.")
	}
	if attempt.OrganizationID != "" && external.OrganizationID != attempt.OrganizationID {
		return errors.New("Sign-in returned a different organization.")
	}
	if attempt.LinkUser != "" {
		user, err := s.authenticateSession(r.Context(), requestToken(r, platformCookie), tenancy.AudiencePlatform)
		if err != nil || user.ID != attempt.LinkUser || user.Version != attempt.LinkVersion {
			return errors.New("Sign in to your existing account before linking it.")
		}
	}
	if attempt.LinkUser == "" {
		_, lookupErr := s.Catalog.ExternalUser(r.Context(), "workos", external.Subject)
		if errors.Is(lookupErr, tenancy.ErrNotFound) {
			if err := s.workos.AdmissionAllowed(r.Context(), external); err != nil {
				return errors.New("Access is by invitation. Join the waitlist to request access.")
			}
		} else if lookupErr != nil {
			return errors.New("Sign-in could not be verified. Try again.")
		}
	}
	return s.completeWorkOSSession(w, r, attempt, external)
}

// Callers must verify either the current local session or the previous password
// before passing a LinkUser. Email verification alone never links accounts.
func (s *Server) completeWorkOSSession(w http.ResponseWriter, r *http.Request, attempt tenancy.LoginAttempt, external workOSIdentity) error {
	user, err := s.Catalog.ResolveExternalUser(r.Context(), tenancy.ExternalLogin{Provider: "workos", Subject: external.Subject, Email: external.Email, Name: external.Name, LinkUserID: attempt.LinkUser, LinkVersion: attempt.LinkVersion})
	if err != nil {
		if errors.Is(err, tenancy.ErrConflict) {
			return errors.New("This email already belongs to a Doodship account. Use its connected WorkOS login.")
		}
		return errors.New("This account cannot sign in.")
	}
	token, err := s.Catalog.CreateExternalSession(r.Context(), user.ID, tenancy.AudiencePlatform, s.Config.SessionDuration, tenancy.LoginOrigin{Provider: "workos", Subject: external.Subject, SessionID: external.SessionID})
	if err != nil {
		return errors.New("Sign-in could not finish. Try again.")
	}
	setSession(w, platformCookie, token, int(s.Config.SessionDuration.Seconds()))
	return nil
}

func (s *Server) authenticateSession(ctx context.Context, token, audience string) (tenancy.User, error) {
	user, err := s.Catalog.AuthenticateSession(ctx, token, audience)
	if err != nil {
		return user, err
	}
	origin, err := s.Catalog.LoginOrigin(ctx, token)
	if errors.Is(err, tenancy.ErrNotFound) {
		if !s.passwordLoginAllowed() {
			return tenancy.User{}, tenancy.ErrDenied
		}
		return user, nil
	}
	if err != nil {
		return tenancy.User{}, err
	}
	if origin.Provider == "workos" {
		if s.workos == nil {
			return tenancy.User{}, tenancy.ErrDenied
		}
		if err = s.checkWorkOSSession(ctx, origin.Subject, origin.SessionID); err != nil {
			return tenancy.User{}, err
		}
	}
	return user, nil
}

func (s *Server) syncWorkOSOrganization(ctx context.Context, tenant tenancy.Tenant) error {
	if s.workos == nil {
		return nil
	}
	// Serialize local syncs; external_id recovers an organization after a crash
	// between its creation and the catalog write.
	s.workosMu.Lock()
	defer s.workosMu.Unlock()
	// Reconciliation may have listed this instance before its owner deleted it.
	if _, err := s.Catalog.Tenant(ctx, tenant.ID); errors.Is(err, tenancy.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	id, err := s.Catalog.WorkOSOrganization(ctx, tenant.ID)
	if errors.Is(err, tenancy.ErrNotFound) {
		id, err = s.workos.EnsureOrganization(ctx, s.Config.RootDomain+":"+tenant.ID, tenant.Name)
		if err == nil {
			err = s.Catalog.BindWorkOSOrganization(ctx, tenant.ID, id)
		}
	}
	if err != nil {
		return err
	}
	members, err := s.Catalog.TenantMembers(ctx, tenant.ID)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, m := range members {
		subject, e := s.Catalog.ExternalSubject(ctx, "workos", m.UserID)
		if errors.Is(e, tenancy.ErrNotFound) {
			continue
		}
		if e != nil {
			return e
		}
		user, e := s.Catalog.User(ctx, m.UserID)
		if e != nil {
			return e
		}
		wanted[subject] = m.State == tenancy.StateActive && user.State == tenancy.StateActive
	}
	raw, err := json.Marshal(wanted)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	previous, err := s.Catalog.WorkOSSyncDigest(ctx, tenant.ID)
	if err != nil {
		return err
	}
	if previous == digest {
		return nil
	}
	if err = s.workos.SyncMembers(ctx, id, wanted); err != nil {
		return err
	}
	return s.Catalog.SaveWorkOSSyncDigest(ctx, tenant.ID, digest)
}

func (s *Server) passwordLoginAllowed() bool {
	return s.workos == nil || s.Config.WorkOS.AllowPasswordLogin
}

// Bound upstream revocation latency while avoiding an API request for every
// tenant poll or stream tick. Fail closed when the cache expires.
func (s *Server) checkWorkOSSession(ctx context.Context, subject, id string) error {
	key := subject + ":" + id
	s.workosSessionMu.Lock()
	until := s.workosSessions[key]
	s.workosSessionMu.Unlock()
	if time.Now().Before(until) {
		return nil
	}
	if err := s.workos.SessionActive(ctx, subject, id); err != nil {
		return err
	}
	s.workosSessionMu.Lock()
	defer s.workosSessionMu.Unlock()
	if s.workosSessions == nil {
		s.workosSessions = map[string]time.Time{}
	}
	if len(s.workosSessions) >= 32768 {
		clear(s.workosSessions)
	}
	s.workosSessions[key] = time.Now().Add(30 * time.Second)
	return nil
}
