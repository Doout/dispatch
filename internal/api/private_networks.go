package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/laneway"
	"github.com/doout/dispatch/internal/privateaccess"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type privateNetworkRequest struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	SocketPath string `json:"socketPath"`
	Authority  string `json:"authority"`
	Route      string `json:"route"`
}

type lanewayConnectorInstallRequest struct {
	BootstrapCommand string `json:"bootstrapCommand"`
}

func (a *API) listPrivateNetworks(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListPrivateNetworks(r.Context())
	a.list(w, items, err)
}

func (a *API) createPrivateNetwork(w http.ResponseWriter, r *http.Request) {
	var input privateNetworkRequest
	if !decode(w, r, &input) {
		return
	}
	item, detail := privateNetworkInput(input, nil)
	if detail != "" {
		problem(w, http.StatusBadRequest, "Invalid private network", detail)
		return
	}
	now := time.Now().UTC()
	item.ID, item.State, item.CreatedAt, item.UpdatedAt = ulid.Make().String(), "unverified", now, now
	if item.Driver == edge.DriverAgent {
		token, tokenHash, err := newEdgeToken()
		if err != nil {
			a.internal(w, err)
			return
		}
		item.TokenHash, item.EnrollmentToken, item.State = tokenHash, token, "waiting"
	} else if item.Driver == privateaccess.DriverLanewayConnector {
		item.State = "waiting"
	}
	if err := a.store.CreatePrivateNetwork(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	if item.Driver == laneway.DriverNetwork {
		a.verifyLanewayNetwork(w, r, item)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) updatePrivateNetwork(w http.ResponseWriter, r *http.Request) {
	existing, err := a.store.GetPrivateNetwork(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Private network")
		return
	}
	var input privateNetworkRequest
	if !decode(w, r, &input) {
		return
	}
	item, detail := privateNetworkInput(input, &existing)
	if detail != "" {
		problem(w, http.StatusBadRequest, "Invalid private network", detail)
		return
	}
	item.ID, item.CreatedAt, item.UpdatedAt = existing.ID, existing.CreatedAt, time.Now().UTC()
	item.TokenHash = existing.TokenHash
	sameConnection := item.Driver == edge.DriverAgent
	if item.Driver == privateaccess.DriverLaneway {
		sameConnection = item.Config["socketPath"] == existing.Config["socketPath"]
	}
	if item.Driver == privateaccess.DriverLanewayConnector {
		sameConnection = item.Config["authority"] == existing.Config["authority"]
	}
	if sameConnection {
		item.Details, item.State, item.LastVerifiedAt = existing.Details, existing.State, existing.LastVerifiedAt
	} else {
		item.State = "unverified"
	}
	if err := a.store.UpdatePrivateNetwork(r.Context(), item); err != nil {
		a.notFoundOrInternal(w, err, "Private network")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) verifyPrivateNetwork(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetPrivateNetwork(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Private network")
		return
	}
	if item.Driver == edge.DriverAgent {
		lastSeen, err := time.Parse(time.RFC3339Nano, item.Details["lastSeenAt"])
		if err != nil || time.Since(lastSeen) > 75*time.Second {
			item.State, item.UpdatedAt = "offline", time.Now().UTC()
			_ = a.store.UpdatePrivateNetwork(r.Context(), item)
			problem(w, http.StatusBadGateway, "Edge node is offline", "Start the node and wait for its outbound connection.")
			return
		}
		verified := time.Now().UTC()
		item.State, item.LastVerifiedAt, item.UpdatedAt = "ready", &verified, verified
		if err := a.store.UpdatePrivateNetwork(r.Context(), item); err != nil {
			a.internal(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
		return
	}
	if item.Driver == privateaccess.DriverLanewayConnector {
		state, err := privateaccess.ConnectorStatus(r.Context(), item.Config["containerName"])
		now := time.Now().UTC()
		item.UpdatedAt = now
		if item.Details == nil {
			item.Details = map[string]string{}
		}
		item.Details["containerState"] = state
		if err != nil {
			item.State = "error"
			item.Details["error"] = err.Error()
			_ = a.store.UpdatePrivateNetwork(r.Context(), item)
			problem(w, http.StatusBadGateway, "Laneway Connector unavailable", err.Error())
			return
		}
		delete(item.Details, "error")
		item.State, item.LastVerifiedAt = "ready", &now
		if err := a.store.UpdatePrivateNetwork(r.Context(), item); err != nil {
			a.internal(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	snapshot, err := privateaccess.CheckLaneway(ctx, item.Config["socketPath"])
	now := time.Now().UTC()
	item.UpdatedAt = now
	if err != nil {
		item.State, item.Details = "error", map[string]string{"error": err.Error()}
		_ = a.store.UpdatePrivateNetwork(r.Context(), item)
		problem(w, http.StatusBadGateway, "Laneway is unavailable", err.Error())
		return
	}
	item.State, item.Details, item.LastVerifiedAt = "ready", privateaccess.LanewayDetails(snapshot), &now
	if err := a.store.UpdatePrivateNetwork(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) deletePrivateNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	network, err := a.store.GetPrivateNetwork(r.Context(), id)
	if err != nil {
		a.notFoundOrInternal(w, err, "Private network")
		return
	}
	stores, err := a.store.ListSecretStores(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, item := range stores {
		if item.Config["privateNetworkId"] == id {
			problem(w, http.StatusConflict, "Private network in use", "Change the secret store connection first.")
			return
		}
	}
	githubApps, err := a.store.ListGitHubApps(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, item := range githubApps {
		if item.PrivateNetworkID == id {
			problem(w, http.StatusConflict, "Edge node in use", "Change the GitHub connection route first.")
			return
		}
	}
	if network.Driver == privateaccess.DriverLanewayConnector && network.Config["containerName"] != "" {
		if err := privateaccess.RemoveConnector(r.Context(), network.Config["containerName"]); err != nil {
			problem(w, http.StatusBadGateway, "Could not remove Connector", err.Error())
			return
		}
	}
	if err := a.store.DeletePrivateNetwork(r.Context(), id); err != nil {
		a.notFoundOrInternal(w, err, "Private network")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) installLanewayConnector(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetPrivateNetwork(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Private network")
		return
	}
	if item.Driver != privateaccess.DriverLanewayConnector {
		problem(w, http.StatusBadRequest, "Not a Laneway Connector", "Choose a Laneway Connector connection.")
		return
	}
	if item.Config["containerName"] != "" {
		problem(w, http.StatusConflict, "Connector already installed", "Remove the connection before installing it again.")
		return
	}
	var input lanewayConnectorInstallRequest
	if !decode(w, r, &input) {
		return
	}
	bootstrap, err := privateaccess.ParseConnectorBootstrap(input.BootstrapCommand, item.Config["authority"])
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid Laneway bootstrap", err.Error())
		return
	}
	containerName, err := privateaccess.InstallConnector(r.Context(), bootstrap)
	now := time.Now().UTC()
	if item.Details == nil {
		item.Details = map[string]string{}
	}
	item.UpdatedAt = now
	if err != nil {
		item.State = "error"
		item.Details["error"] = err.Error()
		_ = a.store.UpdatePrivateNetwork(r.Context(), item)
		problem(w, http.StatusBadGateway, "Connector installation failed", err.Error())
		return
	}
	item.Config["containerName"] = containerName
	item.Config["managed"] = "true"
	item.State = "waiting"
	item.Details = map[string]string{"containerName": containerName, "installedAt": now.Format(time.RFC3339Nano), "containerState": "starting"}
	if state, statusErr := privateaccess.ConnectorStatus(r.Context(), containerName); statusErr == nil {
		item.State, item.LastVerifiedAt, item.Details["containerState"] = "ready", &now, state
	}
	if err := a.store.UpdatePrivateNetwork(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func privateNetworkInput(input privateNetworkRequest, existing *core.PrivateNetwork) (core.PrivateNetwork, string) {
	input.Name, input.Driver = strings.TrimSpace(input.Name), strings.TrimSpace(input.Driver)
	if input.Driver == "" {
		input.Driver = privateaccess.DriverLaneway
	}
	item := core.PrivateNetwork{Name: input.Name, Driver: input.Driver, Config: map[string]string{}, Details: map[string]string{}}
	if input.Name == "" || len(input.Name) > 80 {
		return item, "Enter a name no longer than 80 characters."
	}
	if input.Driver != privateaccess.DriverLaneway && input.Driver != privateaccess.DriverLanewayConnector && input.Driver != edge.DriverAgent {
		return item, "Choose a supported private network driver."
	}
	if input.Driver == privateaccess.DriverLaneway {
		socketPath, err := privateaccess.ValidateSocketPath(input.SocketPath)
		if err != nil {
			return item, err.Error()
		}
		item.Config["socketPath"] = socketPath
	} else if input.Driver == privateaccess.DriverLanewayConnector {
		authority, err := privateaccess.ValidateLanewayAuthority(input.Authority)
		if err != nil {
			return item, err.Error()
		}
		item.Config["authority"] = authority
		route := strings.TrimSpace(input.Route)
		if address, err := netip.ParseAddr(route); err == nil {
			route = netip.PrefixFrom(address, address.BitLen()).String()
		}
		if prefix, err := netip.ParsePrefix(route); err != nil || !prefix.IsValid() {
			return item, "Enter the private Dispatch IP or CIDR exposed through Laneway."
		}
		item.Config["route"] = route
		if existing != nil {
			if existing.Config["authority"] != authority && existing.Config["containerName"] != "" {
				return item, "Remove the installed Connector before changing its control plane."
			}
			item.Config["containerName"] = existing.Config["containerName"]
			item.Config["managed"] = existing.Config["managed"]
			if existing.Config["route"] != route && existing.Config["containerName"] != "" {
				return item, "Update the Laneway route on its control plane before changing this address."
			}
		}
	} else {
		item.Config["mode"] = "agent"
	}
	if existing != nil && existing.Driver != input.Driver {
		return item, "The private network driver cannot be changed."
	}
	return item, ""
}

func newEdgeToken() (string, string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(value)
	hash := sha256.Sum256([]byte(token))
	return token, base64.RawURLEncoding.EncodeToString(hash[:]), nil
}
