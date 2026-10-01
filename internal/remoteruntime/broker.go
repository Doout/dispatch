package remoteruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/store"
)

type Broker struct {
	Store store.RuntimeJobStore
	Vault *secretcrypto.Vault
}

func (b *Broker) Submit(ctx context.Context, id string, r Request) (core.RuntimeJob, error) {
	if b == nil || b.Store == nil || b.Vault == nil {
		return core.RuntimeJob{}, errors.New("encrypted remote execution is not configured")
	}
	if !identityPattern.MatchString(id) {
		return core.RuntimeJob{}, errors.New("invalid runtime operation identity")
	}
	if err := r.Validate(); err != nil {
		return core.RuntimeJob{}, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return core.RuntimeJob{}, err
	}
	defer clear(raw)
	if len(raw) > MaxPayload {
		return core.RuntimeJob{}, errors.New("runtime request exceeds the payload limit")
	}
	digest := sha256.Sum256(raw)
	encrypted, err := b.Vault.Encrypt("runtime-job:"+id+":request", raw)
	if err != nil {
		return core.RuntimeJob{}, err
	}
	credential, err := b.Store.GetEdgeCredential(ctx, r.Server.AgentNodeID)
	if err != nil || credential.Revoked || credential.PublicKey == "" {
		return core.RuntimeJob{}, errors.New("runtime target requires a current enrolled identity")
	}
	now := time.Now().UTC()
	j := core.RuntimeJob{ID: id, ServerID: r.Server.ID, NodeID: r.Server.AgentNodeID, NodeGeneration: credential.Generation, ProjectID: r.Application.ProjectID, AppID: r.Application.ID, DeploymentID: r.Deployment.ID, Operation: string(r.Operation), RequestDigest: hex.EncodeToString(digest[:]), EncryptedRequest: encrypted, CreatedAt: now, ExpiresAt: now.Add(MaxDuration)}
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(j.ExpiresAt) {
		j.ExpiresAt = deadline
	}
	if r.Service != nil {
		j.ServiceRunID = r.Service.Request.Run.ID
		if err := b.validateServiceOwner(ctx, j, r); err != nil {
			return core.RuntimeJob{}, err
		}
	}
	if err = b.validateStorageOwner(ctx, j, r); err != nil {
		return core.RuntimeJob{}, err
	}
	if err = b.Store.CreateRuntimeJob(ctx, j); err != nil {
		return core.RuntimeJob{}, err
	}
	return b.Store.GetRuntimeJob(ctx, id)
}

