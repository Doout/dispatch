package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/repositoryproxy"
	"github.com/doout/dispatch/internal/sourceerror"
)

// ClientForApp resolves the current route for each request. Pollers and log
// verifiers can keep a client across changes to the connection.
func (m *Manager) ClientForApp(appID string, timeout time.Duration) *http.Client {
	client := m.routedClient(appTransport{id: appID})
	client.Timeout = timeout
	return client
}

type repositoryConnectionKey struct{}

// WithRepositoryConnection keeps installation and repository API requests for a
// named binding on their original endpoint, including when GitHub reports a move.
func WithRepositoryConnection(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, repositoryConnectionKey{}, id)
}

func (m *Manager) routedClient(route appTransport) *http.Client {
	client := http.Client{Timeout: 15 * time.Second}
	if m.Client != nil {
		client = *m.Client
	}
	route.manager, route.base = m, client.Transport
	if route.base == nil {
		route.base = http.DefaultTransport
	}
	client.Transport = route
	return &client
}

type appTransport struct {
	manager *Manager
	id      string
	// Manifest conversion has settings before the App exists in the store.
	connection *core.GitHubAppConnection
	base       http.RoundTripper
}

func (t appTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// The base transport closes bodies it accepts. An edge job copies the body.
	sentToBase := false
	if body := r.Body; body != nil {
		defer func() {
			if !sentToBase {
				body.Close()
			}
		}()
	}
	connection := t.connection
	if connection == nil {
		if t.manager.Store == nil {
			return nil, errors.New("GitHub App route storage is not configured")
		}
		item, err := t.manager.Store.GetGitHubApp(r.Context(), t.id)
		if err != nil {
			return nil, fmt.Errorf("load GitHub App route: %w", err)
		}
		connection = &item
	}
	ctx := repositoryproxy.WithGitHubAppConnection(r.Context(), *connection)
	r = r.WithContext(ctx)
	if connection.PrivateNetworkID != "" {
		request, proxied, err := t.manager.Proxy.Route(r)
		if err != nil {
			return nil, err
		}
		if !proxied {
			return t.manager.edgeRequest(r, connection.PrivateNetworkID)
		}
		r = request
	}
	sentToBase = true
	return t.base.RoundTrip(r)
}

func (m *Manager) edgeRequest(r *http.Request, networkID string) (*http.Response, error) {
	if m.Store == nil || m.Edge == nil {
		return nil, errors.New("GitHub edge routing is not configured")
	}
	network, err := m.Store.GetPrivateNetwork(r.Context(), networkID)
	if err != nil {
		return nil, fmt.Errorf("load GitHub network route: %w", err)
	}
	response, err := m.Edge.Do(r.Context(), network, r)
	// The enrolled agent returns redirects without following them. A private endpoint cannot choose
	// a second destination for the controller to contact.
	if err == nil && response.StatusCode >= 300 && response.StatusCode < 400 && response.Header.Get("Location") != "" {
		defer response.Body.Close()
		return nil, sourceerror.FromResponse(response)
	}
	return response, err
}
