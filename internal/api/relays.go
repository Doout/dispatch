package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/relay"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

var relayProviderPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

type relayWebhookRequest struct {
	Name                 string `json:"name"`
	Provider             string `json:"provider"`
	ProviderConnectionID string `json:"providerConnectionId"`
}

func (a *API) relayClient(ctx context.Context, server core.Server) (relay.Client, error) {
	if server.Runtime != core.ServerRuntimeRelay || server.Relay == nil || server.Relay.EncryptedAccessToken == "" {
		return relay.Client{}, errors.New("server is not a configured relay")
	}
	if a.eventConfig.Vault == nil {
		return relay.Client{}, errors.New("secret vault is not configured")
	}
	token, err := a.eventConfig.Vault.Decrypt("relay-server:"+server.ID+":access-token", server.Relay.EncryptedAccessToken)
	if err != nil {
		return relay.Client{}, err
	}
	return relay.Client{BaseURL: server.Address, Token: string(token)}, nil
}

func (a *API) verifyRelayServer(w http.ResponseWriter, r *http.Request) {
	server, err := a.store.GetServer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Relay server")
		return
	}
	client, err := a.relayClient(r.Context(), server)
	if err != nil {
		problem(w, 409, "Relay unavailable", err.Error())
		return
	}
	status, err := client.Verify(r.Context())
	if err != nil {
		server.State = "degraded"
		server.Relay.LastError = err.Error()
		_ = a.store.UpdateServer(r.Context(), server)
		problem(w, 502, "Relay connection failed", err.Error())
		return
	}
	now := time.Now().UTC()
	server.State = "connected"
	server.Relay.LastConnectedAt = &now
	server.Relay.LastError = ""
	server.Relay.PendingEvents = status.Pending
	server.Relay.OldestPendingAt = status.OldestPending
	_ = a.store.UpdateServer(r.Context(), server)
	writeJSON(w, 200, server)
}

func (a *API) listRelayWebhooks(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListRelayWebhooks(r.Context(), chi.URLParam(r, "id"))
	a.list(w, items, err)
}

func (a *API) createRelayWebhook(w http.ResponseWriter, r *http.Request) {
	server, err := a.store.GetServer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Relay server")
		return
	}
	var input relayWebhookRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.ProviderConnectionID = strings.TrimSpace(input.ProviderConnectionID)
	if input.Name == "" || len(input.Name) > 80 {
		problem(w, 400, "Webhook name required", "Enter a name no longer than 80 characters.")
		return
	}
	if !relayProviderPattern.MatchString(input.Provider) {
		problem(w, 400, "Provider invalid", "Use a short lowercase provider identifier.")
		return
	}
	if input.Provider == string(core.EventProviderGitHub) {
		if input.ProviderConnectionID == "" {
			problem(w, 400, "GitHub connection required", "Choose the GitHub App that will verify and process this endpoint.")
			return
		}
		if _, err := a.store.GetGitHubApp(r.Context(), input.ProviderConnectionID); err != nil {
			a.notFoundOrInternal(w, err, "GitHub App")
			return
		}
	}
	item, err := a.provisionRelayWebhook(r.Context(), server, input.Name, core.EventProvider(input.Provider), input.ProviderConnectionID)
	if err != nil {
		problem(w, 502, "Relay endpoint could not be created", err.Error())
		return
	}
	writeJSON(w, 201, item)
}

func (a *API) provisionRelayWebhook(ctx context.Context, server core.Server, name string, provider core.EventProvider, connectionID string) (core.RelayWebhook, error) {
	client, err := a.relayClient(ctx, server)
	if err != nil {
		return core.RelayWebhook{}, err
	}
	remote, err := client.CreateHook(ctx, name)
	if err != nil {
		return core.RelayWebhook{}, err
	}
	now := time.Now().UTC()
	item := core.RelayWebhook{ID: ulid.Make().String(), ServerID: server.ID, Name: name, Provider: provider, ProviderConnectionID: connectionID, RemoteID: remote.ID, URL: remote.URL, State: "active", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateRelayWebhook(ctx, item); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.DeleteHook(cleanup, remote.ID)
		return core.RelayWebhook{}, err
	}
	return item, nil
}

