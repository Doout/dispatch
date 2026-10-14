package hosted

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/tenancy"
	workos "github.com/workos/workos-go/v10"
)

type WorkOSConfig struct {
	ClientID           string
	APIKey             string
	AllowPasswordLogin bool
	LoginMethods       []string
}

func (c WorkOSConfig) loginMethods() []string {
	if len(c.LoginMethods) == 0 {
		return []string{"email-code", "password"}
	}
	return c.LoginMethods
}
func (c WorkOSConfig) hasLoginMethod(method string) bool {
	for _, item := range c.loginMethods() {
		if item == method {
			return true
		}
	}
	return false
}

func (c WorkOSConfig) enabled() bool { return c.ClientID != "" && c.APIKey != "" }
func (c WorkOSConfig) validate() error {
	for _, method := range c.LoginMethods {
		switch method {
		case "email-code", "password", "github", "google", "sso":
		default:
			return errors.New("unsupported WorkOS login method")
		}
	}
	if c.enabled() && !c.hasLoginMethod("email-code") {
		return errors.New("WorkOS email codes are required for invitations and account linking")
	}
	if (c.ClientID == "") != (c.APIKey == "") {
		return errors.New("WorkOS requires both a client ID and an API key file")
	}
	if c.AllowPasswordLogin && !c.enabled() {
		return errors.New("WorkOS password migration requires WorkOS configuration")
	}
	return nil
}

type workOSIdentity struct{ Subject, Email, Name, SessionID, OrganizationID string }
type workOSService interface {
	AuthorizationURL(callback, state, challenge, organization string) string
	Exchange(context.Context, string, string) (workOSIdentity, error)
	SessionActive(context.Context, string, string) error
	RevokeSession(context.Context, string) error
	EnsureOrganization(context.Context, string, string) (string, error)
	DeleteOrganization(context.Context, string, string) error
	SyncMembers(context.Context, string, map[string]bool) error
	AdmissionAllowed(context.Context, workOSIdentity) error
}

// Require an accepted invitation when admitting a new platform account. This
// also closes application access if the AuthKit signup settings drift.
func (c *workOSClient) AdmissionAllowed(ctx context.Context, identity workOSIdentity) error {
	iter := c.client.UserManagement().ListInvitations(ctx, &workos.UserManagementListInvitationsParams{Email: &identity.Email})
	for iter.Next() {
		invitation := iter.Current()
		if invitation.State == "accepted" && invitation.AcceptedUserID != nil && *invitation.AcceptedUserID == identity.Subject && strings.EqualFold(invitation.Email, identity.Email) {
			return nil
		}
	}
	if err := iter.Err(); err != nil {
		return err
	}
	return tenancy.ErrDenied
}

type workOSClient struct{ client *workos.Client }

