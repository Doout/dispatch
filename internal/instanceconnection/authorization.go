package instanceconnection

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/instancebridge"
)

func (m *Manager) mappedUser(ctx context.Context, revision uint64, subject string) (core.User, error) {
	m.mu.RLock()
	if revision != m.revision || !m.current.Configured || !m.current.RemoteAccess {
		m.mu.RUnlock()
		return core.User{}, errors.New("remote access is disabled")
	}
	userID := ""
	for _, mapping := range m.current.UserMappings {
		if mapping.Subject == subject {
			userID = mapping.UserID
			break
		}
	}
	m.mu.RUnlock()
	if userID == "" {
		return core.User{}, errors.New("this account has no local access grant")
	}
	user, err := m.config.LookupUser(ctx, userID)
	if err != nil || user.State != core.UserStateActive {
		return core.User{}, errors.New("local account access has been revoked")
	}
	return user, nil
}
func allowedWebhook(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	p := r.URL.Path
	if path.Clean(p) != p || r.URL.EscapedPath() != p {
		return false
	}
	if p == "/api/v1/events/github" {
		return true
	}
	if !strings.HasPrefix(p, "/api/v1/events/github/apps/") {
		return false
	}
	id := strings.TrimPrefix(p, "/api/v1/events/github/apps/")
	return id != "" && !strings.ContainsAny(id, "/\\")
}
func allowedUser(r *http.Request) bool {
	p := r.URL.Path
	if path.Clean(p) != p || strings.Contains(p, "\\") {
		return false
	}
	if !strings.HasPrefix(p, "/api/v1/") {
		return false
	}
	if instancebridge.AccessManagementRequestAllowed(r) {
		return true
	}
	// Connection grants, machine credentials, and login sessions stay local.
	for _, prefix := range []string{"/api/v1/instance-connection", "/api/v1/automation", "/api/v1/service-accounts", "/api/v1/edge/", "/api/v1/bootstrap/", "/api/v1/auth/providers"} {
		if strings.HasPrefix(p, prefix) {
			return false
		}
	}
	if strings.HasPrefix(p, "/api/v1/auth/") {
		return r.Method == http.MethodGet && (p == "/api/v1/auth/me" || p == "/api/v1/auth/profile")
	}
	if strings.HasPrefix(p, "/api/v1/users") || strings.HasPrefix(p, "/api/v1/teams") || strings.HasPrefix(p, "/api/v1/role-assignments") {
		return r.Method == http.MethodGet
	}
	return true
}
func (m *Manager) authorize(ctx context.Context, revision uint64, operation instancebridge.Operation, identity instancebridge.Identity, r *http.Request) error {
	m.mu.RLock()
	live := m.current.Configured && revision == m.revision
	m.mu.RUnlock()
	if !live {
		return errors.New("connection has been revoked")
	}
	if operation == instancebridge.OperationNative {
		m.mu.RLock()
		remote := m.current.RemoteAccess
		origin := m.publicOriginLocked()
		m.mu.RUnlock()
		if !remote || origin == "" || r.Host != strings.TrimPrefix(origin, "https://") || !instancebridge.NativeRequestAllowed(r) {
			return errors.New("instance sign-in is not allowed")
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") != origin {
			return errors.New("instance request origin does not match")
		}
		return nil
	}
	if operation == instancebridge.OperationWebhook {
		if !allowedWebhook(r) {
			return errors.New("webhook route is not allowed")
		}
		return nil
	}
	if operation == instancebridge.OperationWorkload {
		return nil
	}
	if operation == instancebridge.OperationNetwork {
		if !instancebridge.NetworkRequestAllowed(r) {
			return errors.New("network route is not allowed")
		}
		return nil
	}
	if operation != instancebridge.OperationUser || !allowedUser(r) {
		return errors.New("remote route is not allowed")
	}
	_, err := m.mappedUser(ctx, revision, identity.UserID)
	return err
}
func (m *Manager) wrap(next http.Handler, revision uint64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		operation := instancebridge.OperationFromContext(r.Context())
		id, _ := instancebridge.IdentityFromContext(r.Context())
		if err := m.authorize(r.Context(), revision, operation, id, r); err != nil {
			http.Error(w, "Instance access denied.", http.StatusForbidden)
			return
		}
		m.mu.Lock()
		if m.revision == revision {
			m.current.Connected = true
			m.current.LastError = ""
		}
		m.mu.Unlock()
		if operation == instancebridge.OperationWebhook || operation == instancebridge.OperationNetwork {
			next.ServeHTTP(w, r)
			return
		}
		if operation == instancebridge.OperationNative {
			m.mu.RLock()
			origin := m.publicOriginLocked()
			m.mu.RUnlock()
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			ctx = context.WithValue(ctx, requestKey{}, requestIdentity{revision: revision, native: true, origin: origin})
			go func() {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if !m.NativeRequest(r.WithContext(ctx)) {
							cancel()
							return
						}
					}
				}
			}()
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		if operation == instancebridge.OperationWorkload {
			m.serveWorkload(w, r)
			return
		}
		user, err := m.mappedUser(r.Context(), revision, id.UserID)
		if err != nil {
			http.Error(w, "Instance access denied.", http.StatusForbidden)
			return
		}
		actor := core.Identity{ID: user.ID, Kind: core.PrincipalUser, Username: user.Username, DisplayName: user.DisplayName, SystemRole: user.SystemRole}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		ctx = context.WithValue(ctx, requestKey{}, requestIdentity{identity: actor, revision: revision})
		// Long-running logs and streams stop if the local user is disabled or unlinked.
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					current, err := m.mappedUser(ctx, revision, id.UserID)
					if err != nil || current.SystemRole != user.SystemRole || !current.UpdatedAt.Equal(user.UpdatedAt) {
						cancel()
						return
					}
				}
			}
		}()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Identity only accepts context established by this manager's current outbound
