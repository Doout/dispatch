package laneway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const DriverNetwork = "laneway_network"

var NetworkManagementScopes = []string{
	"network.read",
	"node.read",
	"enrollment.issue",
	"route.read",
	"route.manage",
}

var ErrNodeInstallerUnavailable = errors.New("Laneway node installation is unavailable")

type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Laneway returned %s: %s", http.StatusText(e.StatusCode), e.Message)
}

type Client struct {
	Authority  string
	Token      string
	HTTPClient *http.Client
}

type ApplicationManifest struct {
	Name                    string   `json:"name"`
	HomepageURI             string   `json:"homepage_uri"`
	SetupURI                string   `json:"setup_uri"`
	RedirectURIs            []string `json:"redirect_uris"`
	Scopes                  []string `json:"scopes"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type ApplicationRegistrationRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
}

type ApplicationRegistration struct {
	ApplicationID string `json:"application_id"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	Name          string `json:"name"`
}

type OAuthAuthorizationRequest struct {
	ClientID      string
	RedirectURI   string
	State         string
	CodeChallenge string
	Scopes        []string
}

type OAuthTokenRequest struct {
	Code         string
	CodeVerifier string
	RedirectURI  string
}

type OAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Installation struct {
		ID            string  `json:"installation_id"`
		ApplicationID string  `json:"application_id"`
		Network       Network `json:"network"`
	} `json:"installation"`
}

type Network struct {
	ID                 string `json:"network_id"`
	Name               string `json:"name"`
	IPv4Pool           string `json:"ipv4_pool"`
	IPv6Pool           string `json:"ipv6_pool,omitempty"`
	ConfigurationEpoch uint64 `json:"configuration_epoch"`
	CreatedAt          int64  `json:"created_at_unix_seconds"`
}

type Node struct {
	ID                  string `json:"node_id"`
	NetworkID           string `json:"network_id"`
	Name                string `json:"name"`
	EnabledCapabilities uint64 `json:"enabled_capabilities"`
	IPv4Address         string `json:"ipv4_address,omitempty"`
	IPv6Address         string `json:"ipv6_address,omitempty"`
	EnrollmentClass     string `json:"enrollment_class"`
	RevokedAt           int64  `json:"revoked_at_unix_seconds,omitempty"`
}

type EndpointStatus struct {
	NodeID         string `json:"node_id"`
	NetworkID      string `json:"network_id"`
	NodeName       string `json:"node_name"`
	Freshness      string `json:"freshness"`
	LastReportedAt int64  `json:"last_reported_at_unix_seconds,omitempty"`
	Report         *struct {
		ProductVersion string `json:"product_version"`
		Platform       string `json:"platform"`
		CarrierState   string `json:"carrier_state"`
		RouteState     string `json:"route_state"`
	} `json:"report,omitempty"`
}

type Route struct {
	ID        string `json:"route_id"`
	NetworkID string `json:"network_id"`
	NodeID    string `json:"node_id"`
	Prefix    string `json:"prefix"`
	Kind      string `json:"kind"`
	Mode      string `json:"mode"`
	Metric    int    `json:"metric"`
	State     string `json:"state"`
}

type Inventory struct {
	Network          Network          `json:"network"`
	Nodes            []Node           `json:"nodes"`
	EndpointStatuses []EndpointStatus `json:"endpointStatuses"`
	Routes           []Route          `json:"routes"`
}

type NodeInstallerRequest struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	InstallMode string `json:"install_mode"`
}

type NodeInstaller struct {
	ID                   string `json:"installation_id"`
	Command              string `json:"command"`
	ExpiresAtUnixSeconds int64  `json:"expires_at_unix_seconds"`
}

type AssignRouteRequest struct {
	NetworkID string `json:"network_id"`
	NodeID    string `json:"node_id"`
	Prefix    string `json:"prefix"`
	Mode      string `json:"mode"`
	Metric    int    `json:"metric"`
}

func ValidateAuthority(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("enter an HTTPS Laneway URL")
	}
	parsed.Path, parsed.RawPath = strings.TrimRight(parsed.Path, "/"), ""
	return parsed.String(), nil
}

