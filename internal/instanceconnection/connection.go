// Package instanceconnection connects a complete self-hosted installation to its
// control plane. Credentials and access decisions stay in this installation.
package instanceconnection

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/instancebridge"
	"github.com/doout/dispatch/internal/store"
)

type UserMapping struct {
	Subject string `json:"subject"`
	UserID  string `json:"userID"`
}
type Setup struct {
	ControlPlaneURL string        `json:"controlPlaneURL"`
	EnrollmentToken string        `json:"enrollmentToken,omitempty"`
	DirectURL       string        `json:"directURL"`
	RemoteAccess    bool          `json:"remoteAccess"`
	UserMappings    []UserMapping `json:"userMappings"`
	OwnerUserID     string        `json:"ownerUserID,omitempty"`
}
type Status struct {
	Configured      bool          `json:"configured"`
	Connected       bool          `json:"connected"`
	ControlPlaneURL string        `json:"controlPlaneURL"`
	InstanceID      string        `json:"instanceID"`
	InstallationID  string        `json:"installationID"`
	DirectURL       string        `json:"directURL"`
	RemoteAccess    bool          `json:"remoteAccess"`
	UserMappings    []UserMapping `json:"userMappings"`
	LastError       string        `json:"lastError,omitempty"`
}
type saved struct {
	Status
	InstanceSlug string `json:"instanceSlug"`
	RootDomain   string `json:"rootDomain"`
	Credential   string `json:"credential"`
	Generation   int64  `json:"generation"`
	WebSocketURL string `json:"websocketURL"`
}
type Config struct {
	File                  string
	LookupUser            func(context.Context, string) (core.User, error)
	LookupApp             func(context.Context, string) (core.App, error)
	ListRoutes            func(context.Context) ([]core.ApplicationRoute, error)
	LookupRoute           func(context.Context, string) (core.ApplicationRoute, error)
	LookupRouteByHostname func(context.Context, string) (core.ApplicationRoute, error)
	Usage                 func(context.Context, time.Time, time.Time) (store.TenantUsageTotals, error)
	Client                *http.Client
	// AllowInsecure is for loopback integration tests, never enabled by dispatch.
	AllowInsecure bool
}
type Manager struct {
	mu         sync.RWMutex
	operations sync.Mutex
	config     Config
	current    saved
	handler    http.Handler
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	revision   uint64
}
type requestIdentity struct {
	identity core.Identity
	revision uint64
}
type requestKey struct{}

