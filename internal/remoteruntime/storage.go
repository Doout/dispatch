package remoteruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
)

const (
	StorageInspect runtimecontract.Operation = "storage_inspect"
	StorageDelete  runtimecontract.Operation = "storage_delete"
)

func IsStorageOperation(op runtimecontract.Operation) bool {
	return op == StorageInspect || op == StorageDelete
}
func StorageSubject(server string) string {
	sum := sha256.Sum256([]byte(server))
	return "storage-target-" + hex.EncodeToString(sum[:])
}
func NewStorageRequest(server core.Server, item *core.StorageResource) Request {
	app := core.App{ID: StorageSubject(server.ID), ServerID: server.ID, BuildType: core.BuildTypeDockerfile}
	op := StorageInspect
	if item != nil {
		app.ID, app.ProjectID = item.ID, item.ProjectID
		op = StorageDelete
	}
	r := NewRequest(op, core.Deployment{}, app, server)
	r.Storage = item
	return r
}

var volumeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)

func (r Request) validateStorage() error {
	if !identityPattern.MatchString(r.Server.ID) || !identityPattern.MatchString(r.Server.AgentNodeID) || r.Server.Runtime != core.ServerRuntimeDocker || r.Application.ServerID != r.Server.ID {
		return errors.New("storage request does not match an enrolled Docker target")
	}
	if r.Retention != nil || r.Service != nil || r.Deployment.ID != "" || r.Inputs.ComposeContent != "" || r.Inputs.SourceCredential != "" || len(r.Inputs.Services) > 0 {
		return errors.New("storage requests cannot carry workload execution inputs")
	}
	if r.Operation == StorageInspect {
		if r.Storage != nil || r.Application.ID != StorageSubject(r.Server.ID) || r.Application.ProjectID != "" {
			return errors.New("storage inspection requires a target-scoped subject")
		}
		return nil
	}
	item := r.Storage
	if item == nil || item.ServerID != r.Server.ID || item.Kind != "docker_volume" || item.Namespace != "" || !volumeName.MatchString(item.Name) || item.Identity == "" || item.Evidence == "" || item.Revision < 1 || !identityPattern.MatchString(item.ProjectID) || !identityPattern.MatchString(item.OwnerID) {
		return errors.New("storage deletion requires captured verified volume ownership")
	}
	id := fmt.Sprintf("storage-%x", sha256.Sum256([]byte(r.Server.ID+"\x00docker_volume\x00\x00"+item.Name)))
	if item.ID != id || r.Application.ID != item.ID || r.Application.ProjectID != item.ProjectID || item.DeleteBlockedReason() != "" {
		return errors.New("storage ownership, retention policy or consumer state does not authorize deletion")
	}
	return nil
}
func (b *Broker) validateStorageOwner(ctx context.Context, j core.RuntimeJob, r Request) error {
	if !IsStorageOperation(r.Operation) {
		return nil
	}
	if err := r.validateStorage(); err != nil {
		return err
	}
	if r.Operation == StorageInspect {
		return nil
	}
	item, err := b.Store.GetStorage(ctx, r.Storage.ID)
	if err != nil {
		return errors.New("storage ownership record is unavailable")
	}
	// Freshness timestamps do not authorize different ownership, policy or consumers.
	before, current := *r.Storage, item
	before.ObservedAt = current.ObservedAt
	expected, _ := json.Marshal(before)
	actual, _ := json.Marshal(current)
	if string(expected) != string(actual) || item.ServerID != j.ServerID || item.ProjectID != j.ProjectID || item.ID != j.AppID {
		return errors.New("storage changed after deletion review; inspect and review it again")
	}
	return nil
}

func (r Request) ValidateStorageResult(result Result) error {
	if r.Operation != StorageInspect {
		if result.Storage != nil {
			return errors.New("unexpected remote storage inventory")
		}
		return nil
	}
	if result.State == "succeeded" && result.Storage == nil {
		return errors.New("remote storage inspection did not confirm its inventory")
	}
	if len(result.Storage) > 10000 {
		return errors.New("remote storage inventory exceeds its limit")
	}
	seen := map[string]bool{}
	for _, observation := range result.Storage {
		item := observation.Resource
		if item.Kind != "docker_volume" || item.Namespace != "" || !volumeName.MatchString(item.Name) || seen[item.Name] || item.Identity == "" || item.Evidence == "" || item.Independent || item.State != "" && item.State != "present" || item.ServerID != "" && item.ServerID != r.Server.ID || len(item.Consumers) > 10000 {
			return errors.New("remote inventory contains invalid volume evidence")
		}
		seen[item.Name] = true
	}
	return nil
}