func NewApplicationAction(authority string) (string, error) {
	base, err := ValidateAuthority(authority)
	if err != nil {
		return "", err
	}
	parsed, _ := url.Parse(base)
	parsed.Path = path.Join(parsed.Path, "/applications/new")
	return parsed.String(), nil
}

func AuthorizationURL(authority string, request OAuthAuthorizationRequest) (string, error) {
	base, err := ValidateAuthority(authority)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(request.ClientID) == "" || strings.TrimSpace(request.RedirectURI) == "" || strings.TrimSpace(request.State) == "" || strings.TrimSpace(request.CodeChallenge) == "" {
		return "", errors.New("complete the Laneway authorization request")
	}
	parsed, _ := url.Parse(base)
	parsed.Path = path.Join(parsed.Path, "/oauth/authorize")
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", request.ClientID)
	query.Set("redirect_uri", request.RedirectURI)
	query.Set("scope", strings.Join(request.Scopes, " "))
	query.Set("state", request.State)
	query.Set("code_challenge", request.CodeChallenge)
	query.Set("code_challenge_method", "S256")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (c Client) ExchangeApplicationRegistration(ctx context.Context, request ApplicationRegistrationRequest) (ApplicationRegistration, error) {
	var response ApplicationRegistration
	if strings.TrimSpace(request.Code) == "" || strings.TrimSpace(request.CodeVerifier) == "" {
		return response, errors.New("Laneway application code is missing")
	}
	if err := c.requestJSON(ctx, http.MethodPost, "/v1/application-registrations/exchange", request, &response, false); err != nil {
		return response, err
	}
	if strings.TrimSpace(response.ApplicationID) == "" || strings.TrimSpace(response.ClientID) == "" || strings.TrimSpace(response.ClientSecret) == "" {
		return response, errors.New("Laneway returned an incomplete application registration")
	}
	return response, nil
}

func (c Client) ExchangeOAuthCode(ctx context.Context, clientID, clientSecret string, request OAuthTokenRequest) (OAuthTokenResponse, error) {
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {request.Code},
		"code_verifier": {request.CodeVerifier},
		"redirect_uri":  {request.RedirectURI},
	}
	return c.oauthToken(ctx, clientID, clientSecret, values)
}

func (c Client) RefreshOAuthToken(ctx context.Context, clientID, clientSecret, refreshToken string) (OAuthTokenResponse, error) {
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	return c.oauthToken(ctx, clientID, clientSecret, values)
}

