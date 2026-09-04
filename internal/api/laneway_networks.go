package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/laneway"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

const lanewayAuthorizationLifetime = 10 * time.Minute

type lanewayAuthorizationState struct {
	Kind          string
	Name          string
	Authority     string
	CodeVerifier  string
	RedirectURI   string
	ApplicationID string
	ExpiresAt     time.Time
}

type lanewayCredentialBundle struct {
	AccessToken        string `json:"accessToken"`
	RefreshToken       string `json:"refreshToken,omitempty"`
	LegacyClientSecret string `json:"clientSecret,omitempty"`
}

type lanewayAuthorizationStart struct {
	Method string            `json:"method"`
	Action string            `json:"action"`
	Fields map[string]string `json:"fields,omitempty"`
}

type lanewayAuthorizationRequest struct {
	Name      string `json:"name"`
	Authority string `json:"authority"`
}

type lanewayNodeInstallerRequest struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	InstallMode string `json:"installMode"`
}

type lanewayRouteRequest struct {
	NodeID string `json:"nodeId"`
	Prefix string `json:"prefix"`
	Mode   string `json:"mode"`
	Metric int    `json:"metric"`
}

func (a *API) startLanewayAuthorization(w http.ResponseWriter, r *http.Request) {
	if a.eventConfig.Vault == nil {
		problem(w, http.StatusServiceUnavailable, "Secret storage unavailable", "Configure the master encryption key before connecting Laneway.")
		return
	}
	var input lanewayAuthorizationRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 80 {
		problem(w, http.StatusBadRequest, "Invalid connection", "Enter a name no longer than 80 characters.")
		return
	}
	authority, err := laneway.ValidateAuthority(input.Authority)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid connection", err.Error()+".")
		return
	}
	application, found, err := a.reusableLanewayApplication(r.Context(), authority)
	if err != nil {
		a.internal(w, err)
		return
	}
	if found {
		start, err := a.beginLanewayNetworkInstallation(r.Context(), input.Name, application)
		if err != nil {
			a.internal(w, err)
			return
		}
		writeJSON(w, http.StatusOK, start)
		return
	}
	state, err := randomLanewayURLToken(32)
	if err != nil {
		a.internal(w, err)
		return
	}
	verifier, err := randomLanewayURLToken(48)
	if err != nil {
		a.internal(w, err)
		return
	}
	challenge := sha256.Sum256([]byte(verifier))
	origin, err := a.lanewayPublicOrigin()
	if err != nil {
		problem(w, http.StatusServiceUnavailable, "Public URL unavailable", err.Error()+".")
		return
	}
	setupURI := origin + "/api/v1/laneway-applications/setup"
	redirectURI := origin + "/api/v1/laneway-networks/callback"
	action, err := laneway.NewApplicationAction(authority)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid connection", err.Error()+".")
		return
	}
	manifest, err := json.Marshal(laneway.ApplicationManifest{
		Name:                    "Dispatch",
		HomepageURI:             origin,
		SetupURI:                setupURI,
		RedirectURIs:            []string{redirectURI},
		Scopes:                  laneway.NetworkManagementScopes,
		TokenEndpointAuthMethod: "client_secret_basic",
	})
	if err != nil {
		a.internal(w, err)
		return
	}
	now := time.Now().UTC()
	if err := a.saveLanewayState(r.Context(), state, lanewayAuthorizationState{Kind: "registration", Name: input.Name, Authority: authority, CodeVerifier: verifier, RedirectURI: redirectURI, ExpiresAt: now.Add(lanewayAuthorizationLifetime)}); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lanewayAuthorizationStart{Method: "post", Action: action, Fields: map[string]string{
		"manifest": string(manifest), "state": state, "code_challenge": base64.RawURLEncoding.EncodeToString(challenge[:]), "code_challenge_method": "S256",
	}})
}