func (b *Broker) request(j core.RuntimeJob) (Request, error) {
	var r Request
	raw, err := b.Vault.Decrypt("runtime-job:"+j.ID+":request", j.EncryptedRequest)
	if err != nil {
		return r, err
	}
	defer clear(raw)
	if len(raw) > MaxPayload {
		return r, errors.New("runtime request exceeds the payload limit")
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	if err = r.Validate(); err != nil {
		return r, err
	}
	if r.Server.ID != j.ServerID || r.Server.AgentNodeID != j.NodeID || r.Application.ID != j.AppID || r.Application.ProjectID != j.ProjectID || string(r.Operation) != j.Operation {
		return r, errors.New("runtime request identity changed")
	}
	return r, nil
}

func (b *Broker) Lease(ctx context.Context, node string) (*LeasedJob, error) {
	j, err := b.Store.LeaseRuntimeJob(ctx, node, time.Now().UTC(), LeaseDuration)
	if err != nil || j == nil {
		return nil, err
	}
	r, err := b.request(*j)
	if err == nil {
		err = b.validateServiceOwner(ctx, *j, r)
		if err == nil {
			err = b.validateStorageOwner(ctx, *j, r)
		}
	}
	if err != nil {
		return nil, err
	}
	return &LeasedJob{ID: j.ID, Attempt: j.Attempt, Digest: j.RequestDigest, LeaseToken: j.LeaseToken, ExpiresAt: j.ExpiresAt, CancelRequested: j.CancelRequested, Request: r}, nil
}

func (b *Broker) Renew(ctx context.Context, node, id string, h Heartbeat) (bool, error) {
	j, err := b.Store.GetRuntimeJob(ctx, id)
	if err != nil {
		return false, err
	}
	if j.NodeID != node {
		return false, store.ErrNotFound
	}
	r, err := b.request(j)
	if err == nil {
		err = b.validateServiceOwner(ctx, j, r)
		if err == nil {
			err = b.validateStorageOwner(ctx, j, r)
		}
	}
	if err != nil {
		return false, err
	}
	if len(h.Message) > 4096 {
		return false, errors.New("runtime progress exceeds the limit")
	}
	switch core.DeploymentState(h.Phase) {
	case "", core.DeploymentFetching, core.DeploymentBuilding, core.DeploymentStarting, core.DeploymentChecking, core.DeploymentRouting:
	default:
		return false, errors.New("invalid runtime progress phase")
	}
	return b.Store.RenewRuntimeJob(ctx, node, id, h.LeaseToken, h.Phase, r.Redact(h.Message), time.Now().UTC(), LeaseDuration)
}

func (b *Broker) Complete(ctx context.Context, node, id string, c Completion) error {
	j, err := b.Store.GetRuntimeJob(ctx, id)
	if err != nil {
		return err
	}
	if j.NodeID != node {
		return store.ErrNotFound
	}
	r, err := b.request(j)
	if err == nil {
		err = b.validateServiceOwner(ctx, j, r)
		if err == nil {
			err = b.validateStorageOwner(ctx, j, r)
		}
	}
	if err != nil {
		return err
	}
	if err := r.ValidateRoute(c.Result.Route); err != nil {
		return err
	}
	if err := r.ValidateHealth(c.Result.Health); err != nil {
		return err
	}
	if c.Result.Health != nil {
		for index := range c.Result.Health.Checks {
			c.Result.Health.Checks[index].Message = r.Redact(c.Result.Health.Checks[index].Message)
		}
	}
	if (r.Operation == runtimecontract.Deploy || r.Operation == runtimecontract.Rollback) && r.Deployment.Health.State != "" && c.Result.State == "succeeded" && (c.Result.Health == nil || c.Result.Health.State != "passed") {
		return errors.New("successful remote deployment requires persisted passing health evidence")
	}
	if err := r.ValidateServiceResult(c.Result); err != nil {
		return err
	}
	if err := r.ValidateStorageResult(c.Result); err != nil {
		return err
	}
	c.Result.Message, c.Result.Logs = r.Redact(c.Result.Message), r.Redact(c.Result.Logs)
	if len(c.Result.Resources) > 1000 {
		return errors.New("too many runtime resources")
	}
	for _, resource := range c.Result.Resources {
		if resource.ApplicationID != j.AppID {
			return errors.New("runtime result contains another application's resource")
		}
	}
	raw, err := json.Marshal(c.Result)
	if err != nil {
		return err
	}
	defer clear(raw)
	if len(raw) > MaxResult {
		return errors.New("runtime result exceeds the payload limit")
	}
	encrypted, err := b.Vault.Encrypt("runtime-job:"+id+":result", raw)
	if err != nil {
		return err
	}
	return b.Store.CompleteRuntimeJob(ctx, node, id, c.LeaseToken, c.Result.State, encrypted, time.Now().UTC())
}

func (b *Broker) Wait(ctx context.Context, id string, progress func(core.DeploymentState, string) error) (Result, error) {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	lastPhase, lastMessage := "", ""
	for {
		if ctx.Err() != nil {
			b.cancel(id)
			return Result{}, ctx.Err()
		}
		j, err := b.Store.GetRuntimeJob(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				b.cancel(id)
			}
			return Result{}, err
		}
		if j.Phase != "" && (j.Phase != lastPhase || j.Message != lastMessage) && progress != nil {
			if err = progress(core.DeploymentState(j.Phase), j.Message); err != nil {
				b.cancel(id)
				return Result{}, err
			}
			lastPhase, lastMessage = j.Phase, j.Message
		}
		if j.Terminal() {
			if j.EncryptedResult == "" {
				return Result{State: j.State}, &runtimecontract.Error{Code: runtimecontract.Uncertain, Message: "Remote runtime outcome is unknown; inspect the target before retrying."}
			}
			raw, err := b.Vault.Decrypt("runtime-job:"+id+":result", j.EncryptedResult)
			if err != nil {
				return Result{}, err
			}
			var result Result
			err = json.Unmarshal(raw, &result)
			clear(raw)
			if err != nil {
				return result, err
			}
			if result.State != "succeeded" {
				var cause error
				if result.Code == runtimecontract.Cancelled {
					cause = context.Canceled
				}
				if result.Code == runtimecontract.DeadlineExceeded {
					cause = context.DeadlineExceeded
				}
				return result, &runtimecontract.Error{Code: result.Code, Action: runtimecontract.Operation(j.Operation), Message: result.Message, Cause: cause}
			}
			return result, nil
		}
		if time.Now().After(j.ExpiresAt) {
			_ = b.Store.ExpireRuntimeJobs(ctx, time.Now().UTC())
			return Result{}, &runtimecontract.Error{Code: runtimecontract.Uncertain, Message: "Remote runtime deadline expired; inspect the target before retrying."}
		}
		select {
		case <-ctx.Done():
			cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := b.Store.CancelRuntimeJob(cancelCtx, id, time.Now().UTC())
			cancel()
			if err != nil {
				return Result{}, fmt.Errorf("request remote cancellation: %w", err)
			}
			return Result{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (b *Broker) cancel(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = b.Store.CancelRuntimeJob(ctx, id, time.Now().UTC())
}

func (b *Broker) RunExpiration(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := b.Store.ExpireRuntimeJobs(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			logger.Warn("runtime operation expiration failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Broker) Result(j core.RuntimeJob) (Result, error) {
	var result Result
	raw, err := b.Vault.Decrypt("runtime-job:"+j.ID+":result", j.EncryptedResult)
	if err != nil {
		return result, err
	}
	defer clear(raw)
	err = json.Unmarshal(raw, &result)
	return result, err
}

func (b *Broker) validateServiceOwner(ctx context.Context, j core.RuntimeJob, r Request) error {
	if r.Service == nil {
		if j.ServiceRunID != "" {
			return errors.New("service job identity missing")
		}
		return nil
	}
	run, err := b.Store.GetServiceProvisionRun(ctx, j.ServiceRunID)
	if err != nil || run.ID != r.Service.Request.Run.ID || run.ProjectID != j.ProjectID || run.TemplateID != r.Service.Request.Run.TemplateID || run.ServiceName != r.Service.Request.Run.ServiceName || run.Target == nil || run.Target.ServerID != j.ServerID || run.Target.Provider != "docker" {
		return errors.New("service runtime ownership changed")
	}
	return nil
}
