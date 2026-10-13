package hosted

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/tenancy"
	workos "github.com/workos/workos-go/v10"
)

const managedBrowserCookie = "__Host-dispatch-managed-login"

type managedLoginInput struct {
	Method           string `json:"method"`
	Email            string `json:"email"`
	Password         string `json:"password"`
	PreviousPassword string `json:"previousPassword"`
	Code             string `json:"code"`
	State            string `json:"state"`
	OrganizationID   string `json:"organizationId"`
	InvitationToken  string `json:"invitationToken"`
	TenantID         string `json:"tenantId"`
	CodeChallenge    string `json:"codeChallenge"`
	Link             bool   `json:"link"`
	Resume           bool   `json:"resume"`
}
type managedLoginResponse struct {
	Step          string                `json:"step"`
	State         string                `json:"state,omitempty"`
	Email         string                `json:"email,omitempty"`
	Secret        string                `json:"secret,omitempty"`
	Organizations []managedOrganization `json:"organizations,omitempty"`
	Message       string                `json:"message,omitempty"`
	ReturnTo      string                `json:"returnTo,omitempty"`
}

func (s *Server) managedService(w http.ResponseWriter, r *http.Request) (workOSFormService, bool) {
	client, ok := s.workos.(workOSFormService)
	if !ok {
		http.NotFound(w, r)
		return nil, false
	}
	// Login CSRF matters even before a session cookie exists.
	if !validOrigin(r.Header.Get("Origin"), s.Config.Origin()) {
		problem(w, 403, "Origin not allowed.")
		return nil, false
	}
	w.Header().Set("Cache-Control", "no-store")
	return client, true
}
func validManagedEmail(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value && len(value) <= 254
}
func managedIP(r *http.Request) string { ip, _, _ := net.SplitHostPort(r.RemoteAddr); return ip }

func (s *Server) managedAttempt(w http.ResponseWriter, r *http.Request, in managedLoginInput) (tenancy.LoginAttempt, bool) {
	a := tenancy.LoginAttempt{ReturnTo: "/"}
	if in.Link {
		u, ok := s.user(w, r)
		if !ok {
			return a, false
		}
		if !strings.EqualFold(u.Email, in.Email) || in.InvitationToken != "" || in.TenantID != "" {
			problem(w, 400, "Use your existing account email to connect WorkOS.")
			return a, false
		}
		a.LinkUser, a.LinkVersion, a.ReturnTo = u.ID, u.Version, "/?view=account"
	}
	if in.TenantID != "" {
		// A tenant handoff is still checked against local membership after login.
		tenant, err := s.Catalog.Tenant(r.Context(), in.TenantID)
		if err != nil {
			catalogError(w, err)
			return a, false
		}
		if !validCodeChallenge(in.CodeChallenge) {
			problem(w, 400, "Start sign-in from the instance address.")
			return a, false
		}
		if err := s.syncWorkOSOrganization(r.Context(), tenant); err != nil {
			problem(w, 503, "Instance sign-in is being prepared. Try again shortly.")
			return a, false
		}
		a.OrganizationID, err = s.Catalog.WorkOSOrganization(r.Context(), tenant.ID)
		if err != nil {
			catalogError(w, err)
			return a, false
		}
		a.ReturnTo = "/?" + url.Values{"tenant": {tenant.ID}, "challenge": {in.CodeChallenge}}.Encode()
	} else if in.CodeChallenge != "" {
		problem(w, 400, "Start sign-in from the instance address.")
		return a, false
	}
	return a, true
}

func (s *Server) saveManagedStep(w http.ResponseWriter, r *http.Request, attempt tenancy.LoginAttempt, state managedAuthentication, message string) {
	result, err := s.createManagedStep(w, r, attempt, state, message)
	if err != nil {
		if errors.Is(err, tenancy.ErrExpired) {
			problem(w, 401, "Sign-in expired. Start again.")
		} else {
			problem(w, 503, "Sign-in is unavailable. Try again shortly.")
		}
		return
	}
	respond(w, 200, result)
}
func (s *Server) createManagedStep(w http.ResponseWriter, r *http.Request, attempt tenancy.LoginAttempt, state managedAuthentication, message string) (managedLoginResponse, error) {
	if !time.Now().Before(state.Deadline) {
		return managedLoginResponse{}, tenancy.ErrExpired
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return managedLoginResponse{}, err
	}
	attempt.Continuation = raw
	browser, err := loginEntropy()
	if err != nil {
		return managedLoginResponse{}, err
	}
	token, err := s.Catalog.CreateLoginAttempt(r.Context(), browser, attempt)
	if err != nil {
		return managedLoginResponse{}, err
	}
	setSession(w, managedBrowserCookie, browser, 600)
	return managedLoginResponse{Step: state.Step, State: token, Email: state.Email, Secret: state.Secret, Organizations: state.Organizations, Message: message}, nil
}