func (a *API) deleteRelayWebhook(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetRelayWebhook(r.Context(), chi.URLParam(r, "webhookId"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Relay webhook")
		return
	}
	if item.ServerID != chi.URLParam(r, "id") {
		a.notFoundOrInternal(w, store.ErrNotFound, "Relay webhook")
		return
	}
	connections, err := a.store.ListGitHubApps(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, connection := range connections {
		if connection.RelayWebhookID == item.ID {
			problem(w, http.StatusConflict, "Webhook endpoint in use", "Remove or change the provider connection before deleting this endpoint.")
			return
		}
	}
	server, err := a.store.GetServer(r.Context(), item.ServerID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Relay server")
		return
	}
	client, err := a.relayClient(r.Context(), server)
	if err != nil {
		problem(w, 409, "Relay unavailable", err.Error())
		return
	}
	if err := client.DeleteHook(r.Context(), item.RemoteID); err != nil {
		problem(w, 502, "Relay endpoint could not be removed", err.Error())
		return
	}
	if err := a.store.DeleteRelayWebhook(r.Context(), item.ID); err != nil {
		a.notFoundOrInternal(w, err, "Relay webhook")
		return
	}
	w.WriteHeader(204)
}

// RunRelayConsumers maintains one outbound long-poll connection per relay
// server. Leases are acknowledged only after provider processing completes.
func (a *API) RunRelayConsumers(ctx context.Context) {
	type worker struct{ cancel context.CancelFunc }
	workers := map[string]worker{}
	var mu sync.Mutex
	reconcile := func() {
		servers, err := a.store.ListServers(ctx)
		if err != nil {
			if a.logger != nil {
				a.logger.Error("list relay servers", "error", err)
			}
			return
		}
		present := map[string]bool{}
		mu.Lock()
		defer mu.Unlock()
		for _, server := range servers {
			if server.Runtime != core.ServerRuntimeRelay {
				continue
			}
			present[server.ID] = true
			if _, ok := workers[server.ID]; ok {
				continue
			}
			workerCtx, cancel := context.WithCancel(ctx)
			workers[server.ID] = worker{cancel: cancel}
			go a.consumeRelay(workerCtx, server.ID)
		}
		for id, worker := range workers {
			if !present[id] {
				worker.cancel()
				delete(workers, id)
			}
		}
	}
	reconcile()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, worker := range workers {
			worker.cancel()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

func (a *API) consumeRelay(ctx context.Context, serverID string) {
	backoff := time.Second
	for ctx.Err() == nil {
		server, err := a.store.GetServer(ctx, serverID)
		if err != nil {
			return
		}
		client, err := a.relayClient(ctx, server)
		if err != nil {
			a.markRelayError(ctx, server, err)
			return
		}
		delivery, err := client.Next(ctx)
		if err != nil {
			a.markRelayError(ctx, server, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if delivery == nil {
			a.refreshRelayStatus(ctx, client, server)
			continue
		}
		disposition, detail := a.processRelayDelivery(ctx, server, *delivery)
		ackCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = client.Acknowledge(ackCtx, *delivery, disposition, detail)
		cancel()
		if err != nil {
			a.markRelayError(ctx, server, fmt.Errorf("ack delivery %s: %w", delivery.ID, err))
		} else {
			a.refreshRelayStatus(ctx, client, server)
		}
	}
}

func (a *API) refreshRelayStatus(ctx context.Context, client relay.Client, server core.Server) {
	status, err := client.Verify(ctx)
	if err != nil {
		a.markRelayError(ctx, server, err)
		return
	}
	now := time.Now().UTC()
	server.State = "connected"
	server.Relay.LastConnectedAt = &now
	server.Relay.LastError = ""
	server.Relay.PendingEvents = status.Pending
	server.Relay.OldestPendingAt = status.OldestPending
	_ = a.store.UpdateServer(ctx, server)
}

func (a *API) markRelayError(ctx context.Context, server core.Server, err error) {
	server.State = "degraded"
	if server.Relay != nil {
		server.Relay.LastError = err.Error()
	}
	_ = a.store.UpdateServer(ctx, server)
	if a.logger != nil {
		a.logger.Warn("relay connection degraded", "server_id", server.ID, "error", err)
	}
}

func (a *API) processRelayDelivery(ctx context.Context, server core.Server, delivery relay.Delivery) (string, string) {
	endpoint, err := a.store.GetRelayWebhookByRemoteID(ctx, server.ID, delivery.HookID)
	if err != nil {
		return "dead", "No Dispatch webhook binding exists for this relay endpoint."
	}
	endpoint.LastDeliveryAt = &delivery.ReceivedAt
	endpoint.UpdatedAt = time.Now().UTC()
	if endpoint.Provider != core.EventProviderGitHub {
		endpoint.LastError = "No provider adapter is installed for " + string(endpoint.Provider)
		_ = a.store.UpdateRelayWebhook(ctx, endpoint)
		return "dead", endpoint.LastError
	}
	request := httptest.NewRequest(http.MethodPost, "http://dispatch.local/api/v1/events/github/apps/"+endpoint.ProviderConnectionID, bytes.NewReader(delivery.Body)).WithContext(ctx)
	request.Header = http.Header(delivery.Headers)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", endpoint.ProviderConnectionID)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	recorder := httptest.NewRecorder()
	a.githubAppWebhook(recorder, request)
	status := recorder.Code
	if status >= 200 && status < 300 {
		endpoint.LastError = ""
		_ = a.store.UpdateRelayWebhook(ctx, endpoint)
		return "ack", ""
	}
	detail := strings.TrimSpace(recorder.Body.String())
	if len(detail) > 1000 {
		detail = detail[:1000]
	}
	endpoint.LastError = detail
	_ = a.store.UpdateRelayWebhook(ctx, endpoint)
	if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound {
		return "dead", detail
	}
	return "retry", detail
}