func Open(cfg Config) (*Manager, error) {
	if cfg.File == "" || cfg.LookupUser == nil {
		return nil, errors.New("connection storage and user lookup are required")
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	m := &Manager{config: cfg}
	raw, err := os.ReadFile(cfg.File)
	if err == nil {
		info, statErr := os.Stat(cfg.File)
		if statErr != nil {
			return nil, statErr
		}
		if info.Mode().Perm()&0077 != 0 {
			m.current = saved{Status: Status{LastError: "Saved connection credentials were not private. Connect again."}}
		} else if err = json.Unmarshal(raw, &m.current); err != nil {
			m.current = saved{Status: Status{LastError: "Saved connection settings are invalid. Connect again."}}
		}
		m.current.Connected = false
		if m.current.Configured {
			if _, err = origin(m.current.ControlPlaneURL, cfg.AllowInsecure); err != nil {
				m.current = saved{Status: Status{LastError: "Saved connection settings are invalid. Connect again."}}
			} else if err = m.validateWebSocket(m.current.WebSocketURL, m.current.ControlPlaneURL); err != nil {
				m.current = saved{Status: Status{LastError: "Saved connection settings are invalid. Connect again."}}
			} else if m.current.Credential == "" || m.current.InstanceID == "" || m.current.Generation < 1 {
				m.current = saved{Status: Status{LastError: "Saved connection credentials are incomplete. Connect again."}}
			} else {
				m.current.LastError = ""
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if m.current.InstallationID == "" {
		id := make([]byte, 24)
		if _, err = rand.Read(id); err != nil {
			return nil, err
		}
		m.current.InstallationID = hex.EncodeToString(id)
		if err = m.save(m.current); err != nil {
			return nil, err
		}
	}
	return m, nil
}
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.current.Status
	s.UserMappings = append([]UserMapping{}, s.UserMappings...)
	return s
}
func (m *Manager) Start(ctx context.Context, handler http.Handler) {
	m.operations.Lock()
	defer m.operations.Unlock()
	m.ctx = ctx
	m.handler = handler
	m.restart()
}
func (m *Manager) Stop() { m.operations.Lock(); defer m.operations.Unlock(); m.stop() }
func (m *Manager) stop() {
	m.mu.Lock()
	m.revision++
	cancel, done := m.cancel, m.done
	m.cancel = nil
	m.done = nil
	m.current.Connected = false
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
func (m *Manager) restart() {
	m.stop()
	m.mu.Lock()
	if m.ctx == nil || m.handler == nil || !m.current.Configured {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	done := make(chan struct{})
	m.done = done
	s := m.current
	revision := m.revision
	handler := m.handler
	m.mu.Unlock()
	go func() {
		defer close(done)
		if m.config.ListRoutes != nil && m.config.LookupApp != nil {
			go m.reconcileRoutes(ctx, s)
		}
		if m.config.Usage != nil {
			go m.reportUsage(ctx, s)
		}
		retry := time.Second
		for ctx.Err() == nil {
			policyErr := m.call(ctx, http.MethodPut, s.ControlPlaneURL+"/api/v1/instance-connections/policy", s.Credential, map[string]any{"directURL": s.DirectURL, "remoteAccess": s.RemoteAccess}, nil)
			if policyErr != nil {
				m.setError(revision, "Cannot reach the control plane. Reconnecting.")
			} else {
				connector, err := instancebridge.NewConnector(instancebridge.ConnectorConfig{Endpoint: s.WebSocketURL, Credential: s.Credential, RemoteAccess: s.RemoteAccess, AllowInsecure: m.config.AllowInsecure, Handler: m.wrap(handler, revision), Authorize: func(ctx context.Context, op instancebridge.Operation, id instancebridge.Identity, r *http.Request) error {
					return m.authorize(ctx, revision, op, id, r)
				}, OnConnected: func() {
					m.mu.Lock()
					if m.revision == revision {
						m.current.Connected = true
						m.current.LastError = ""
					}
					m.mu.Unlock()
				}})
				if err != nil {
					m.setError(revision, "The connection could not start.")
					return
				}
				err = connector.Connect(ctx)
				m.mu.Lock()
				if m.revision == revision {
					m.current.Connected = false
					if ctx.Err() == nil {
						m.current.LastError = "Cannot reach the control plane. Reconnecting."
					}
				}
				m.mu.Unlock()
			}
			if ctx.Err() != nil {
				return
			}
			timer := time.NewTimer(retry)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if retry < 30*time.Second {
				retry *= 2
				if retry > 30*time.Second {
					retry = 30 * time.Second
				}
			}
		}
	}()
}
func (m *Manager) setError(revision uint64, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if revision == m.revision {
		m.current.LastError = message
	}
}
func (m *Manager) Configure(ctx context.Context, input Setup) (Status, error) {
	m.operations.Lock()
	defer m.operations.Unlock()
	cp, err := origin(input.ControlPlaneURL, m.config.AllowInsecure)
	if err != nil {
		return Status{}, err
	}
	if input.DirectURL != "" {
		if err = validateDirectURL(input.DirectURL); err != nil {
			return Status{}, err
		}
	}
	if len(input.UserMappings) > 5000 {
		return Status{}, errors.New("at most 5000 user mappings are allowed")
	}
	seen := map[string]bool{}
	if input.OwnerUserID != "" {
		user, lookupErr := m.config.LookupUser(ctx, input.OwnerUserID)
		if lookupErr != nil || user.State != core.UserStateActive {
			return Status{}, errors.New("initial access requires an active local account")
		}
	}
	for _, mapping := range input.UserMappings {
		if mapping.Subject == "" || len(mapping.Subject) > 256 || mapping.UserID == "" || seen[mapping.Subject] {
			return Status{}, errors.New("user mappings require unique account IDs and local users")
		}
		seen[mapping.Subject] = true
		user, lookupErr := m.config.LookupUser(ctx, mapping.UserID)
		if lookupErr != nil || user.State != core.UserStateActive {
			return Status{}, errors.New("mapped users must be active local accounts")
		}
	}
	m.mu.RLock()
	next := m.current
	m.mu.RUnlock()
	if input.EnrollmentToken == "" {
		if !next.Configured || next.ControlPlaneURL != cp {
			return Status{}, errors.New("an enrollment token is required to connect")
		}
	} else {
		if len(input.EnrollmentToken) > 1024 {
			return Status{}, errors.New("enrollment token is invalid")
		}
		var response struct {
			InstanceID   string `json:"instanceID"`
			Credential   string `json:"credential"`
			Generation   int64  `json:"generation"`
			WebSocketURL string `json:"websocketURL"`
			OwnerSubject string `json:"ownerSubject"`
			InstanceSlug string `json:"instanceSlug"`
			RootDomain   string `json:"rootDomain"`
		}
		if err = m.call(ctx, http.MethodPost, cp+"/api/v1/instance-connections/enroll", "", map[string]any{"enrollmentToken": input.EnrollmentToken, "directURL": input.DirectURL, "remoteAccess": input.RemoteAccess, "installationID": next.InstallationID}, &response); err != nil {
			return Status{}, err
		}
		if response.InstanceID == "" || response.Credential == "" || response.Generation < 1 {
			return Status{}, errors.New("control plane returned incomplete connection credentials")
		}
		if err = m.validateWebSocket(response.WebSocketURL, cp); err != nil {
			return Status{}, err
		}
		next.Credential = response.Credential
		next.InstanceID = response.InstanceID
		next.Generation = response.Generation
		next.WebSocketURL = response.WebSocketURL
		next.InstanceSlug = response.InstanceSlug
		next.RootDomain = response.RootDomain
		if input.OwnerUserID != "" {
			user, lookupErr := m.config.LookupUser(ctx, input.OwnerUserID)
			if lookupErr != nil || user.State != core.UserStateActive || response.OwnerSubject == "" {
				return Status{}, errors.New("an active local user and control plane account ID are required")
			}
			if !seen[response.OwnerSubject] {
				input.UserMappings = append(input.UserMappings, UserMapping{Subject: response.OwnerSubject, UserID: user.ID})
			}
		}
	}
	next.Configured = true
	next.ControlPlaneURL = cp
	next.DirectURL = input.DirectURL
	next.RemoteAccess = input.RemoteAccess
	next.UserMappings = append([]UserMapping{}, input.UserMappings...)
	next.LastError = ""
	next.Connected = false
	if err = m.save(next); err != nil {
		return Status{}, err
	}
	m.stop()
	m.mu.Lock()
	m.current = next
	m.mu.Unlock()
	m.restart()
	return m.Status(), nil
}
func (m *Manager) Disconnect(ctx context.Context) error {
	m.operations.Lock()
	defer m.operations.Unlock()
	// Moving the credential file first makes revocation survive a crash even
	// if writing the cleared configuration fails. A revoked file is never loaded.
	if err := os.Rename(m.config.File, m.config.File+".revoked"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	m.stop()
	m.mu.Lock()
	old := m.current
	m.current = saved{Status: Status{InstallationID: old.InstallationID, UserMappings: []UserMapping{}}}
	next := m.current
	m.mu.Unlock()
	if err := m.save(next); err != nil {
		return err
	}
	_ = os.Remove(m.config.File + ".revoked")
	if old.Configured { // Local revocation succeeds even when the control plane is unavailable.
		notifyCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		_ = m.call(notifyCtx, http.MethodDelete, old.ControlPlaneURL+"/api/v1/instance-connections/current", old.Credential, nil, nil)
	}
	return nil
}
func (m *Manager) save(value saved) error {
	dir := filepath.Dir(m.config.File)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".instance-connection-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err = temp.Chmod(0600); err == nil {
		_, err = temp.Write(raw)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, m.config.File)
}
func (m *Manager) call(ctx context.Context, method, endpoint, credential string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := m.config.Client.Do(req)
	if err != nil {
		return errors.New("cannot reach the control plane")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("control plane refused the connection request (%d)", resp.StatusCode)
	}
	if output != nil {
		if err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(output); err != nil {
			return errors.New("control plane returned an invalid response")
		}
	}
	return nil
}
func origin(value string, insecure bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Scheme != "https" && !(insecure && u.Scheme == "http") {
		return "", errors.New("control plane address must be an HTTPS origin")
	}
	u.Path = ""
	return u.String(), nil
}
func validateDirectURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("instance address must be an HTTP or HTTPS URL")
	}
	return nil
}
func (m *Manager) validateWebSocket(value, cp string) error {
	u, err := url.Parse(value)
	base, _ := url.Parse(cp)
	if err != nil || u.Host != base.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/v1/instance-connections/ws" || u.Scheme != "wss" && !(m.config.AllowInsecure && u.Scheme == "ws") {
		return errors.New("control plane returned an invalid WebSocket address")
	}
	return nil
}