func (s *Server) managedLogin(w http.ResponseWriter, r *http.Request) {
	client, ok := s.managedService(w, r)
	if !ok {
		return
	}
	var in managedLoginInput
	if !decode(w, r, &in) {
		return
	}
	var attempt tenancy.LoginAttempt
	var state managedAuthentication
	if in.State != "" {
		cookie, err := r.Cookie(managedBrowserCookie)
		if err != nil {
			problem(w, 401, "Sign-in expired. Start again.")
			return
		}
		attempt, err = s.Catalog.ConsumeLoginAttempt(r.Context(), in.State, cookie.Value)
		if err != nil || len(attempt.Continuation) == 0 || json.Unmarshal(attempt.Continuation, &state) != nil || !time.Now().Before(state.Deadline) {
			problem(w, 401, "Sign-in expired. Start again.")
			return
		}
		setSession(w, managedBrowserCookie, "", -1)
		if !s.throttle.allow(r, "managed:"+state.Email) {
			problem(w, 429, "Wait before trying again.")
			return
		}
		if in.Resume {
			s.saveManagedStep(w, r, attempt, state, "")
			return
		}
		if state.Step == "link-account" {
			s.finishManagedMigration(w, r, attempt, state, in.PreviousPassword)
			return
		}
		if state.Step == "organization" {
			allowed := false
			for _, org := range state.Organizations {
				if org.ID == in.OrganizationID {
					allowed = true
				}
			}
			if !allowed {
				s.saveManagedStep(w, r, attempt, state, "Choose an organization from the list.")
				return
			}
		} else if !validManagedCode(in.Code) {
			s.saveManagedStep(w, r, attempt, state, "Enter the six-digit code.")
			return
		}
	} else {
		if in.Resume {
			problem(w, 400, "Start sign-in again.")
			return
		}
		in.Email = cleanEmail(in.Email)
		if !validManagedEmail(in.Email) || len(in.InvitationToken) > 512 || len(in.Password) > 4096 {
			problem(w, 400, "Enter a valid email and sign-in details.")
			return
		}
		if !s.throttle.allow(r, "managed:"+in.Email) {
			problem(w, 429, "Wait before trying again.")
			return
		}
		if in.Method != "password" && in.Method != "email-code" || !s.Config.WorkOS.hasLoginMethod(in.Method) {
			problem(w, 400, "This sign-in method is not enabled.")
			return
		}
		attempt, ok = s.managedAttempt(w, r, in)
		if !ok {
			return
		}
		state = managedAuthentication{Step: in.Method, Email: in.Email, Invitation: in.InvitationToken, Deadline: time.Now().Add(10 * time.Minute)}
		// Reject invalid invitations before sending mail or accepting credentials.
		if in.InvitationToken != "" {
			email, err := client.InvitationEmail(r.Context(), in.InvitationToken)
			if err != nil || email != in.Email {
				problem(w, 400, "This invitation is invalid or has expired.")
				return
			}
		}
		if in.Method == "email-code" {
			// Do not create uninvited WorkOS users through the headless API.
			eligible := in.InvitationToken != "" || attempt.LinkUser != ""
			prepareUser := eligible
			if !eligible {
				u, err := s.Catalog.UserByEmail(r.Context(), in.Email)
				if err == nil {
					_, err = s.Catalog.ExternalSubject(r.Context(), "workos", u.ID)
					eligible = err == nil && u.State == tenancy.StateActive
					if errors.Is(err, tenancy.ErrNotFound) && s.canMigrateAccount(u) {
						eligible, prepareUser = true, true
					}
				}
				if err != nil && !errors.Is(err, tenancy.ErrNotFound) {
					problem(w, 503, "Sign-in is unavailable. Try again.")
					return
				}
				if !eligible {
					eligible, err = client.EmailAllowed(r.Context(), in.Email)
					if err != nil {
						problem(w, 503, "Sign-in is unavailable. Try again.")
						return
					}
				}
			}
			state.Denied = !eligible
			if eligible {
				if err := client.SendCode(r.Context(), in.Email, in.InvitationToken, managedIP(r), r.UserAgent(), prepareUser); err != nil {
					managedProviderError(w, err, "The code could not be sent. Try again.")
					return
				}
			}
			s.saveManagedStep(w, r, attempt, state, "")
			return
		}
		if in.Password == "" {
			problem(w, 400, "Enter your password.")
			return
		}
	}
	if state.Denied {
		s.saveManagedStep(w, r, attempt, state, "The code was not accepted. Check your invitation or try again.")
		return
	}
	result, err := client.Authenticate(r.Context(), state, managedCredentials{Password: in.Password, Code: in.Code, Organization: in.OrganizationID, IP: managedIP(r), Agent: r.UserAgent()})
	if err != nil {
		if in.State != "" {
			message := "The code was not accepted. Try again, or start sign-in again."
			var apiErr *workos.APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode >= 500 {
				message = "Sign-in is unavailable. Try again shortly."
			}
			s.saveManagedStep(w, r, attempt, state, message)
		} else {
			managedProviderError(w, err, "Email or password was not accepted.")
		}
		return
	}
	if result.Next != nil {
		if result.Next.Step == "organization" && attempt.OrganizationID != "" {
			allowed := []managedOrganization{}
			for _, org := range result.Next.Organizations {
				if org.ID == attempt.OrganizationID {
					allowed = append(allowed, org)
				}
			}
			if len(allowed) == 0 {
				problem(w, 403, "This account cannot sign in to this instance.")
				return
			}
			result.Next.Organizations = allowed
		}
		s.saveManagedStep(w, r, attempt, *result.Next, "")
		return
	}
	if result.Identity == nil || !strings.EqualFold(result.Identity.Email, state.Email) {
		problem(w, 401, "Sign-in could not be verified.")
		return
	}
	if attempt.LinkUser == "" && s.passwordLoginAllowed() {
		_, externalErr := s.Catalog.ExternalUser(r.Context(), "workos", result.Identity.Subject)
		user, userErr := s.Catalog.UserByEmail(r.Context(), result.Identity.Email)
		if errors.Is(externalErr, tenancy.ErrNotFound) && userErr == nil && s.canMigrateAccount(user) {
			_, linkedErr := s.Catalog.ExternalSubject(r.Context(), "workos", user.ID)
			if errors.Is(linkedErr, tenancy.ErrNotFound) {
				attempt.LinkUser, attempt.LinkVersion = user.ID, user.Version
				state = managedAuthentication{Step: "link-account", Email: state.Email, Deadline: state.Deadline, Migration: result.Identity}
				s.saveManagedStep(w, r, attempt, state, "")
				return
			}
		}
	}
	if err = s.issueWorkOSSession(w, r, attempt, *result.Identity); err != nil {
		problem(w, 403, err.Error())
		return
	}
	setSession(w, managedBrowserCookie, "", -1)
	respond(w, 200, managedLoginResponse{Step: "complete", ReturnTo: attempt.ReturnTo})
}

