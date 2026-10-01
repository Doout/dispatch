package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/bootstrap"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

type LifecycleStore interface {
	ProviderStore
	store.InfrastructureLifecycleStore
	GetProject(context.Context, string) (core.Project, error)
}

type CreateInput struct {
	Bootstrap      *core.TargetBootstrapPlan `json:"bootstrap,omitempty"`
	ActorID        string                    `json:"-"`
	ProjectID      string                    `json:"projectId"`
	ProviderID     string                    `json:"providerId"`
	Name           string                    `json:"name"`
	Region         string                    `json:"region"`
	Size           string                    `json:"size"`
	Image          string                    `json:"image"`
	Network        string                    `json:"network"`
	SSHKeySecretID string                    `json:"sshKeySecretId"`
	Config         map[string]any            `json:"config"`
	SecretRefs     map[string]string         `json:"secretRefs,omitempty"`
}

type Acceptance struct {
	ReviewID    string `json:"reviewId"`
	Digest      string `json:"digest"`
	ConfirmName string `json:"confirmName"`
	RequestKey  string `json:"requestKey"`
}

type Accepted struct {
	Server    core.ManagedServer           `json:"server"`
	Operation core.InfrastructureOperation `json:"operation"`
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}
func (m *Manager) lifecycle() (LifecycleStore, error) {
	s, ok := m.Store.(LifecycleStore)
	if !ok {
		return nil, errors.New("durable infrastructure storage is unavailable")
	}
	return s, nil
}
func (m *Manager) authorize(ctx context.Context, project, providerID, permission string) error {
	if m.Authorize != nil {
		return m.Authorize(ctx, project, providerID, permission)
	}
	return nil
}
func digest(value string) string { h := sha256.Sum256([]byte(value)); return hex.EncodeToString(h[:]) }
func operationID(providerID, actor, key, action string) (string, error) {
	if actor == "" || len(key) < 8 || len(key) > 128 || strings.TrimSpace(key) != key {
		return "", errors.New("supply a stable actor and a request key of 8 to 128 bytes")
	}
	return "infra-" + digest(providerID+"\x00"+actor+"\x00"+action+"\x00"+key), nil
}
func ownership(r core.InfrastructureReview) map[string]string {
	return map[string]string{"dispatch.provider-registration": r.ProviderID, "dispatch.project": r.ProjectID, "dispatch.server": r.ServerID, "dispatch.request": r.ID}
}

