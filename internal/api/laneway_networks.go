package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/laneway"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

const lanewayAuthorizationLifetime = 10 * time.Minute

type lanewayAuthorizationState struct {
	Name         string
	Authority    string
	CodeVerifier string
	RedirectURI  string
	ExpiresAt    time.Time
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
	redirectURI := a.lanewayPublicOrigin(r) + "/api/v1/laneway-networks/callback"
	authorizationURL, err := laneway.AuthorizeURL(authority, laneway.AuthorizationRequest{
		ApplicationName: "Dispatch",
		ApplicationURL:  a.lanewayPublicOrigin(r),
		RedirectURI:     redirectURI,
		State:           state,
		CodeChallenge:   base64.RawURLEncoding.EncodeToString(challenge[:]),
		Permissions:     laneway.DispatchPermissions,
	})
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid connection", err.Error()+".")
		return
	}
	now := time.Now().UTC()
	a.lanewayMu.Lock()
	for pendingState, pending := range a.lanewayStates {
		if now.After(pending.ExpiresAt) {
			delete(a.lanewayStates, pendingState)
		}
	}
	a.lanewayStates[state] = lanewayAuthorizationState{Name: input.Name, Authority: authority, CodeVerifier: verifier, RedirectURI: redirectURI, ExpiresAt: now.Add(lanewayAuthorizationLifetime)}
	a.lanewayMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"authorizationUrl": authorizationURL})
}

func (a *API) completeLanewayAuthorization(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	a.lanewayMu.Lock()
	pending, ok := a.lanewayStates[state]
	if ok {
		delete(a.lanewayStates, state)
	}
	a.lanewayMu.Unlock()
	if !ok || state == "" || time.Now().UTC().After(pending.ExpiresAt) {
		a.redirectLanewayStatus(w, r, "error", "Laneway authorization expired. Start the connection again.")
		return
	}
	if detail := strings.TrimSpace(r.URL.Query().Get("error_description")); detail != "" {
		a.redirectLanewayStatus(w, r, "error", detail)
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
	response, err := (laneway.Client{Authority: pending.Authority}).Exchange(r.Context(), laneway.TokenRequest{Code: code, CodeVerifier: pending.CodeVerifier, RedirectURI: pending.RedirectURI})
	if err != nil {
		a.redirectLanewayStatus(w, r, "error", err.Error())
		return
	}
	now := time.Now().UTC()
	item := core.PrivateNetwork{
		ID:        ulid.Make().String(),
		Name:      pending.Name,
		Driver:    laneway.DriverNetwork,
		Config:    map[string]string{"authority": pending.Authority, "networkId": response.Network.ID, "networkName": response.Network.Name, "ipv4Pool": response.Network.IPv4Pool, "ipv6Pool": response.Network.IPv6Pool, "principalId": response.PrincipalID, "tokenType": response.TokenType, "permissions": strings.Join(response.Permissions, ",")},
		Details:   map[string]string{"nodeCount": "0", "routeCount": "0", "configurationEpoch": strconv.FormatUint(response.Network.ConfigurationEpoch, 10)},
		State:     "ready",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if response.ExpiresAtUnixSeconds > 0 {
		item.Config["tokenExpiresAt"] = time.Unix(response.ExpiresAtUnixSeconds, 0).UTC().Format(time.RFC3339)
	}
	item.EncryptedCredentials, err = a.eventConfig.Vault.Encrypt("laneway-network:"+item.ID, []byte(response.AccessToken))
	if err != nil {
		a.redirectLanewayStatus(w, r, "error", "Could not store the Laneway credential.")
		return
	}
	item.CredentialsConfigured = true
	if err := a.store.CreatePrivateNetwork(r.Context(), item); err != nil {
		a.redirectLanewayStatus(w, r, "error", "Could not save the Laneway network.")
		return
	}
	a.redirectLanewayStatus(w, r, "connected", "")
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
	if input.InstallMode != "docker_compose" && input.InstallMode != "systemd" {
		problem(w, http.StatusBadRequest, "Invalid install method", "Choose Docker Compose or systemd.")
		return
	}
	installer, err := client.CreateNodeInstaller(r.Context(), item.Config["networkId"], laneway.NodeInstallerRequest{
		Name: input.Name, Kind: input.Kind, InstallMode: input.InstallMode,
	})
	if err != nil {
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
	token, err := a.eventConfig.Vault.Decrypt("laneway-network:"+item.ID, item.EncryptedCredentials)
	if err != nil {
		a.internal(w, err)
		return item, laneway.Client{}, false
	}
	return item, laneway.Client{Authority: item.Config["authority"], Token: string(token)}, true
}

func (a *API) redirectLanewayStatus(w http.ResponseWriter, r *http.Request, status, detail string) {
	target := a.lanewayPublicOrigin(r) + "/?view=connections&lanewayStatus=" + url.QueryEscape(status)
	if detail != "" {
		target += "&detail=" + url.QueryEscape(detail)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *API) lanewayPublicOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host
}

func randomLanewayURLToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
