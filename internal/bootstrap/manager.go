// Package bootstrap installs the reviewed agent artifact and hands all routine
// operations to its enrolled, key-bound runtime. It has no arbitrary command API.
package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/ssh"
)

var ErrConflict = errors.New("bootstrap review or target identity changed; review it again")
var ErrWaiting = errors.New("the reviewed provider resource is not allocated yet")
var ErrCredential = errors.New("bootstrap claim is invalid, expired or unavailable")
var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

const MaxArtifactBytes = 128 << 20

type Store interface {
	store.TargetBootstrapStore
	edge.CredentialStore
	GetPrivateNetwork(context.Context, string) (core.PrivateNetwork, error)
	CreatePrivateNetwork(context.Context, core.PrivateNetwork) error
	UpdatePrivateNetwork(context.Context, core.PrivateNetwork) error
	GetServer(context.Context, string) (core.Server, error)
	CreateServer(context.Context, core.Server) error
	UpdateServer(context.Context, core.Server) error
}

// Binding must come from the accepted provider lifecycle or existing target
// inventory. A claim cannot supply or override this authoritative binding.
type Binding struct {
	ReviewID, ServerID, NodeID, ProviderID, ProjectID, ResourceID, Address string
	Accepted, Cancelled                                                    bool
}
type SSHCredentials struct {
	Password           string `json:"password,omitempty"`
	PrivateKey         string `json:"privateKey,omitempty"`
	PrivateKeyPassword string `json:"privateKeyPassword,omitempty"`
}
type privateInputs struct {
	ClaimToken      string         `json:"claimToken"`
	EnrollmentToken string         `json:"enrollmentToken,omitempty"`
	SSH             SSHCredentials `json:"ssh,omitempty"`
}
type Claim struct {
	State     string     `json:"state"`
	NodeID    string     `json:"nodeId,omitempty"`
	Token     string     `json:"token,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}
type Manager struct {
	Store                       Store
	Vault                       *secretcrypto.Vault
	ControllerURL, ArtifactRoot string
	ResolveTarget               func(context.Context, string) (Binding, error)
	Now                         func() time.Time
	SSHInstall                  func(context.Context, core.TargetBootstrap, SSHCredentials, string) error
	mu                          sync.Mutex
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}
func token() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func scope(id string) string { return "target-bootstrap:" + id }
func (m *Manager) inputs(item core.TargetBootstrap) (privateInputs, error) {
	var input privateInputs
	raw, err := m.Vault.Decrypt(scope(item.ID), item.EncryptedInput)
	if err != nil {
		return input, err
	}
	defer clear(raw)
	err = json.Unmarshal(raw, &input)
	return input, err
}
func (m *Manager) seal(item *core.TargetBootstrap, input privateInputs) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	defer clear(raw)
	item.EncryptedInput, err = m.Vault.Encrypt(scope(item.ID), raw)
	return err
}
func (m *Manager) save(ctx context.Context, item *core.TargetBootstrap) error {
	item.UpdatedAt = m.now()
	err := m.Store.UpdateTargetBootstrap(ctx, *item, item.Revision)
	if err == nil {
		item.Revision++
	}
	return err
}

func (m *Manager) Artifact(platform string) (string, []byte, error) {
	if platform != "linux-amd64" && platform != "linux-arm64" {
		return "", nil, errors.New("choose linux-amd64 or linux-arm64")
	}
	path := filepath.Join(m.ArtifactRoot, platform)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > MaxArtifactBytes {
		return "", nil, errors.New("the pinned agent artifact is unavailable on this controller")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > MaxArtifactBytes {
		return "", nil, errors.New("cannot read the agent artifact")
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), raw, nil
}
func (m *Manager) normalize(plan core.TargetBootstrapPlan) (core.TargetBootstrapPlan, error) {
	origin, err := url.Parse(strings.TrimRight(m.ControllerURL, "/"))
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" {
		return plan, errors.New("bootstrap requires the controller's configured public HTTPS origin")
	}
	plan.ControllerURL = origin.String()
	if len(plan.TargetName) > 100 || strings.ContainsAny(plan.TargetName, "\r\n\x00") {
		return plan, errors.New("target name is invalid")
	}
	digest, _, err := m.Artifact(plan.Platform)
	if err != nil {
		return plan, err
	}
	plan.ArtifactSHA256 = digest
	plan.ArtifactURL = origin.String() + "/edge/artifacts/" + digest + "/" + plan.Platform
	if plan.InstallRuntime && plan.ImageFamily != "ubuntu-24.04" {
		return plan, errors.New("automatic runtime prerequisites currently require Ubuntu 24.04")
	}
	if !plan.InstallRuntime && plan.ImageFamily != "existing-systemd" && plan.ImageFamily != "ubuntu-24.04" {
		return plan, errors.New("choose Ubuntu 24.04 or an existing systemd host with runtime prerequisites")
	}
	if plan.Method != "cloud_init" && plan.Method != "ssh" {
		return plan, errors.New("bootstrap method must be cloud_init or ssh")
	}
	plan.Actions = []string{"Verify the downloaded agent SHA-256 before installation", "Preserve the target's persistent identity and runtime receipts", "Install one dispatch-edge systemd service", "Verify Docker daemon, Compose v2 and Git", "Enroll over HTTPS and wait for authenticated runtime readiness"}
	if plan.InstallRuntime {
		plan.Actions = append([]string{"Install Ubuntu Docker, Compose v2, Git, curl and CA certificates"}, plan.Actions...)
	}
	if plan.ReplaceIdentity {
		plan.Actions = append([]string{"Retire the reviewed identity generation and replace its private key"}, plan.Actions...)
	}
	if plan.Method == "ssh" {
		if plan.SSHHost == "" || strings.ContainsAny(plan.SSHHost, " /?#@\r\n") || !identifier.MatchString(plan.SSHUser) {
			return plan, errors.New("enter a valid SSH host and user")
		}
		if plan.SSHPort == 0 {
			plan.SSHPort = 22
		}
		if plan.SSHPort < 1 || plan.SSHPort > 65535 {
			return plan, errors.New("invalid SSH port")
		}
		if !plan.SSHVerified || len(plan.SSHHostKey) > 8192 {
			return plan, errors.New("verify the SSH host key through a trusted channel before reviewing installation")
		}
		key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(plan.SSHHostKey))
		if err != nil || len(strings.TrimSpace(string(rest))) != 0 {
			return plan, errors.New("enter one verified SSH host public key")
		}
		plan.SSHHostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		plan.SSHFingerprint = ssh.FingerprintSHA256(key)
		if plan.SSHUser != "root" {
			plan.Actions = append([]string{"Run the approved installer through passwordless sudo"}, plan.Actions...)
		}
	} else if plan.ReplaceIdentity {
		return plan, errors.New("identity replacement requires verified SSH recovery")
	}
	return plan, nil
}

// Prepare creates an inert, expiring review. Its rendered claim is safe to save
// only in the encrypted provider request; no API response should return it.
func (m *Manager) Prepare(ctx context.Context, b Binding, plan core.TargetBootstrapPlan, credentials SSHCredentials, actor string) (core.TargetBootstrap, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var item core.TargetBootstrap
	if m.Vault == nil || m.Store == nil {
		return item, "", errors.New("encrypted bootstrap storage is unavailable")
	}
	if !identifier.MatchString(b.ServerID) || !identifier.MatchString(b.NodeID) || actor == "" {
		return item, "", errors.New("bootstrap requires an intended target, node and reviewing actor")
	}
	if plan.TargetName == "" {
		plan.TargetName = b.ServerID
	}
	var err error
	plan, err = m.normalize(plan)
	if err != nil {
		return item, "", err
	}
	if plan.Method == "ssh" {
		if _, err = sshAuth(credentials); err != nil {
			return item, "", err
		}
	}
	now := m.now()
	item = core.TargetBootstrap{ID: ulid.Make().String(), ResourceID: b.ResourceID, ReviewID: b.ReviewID, ServerID: b.ServerID, NodeID: b.NodeID, ProviderID: b.ProviderID, ProjectID: b.ProjectID, Plan: plan, ActorID: actor, State: "planned", InstallationState: "pending", EnrollmentState: "pending", RuntimeState: "pending", Revision: 1, CreatedAt: now, UpdatedAt: now, ReviewExpiresAt: now.Add(15 * time.Minute), ClaimExpiresAt: now.Add(24 * time.Hour), Message: "Review the target, pinned artifact and installation actions before approval."}
	credential, err := m.Store.GetEdgeCredential(ctx, item.NodeID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return item, "", err
	}
	item.ExpectedGeneration = credential.Generation
	if credential.PublicKey != "" && !plan.ReplaceIdentity && plan.Method == "cloud_init" {
		return item, "", errors.New("provider bootstrap cannot replace an existing enrolled identity")
	}
	approved, _ := json.Marshal(struct {
		Binding    Binding
		Plan       core.TargetBootstrapPlan
		Generation int64
	}{b, plan, item.ExpectedGeneration})
	digest := sha256.Sum256(approved)
	item.Digest = "sha256:" + hex.EncodeToString(digest[:])
	claim, err := token()
	if err != nil {
		return item, "", err
	}
	item.ClaimHash = edge.TokenHash(claim)
	input := privateInputs{ClaimToken: claim, SSH: credentials}
	if err = m.seal(&item, input); err != nil {
		return item, "", err
	}
	if err = m.Store.CreateTargetBootstrap(ctx, item); err != nil {
		return item, "", err
	}
	script, err := renderCloudInit(item, input.ClaimToken)
	return item, script, err
}
func (m *Manager) Accept(ctx context.Context, id, digest string) (core.TargetBootstrap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.Store.GetTargetBootstrap(ctx, id)
	if err != nil {
		return item, err
	}
	if item.Digest != digest || item.State == "cancelled" {
		return item, ErrConflict
	}
	if item.AcceptedAt != nil {
		return item, nil
	}
	if item.State != "planned" || !item.ReviewExpiresAt.After(m.now()) {
		return item, ErrConflict
	}
	if item.ProviderID != "" && item.Plan.Method == "ssh" {
		if _, err = m.binding(ctx, item); err != nil {
			return item, err
		}
	}
	if item.ProviderID == "" {
		if err = m.bindImportedTarget(ctx, item); err != nil {
			return item, err
		}
	}
	now := m.now()
	item.AcceptedAt = &now
	item.State = "accepted"
	item.Message = "Installation approved; waiting for the intended target."
	item.UpdatedAt = now
	err = m.Store.AcceptTargetBootstrap(ctx, item, item.Revision)
	if err == nil {
		item.Revision++
	}
	return item, err
}
func (m *Manager) binding(ctx context.Context, item core.TargetBootstrap) (Binding, error) {
	if m.ResolveTarget == nil {
		return Binding{}, errors.New("target ownership resolver is unavailable")
	}
	binding, err := m.ResolveTarget(ctx, item.ServerID)
	if err != nil {
		return binding, err
	}
	if binding.ServerID != item.ServerID || binding.NodeID != item.NodeID || binding.ProviderID != item.ProviderID || binding.ProjectID != item.ProjectID || binding.ReviewID != item.ReviewID || item.ResourceID != "" && binding.ResourceID != item.ResourceID || item.Plan.Method == "ssh" && binding.Address != "" && binding.Address != item.Plan.SSHHost || binding.Cancelled {
		return binding, ErrConflict
	}
	if !binding.Accepted || binding.ResourceID == "" {
		return binding, ErrWaiting
	}
	return binding, nil
}
func (m *Manager) ensureNode(ctx context.Context, item core.TargetBootstrap) error {
	node, err := m.Store.GetPrivateNetwork(ctx, item.NodeID)
	if err == nil {
		if node.Driver != edge.DriverAgent {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	now := m.now()
	return m.Store.CreatePrivateNetwork(ctx, core.PrivateNetwork{ID: item.NodeID, Name: "Target " + item.ServerID, Driver: edge.DriverAgent, State: "waiting", Config: map[string]string{}, Details: map[string]string{"bootstrapServerId": item.ServerID}, CreatedAt: now, UpdatedAt: now})
}

// Claim issues an enrollment token only for an approved, allocated target. It
// returns the same encrypted token after a lost response; expiry refreshes only
// the intended unconsumed generation. Upgrades never rotate a valid identity.
func (m *Manager) Claim(ctx context.Context, id, claimToken string) (Claim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var response Claim
	item, err := m.Store.GetTargetBootstrap(ctx, id)
	if err != nil || len(claimToken) > 128 || subtle.ConstantTimeCompare([]byte(edge.TokenHash(claimToken)), []byte(item.ClaimHash)) != 1 || !item.ClaimExpiresAt.After(m.now()) {
		return response, ErrCredential
	}
	if item.AcceptedAt == nil || item.State == "failed" || item.State == "cancelled" {
		return response, ErrCredential
	}
	binding, err := m.binding(ctx, item)
	if err != nil {
		return response, err
	}
	item.ResourceID = binding.ResourceID
	if err = m.ensureNode(ctx, item); err != nil {
		return response, err
	}
	credential, err := m.Store.GetEdgeCredential(ctx, item.NodeID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return response, err
	}
	expected := item.ExpectedGeneration
	if item.Generation > 0 {
		expected = item.Generation
	}
	if credential.Generation != expected {
		return response, ErrConflict
	}
	if credential.PublicKey != "" && !credential.Revoked && (!item.Plan.ReplaceIdentity || item.Generation > 0) {
		if item.State == "ready" {
			return Claim{State: "enrolled", NodeID: item.NodeID}, nil
		}
		item.EnrollmentState = "enrolled"
		item.State = "waiting"
		item.Message = "Agent identity enrolled; waiting for verified runtime capabilities."
		if err = m.save(ctx, &item); err != nil {
			return response, err
		}
		return Claim{State: "enrolled", NodeID: item.NodeID}, nil
	}
	input, err := m.inputs(item)
	if err != nil {
		return response, err
	}
	if input.EnrollmentToken != "" && credential.EnrollmentHash == edge.TokenHash(input.EnrollmentToken) && credential.EnrollmentExpiresAt.After(m.now()) && !credential.Revoked {
		expires := credential.EnrollmentExpiresAt
		return Claim{State: "enroll", NodeID: item.NodeID, Token: input.EnrollmentToken, ExpiresAt: &expires}, nil
	}
	input.EnrollmentToken, err = token()
	if err != nil {
		return response, err
	}
	now := m.now()
	expires := now.Add(edge.EnrollmentLifetime)
	item.Generation = expected + 1
	item.State = "waiting"
	item.EnrollmentState = "waiting"
	item.UpdatedAt = now
	item.Message = "Fresh enrollment issued for the intended target; waiting for its persistent identity."
	if err = m.seal(&item, input); err != nil {
		return response, err
	}
	next := core.EdgeCredential{NetworkID: item.NodeID, EnrollmentHash: edge.TokenHash(input.EnrollmentToken), EnrollmentExpiresAt: expires, UpdatedAt: now}
	if err = m.Store.IssueBootstrapEnrollment(ctx, item, item.Revision, next, expected); err != nil {
		return response, err
	}
	return Claim{State: "enroll", NodeID: item.NodeID, Token: input.EnrollmentToken, ExpiresAt: &expires}, nil
}
func (m *Manager) Refresh(ctx context.Context, id string) (core.TargetBootstrap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.Store.GetTargetBootstrap(ctx, id)
	if err != nil {
		return item, err
	}
	before := item
	finish := func() (core.TargetBootstrap, error) {
		if item.State == before.State && item.InstallationState == before.InstallationState && item.EnrollmentState == before.EnrollmentState && item.RuntimeState == before.RuntimeState && item.Message == before.Message && item.EncryptedInput == before.EncryptedInput && item.ClaimHash == before.ClaimHash {
			return item, nil
		}
		err := m.save(ctx, &item)
		return item, err
	}
	if item.AcceptedAt == nil && !item.ReviewExpiresAt.After(m.now()) && item.EncryptedInput != "" {
		item.State = "failed"
		item.ClaimHash = ""
		item.EncryptedInput = ""
		item.Message = "The installation review expired before approval. Create a new review."
		return finish()
	}
	if item.AcceptedAt == nil || item.State == "failed" || item.State == "cancelled" {
		return item, nil
	}
	if item.State != "ready" && !item.ClaimExpiresAt.After(m.now()) {
		item.State = "failed"
		item.ClaimHash = ""
		item.EncryptedInput = ""
		item.Message = "The installation claim expired. Create a new verified SSH review for this same target."
		return finish()
	}
	if _, err = m.binding(ctx, item); err != nil {
		if errors.Is(err, ErrWaiting) {
			return item, nil
		}
		return item, err
	}
	credential, err := m.Store.GetEdgeCredential(ctx, item.NodeID)
	if errors.Is(err, store.ErrNotFound) {
		if m.now().After(item.AcceptedAt.Add(30*time.Minute)) && item.State != "unknown" {
			item.State = "unknown"
			item.InstallationState = "interrupted"
			item.Message = "No enrollment arrived after the installation window. Inspect cloud-init or retry through verified SSH on this same machine."
			return finish()
		}
		return item, nil
	}
	if err != nil {
		return item, err
	}
	generation := item.Generation
	if generation == 0 {
		generation = item.ExpectedGeneration
	}
	if credential.Generation != generation || credential.Revoked {
		return item, ErrConflict
	}
	if credential.PublicKey == "" {
		if !credential.EnrollmentExpiresAt.After(m.now()) {
			item.EnrollmentState = "expired"
			item.Message = "Enrollment expired; retry the approved installer to obtain a fresh token for this target."
		}
		return finish()
	}
	if item.Plan.ReplaceIdentity && item.Generation <= item.ExpectedGeneration {
		return item, nil
	}
	item.EnrollmentState = "enrolled"
	node, err := m.Store.GetPrivateNetwork(ctx, item.NodeID)
	if err != nil {
		return item, err
	}
	checked, _ := time.Parse(time.RFC3339Nano, node.Details["runtimeCheckedAt"])
	if (item.Plan.Method != "ssh" || item.InstallationState == "installed") && node.Details["agentArtifactSHA256"] == item.Plan.ArtifactSHA256 && !checked.After(m.now().Add(10*time.Second)) && node.Details["runtimeVersion"] == remoteruntime.APIVersion && strings.Contains(","+node.Details["runtimeCapabilities"]+",", ",deploy,") && checked.After(m.now().Add(-2*time.Minute)) && checked.After(*item.AcceptedAt) {
		item.InstallationState = "installed"
		item.RuntimeState = "ready"
		item.State = "ready"
		item.ClaimHash = ""
		item.EncryptedInput = ""
		item.Message = "The intended agent identity and Docker runtime are ready."
		if item.ProviderID == "" {
			server, e := m.Store.GetServer(ctx, item.ServerID)
			if e != nil || server.AgentNodeID != item.NodeID {
				return item, ErrConflict
			}
			server.State = "ready"
			if e = m.Store.UpdateServer(ctx, server); e != nil {
				return item, e
			}
		}
	} else {
		item.RuntimeState = "pending"
		if before.State != "ready" {
			item.State = "waiting"
		}
		item.Message = "Agent enrolled; waiting for a fresh Docker, Compose and Git readiness check."
	}
	return finish()
}
func sshAddress(plan core.TargetBootstrapPlan) string {
	return net.JoinHostPort(strings.Trim(plan.SSHHost, "[]"), fmt.Sprint(plan.SSHPort))
}

func (m *Manager) bindImportedTarget(ctx context.Context, item core.TargetBootstrap) error {
	if item.Plan.Method != "ssh" {
		return ErrConflict
	}
	server, err := m.Store.GetServer(ctx, item.ServerID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if errors.Is(err, store.ErrNotFound) {
		if err = m.ensureNode(ctx, item); err != nil {
			return err
		}
		name := item.Plan.TargetName
		if name == "" {
			name = item.ServerID
		}
		return m.Store.CreateServer(ctx, core.Server{ID: item.ServerID, ProjectID: item.ProjectID, Name: name, Address: item.Plan.SSHHost, Runtime: core.ServerRuntimeDocker, State: "waiting", AgentMode: "outbound-runtime", AgentNodeID: item.NodeID, CreatedAt: m.now()})
	}
	if server.Runtime != core.ServerRuntimeDocker || server.Address == "local" || server.Address != item.Plan.SSHHost || server.ProjectID != item.ProjectID || server.AgentNodeID != "" && server.AgentNodeID != item.NodeID {
		return ErrConflict
	}
	if err = m.ensureNode(ctx, item); err != nil {
		return err
	}
	server.AgentNodeID = item.NodeID
	server.AgentMode = "outbound-runtime"
	if item.Plan.ReplaceIdentity || server.State != "ready" {
		server.State = "waiting"
	}
	return m.Store.UpdateServer(ctx, server)
}

// Progress accepts only fixed non-secret phases from the scoped installer.
func (m *Manager) Progress(ctx context.Context, id, claimToken, phase string) (core.TargetBootstrap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.Store.GetTargetBootstrap(ctx, id)
	if err != nil || len(claimToken) > 128 || subtle.ConstantTimeCompare([]byte(edge.TokenHash(claimToken)), []byte(item.ClaimHash)) != 1 || !item.ClaimExpiresAt.After(m.now()) {
		return item, ErrCredential
	}
	if item.AcceptedAt == nil || item.State == "cancelled" {
		return item, ErrCredential
	}
	if _, err = m.binding(ctx, item); err != nil {
		return item, err
	}
	if item.State == "ready" {
		return item, nil
	}
	switch phase {
	case "downloading", "installing":
		item.InstallationState = phase
		item.State = "installing"
		item.Message = "The approved installer is verifying prerequisites and the pinned agent artifact."
	case "installed":
		item.InstallationState = "installed"
		item.State = "waiting"
		item.Message = "Agent installed; waiting for authenticated enrollment and runtime readiness."
	case "failed":
		item.InstallationState = "interrupted"
		item.State = "unknown"
		item.Message = "Installation was interrupted. Retry the approved installer on the same intended target."
	default:
		return item, errors.New("unsupported bootstrap progress phase")
	}
	return item, m.save(ctx, &item)
}