func (m *Manager) ReviewCreate(ctx context.Context, in CreateInput) (core.InfrastructureReview, error) {
	var review core.InfrastructureReview
	data, err := m.lifecycle()
	if err != nil {
		return review, err
	}
	if err = m.authorize(ctx, in.ProjectID, in.ProviderID, "infrastructure.create"); err != nil {
		return review, err
	}
	if _, err = data.GetProject(ctx, in.ProjectID); err != nil {
		return review, err
	}
	if in.Name != strings.TrimSpace(in.Name) || len(in.Name) < 1 || len(in.Name) > 80 {
		return review, errors.New("server name must contain 1 to 80 bytes")
	}
	client, p, err := m.Adapter(ctx, in.ProviderID, provider.CapabilityCreate, "")
	if err != nil {
		return review, err
	}
	var manifest provider.Manifest
	if json.Unmarshal(p.Manifest, &manifest) != nil || !slices.Contains(manifest.Capabilities, provider.CapabilityOwnership) || !slices.Contains(p.Capabilities, provider.CapabilityInspect) {
		return review, errors.New("creation requires provider ownership labels and approved inspection")
	}
	config, _, err := m.resolveConfiguration(ctx, manifest.ConfigurationSchema, in.Config, in.SecretRefs)
	if err != nil {
		return review, err
	}
	if err = client.Validate(ctx, config); err != nil {
		return review, errors.New("provider rejected the configuration")
	}
	for kind, selected := range map[string]string{"regions": in.Region, "sizes": in.Size, "images": in.Image, "networks": in.Network} {
		options, e := client.Options(ctx, provider.OptionRequest{Kind: kind, Config: config})
		if e != nil {
			return review, errors.New("provider options are unavailable")
		}
		found := false
		for _, option := range options {
			if option.ID == selected {
				found = true
				break
			}
		}
		if !found {
			return review, errors.New("choose a currently available region, size, image and network")
		}
	}
	ssh, err := data.GetSecret(ctx, in.SSHKeySecretID)
	if err != nil || ssh.Type != core.SecretTypeSSHPrivateKey || strings.TrimSpace(ssh.PublicValue) == "" {
		return review, errors.New("choose an SSH key with a stored public key")
	}
	now := m.now()
	review = core.InfrastructureReview{ID: ulid.Make().String(), ServerID: ulid.Make().String(), ProjectID: in.ProjectID, ProviderID: p.ID, ProviderRevision: p.Revision, ManifestDigest: p.ManifestDigest, Name: in.Name, State: "open", CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	request := provider.CreateServerRequest{Name: in.Name, Region: in.Region, Size: in.Size, Image: in.Image, Network: in.Network, SSHKey: ssh.PublicValue, ProviderConfig: config, Labels: ownership(review)}

	if in.Bootstrap != nil {
		if m.Bootstrap == nil || in.Bootstrap.Method != "cloud_init" {
			return review, errors.New("cloud-init bootstrap is unavailable")
		}
		in.Bootstrap.TargetName = in.Name
		prepared, userData, e := m.Bootstrap.Prepare(ctx, bootstrap.Binding{ReviewID: review.ID, ServerID: review.ServerID, NodeID: "node-" + review.ServerID, ProviderID: review.ProviderID, ProjectID: review.ProjectID}, *in.Bootstrap, bootstrap.SSHCredentials{}, in.ActorID)
		if e != nil {
			return review, e
		}
		review.BootstrapID = prepared.ID
		in.Bootstrap = &prepared.Plan
		request.Bootstrap = userData
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return review, errors.New("configuration is invalid")
	}
	if len(raw) > 256<<10 {
		return review, errors.New("server configuration is too large")
	}
	review.EncryptedRequest, err = m.Vault.Encrypt("infrastructure-review:"+review.ID, raw)
	if err != nil {
		return review, err
	}
	review.Digest = digest(review.EncryptedRequest)
	review.Input, err = json.Marshal(in)
	if err != nil {
		return review, err
	}
	return review, data.CreateInfrastructureReview(ctx, review)
}

func (m *Manager) AcceptCreate(ctx context.Context, actor string, in Acceptance) (Accepted, error) {
	var result Accepted
	data, err := m.lifecycle()
	if err != nil {
		return result, err
	}
	r, err := data.GetInfrastructureReview(ctx, in.ReviewID)
	if err != nil {
		return result, err
	}
	if err = m.authorize(ctx, r.ProjectID, r.ProviderID, "infrastructure.create"); err != nil {
		return result, err
	}
	if in.ConfirmName != r.Name || in.Digest != r.Digest {
		return result, store.ErrInfrastructureChanged
	}
	var id string
	if claim, ok := core.MutationAcceptanceFromContext(ctx); ok {
		id = claim.OperationID
	} else {
		id, err = operationID(r.ProviderID, actor, in.RequestKey, "create")
		if err != nil {
			return result, err
		}
	}
	result.Server, result.Operation, err = data.AcceptInfrastructureReview(ctx, r.ID, r.Digest, id, actor, m.now(), m.Admission)
	if err == nil {
		core.RecordAcceptedOperation(ctx, result.Operation.ID)
	}
	return result, err
}

func (m *Manager) ChangeOperation(ctx context.Context, id, action string) error {
	data, err := m.lifecycle()
	if err != nil {
		return err
	}
	op, err := data.GetInfrastructureOperation(ctx, id)
	if err != nil {
		return err
	}
	server, err := data.GetManagedServer(ctx, op.ServerID)
	if err != nil {
		return err
	}
	if err = m.authorize(ctx, server.ProjectID, server.ProviderID, "infrastructure.modify"); err != nil {
		return err
	}
	switch action {
	case "retry":
		return data.RetryInfrastructureOperation(ctx, id, m.now())
	case "cancel":
		return data.CancelInfrastructureOperation(ctx, id, m.now())
	}
	return errors.New("unsupported operation action")
}
func (m *Manager) reviewedRequest(r core.InfrastructureReview) (provider.CreateServerRequest, error) {
	var request provider.CreateServerRequest
	raw, err := m.Vault.Decrypt("infrastructure-review:"+r.ID, r.EncryptedRequest)
	if err != nil {
		return request, err
	}
	if digest(r.EncryptedRequest) != r.Digest || json.Unmarshal(raw, &request) != nil {
		return request, errors.New("saved review is invalid")
	}
	return request, nil
}
func ownedResource(r core.InfrastructureReview, s provider.Server) error {
	if !provider.ValidID(s.ID) || s.Name != r.Name {
		return errors.New("provider resource identity does not match the review")
	}
	for key, value := range ownership(r) {
		if s.Labels[key] != value {
			return fmt.Errorf("provider resource lacks reviewed ownership")
		}
	}
	return nil
}
