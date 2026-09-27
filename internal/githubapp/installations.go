package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type repositoryInstallation struct {
	ID        int64
	ExpiresAt time.Time
}

// RepositoryInstallation resolves access using the App's identity, independently
// of the installation retained from the setup callback.
func (m *Manager) RepositoryInstallation(ctx context.Context, id, repository string) (int64, error) {
	path, err := repositoryPath(repository)
	if err != nil {
		return 0, err
	}
	key := id + ":" + strings.ToLower(path)
	m.mu.Lock()
	cached := m.repositories[key]
	m.mu.Unlock()
	if cached.ID > 0 && time.Now().Before(cached.ExpiresAt) {
		return cached.ID, nil
	}
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return 0, err
	}
	jwt, err := m.appJWT(connection)
	if err != nil {
		return 0, err
	}
	var result struct {
		ID          int64      `json:"id"`
		AppID       int64      `json:"app_id"`
		SuspendedAt *time.Time `json:"suspended_at"`
	}
	endpoint := strings.TrimRight(connection.APIURL, "/") + "/repos/" + path + "/installation"
	if err := m.request(ctx, http.MethodGet, endpoint, jwt, nil, &result, connection.PrivateNetworkID); err != nil {
		return 0, fmt.Errorf("find GitHub App installation for %s: %w. Check that this App is installed for the repository and has access to it", repository, err)
	}
	if result.ID < 1 || result.AppID != connection.AppID {
		return 0, errors.New("GitHub returned an installation for a different App")
	}
	if result.SuspendedAt != nil {
		return 0, fmt.Errorf("GitHub App installation for %s is suspended", repository)
	}
	m.mu.Lock()
	if m.repositories == nil {
		m.repositories = map[string]repositoryInstallation{}
	}
	for key, entry := range m.repositories {
		if time.Now().After(entry.ExpiresAt) {
			delete(m.repositories, key)
		}
	}
	m.repositories[key] = repositoryInstallation{ID: result.ID, ExpiresAt: time.Now().Add(time.Minute)}
	m.mu.Unlock()
	return result.ID, nil
}

func (m *Manager) RepositoryToken(ctx context.Context, id, repository string) (string, error) {
	installation, err := m.RepositoryInstallation(ctx, id, repository)
	if err != nil {
		return "", err
	}
	return m.installationToken(ctx, id, installation)
}

func tokenCacheKey(id string, installation int64) string {
	return fmt.Sprintf("%s:%d", id, installation)
}
