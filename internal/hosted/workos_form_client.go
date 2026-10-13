package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/tenancy"
	workos "github.com/workos/workos-go/v10"
)

// Provider continuation tokens and challenge IDs stay on the server. The UI
// receives only the step, display information, and a browser-bound local state.
type managedAuthentication struct {
	Step          string                `json:"step"`
	Email         string                `json:"email,omitempty"`
	Invitation    string                `json:"invitation,omitempty"`
	PendingToken  string                `json:"pendingToken,omitempty"`
	Challenge     string                `json:"challenge,omitempty"`
	Secret        string                `json:"secret,omitempty"`
	Organizations []managedOrganization `json:"organizations,omitempty"`
	Deadline      time.Time             `json:"deadline"`
	Denied        bool                  `json:"denied,omitempty"`
	Migration     *workOSIdentity       `json:"migration,omitempty"`
}
type managedOrganization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type managedCredentials struct{ Password, Code, Organization, IP, Agent string }
type managedResult struct {
	Identity *workOSIdentity
	Next     *managedAuthentication
}
type workOSFormService interface {
	Authenticate(context.Context, managedAuthentication, managedCredentials) (managedResult, error)
	SendCode(context.Context, string, string, string, string, bool) error
	InvitationEmail(context.Context, string) (string, error)
	RequestPasswordReset(context.Context, string) error
	ResetPassword(context.Context, string, string) error
	JoinWaitlist(context.Context, string) error
	EmailAllowed(context.Context, string) (bool, error)
}

func (c *workOSClient) EmailAllowed(ctx context.Context, email string) (bool, error) {
	users := c.client.UserManagement().List(ctx, &workos.UserManagementListParams{Email: &email})
	for users.Next() {
		u := users.Current()
		if !strings.EqualFold(u.Email, email) {
			continue
		}
		err := c.AdmissionAllowed(ctx, workOSIdentity{Subject: u.ID, Email: u.Email})
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, tenancy.ErrDenied) {
			return false, err
		}
	}
	return false, users.Err()
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (c *workOSClient) AuthorizationURLFor(callback, state, challenge, organization, method, email string) string {
	pkce := "S256"
	params := &workos.UserManagementGetAuthorizationURLParams{RedirectURI: callback, State: &state, CodeChallenge: &challenge, CodeChallengeMethod: &pkce, OrganizationID: optional(organization)}
	if method == "sso" {
		params.LoginHint = optional(email)
		if _, domain, ok := strings.Cut(email, "@"); ok {
			params.DomainHint = &domain
		}
	} else {
		provider := workos.UserManagementAuthenticationProvider(map[string]string{"github": "GitHubOAuth", "google": "GoogleOAuth"}[method])
		params.Provider = &provider
	}
	return c.client.UserManagement().GetAuthorizationURL(params)
}

func (c *workOSClient) Authenticate(ctx context.Context, state managedAuthentication, in managedCredentials) (managedResult, error) {
	var result *workos.AuthenticateResponse
	var err error
	ip, agent := optional(in.IP), optional(in.Agent)
	switch state.Step {
	case "password":
		result, err = c.client.UserManagement().AuthenticateWithPassword(ctx, &workos.UserManagementAuthenticateWithPasswordParams{Email: state.Email, Password: in.Password, InvitationToken: optional(state.Invitation), IPAddress: ip, UserAgent: agent})
	case "email-code":
		result, err = c.client.UserManagement().AuthenticateWithMagicAuth(ctx, &workos.UserManagementAuthenticateWithMagicAuthParams{Email: state.Email, Code: in.Code, InvitationToken: optional(state.Invitation), IPAddress: ip, UserAgent: agent})
	case "verify-email":
		result, err = c.client.UserManagement().AuthenticateWithEmailVerification(ctx, &workos.UserManagementAuthenticateWithEmailVerificationParams{Code: in.Code, PendingAuthenticationToken: state.PendingToken, IPAddress: ip, UserAgent: agent})
	case "totp":
		result, err = c.client.UserManagement().AuthenticateWithTOTP(ctx, &workos.UserManagementAuthenticateWithTOTPParams{Code: in.Code, PendingAuthenticationToken: state.PendingToken, AuthenticationChallengeID: state.Challenge, IPAddress: ip, UserAgent: agent})
	case "organization":
		result, err = c.client.UserManagement().AuthenticateWithOrganizationSelection(ctx, &workos.UserManagementAuthenticateWithOrganizationSelectionParams{OrganizationID: in.Organization, PendingAuthenticationToken: state.PendingToken, IPAddress: ip, UserAgent: agent})
	default:
		return managedResult{}, tenancy.ErrDenied
	}
	if err != nil {
		return c.authenticationContinuation(ctx, state, err)
	}
	identity, err := c.authenticationIdentity(ctx, result)
	if err != nil {
		return managedResult{}, err
	}
	return managedResult{Identity: &identity}, nil
}