func (a *API) completeLanewayApplicationRegistration(w http.ResponseWriter, r *http.Request) {
	pending, err := a.consumeLanewayState(r.Context(), strings.TrimSpace(r.URL.Query().Get("state")))
	if err != nil || pending.Kind != "registration" {
		a.redirectLanewayStatus(w, r, "error", "Laneway application registration expired. Start the connection again.")
		return
	}
	if lanewayCallbackFailed(r) {
		a.redirectLanewayStatus(w, r, "error", "Laneway application registration was not approved.")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		a.redirectLanewayStatus(w, r, "error", "Laneway did not return an application registration code.")
		return
	}
	registration, err := (laneway.Client{Authority: pending.Authority}).ExchangeApplicationRegistration(r.Context(), laneway.ApplicationRegistrationRequest{
		Code: code, CodeVerifier: pending.CodeVerifier,
	})
	if err != nil {
		a.logger.Warn("Laneway application registration failed", "authority", pending.Authority, "error", err)
		a.redirectLanewayStatus(w, r, "error", lanewayApplicationRegistrationFailure(err))
		return
	}
	now := time.Now().UTC()
	application := core.LanewayApplication{
		ID: ulid.Make().String(), Name: registration.Name, Authority: pending.Authority,
		RemoteApplicationID: registration.ApplicationID, ClientID: registration.ClientID,
		State: "active", CreatedAt: now, UpdatedAt: now,
	}
	if strings.TrimSpace(application.Name) == "" {
		application.Name = "Dispatch"
	}
	application.EncryptedClientSecret, err = a.eventConfig.Vault.Encrypt("laneway-application:"+application.ID, []byte(registration.ClientSecret))
	if err == nil {
		err = a.store.CreateLanewayApplication(r.Context(), application)
	}
	if err != nil {
		a.redirectLanewayStatus(w, r, "error", "Could not save the Laneway application.")
		return
	}
	start, err := a.beginLanewayNetworkInstallation(r.Context(), pending.Name, application)
	if err != nil {
		a.redirectLanewayStatus(w, r, "error", "Could not start Laneway network authorization.")
		return
	}
	http.Redirect(w, r, start.Action, http.StatusSeeOther)
}

