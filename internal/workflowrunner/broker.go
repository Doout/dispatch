package workflowrunner

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

type Broker struct {
	Store     store.WorkflowWorkerStore
	Vault     *secretcrypto.Vault
	Authorize func(context.Context, Request) error
}

func (b *Broker) authorize(ctx context.Context, r Request) error {
	if b == nil || b.Store == nil || b.Vault == nil || b.Authorize == nil {
		return errors.New("remote workflow execution is not configured")
	}
	if err := r.Validate(); err != nil {
		return err
	}
	return b.Authorize(ctx, r)
}
func NodeMode(node core.PrivateNetwork) string { return node.Config["workflowMode"] }
func nodeAllows(node core.PrivateNetwork, r Request) bool {
	needsDocker := false
	if r.Workflow != nil {
		var job struct {
			Builder string `json:"builder"`
		}
		if json.Unmarshal(r.Workflow.Job, &job) != nil {
			return false
		}
		needsDocker = job.Builder == "docker"
	}
	if r.Deployment != nil && r.Deployment.Server.Runtime == core.ServerRuntimeDocker {
		needsDocker = true
	}
	if needsDocker && node.Details["workerDocker"] != "true" {
		return false
	}

	if d := r.Deployment; d != nil {
		if d.Server.Runtime == core.ServerRuntimeDocker && d.Server.AgentNodeID == "" {
			return false
		}
		if d.Server.AgentNodeID != "" && d.Server.AgentNodeID != node.ID {
			return false
		}
	}
	if p := r.StorageOperation; p != nil && p.Server.AgentNodeID != "" && p.Server.AgentNodeID != node.ID {
		return false
	}
	if p := r.ServiceProvision; p != nil && p.Server.AgentNodeID != "" && p.Server.AgentNodeID != node.ID {
		return false
	}
	return node.Driver == "dispatch_agent" && NodeMode(node) == r.Mode && (node.Config["workflowProjectId"] == "" || node.Config["workflowProjectId"] == r.ProjectID)
}
func operationID(r Request) string {
	if p := r.StorageOperation; p != nil && p.Resource != nil {
		digest := sha256.Sum256([]byte(r.Kind() + "\x00" + r.ProjectID + "\x00" + p.Resource.ID + "\x00" + r.RevisionID))
		return "worker-" + hex.EncodeToString(digest[:])
	}
	if r.Deployment != nil || r.ServiceProvision != nil || r.Workflow != nil && r.Workflow.ServiceRunID != "" {
		revision := r.RevisionID
		if p := r.ServiceProvision; p != nil && p.Action() == "provision" && p.Request.OperationID != "" {
			revision = p.Request.OperationID
		}
		if r.Workflow != nil && r.Workflow.ServiceRunID != "" {
			revision = r.Workflow.ServiceRunID
		}
		digest := sha256.Sum256([]byte(r.Kind() + "\x00" + r.ProjectID + "\x00" + r.ResourceID + "\x00" + revision))
		return "worker-" + hex.EncodeToString(digest[:])
	}
	return ulid.Make().String()
}
func (b *Broker) Run(ctx context.Context, r Request, progress func(string)) (Result, error) {
	if err := b.authorize(ctx, r); err != nil {
		return Result{}, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return Result{}, err
	}
	defer clear(raw)
	if len(raw) > MaxPayload {
		return Result{}, errors.New("workflow worker request exceeds the payload limit")
	}
	digest := sha256.Sum256(raw)
	encodedDigest := hex.EncodeToString(digest[:])
	id := operationID(r)
	if existing, err := b.Store.GetWorkflowWorkerJob(ctx, id); err == nil {
		if existing.Digest != encodedDigest {
			return Result{}, errors.New("accepted worker operation already exists with different inputs")
		}
		return b.wait(ctx, r, existing, progress)
	} else if !errors.Is(err, store.ErrNotFound) {
		return Result{}, err
	}
	nodes, err := b.Store.ListPrivateNetworks(ctx)
	if err != nil {
		return Result{}, err
	}
	available := []core.PrivateNetwork{}
	for _, node := range nodes {
		checked, e := time.Parse(time.RFC3339Nano, node.Details["workerCheckedAt"])
		if nodeAllows(node, r) && node.Details["workerVersion"] == Version && e == nil && checked.After(time.Now().Add(-90*time.Second)) && checked.Before(time.Now().Add(10*time.Second)) {
			available = append(available, node)
		}
	}
	if len(available) == 0 {
		return Result{}, errors.New("no enrolled workflow worker is available for this project and execution mode")
	}
	selection := sha256.Sum256([]byte(id))
	node := available[binary.BigEndian.Uint64(selection[:8])%uint64(len(available))]
	credential, err := b.Store.GetEdgeCredential(ctx, node.ID)
	if err != nil || credential.Revoked || credential.PublicKey == "" {
		return Result{}, errors.New("workflow worker enrollment is unavailable")
	}
	encoded, err := b.Vault.Encrypt("workflow-worker:"+id+":request", raw)
	if err != nil {
		return Result{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(MaxDuration)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(expires) {
		expires = deadline
	}
	j := core.WorkflowWorkerJob{ID: id, NodeID: node.ID, Generation: credential.Generation, ProjectID: r.ProjectID, ResourceID: r.ResourceID, RevisionID: r.RevisionID, Kind: r.Kind(), Mode: r.Mode, Digest: encodedDigest, Request: encoded, ExpiresAt: expires, CreatedAt: now}
	if err = b.Store.CreateWorkflowWorkerJob(ctx, j); err != nil {
		existing, lookup := b.Store.GetWorkflowWorkerJob(ctx, id)
		if lookup != nil || existing.Digest != encodedDigest {
			return Result{}, err
		}
		j = existing
	}
	return b.wait(ctx, r, j, progress)
}
func (b *Broker) wait(ctx context.Context, r Request, j core.WorkflowWorkerJob, progress func(string)) (Result, error) {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	last := ""
	lastLog := ""
	for {
		current, e := b.Store.GetWorkflowWorkerJob(ctx, j.ID)
		if e != nil {
			return Result{}, e
		}
		if current.Progress != "" && current.Progress != last {
			log, e := b.Vault.Decrypt("workflow-worker:"+j.ID+":progress", current.Progress)
			if e != nil {
				return Result{}, e
			}
			display := r.Redact(string(log))
			if progress != nil && display != "" && display != lastLog {
				progress(display)
			}
			lastLog = display
			clear(log)
			last = current.Progress
		}
		if current.State != "pending" && current.State != "running" {
			if current.Result == "" {
				return Result{}, fmt.Errorf("worker operation %s", current.State)
			}
			result, e := b.Vault.Decrypt("workflow-worker:"+j.ID+":result", current.Result)
			if e != nil {
				return Result{}, e
			}
			defer clear(result)
			var out Result
			if e = json.Unmarshal(result, &out); e != nil {
				return Result{}, e
			}
			if progress != nil {
				progress(out.Log)
			}
			if out.State != "succeeded" {
				return out, errors.New(out.Error)
			}
			return out, nil
		}
		if !current.ExpiresAt.After(time.Now()) {
			_ = b.Store.CancelWorkflowWorkerJob(context.Background(), j.ID, time.Now().UTC())
			return Result{}, errors.New("workflow worker deadline exceeded")
		}
		select {
		case <-ctx.Done():
			_ = b.Store.CancelWorkflowWorkerJob(context.Background(), j.ID, time.Now().UTC())
			return Result{}, ctx.Err()
		case <-ticker.C:
		}
	}
}
func (b *Broker) request(ctx context.Context, j core.WorkflowWorkerJob) (Request, error) {
	var r Request
	raw, err := b.Vault.Decrypt("workflow-worker:"+j.ID+":request", j.Request)
	if err != nil {
		return r, err
	}
	defer clear(raw)
	if err = json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	digest := sha256.Sum256(raw)
	if r.ProjectID != j.ProjectID || r.ResourceID != j.ResourceID || r.RevisionID != j.RevisionID || r.Mode != j.Mode || r.Kind() != j.Kind || hex.EncodeToString(digest[:]) != j.Digest {
		return r, store.ErrWorkerLease
	}
	node, err := b.Store.GetPrivateNetwork(ctx, j.NodeID)
	if err != nil || !nodeAllows(node, r) {
		return r, store.ErrWorkerLease
	}
	credential, err := b.Store.GetEdgeCredential(ctx, j.NodeID)
	if err != nil || credential.Revoked || credential.Generation != j.Generation || credential.PublicKey == "" {
		return r, store.ErrWorkerLease
	}
	return r, b.authorize(ctx, r)
}
func (b *Broker) Lease(ctx context.Context, node string, generation int64, mode string) (*LeasedJob, error) {
	j, err := b.Store.LeaseWorkflowWorkerJob(ctx, node, generation, mode, time.Now().UTC())
	if err != nil || j == nil {
		return nil, err
	}
	r, err := b.request(ctx, *j)
	if err != nil {
		state := "cancelled"
		if j.Attempt > 1 {
			state = "unknown"
		}
		raw, _ := json.Marshal(Result{State: state, Error: "Worker authorization changed before dispatch."})
		encrypted, encryptionErr := b.Vault.Encrypt("workflow-worker:"+j.ID+":result", raw)
		if encryptionErr == nil {
			_ = b.Store.CompleteWorkflowWorkerJob(ctx, j.NodeID, j.ID, j.LeaseToken, state, encrypted, time.Now().UTC())
		}
		return nil, err
	}
	return &LeasedJob{ID: j.ID, Digest: j.Digest, Attempt: j.Attempt, LeaseToken: j.LeaseToken, ExpiresAt: j.ExpiresAt, Request: r}, nil
}
func (b *Broker) Renew(ctx context.Context, node, id string, p Progress) (bool, error) {
	j, err := b.Store.GetWorkflowWorkerJob(ctx, id)
	if err != nil || j.NodeID != node {
		return false, store.ErrWorkerLease
	}
	r, err := b.request(ctx, j)
	if err != nil {
		return false, err
	}
	if len(p.Log) > MaxLog {
		return false, errors.New("worker output exceeds the log limit")
	}
	log, err := b.Vault.Encrypt("workflow-worker:"+id+":progress", []byte(r.Redact(p.Log)))
	if err != nil {
		return false, err
	}
	return b.Store.RenewWorkflowWorkerJob(ctx, node, id, p.LeaseToken, log, time.Now().UTC())
}
func (b *Broker) Complete(ctx context.Context, node, id string, c Completion) error {
	j, err := b.Store.GetWorkflowWorkerJob(ctx, id)
	if err != nil || j.NodeID != node {
		return store.ErrWorkerLease
	}
	r, err := b.request(ctx, j)
	if err != nil {
		return err
	}
	c.Result.Log, c.Result.Error = r.Redact(c.Result.Log), r.Redact(c.Result.Error)
	raw, err := json.Marshal(c.Result)
	if err != nil {
		return err
	}
	defer clear(raw)
	if len(raw) > MaxPayload {
		return errors.New("worker result exceeds the payload limit")
	}
	result, err := b.Vault.Encrypt("workflow-worker:"+id+":result", raw)
	if err != nil {
		return err
	}
	return b.Store.CompleteWorkflowWorkerJob(ctx, node, id, c.LeaseToken, c.Result.State, result, time.Now().UTC())
}