func (c Client) RevokeOAuthToken(ctx context.Context, clientID, clientSecret, refreshToken string) error {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" || strings.TrimSpace(refreshToken) == "" {
		return errors.New("Laneway revocation credentials are missing")
	}
	authority, err := ValidateAuthority(c.Authority)
	if err != nil {
		return err
	}
	values := url.Values{"token": {refreshToken}, "token_type_hint": {"refresh_token"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(authority, "/")+"/oauth/revoke", strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(clientID, clientSecret)
	return c.do(request, nil)
}

func (c Client) oauthToken(ctx context.Context, clientID, clientSecret string, values url.Values) (OAuthTokenResponse, error) {
	var response OAuthTokenResponse
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return response, errors.New("Laneway application credentials are missing")
	}
	authority, err := ValidateAuthority(c.Authority)
	if err != nil {
		return response, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(authority, "/")+"/oauth/token", strings.NewReader(values.Encode()))
	if err != nil {
		return response, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(clientID, clientSecret)
	if err := c.do(request, &response); err != nil {
		return response, err
	}
	if strings.TrimSpace(response.AccessToken) == "" || !strings.EqualFold(strings.TrimSpace(response.TokenType), "Bearer") {
		return response, errors.New("Laneway returned an incomplete access token")
	}
	return response, nil
}

func (c Client) Inventory(ctx context.Context, networkID string) (Inventory, error) {
	var inventory Inventory
	network, err := c.Network(ctx, networkID)
	if err != nil {
		return inventory, err
	}
	inventory.Network = network
	base := "/v1/admin/networks/" + url.PathEscape(networkID)
	var nodes struct {
		Nodes []Node `json:"nodes"`
	}
	if err := c.requestJSON(ctx, http.MethodGet, base+"/nodes?limit=500", nil, &nodes, true); err != nil {
		return inventory, err
	}
	var statuses struct {
		EndpointStatuses []EndpointStatus `json:"endpoint_statuses"`
	}
	if err := c.requestJSON(ctx, http.MethodGet, base+"/endpoint-statuses?limit=500", nil, &statuses, true); err != nil {
		return inventory, err
	}
	var routes struct {
		Routes []Route `json:"routes"`
	}
	if err := c.requestJSON(ctx, http.MethodGet, base+"/routes?limit=500", nil, &routes, true); err != nil {
		return inventory, err
	}
	inventory.Nodes, inventory.EndpointStatuses, inventory.Routes = nodes.Nodes, statuses.EndpointStatuses, routes.Routes
	return inventory, nil
}

func (c Client) Network(ctx context.Context, networkID string) (Network, error) {
	var network Network
	if strings.TrimSpace(networkID) == "" {
		return network, errors.New("Laneway network ID is missing")
	}
	base := "/v1/admin/networks/" + url.PathEscape(networkID)
	if err := c.requestJSON(ctx, http.MethodGet, base, nil, &network, true); err != nil {
		return network, err
	}
	return network, nil
}

func (c Client) CreateNodeInstaller(ctx context.Context, networkID string, request NodeInstallerRequest) (NodeInstaller, error) {
	var installer NodeInstaller
	if strings.TrimSpace(networkID) == "" {
		return installer, errors.New("Laneway network ID is missing")
	}
	endpoint := "/v1/admin/networks/" + url.PathEscape(networkID) + "/node-installers"
	if err := c.requestJSON(ctx, http.MethodPost, endpoint, request, &installer, true); err != nil {
		var responseError *HTTPError
		if errors.As(err, &responseError) && responseError.StatusCode == http.StatusNotFound {
			return installer, ErrNodeInstallerUnavailable
		}
		return installer, err
	}
	if strings.TrimSpace(installer.Command) == "" {
		return installer, errors.New("Laneway returned an incomplete node installer")
	}
	return installer, nil
}

func (c Client) AssignRoute(ctx context.Context, networkID string, request AssignRouteRequest) (Route, error) {
	var route Route
	if strings.TrimSpace(networkID) == "" {
		return route, errors.New("Laneway network ID is missing")
	}
	request.NetworkID = networkID
	if err := c.requestJSON(ctx, http.MethodPost, "/v1/admin/routes/assign", request, &route, true); err != nil {
		return route, err
	}
	return route, nil
}

func (c Client) requestJSON(ctx context.Context, method, requestPath string, body any, target any, authenticated bool) error {
	authority, err := ValidateAuthority(c.Authority)
	if err != nil {
		return err
	}
	requestURL := strings.TrimRight(authority, "/") + requestPath
	var reader io.Reader
	if body != nil {
		pipeReader, pipeWriter := io.Pipe()
		reader = pipeReader
		go func() {
			err := json.NewEncoder(pipeWriter).Encode(body)
			_ = pipeWriter.CloseWithError(err)
		}()
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		if strings.TrimSpace(c.Token) == "" {
			return errors.New("Laneway access token is missing")
		}
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return c.do(request, target)
}

func (c Client) do(request *http.Request, target any) error {
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	requestClient := *client
	requestClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := requestClient.Do(request)
	if err != nil {
		return fmt.Errorf("Laneway request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
			Message          string `json:"message"`
			Detail           string `json:"detail"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&failure)
		message := strings.TrimSpace(failure.ErrorDescription)
		if message == "" {
			message = strings.TrimSpace(failure.Message)
		}
		if message == "" {
			message = strings.TrimSpace(failure.Detail)
		}
		if message == "" {
			message = strings.TrimSpace(failure.Error)
		}
		if message == "" {
			message = response.Status
		}
		return &HTTPError{StatusCode: response.StatusCode, Message: message}
	}
	if target == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode Laneway response: %w", err)
	}
	return nil
}