func (s *Server) canMigrateAccount(user tenancy.User) bool {
	return s.passwordLoginAllowed() && user.State == tenancy.StateActive && user.EmailVerified && user.PasswordHash != ""
}

func (s *Server) finishManagedMigration(w http.ResponseWriter, r *http.Request, attempt tenancy.LoginAttempt, state managedAuthentication, password string) {
	if !s.passwordLoginAllowed() || attempt.LinkUser == "" || state.Migration == nil || state.Migration.Subject == "" || state.Migration.SessionID == "" || !strings.EqualFold(state.Migration.Email, state.Email) || (attempt.OrganizationID != "" && attempt.OrganizationID != state.Migration.OrganizationID) {
		problem(w, 403, "Account connection is unavailable. Start sign-in again.")
		return
	}
	if password == "" || len(password) > 4096 {
		s.saveManagedStep(w, r, attempt, state, "Enter your previous Doodship password.")
		return
	}
	user, err := s.Catalog.AuthenticatePassword(r.Context(), state.Email, password)
	if err != nil || user.ID != attempt.LinkUser || user.Version != attempt.LinkVersion || !s.canMigrateAccount(user) {
		s.saveManagedStep(w, r, attempt, state, "Your previous password was not accepted. Try again.")
		return
	}
	// A code may have been verified minutes ago. Check the provider again before
	// connecting it, rather than accepting a revoked session from cached state.
	if err := s.workos.SessionActive(r.Context(), state.Migration.Subject, state.Migration.SessionID); err != nil {
		problem(w, 401, "Sign-in expired. Start again.")
		return
	}
	if err := s.completeWorkOSSession(w, r, attempt, *state.Migration); err != nil {
		problem(w, 403, err.Error())
		return
	}
	setSession(w, managedBrowserCookie, "", -1)
	respond(w, 200, managedLoginResponse{Step: "complete", ReturnTo: attempt.ReturnTo})
}