func (c *workOSClient) authenticationContinuation(ctx context.Context, state managedAuthentication, err error) (managedResult, error) {
	var apiErr *workos.APIError
	if !errors.As(err, &apiErr) || apiErr.PendingAuthenticationToken == "" {
		return managedResult{}, err
	}
	var body struct {
		Organizations []managedOrganization       `json:"organizations"`
		Factors       []struct{ ID, Type string } `json:"authentication_factors"`
		User          struct{ ID, Email string }  `json:"user"`
	}
	if json.Unmarshal([]byte(apiErr.RawBody), &body) != nil {
		return managedResult{}, err
	}
	if state.Email == "" {
		state.Email = cleanEmail(body.User.Email)
		if state.Email == "" {
			var e struct {
				Email string `json:"email"`
			}
			_ = json.Unmarshal([]byte(apiErr.RawBody), &e)
			state.Email = cleanEmail(e.Email)
		}
	}
	state.PendingToken = apiErr.PendingAuthenticationToken
	state.Secret, state.Challenge = "", ""
	switch apiErr.Code {
	case workos.EmailVerificationRequiredCode:
		state.Step = "verify-email"
	case workos.OrganizationSelectionRequiredCode:
		if len(body.Organizations) == 0 {
			return managedResult{}, err
		}
		state.Step, state.Organizations = "organization", body.Organizations
	case workos.MFAChallengeCode:
		factor := ""
		for _, f := range body.Factors {
			if f.Type == "totp" && f.ID != "" {
				factor = f.ID
				break
			}
		}
		if factor == "" {
			return managedResult{}, err
		}
		challenge, e := c.client.MultiFactorAuth().ChallengeFactor(ctx, factor, &workos.MultiFactorAuthChallengeFactorParams{})
		if e != nil {
			return managedResult{}, e
		}
		state.Step, state.Challenge = "totp", challenge.ID
	case workos.MFAEnrollmentCode:
		if body.User.ID == "" || !strings.EqualFold(body.User.Email, state.Email) {
			return managedResult{}, tenancy.ErrDenied
		}
		issuer := "Doodship"
		factor, e := c.client.MultiFactorAuth().CreateUserAuthFactor(ctx, body.User.ID, &workos.MultiFactorAuthCreateUserAuthFactorParams{Type: "totp", TOTPIssuer: &issuer, TOTPUser: &state.Email})
		if e != nil {
			return managedResult{}, e
		}
		if factor.AuthenticationFactor == nil || factor.AuthenticationFactor.TOTP == nil || factor.AuthenticationChallenge == nil {
			return managedResult{}, tenancy.ErrDenied
		}
		state.Step, state.Challenge, state.Secret = "totp", factor.AuthenticationChallenge.ID, factor.AuthenticationFactor.TOTP.Secret
	default:
		return managedResult{}, err
	}
	return managedResult{Next: &state}, nil
}

func (c *workOSClient) InvitationEmail(ctx context.Context, token string) (string, error) {
	i, err := c.client.UserManagement().FindInvitationByToken(ctx, token)
	if err != nil {
		return "", err
	}
	expires, err := time.Parse(time.RFC3339, i.ExpiresAt)
	if err != nil || !expires.After(time.Now()) || i.State != "pending" {
		return "", tenancy.ErrDenied
	}
	return cleanEmail(i.Email), nil
}
func (c *workOSClient) SendCode(ctx context.Context, email, invitation, ip, agent string, prepare bool) error {
	if prepare {
		users := c.client.UserManagement().List(ctx, &workos.UserManagementListParams{Email: &email})
		found := false
		for users.Next() {
			if strings.EqualFold(users.Current().Email, email) {
				found = true
				break
			}
		}
		if err := users.Err(); err != nil {
			return err
		}
		if !found {
			// Only a validated invitation or a signed-in local account may
			// provision a provider user. Ownership still requires the email code.
			_, err := c.client.UserManagement().Create(ctx, &workos.UserManagementCreateParams{Email: email})
			var apiErr *workos.APIError
			if err != nil && (!errors.As(err, &apiErr) || apiErr.Code != "user_already_exists") {
				return err
			}
		}
	}
	_, err := c.client.UserManagement().CreateMagicAuth(ctx, &workos.UserManagementCreateMagicAuthParams{Email: email, InvitationToken: optional(invitation), IPAddress: optional(ip), UserAgent: optional(agent)})
	return err
}
func (c *workOSClient) RequestPasswordReset(ctx context.Context, email string) error {
	_, err := c.client.UserManagement().ResetPassword(ctx, &workos.UserManagementResetPasswordParams{Email: email})
	return err
}
func (c *workOSClient) ResetPassword(ctx context.Context, token, password string) error {
	_, err := c.client.UserManagement().ConfirmPasswordReset(ctx, &workos.UserManagementConfirmPasswordResetParams{Token: token, NewPassword: password})
	return err
}
func (c *workOSClient) JoinWaitlist(ctx context.Context, email string) error {
	send := true
	_, err := c.client.UserManagement().CreateWaitlistEntry(ctx, "default", &workos.UserManagementCreateWaitlistEntryParams{Email: email, SendConfirmationEmail: &send})
	var apiErr *workos.APIError
	if errors.As(err, &apiErr) && apiErr.Code == "user_already_exists" {
		return nil
	}
	return err
}