func newWorkOSClient(cfg WorkOSConfig) *workOSClient {
	return &workOSClient{workos.NewClient(cfg.APIKey, workos.WithClientID(cfg.ClientID), workos.WithHTTPClient(&http.Client{Timeout: 15 * time.Second}), workos.WithMaxRetries(1))}
}
func (c *workOSClient) AuthorizationURL(callback, state, challenge, organization string) string {
	provider := workos.UserManagementAuthenticationProvider("authkit")
	method := "S256"
	params := &workos.UserManagementGetAuthorizationURLParams{Provider: &provider, RedirectURI: callback, State: &state, CodeChallenge: &challenge, CodeChallengeMethod: &method}
	if organization != "" {
		params.OrganizationID = &organization
	}
	return c.client.UserManagement().GetAuthorizationURL(params)
}
func (c *workOSClient) Exchange(ctx context.Context, code, verifier string) (workOSIdentity, error) {
	return c.ExchangeInvitation(ctx, code, verifier, "")
}
func (c *workOSClient) ExchangeInvitation(ctx context.Context, code, verifier, invitation string) (workOSIdentity, error) {
	result, err := c.client.UserManagement().AuthenticateWithCode(ctx, &workos.UserManagementAuthenticateWithCodeParams{Code: code, CodeVerifier: &verifier, InvitationToken: optional(invitation)})
	if err != nil {
		return workOSIdentity{}, err
	}
	return c.authenticationIdentity(ctx, result)
}
func (c *workOSClient) authenticationIdentity(ctx context.Context, result *workos.AuthenticateResponse) (workOSIdentity, error) {
	if result == nil {
		return workOSIdentity{}, tenancy.ErrDenied
	}
	if result.User == nil || !result.User.EmailVerified || result.User.ID == "" || result.Impersonator != nil {
		return workOSIdentity{}, tenancy.ErrDenied
	}
	// This token comes exclusively from the authenticated WorkOS API exchange,
	// never from the browser. The session is independently checked through its API.
	parts := strings.Split(result.AccessToken, ".")
	if len(parts) != 3 {
		return workOSIdentity{}, tenancy.ErrDenied
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return workOSIdentity{}, err
	}
	var claims struct {
		SID     string `json:"sid"`
		Subject string `json:"sub"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.SID == "" || claims.Subject != result.User.ID {
		return workOSIdentity{}, tenancy.ErrDenied
	}
	if err = c.SessionActive(ctx, result.User.ID, claims.SID); err != nil {
		return workOSIdentity{}, err
	}
	name := ""
	if result.User.FirstName != nil {
		name = *result.User.FirstName
	}
	if result.User.LastName != nil {
		name += " " + *result.User.LastName
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = result.User.Email
	}
	identity := workOSIdentity{Subject: result.User.ID, Email: result.User.Email, Name: name, SessionID: claims.SID}
	if result.OrganizationID != nil {
		identity.OrganizationID = *result.OrganizationID
	}
	return identity, nil
}
func (c *workOSClient) SessionActive(ctx context.Context, subject, session string) error {
	iter := c.client.UserManagement().ListSessions(ctx, subject, &workos.UserManagementListSessionsParams{})
	for iter.Next() {
		s := iter.Current()
		if s.ID == session && s.UserID == subject && s.Status == "active" && s.Impersonator == nil {
			expiry, err := time.Parse(time.RFC3339, s.ExpiresAt)
			if err == nil && expiry.After(time.Now()) {
				return nil
			}
			return tenancy.ErrExpired
		}
	}
	if err := iter.Err(); err != nil {
		return err
	}
	return tenancy.ErrExpired
}
func (c *workOSClient) RevokeSession(ctx context.Context, id string) error {
	return c.client.UserManagement().RevokeSession(ctx, &workos.UserManagementRevokeSessionParams{SessionID: id})
}
func (c *workOSClient) EnsureOrganization(ctx context.Context, external, name string) (string, error) {
	org, err := c.client.Organizations().GetByExternalID(ctx, external)
	if err == nil {
		return org.ID, nil
	}
	var apiErr *workos.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		return "", err
	}
	org, err = c.client.Organizations().Create(ctx, &workos.OrganizationsCreateParams{Name: name, ExternalID: &external})
	if err != nil {
		return "", err
	}
	return org.ID, nil
}
func (c *workOSClient) SyncMembers(ctx context.Context, organization string, wanted map[string]bool) error {
	seen := map[string]bool{}
	iter := c.client.OrganizationMembership().List(ctx, &workos.OrganizationMembershipListParams{OrganizationID: &organization})
	for iter.Next() {
		m := iter.Current()
		seen[m.UserID] = true
		if wanted[m.UserID] && m.Status != "active" {
			if _, err := c.client.OrganizationMembership().Reactivate(ctx, m.ID); err != nil {
				return err
			}
		}
		if !wanted[m.UserID] && m.Status != "inactive" {
			if _, err := c.client.OrganizationMembership().Deactivate(ctx, m.ID); err != nil {
				return err
			}
		}
	}
	if err := iter.Err(); err != nil {
		return err
	}
	for user, active := range wanted {
		if active && !seen[user] {
			if _, err := c.client.OrganizationMembership().Create(ctx, &workos.OrganizationMembershipCreateParams{OrganizationID: organization, UserID: user}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *workOSClient) DeleteOrganization(ctx context.Context, id, external string) error {
	if id == "" {
		org, err := c.client.Organizations().GetByExternalID(ctx, external)
		if err != nil {
			return organizationDeletionError(err)
		}
		id = org.ID
	}
	return organizationDeletionError(c.client.Organizations().Delete(ctx, id))
}

func organizationDeletionError(err error) error {
	var apiErr *workos.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}
