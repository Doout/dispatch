package provision

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/store"
)

type EnrollmentStore interface {
	LifecycleStore
	edge.CredentialStore
	CreatePrivateNetwork(context.Context, core.PrivateNetwork) error
	CreateServer(context.Context, core.Server) error
	GetServer(context.Context, string) (core.Server, error)
	DeleteServer(context.Context, string) error
}
type Enrollment struct {
	NodeID    string    `json:"nodeId"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Enrollment creates a fresh node identity for this server. Tokens are returned
// once and remain outside provider payloads; cloud-init bootstrap is separate.
func (m *Manager) Enrollment(ctx context.Context, id string) (Enrollment, error) {
	var result Enrollment
	data, ok := m.Store.(EnrollmentStore)
	if !ok {
		return result, errors.New("enrollment storage is unavailable")
	}
	server, err := data.GetManagedServer(ctx, id)
	if err != nil {
		return result, err
	}
	if err = m.authorize(ctx, server.ProjectID, server.ProviderID, "infrastructure.modify"); err != nil {
		return result, err
	}
	if server.AllocationState != "allocated" {
		return result, store.ErrInfrastructureChanged
	}
	now := m.now()
	if _, err = data.GetPrivateNetwork(ctx, server.NodeID); errors.Is(err, store.ErrNotFound) {
		node := core.PrivateNetwork{ID: server.NodeID, Name: server.Name, Driver: edge.DriverAgent, Config: map[string]string{}, Details: map[string]string{}, State: "waiting", CreatedAt: now, UpdatedAt: now}
		if err = data.CreatePrivateNetwork(ctx, node); err != nil {
			if _, readErr := data.GetPrivateNetwork(ctx, server.NodeID); readErr != nil {
				return result, err
			}
		}
	} else if err != nil {
		return result, err
	}
	if credential, e := data.GetEdgeCredential(ctx, server.NodeID); e == nil && credential.PublicKey != "" && !credential.Revoked {
		return result, errors.New("this machine is already enrolled; use explicit identity replacement for recovery")
	} else if e != nil && !errors.Is(e, store.ErrNotFound) {
		return result, e
	}
	token, err := edge.RotateCredentials(ctx, data, server.NodeID, now)
	if err != nil {
		return result, err
	}
	server.EnrollmentState = "waiting"
	server.RuntimeState = "waiting"
	server.UpdatedAt = now
	server.Revision++
	if err = data.UpdateManagedServer(ctx, server, server.Revision-1); err != nil {
		return result, err
	}
	return Enrollment{NodeID: server.NodeID, Token: token, ExpiresAt: now.Add(edge.EnrollmentLifetime)}, nil
}

// A provider IP is allocation evidence only. The ordinary target is published
// after this exact node has enrolled and advertised the typed deploy runtime.
func (m *Manager) RefreshReadiness(ctx context.Context) error {
	data, ok := m.Store.(EnrollmentStore)
	if !ok {
		return nil
	}
	servers, err := data.ListManagedServers(ctx)
	if err != nil {
		return err
	}
	for _, server := range servers {
		if server.AllocationState == "deleted" {
			if _, e := data.GetEdgeCredential(ctx, server.NodeID); e == nil {
				if e = data.RevokeEdgeCredential(ctx, server.NodeID, m.now()); e != nil {
					return e
				}
			}
			if _, e := data.GetServer(ctx, server.ID); e == nil {
				if e = data.DeleteServer(ctx, server.ID); e != nil {
					return e
				}
			}
			continue
		}
		if server.AllocationState != "allocated" {
			continue
		}
		enrollment, runtime := "waiting", "waiting"
		credential, e := data.GetEdgeCredential(ctx, server.NodeID)
		if e == nil && credential.PublicKey != "" && !credential.Revoked {
			enrollment = "enrolled"
			node, e := data.GetPrivateNetwork(ctx, server.NodeID)
			checkedAt, checkedErr := time.Parse(time.RFC3339Nano, node.Details["runtimeCheckedAt"])
			if e == nil && checkedErr == nil && !checkedAt.Before(credential.UpdatedAt) && checkedAt.After(m.now().Add(-90*time.Second)) && node.Details["runtimeVersion"] == remoteruntime.APIVersion && slices.Contains(strings.Split(node.Details["runtimeCapabilities"], ","), "deploy") {
				runtime = "ready"
			}
		}
		if server.EnrollmentState != enrollment || server.RuntimeState != runtime {
			server.EnrollmentState = enrollment
			server.RuntimeState = runtime
			server.Revision++
			server.UpdatedAt = m.now()
			if e = data.UpdateManagedServer(ctx, server, server.Revision-1); e != nil {
				return e
			}
		}
		if runtime == "ready" {
			if _, e = data.GetServer(ctx, server.ID); errors.Is(e, store.ErrNotFound) {
				target := core.Server{ID: server.ID, ProjectID: server.ProjectID, Name: server.Name, Address: server.Address, Runtime: core.ServerRuntimeDocker, State: "ready", AgentMode: "outbound-runtime", AgentNodeID: server.NodeID, CreatedAt: m.now()}
				if e = data.CreateServer(ctx, target); e != nil {
					if _, check := data.GetServer(ctx, server.ID); check != nil {
						return e
					}
				}
			} else if e != nil {
				return e
			}
		}
	}
	return nil
}
