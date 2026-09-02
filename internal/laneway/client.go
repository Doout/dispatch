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

var DispatchPermissions = []string{
	"network.read",
	"node.read",
	"node.manage",
	"enrollment.issue",
	"route.read",
	"route.manage",
}

type Client struct {
	Authority  string
	Token      string
	HTTPClient *http.Client
}

type AuthorizationRequest struct {
	ApplicationName string
	ApplicationURL  string
	RedirectURI     string
	State           string
	CodeChallenge   string
	Permissions     []string
}

type TokenRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectURI  string `json:"redirect_uri"`
}

type TokenResponse struct {
	AccessToken          string   `json:"access_token"`
	TokenType            string   `json:"token_type"`
	ExpiresAtUnixSeconds int64    `json:"expires_at_unix_seconds"`
	PrincipalID          string   `json:"principal_id"`
	Network              Network  `json:"network"`
	Permissions          []string `json:"permissions"`
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
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("enter an HTTPS Laneway URL")
	}
	parsed.Path, parsed.RawPath, parsed.RawQuery, parsed.Fragment = strings.TrimRight(parsed.Path, "/"), "", "", ""
	return parsed.String(), nil
}

func AuthorizeURL(authority string, request AuthorizationRequest) (string, error) {
	base, err := ValidateAuthority(authority)
	if err != nil {
		return "", err
	}
	parsed, _ := url.Parse(base)
	parsed.Path = path.Join(parsed.Path, "/integrations/dispatch/authorize")
	query := parsed.Query()
	query.Set("application_name", request.ApplicationName)
	query.Set("application_url", request.ApplicationURL)
	query.Set("redirect_uri", request.RedirectURI)
	query.Set("state", request.State)
	query.Set("code_challenge", request.CodeChallenge)
	query.Set("code_challenge_method", "S256")
	for _, permission := range request.Permissions {
		query.Add("permission", permission)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (c Client) Exchange(ctx context.Context, request TokenRequest) (TokenResponse, error) {
	var response TokenResponse
	if err := c.request(ctx, http.MethodPost, "/v1/integrations/dispatch/token", request, &response, false); err != nil {
		return response, err
	}
	if strings.TrimSpace(response.AccessToken) == "" || strings.TrimSpace(response.Network.ID) == "" {
		return response, errors.New("Laneway returned an incomplete authorization")
	}
	return response, nil
}

func (c Client) Inventory(ctx context.Context, networkID string) (Inventory, error) {
	var inventory Inventory
	if strings.TrimSpace(networkID) == "" {
		return inventory, errors.New("Laneway network ID is missing")
	}
	base := "/v1/admin/networks/" + url.PathEscape(networkID)
	if err := c.request(ctx, http.MethodGet, base, nil, &inventory.Network, true); err != nil {
		return inventory, err
	}
	var nodes struct {
		Nodes []Node `json:"nodes"`
	}
	if err := c.request(ctx, http.MethodGet, base+"/nodes?limit=500", nil, &nodes, true); err != nil {
		return inventory, err
	}
	var statuses struct {
		EndpointStatuses []EndpointStatus `json:"endpoint_statuses"`
	}
	if err := c.request(ctx, http.MethodGet, base+"/endpoint-statuses?limit=500", nil, &statuses, true); err != nil {
		return inventory, err
	}
	var routes struct {
		Routes []Route `json:"routes"`
	}
	if err := c.request(ctx, http.MethodGet, base+"/routes?limit=500", nil, &routes, true); err != nil {
		return inventory, err
	}
	inventory.Nodes, inventory.EndpointStatuses, inventory.Routes = nodes.Nodes, statuses.EndpointStatuses, routes.Routes
	return inventory, nil
}

func (c Client) CreateNodeInstaller(ctx context.Context, networkID string, request NodeInstallerRequest) (NodeInstaller, error) {
	var installer NodeInstaller
	if strings.TrimSpace(networkID) == "" {
		return installer, errors.New("Laneway network ID is missing")
	}
	endpoint := "/v1/admin/networks/" + url.PathEscape(networkID) + "/node-installers"
	if err := c.request(ctx, http.MethodPost, endpoint, request, &installer, true); err != nil {
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
	if err := c.request(ctx, http.MethodPost, "/v1/admin/routes/assign", request, &route, true); err != nil {
		return route, err
	}
	return route, nil
}

func (c Client) request(ctx context.Context, method, requestPath string, body any, target any, authenticated bool) error {
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
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("Laneway request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Message string `json:"message"`
			Detail  string `json:"detail"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&failure)
		message := strings.TrimSpace(failure.Message)
		if message == "" {
			message = strings.TrimSpace(failure.Detail)
		}
		if message == "" {
			message = response.Status
		}
		return fmt.Errorf("Laneway returned %s: %s", response.Status, message)
	}
	if target == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode Laneway response: %w", err)
	}
	return nil
}