func (a *API) completeLanewayAuthorization(w http.ResponseWriter, r *http.Request) {
	pending, err := a.consumeLanewayState(r.Context(), strings.TrimSpace(r.URL.Query().Get("state")))
	if err != nil || pending.Kind != "installation" || pending.ApplicationID == "" {
		a.redirectLanewayStatus(w, r, "error", "Laneway authorization expired. Start the connection again.")
		return
	}
	if lanewayCallbackFailed(r) {
		a.redirectLanewayStatus(w, r, "error", "Laneway network authorization was not approved.")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		a.redirectLanewayStatus(w, r, "error", "Laneway did not return an authorization code.")
		return
	}
	if a.eventConfig.Vault == nil {
		a.redirectLanewayStatus(w, r, "error", "Secret storage is unavailable.")
		return
	}
	application, clientSecret, err := a.lanewayApplication(r.Context(), pending.ApplicationID)
	if err != nil || application.State != "active" || application.Authority != pending.Authority {
		a.redirectLanewayStatus(w, r, "error", "The Laneway application is unavailable.")
		return
	}
	response, err := (laneway.Client{Authority: pending.Authority}).ExchangeOAuthCode(r.Context(), application.ClientID, clientSecret, laneway.OAuthTokenRequest{Code: code, CodeVerifier: pending.CodeVerifier, RedirectURI: pending.RedirectURI})
	if err != nil {
		a.logger.Warn("Laneway network authorization failed", "authority", pending.Authority, "application_id", application.ID, "error", err)
		a.redirectLanewayStatus(w, r, "error", lanewayNetworkAuthorizationFailure(err))
		return
	}
	if response.Installation.ID == "" || response.Installation.Network.ID == "" || response.Installation.ApplicationID != application.RemoteApplicationID {
		a.redirectLanewayStatus(w, r, "error", "Laneway returned an invalid network installation.")
		return
	}
	now := time.Now().UTC()
	item, err := a.store.GetPrivateNetworkByLaneway(r.Context(), application.ID, response.Installation.Network.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.redirectLanewayStatus(w, r, "error", "Could not save the Laneway network.")
		return
	}
	create := errors.Is(err, store.ErrNotFound)
	if create {
		item = core.PrivateNetwork{ID: ulid.Make().String(), CreatedAt: now}
	}
	item.Name, item.Driver = pending.Name, laneway.DriverNetwork
	item.LanewayApplicationID, item.LanewayInstallationID = application.ID, response.Installation.ID
	item.LanewayNetworkID = response.Installation.Network.ID
	item.Config = map[string]string{
		"authority": pending.Authority, "networkId": response.Installation.Network.ID,
		"networkName": response.Installation.Network.Name, "ipv4Pool": response.Installation.Network.IPv4Pool,
		"ipv6Pool": response.Installation.Network.IPv6Pool, "applicationId": application.RemoteApplicationID,
		"clientId": application.ClientID, "installationId": response.Installation.ID,
		"tokenType": response.TokenType, "scopes": response.Scope,
	}
	item.Details = map[string]string{
		"nodeCount": "0", "routeCount": "0",
		"configurationEpoch": strconv.FormatUint(response.Installation.Network.ConfigurationEpoch, 10),
	}
	item.State, item.UpdatedAt = "ready", now
	if response.ExpiresIn > 0 {
		item.Config["tokenExpiresAt"] = now.Add(time.Duration(response.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	credentials, err := json.Marshal(lanewayCredentialBundle{AccessToken: response.AccessToken, RefreshToken: response.RefreshToken})
	if err == nil {
		item.EncryptedCredentials, err = a.eventConfig.Vault.Encrypt("laneway-network:"+item.ID, credentials)
	}
	if err != nil {
		a.redirectLanewayStatus(w, r, "error", "Could not store the Laneway credential.")
		return
	}
	item.CredentialsConfigured = true
	if create {
		err = a.store.CreatePrivateNetwork(r.Context(), item)
	} else {
		err = a.store.UpdatePrivateNetwork(r.Context(), item)
	}
	if err != nil {
		a.redirectLanewayStatus(w, r, "error", "Could not save the Laneway network.")
		return
	}
	a.redirectLanewayStatus(w, r, "connected", "")
}

func lanewayApplicationRegistrationFailure(err error) string {
	var responseError *laneway.HTTPError
	if errors.As(err, &responseError) && (responseError.StatusCode == http.StatusNotFound || responseError.StatusCode == http.StatusMethodNotAllowed) {
		return "This Laneway server does not support application registration. Upgrade Laneway, then start the connection again."
	}
	return "Laneway could not complete application registration. Start the connection again."
}

func lanewayNetworkAuthorizationFailure(err error) string {
	var responseError *laneway.HTTPError
	if errors.As(err, &responseError) {
		status := strconv.Itoa(responseError.StatusCode)
		if text := http.StatusText(responseError.StatusCode); text != "" {
			status += " " + text
		}
		return "Laneway could not complete network authorization (" + status + "). Start the connection again."
	}
	return "Laneway could not complete network authorization. Start the connection again."
}

func (a *API) beginLanewayNetworkInstallation(ctx context.Context, name string, application core.LanewayApplication) (lanewayAuthorizationStart, error) {
	state, err := randomLanewayURLToken(32)
	if err != nil {
		return lanewayAuthorizationStart{}, err
	}
	verifier, err := randomLanewayURLToken(48)
	if err != nil {
		return lanewayAuthorizationStart{}, err
	}
	challenge := sha256.Sum256([]byte(verifier))
	origin, err := a.lanewayPublicOrigin()
	if err != nil {
		return lanewayAuthorizationStart{}, err
	}
	redirectURI := origin + "/api/v1/laneway-networks/callback"
	authorizationURL, err := laneway.AuthorizationURL(application.Authority, laneway.OAuthAuthorizationRequest{
		ClientID: application.ClientID, RedirectURI: redirectURI, State: state,
		CodeChallenge: base64.RawURLEncoding.EncodeToString(challenge[:]), Scopes: laneway.NetworkManagementScopes,
	})
	if err != nil {
		return lanewayAuthorizationStart{}, err
	}
	if err := a.saveLanewayState(ctx, state, lanewayAuthorizationState{
		Kind: "installation", Name: name, Authority: application.Authority, CodeVerifier: verifier, RedirectURI: redirectURI,
		ApplicationID: application.ID,
		ExpiresAt:     time.Now().UTC().Add(lanewayAuthorizationLifetime),
	}); err != nil {
		return lanewayAuthorizationStart{}, err
	}
	return lanewayAuthorizationStart{Method: "redirect", Action: authorizationURL}, nil
}

func lanewayCallbackFailed(r *http.Request) bool {
	return strings.TrimSpace(r.URL.Query().Get("error")) != ""
}

func (a *API) getLanewayInventory(w http.ResponseWriter, r *http.Request) {
	item, client, ok := a.lanewayNetworkClient(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	inventory, err := client.Inventory(r.Context(), item.Config["networkId"])
	if err != nil {
		item.State, item.UpdatedAt = "error", time.Now().UTC()
		if item.Details == nil {
			item.Details = map[string]string{}
		}
		item.Details["error"] = err.Error()
		_ = a.store.UpdatePrivateNetwork(r.Context(), item)
		problem(w, http.StatusBadGateway, "Laneway is unavailable", err.Error())
		return
	}
	now := time.Now().UTC()
	item.State, item.LastVerifiedAt, item.UpdatedAt = "ready", &now, now
	item.Config["networkName"], item.Config["ipv4Pool"], item.Config["ipv6Pool"] = inventory.Network.Name, inventory.Network.IPv4Pool, inventory.Network.IPv6Pool
	item.Details = map[string]string{"nodeCount": strconv.Itoa(len(inventory.Nodes)), "routeCount": strconv.Itoa(len(inventory.Routes)), "configurationEpoch": strconv.FormatUint(inventory.Network.ConfigurationEpoch, 10)}
	_ = a.store.UpdatePrivateNetwork(r.Context(), item)
	writeJSON(w, http.StatusOK, inventory)
}

func (a *API) createLanewayNodeInstaller(w http.ResponseWriter, r *http.Request) {
	item, client, ok := a.lanewayNetworkClient(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var input lanewayNodeInstallerRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Kind = strings.TrimSpace(input.Kind)
	input.InstallMode = strings.TrimSpace(input.InstallMode)
	if input.Name == "" || len(input.Name) > 253 {
		problem(w, http.StatusBadRequest, "Invalid node", "Enter a node name no longer than 253 characters.")
		return
	}
	if input.Kind != "node" && input.Kind != "connector" && input.Kind != "exit" {
		problem(w, http.StatusBadRequest, "Invalid node", "Choose node, connector, or exit.")
		return
	}
	if input.InstallMode != "systemd" {
		problem(w, http.StatusBadRequest, "Unsupported install method", "Laneway currently supports systemd node installation.")
		return
	}
	installer, err := client.CreateNodeInstaller(r.Context(), item.Config["networkId"], laneway.NodeInstallerRequest{
		Name: input.Name, Kind: input.Kind, InstallMode: input.InstallMode,
	})
	if err != nil {
		if errors.Is(err, laneway.ErrNodeInstallerUnavailable) {
			problem(w, http.StatusNotImplemented, "Node installation unavailable", "This Laneway server does not provide node installers.")
			return
		}
		problem(w, http.StatusBadGateway, "Could not create node installer", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, installer)
}

func (a *API) createLanewayRoute(w http.ResponseWriter, r *http.Request) {
	item, client, ok := a.lanewayNetworkClient(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var input lanewayRouteRequest
	if !decode(w, r, &input) {
		return
	}
	input.NodeID = strings.TrimSpace(input.NodeID)
	input.Prefix = strings.TrimSpace(input.Prefix)
	input.Mode = strings.TrimSpace(input.Mode)
	if input.NodeID == "" {
		problem(w, http.StatusBadRequest, "Invalid route", "Choose a node.")
		return
	}
	prefix, err := netip.ParsePrefix(input.Prefix)
	if err != nil || prefix != prefix.Masked() || prefix.Bits() == 0 {
		problem(w, http.StatusBadRequest, "Invalid route", "Enter a canonical non-default IPv4 or IPv6 prefix.")
		return
	}
	if input.Mode != "nat" && input.Mode != "routed" {
		problem(w, http.StatusBadRequest, "Invalid route", "Choose NAT or routed mode.")
		return
	}
	if input.Metric < 0 || input.Metric > 1_000_000 {
		problem(w, http.StatusBadRequest, "Invalid route", "Metric must be between 0 and 1000000.")
		return
	}
	route, err := client.AssignRoute(r.Context(), item.Config["networkId"], laneway.AssignRouteRequest{
		NodeID: input.NodeID, Prefix: input.Prefix, Mode: input.Mode, Metric: input.Metric,
	})
	if err != nil {
		problem(w, http.StatusBadGateway, "Could not assign route", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, route)
}

func (a *API) verifyLanewayNetwork(w http.ResponseWriter, r *http.Request, item core.PrivateNetwork) bool {
	_, client, ok := a.lanewayNetworkClientForItem(w, r, item)
	if !ok {
		return false
	}
	inventory, err := client.Inventory(r.Context(), item.Config["networkId"])
	now := time.Now().UTC()
	item.UpdatedAt = now
	if item.Details == nil {
		item.Details = map[string]string{}
	}
	if err != nil {
		item.State, item.Details["error"] = "error", err.Error()
		_ = a.store.UpdatePrivateNetwork(r.Context(), item)
		problem(w, http.StatusBadGateway, "Laneway is unavailable", err.Error())
		return false
	}
	delete(item.Details, "error")
	item.State, item.LastVerifiedAt = "ready", &now
	item.Config["networkName"], item.Config["ipv4Pool"], item.Config["ipv6Pool"] = inventory.Network.Name, inventory.Network.IPv4Pool, inventory.Network.IPv6Pool
	item.Details["nodeCount"], item.Details["routeCount"], item.Details["configurationEpoch"] = strconv.Itoa(len(inventory.Nodes)), strconv.Itoa(len(inventory.Routes)), strconv.FormatUint(inventory.Network.ConfigurationEpoch, 10)
	if err := a.store.UpdatePrivateNetwork(r.Context(), item); err != nil {
		a.internal(w, err)
		return false
	}
	writeJSON(w, http.StatusOK, item)
	return true
}

func (a *API) lanewayNetworkClient(w http.ResponseWriter, r *http.Request, id string) (core.PrivateNetwork, laneway.Client, bool) {
	item, err := a.store.GetPrivateNetwork(r.Context(), id)
	if err != nil {
		a.notFoundOrInternal(w, err, "Laneway network")
		return item, laneway.Client{}, false
	}
	return a.lanewayNetworkClientForItem(w, r, item)
}

func (a *API) lanewayNetworkClientForItem(w http.ResponseWriter, r *http.Request, item core.PrivateNetwork) (core.PrivateNetwork, laneway.Client, bool) {
	if item.Driver != laneway.DriverNetwork {
		problem(w, http.StatusBadRequest, "Not a Laneway network", "Choose a Laneway network connection.")
		return item, laneway.Client{}, false
	}
	if a.eventConfig.Vault == nil || item.EncryptedCredentials == "" {
		problem(w, http.StatusServiceUnavailable, "Laneway credential unavailable", "Reconnect this Laneway network.")
		return item, laneway.Client{}, false
	}
	plain, err := a.eventConfig.Vault.Decrypt("laneway-network:"+item.ID, item.EncryptedCredentials)
	if err != nil {
		a.internal(w, err)
		return item, laneway.Client{}, false
	}
	credentials := decodeLanewayCredentials(plain)
	if credentials.AccessToken == "" {
		problem(w, http.StatusServiceUnavailable, "Laneway credential unavailable", "Reconnect this Laneway network.")
		return item, laneway.Client{}, false
	}
	expiresAt, _ := time.Parse(time.RFC3339, item.Config["tokenExpiresAt"])
	if !expiresAt.IsZero() && time.Now().UTC().Add(time.Minute).After(expiresAt) {
		leaseToken, err := randomLanewayURLToken(24)
		if err != nil {
			a.internal(w, err)
			return item, laneway.Client{}, false
		}
		now := time.Now().UTC()
		if err := a.store.AcquireLanewayRefreshLease(r.Context(), item.ID, leaseToken, now, now.Add(30*time.Second)); err != nil {
			if errors.Is(err, store.ErrLanewayRefreshBusy) {
				problem(w, http.StatusConflict, "Laneway access is refreshing", "Retry this request.")
			} else {
				a.internal(w, err)
			}
			return item, laneway.Client{}, false
		}
		defer func() { _ = a.store.ReleaseLanewayRefreshLease(context.Background(), item.ID, leaseToken) }()

		item, err = a.store.GetPrivateNetwork(r.Context(), item.ID)
		if err != nil {
			a.notFoundOrInternal(w, err, "Laneway network")
			return item, laneway.Client{}, false
		}
		plain, err = a.eventConfig.Vault.Decrypt("laneway-network:"+item.ID, item.EncryptedCredentials)
		if err != nil {
			a.internal(w, errors.New("decrypt Laneway network credential"))
			return item, laneway.Client{}, false
		}
		credentials = decodeLanewayCredentials(plain)
		expiresAt, _ = time.Parse(time.RFC3339, item.Config["tokenExpiresAt"])
		if !expiresAt.IsZero() && time.Now().UTC().Add(time.Minute).Before(expiresAt) {
			return item, laneway.Client{Authority: item.Config["authority"], Token: credentials.AccessToken}, true
		}
		if credentials.RefreshToken == "" || item.Config["clientId"] == "" {
			problem(w, http.StatusUnauthorized, "Laneway access expired", "Reconnect this Laneway network.")
			return item, laneway.Client{}, false
		}
		clientID, clientSecret := item.Config["clientId"], credentials.LegacyClientSecret
		if item.LanewayApplicationID != "" {
			application, secret, applicationErr := a.lanewayApplication(r.Context(), item.LanewayApplicationID)
			if applicationErr != nil || application.State != "active" || application.Authority != item.Config["authority"] {
				problem(w, http.StatusUnauthorized, "Laneway application unavailable", "Reconnect this Laneway network.")
				return item, laneway.Client{}, false
			}
			clientID, clientSecret = application.ClientID, secret
		}
		if clientSecret == "" {
			problem(w, http.StatusUnauthorized, "Laneway application unavailable", "Reconnect this Laneway network.")
			return item, laneway.Client{}, false
		}
		refreshed, err := (laneway.Client{Authority: item.Config["authority"]}).RefreshOAuthToken(r.Context(), clientID, clientSecret, credentials.RefreshToken)
		if err != nil {
			problem(w, http.StatusBadGateway, "Could not refresh Laneway access", err.Error())
			return item, laneway.Client{}, false
		}
		credentials.AccessToken = refreshed.AccessToken
		if refreshed.RefreshToken != "" {
			credentials.RefreshToken = refreshed.RefreshToken
		}
		credentials.LegacyClientSecret = ""
		encoded, err := json.Marshal(credentials)
		if err == nil {
			item.EncryptedCredentials, err = a.eventConfig.Vault.Encrypt("laneway-network:"+item.ID, encoded)
		}
		if err != nil {
			a.internal(w, err)
			return item, laneway.Client{}, false
		}
		if refreshed.ExpiresIn > 0 {
			item.Config["tokenExpiresAt"] = time.Now().UTC().Add(time.Duration(refreshed.ExpiresIn) * time.Second).Format(time.RFC3339)
		}
		item.UpdatedAt = time.Now().UTC()
		if err := a.store.UpdatePrivateNetwork(r.Context(), item); err != nil {
			a.internal(w, err)
			return item, laneway.Client{}, false
		}
	}
	return item, laneway.Client{Authority: item.Config["authority"], Token: credentials.AccessToken}, true
}

func (a *API) redirectLanewayStatus(w http.ResponseWriter, r *http.Request, status, detail string) {
	origin, _ := a.lanewayPublicOrigin()
	target := origin + "/?view=connections&lanewayStatus=" + url.QueryEscape(status)
	if detail != "" {
		target += "&detail=" + url.QueryEscape(detail)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *API) lanewayPublicOrigin() (string, error) {
	origin := strings.TrimRight(strings.TrimSpace(a.auth.PublicURL), "/")
	parsed, err := url.Parse(origin)
	if origin == "" || err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("set DISPATCH_PUBLIC_URL to the HTTPS origin for this controller")
	}
	return origin, nil
}

func (a *API) revokeLanewayNetwork(ctx context.Context, item core.PrivateNetwork) error {
	if a.eventConfig.Vault == nil || item.EncryptedCredentials == "" {
		return errors.New("Laneway credential is unavailable")
	}
	plain, err := a.eventConfig.Vault.Decrypt("laneway-network:"+item.ID, item.EncryptedCredentials)
	if err != nil {
		return err
	}
	var credentials lanewayCredentialBundle
	if err := json.Unmarshal(plain, &credentials); err != nil || credentials.RefreshToken == "" {
		return errors.New("Laneway refresh token is unavailable")
	}
	clientID, clientSecret := item.Config["clientId"], credentials.LegacyClientSecret
	if item.LanewayApplicationID != "" {
		application, secret, err := a.lanewayApplication(ctx, item.LanewayApplicationID)
		if err != nil {
			return err
		}
		clientID, clientSecret = application.ClientID, secret
	}
	return (laneway.Client{Authority: item.Config["authority"]}).RevokeOAuthToken(ctx, clientID, clientSecret, credentials.RefreshToken)
}

func decodeLanewayCredentials(plain []byte) lanewayCredentialBundle {
	credentials := lanewayCredentialBundle{AccessToken: string(plain)}
	var encoded lanewayCredentialBundle
	if json.Unmarshal(plain, &encoded) == nil {
		return encoded
	}
	return credentials
}

func randomLanewayURLToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