func managedProviderError(w http.ResponseWriter, err error, message string) {
	var apiErr *workos.APIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == 429 {
			problem(w, 429, "Wait before trying again.")
			return
		}
		if apiErr.StatusCode < 500 {
			problem(w, 400, message)
			return
		}
	}
	problem(w, 503, "Sign-in is unavailable. Try again shortly.")
}
func validManagedCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func validCodeChallenge(challenge string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil && len(raw) == 32
}

func (s *Server) managedWaitlist(w http.ResponseWriter, r *http.Request) {
	client, ok := s.managedService(w, r)
	if !ok {
		return
	}
	if s.Config.AllowRegistration {
		http.NotFound(w, r)
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Email = cleanEmail(in.Email)
	if !validManagedEmail(in.Email) {
		problem(w, 400, "Enter a valid email address.")
		return
	}
	if !s.throttle.allow(r, "waitlist:"+in.Email) {
		problem(w, 429, "Wait before trying again.")
		return
	}
	if err := client.JoinWaitlist(r.Context(), in.Email); err != nil {
		managedProviderError(w, err, "Your request could not be saved. Try again.")
		return
	}
	respond(w, 202, map[string]string{"message": "Your request has been received. We'll email you when an invitation is available."})
}
func (s *Server) managedPasswordReset(w http.ResponseWriter, r *http.Request) {
	client, ok := s.managedService(w, r)
	if !ok {
		return
	}
	if !s.Config.WorkOS.hasLoginMethod("password") {
		http.NotFound(w, r)
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Email = cleanEmail(in.Email)
	if !validManagedEmail(in.Email) {
		problem(w, 400, "Enter a valid email address.")
		return
	}
	if !s.throttle.allow(r, "reset:"+in.Email) {
		problem(w, 429, "Wait before trying again.")
		return
	}
	err := client.RequestPasswordReset(r.Context(), in.Email)
	var apiErr *workos.APIError
	if err != nil && (!errors.As(err, &apiErr) || apiErr.StatusCode >= 500 || apiErr.StatusCode == 429) {
		managedProviderError(w, err, "")
		return
	}
	respond(w, 202, map[string]string{"message": "If this email has an account, you'll receive a password reset link."})
}
func (s *Server) managedConfirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	client, ok := s.managedService(w, r)
	if !ok {
		return
	}
	if !s.Config.WorkOS.hasLoginMethod("password") {
		http.NotFound(w, r)
		return
	}
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Token == "" || len(in.Token) > 512 || len(in.Password) < 8 || len(in.Password) > 4096 {
		problem(w, 400, "Enter a password and use a valid reset link.")
		return
	}
	if !s.throttle.allow(r, "reset-token:"+in.Token) {
		problem(w, 429, "Wait before trying again.")
		return
	}
	if err := client.ResetPassword(r.Context(), in.Token, in.Password); err != nil {
		managedProviderError(w, err, "The reset link or password was not accepted. Check the link and use a stronger password.")
		return
	}
	respond(w, 200, map[string]string{"message": "Password changed. Sign in with your new password."})
}
func (s *Server) managedInvitation(w http.ResponseWriter, r *http.Request) {
	client, ok := s.managedService(w, r)
	if !ok {
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Token == "" || len(in.Token) > 512 {
		problem(w, 400, "Use a valid invitation link.")
		return
	}
	if !s.throttle.allow(r, "invitation") {
		problem(w, 429, "Wait before trying again.")
		return
	}
	email, err := client.InvitationEmail(r.Context(), in.Token)
	if err != nil {
		managedProviderError(w, err, "This invitation is invalid or has expired.")
		return
	}
	respond(w, 200, map[string]string{"email": email})
}