// connection. A browser header, cookie, or bearer token cannot create it.
func (m *Manager) Identity(r *http.Request) (core.Identity, bool) {
	value, ok := r.Context().Value(requestKey{}).(requestIdentity)
	if !ok || value.native || r.Context().Err() != nil {
		return core.Identity{}, false
	}
	m.mu.RLock()
	valid := m.current.Configured && m.current.RemoteAccess && value.revision == m.revision
	m.mu.RUnlock()
	if valid {
		user, err := m.config.LookupUser(r.Context(), value.identity.ID)
		valid = err == nil && user.State == core.UserStateActive && user.SystemRole == value.identity.SystemRole
	}
	return value.identity, valid
}

func (m *Manager) publicOriginLocked() string {
	if m.current.InstanceSlug == "" || m.current.RootDomain == "" {
		return ""
	}
	return "https://" + m.current.InstanceSlug + "." + m.current.RootDomain
}

func (m *Manager) NativeRequest(r *http.Request) bool {
	v, ok := r.Context().Value(requestKey{}).(requestIdentity)
	if !ok || !v.native || r.Context().Err() != nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current.Configured && m.current.RemoteAccess && v.revision == m.revision && v.origin == m.publicOriginLocked()
}

func (m *Manager) NativeOrigin(r *http.Request) string {
	if !m.NativeRequest(r) {
		return ""
	}
	v, _ := r.Context().Value(requestKey{}).(requestIdentity)
	return v.origin
}

// RemoteOrigin returns the enrolled public address only for a live remote user.
func (m *Manager) RemoteOrigin(r *http.Request) string {
	if m.NativeRequest(r) {
		return m.NativeOrigin(r)
	}
	if _, valid := m.Identity(r); !valid {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	value, _ := r.Context().Value(requestKey{}).(requestIdentity)
	if !m.current.Configured || !m.current.RemoteAccess || value.revision != m.revision || r.Context().Err() != nil {
		return ""
	}
	origin := m.publicOriginLocked()
	if origin == "" || r.Host != strings.TrimPrefix(origin, "https://") {
		return ""
	}
	return origin
}

// IsRemote identifies trusted transport requests, including revoked requests.
func IsRemote(r *http.Request) bool {
	_, ok := r.Context().Value(requestKey{}).(requestIdentity)
	return ok
}
