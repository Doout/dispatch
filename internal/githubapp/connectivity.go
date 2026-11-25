package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

// TestConnection checks the API root using the same route as manifest conversion.
// It sends no App credentials and does not follow redirects or consume the body.
func (m *Manager) TestConnection(ctx context.Context, app core.GitHubAppConnection) error {
	client := m.routedClient(appTransport{connection: &app})
	client.Timeout = 10 * time.Second
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(app.APIURL, "/")+"/", nil)
	if err != nil || request.URL.Scheme != "https" || request.URL.Hostname() == "" || request.URL.User != nil ||
		request.URL.RawQuery != "" || request.URL.ForceQuery || request.URL.Fragment != "" {
		return errors.New("Enter an HTTPS GitHub API root without credentials, a query, or a fragment.")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := client.Do(request)
	if err != nil {
		return ConnectionDiagnostic(err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300, response.StatusCode == http.StatusUnauthorized:
		return nil
	case response.StatusCode == http.StatusProxyAuthRequired:
		return errors.New("The proxy requires authentication. Check the proxy credentials.")
	case response.StatusCode >= 300 && response.StatusCode < 400:
		return errors.New("The GitHub API redirected the connection. Enter the final API root URL.")
	default:
		return fmt.Errorf("The GitHub API returned HTTP %d. Check the API root URL and network access.", response.StatusCode)
	}
}
