// Package agentruntime executes typed jobs and journals their outcomes before
// acknowledging the controller. An interrupted mutation is never replayed.
package agentruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/store"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type Execute func(context.Context, remoteruntime.Request, deploy.Progress) remoteruntime.Result

type Worker struct {
	directory, node string
	vault           *secretcrypto.Vault
	lock            *os.File
	execute         Execute
	hasWorkload     func(context.Context, remoteruntime.Request) (bool, error)
	mu              sync.Mutex
}

type receipt struct {
	Route  string `json:"route,omitempty"`
	Health string `json:"health,omitempty"`
	Digest string `json:"digest"`
	State  string `json:"state"`
	Result string `json:"result,omitempty"`
}

// Options contains host operator configuration; request payloads cannot set paths.
type Options struct{ RoutingDirectory string }

func Open(directory, node string, options ...Options) (*Worker, error) {
	var publisher *routing.FilePublisher
	if len(options) > 1 {
		return nil, errors.New("multiple runtime options are not supported")
	}
	if len(options) == 1 && options[0].RoutingDirectory != "" {
		if !filepath.IsAbs(options[0].RoutingDirectory) {
			return nil, errors.New("runtime routing directory must be absolute")
		}
		publisher = &routing.FilePublisher{Directory: options[0].RoutingDirectory}
	}
	if !safeID.MatchString(node) {
		return nil, errors.New("invalid runtime node identity")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("runtime state directory must be private to the agent")
	}
	lock, err := os.OpenFile(filepath.Join(directory, "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another runtime worker owns this state directory")
	}
	w := &Worker{directory: directory, node: node, lock: lock}
	ok := false
	defer func() {
		if !ok {
			w.Close()
		}
	}()
	keyPath := filepath.Join(directory, "key")
	if _, err = os.Lstat(keyPath); errors.Is(err, os.ErrNotExist) {
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		err = w.save("key", []byte(base64.StdEncoding.EncodeToString(key)))
		clear(key)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if info, err = os.Lstat(keyPath); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("runtime encryption key must be a private regular file")
	}
	w.vault, err = secretcrypto.OpenFile(keyPath)
	if err != nil {
		return nil, err
	}
	binding, err := os.ReadFile(filepath.Join(directory, "node"))
	if errors.Is(err, os.ErrNotExist) {
		err = w.save("node", []byte(node))
	} else if err == nil && string(binding) != node {
		err = errors.New("runtime state belongs to another node")
	}
	if err != nil {
		return nil, err
	}
	engine := dockerEngine{executor: deploy.DockerExecutor{Routes: publisher, Artifacts: w, Vault: w.vault, ArtifactDirectory: filepath.Join(directory, "artifacts")}, command: command}
	w.execute = engine.Execute
	w.hasWorkload = func(ctx context.Context, r remoteruntime.Request) (bool, error) {
		resources, err := engine.resources(ctx, r)
		return len(resources) > 0, err
	}
	ok = true
	return w, nil
}

func (w *Worker) Close() error {
	if w == nil || w.lock == nil {
		return nil
	}
	err := w.lock.Close()
	w.lock = nil
	return err
}

// save fsyncs both data and directory before the receipt can authorize effects.
func (w *Worker) save(name string, raw []byte) error {
	f, err := os.CreateTemp(w.directory, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), filepath.Join(w.directory, name)); err != nil {
		return err
	}
	dir, err := os.Open(w.directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func failure(code runtimecontract.Code, message string) remoteruntime.Result {
	state := "failed"
	if code == runtimecontract.Uncertain {
		state = "unknown"
	}
	if code == runtimecontract.Cancelled {
		state = "cancelled"
	}
	return remoteruntime.Result{State: state, Code: code, Message: message}
}

func (w *Worker) Run(ctx context.Context, job remoteruntime.LeasedJob, progress deploy.Progress) remoteruntime.Result {
	w.mu.Lock()
	defer w.mu.Unlock()
	if job.Attempt < 1 || !safeID.MatchString(job.ID) || job.Request.Validate() != nil || job.Request.Server.AgentNodeID != w.node {
		return failure(runtimecontract.InvalidRequest, "The runtime job does not match this node or protocol.")
	}
	raw, err := json.Marshal(job.Request)
	if err != nil || len(raw) > remoteruntime.MaxPayload {
		return failure(runtimecontract.InvalidRequest, "The runtime job exceeds its payload limit.")
	}
	digest := sha256.Sum256(raw)
	clear(raw)
	if hex.EncodeToString(digest[:]) != job.Digest {
		return failure(runtimecontract.InvalidRequest, "The runtime request digest does not match its payload.")
	}
	name := "receipt-" + job.ID + ".json"
	var r receipt
	raw, err = os.ReadFile(filepath.Join(w.directory, name))
	if err == nil {
		if json.Unmarshal(raw, &r) != nil || r.Digest != job.Digest {
			return failure(runtimecontract.Conflict, "The operation identity was reused with different inputs.")
		}
		if r.State != "complete" {
			result := failure(runtimecontract.Uncertain, "The agent restarted during this operation. Inspect the target before retrying.")
			if r.Health != "" {
				payload, err := w.vault.Decrypt("health:"+job.ID, r.Health)
				if err == nil {
					var health core.DeploymentHealth
					if json.Unmarshal(payload, &health) == nil && job.Request.ValidateHealth(&health) == nil {
						result.Health = &health
					}
					clear(payload)
				}
			}
			if r.Route != "" {
				payload, err := w.vault.Decrypt("route:"+job.ID, r.Route)
				if err == nil {
					var route core.ApplicationRoute
					if json.Unmarshal(payload, &route) == nil && job.Request.ValidateRoute(&route) == nil {
						result.Route = &route
					}
					clear(payload)
				}
			}
			return result
		}
		payload, err := w.vault.Decrypt("receipt:"+job.ID, r.Result)
		if err != nil {
			return failure(runtimecontract.Uncertain, "The retained operation result cannot be read.")
		}
		defer clear(payload)
		var result remoteruntime.Result
		if json.Unmarshal(payload, &result) != nil {
			return failure(runtimecontract.Uncertain, "The retained operation result is invalid.")
		}
		return result
	}
	if !errors.Is(err, os.ErrNotExist) {
		return failure(runtimecontract.Unavailable, "The operation journal cannot be read.")
	}
	// A missing journal on a reoffered operation can mean the state directory
	// was lost or replaced. The original worker may already have changed Docker.
	if job.Attempt > 1 {
		return failure(runtimecontract.Uncertain, "The operation was previously leased but its local receipt is missing. Inspect the target before retrying.")
	}
	// Persist ownership even after destroy. A new project or target cannot inherit
	// the old application's Docker names or retained deployment artifacts.
	ownerName := "owner-" + job.Request.Application.ID
	owner := job.Request.Application.ProjectID + ":" + job.Request.Server.ID
	previous, err := os.ReadFile(filepath.Join(w.directory, ownerName))
	if err == nil && string(previous) != owner {
		return failure(runtimecontract.OwnershipConflict, "The workload identity belongs to another project or target.")
	}
	if errors.Is(err, os.ErrNotExist) {
		err = w.save(ownerName, []byte(owner))
	}
	if err != nil {
		return failure(runtimecontract.Unavailable, "Workload ownership cannot be persisted.")
	}
	if ctx.Err() != nil || job.CancelRequested {
		return failure(runtimecontract.Cancelled, "The operation was cancelled before execution.")
	}
	if !job.ExpiresAt.After(time.Now()) {
		return failure(runtimecontract.DeadlineExceeded, "The operation expired before execution.")
	}
	redact, err := w.redactor(ctx, job.Request)
	if err != nil {
		return failure(runtimecontract.Unavailable, "The workload secret redaction record cannot be saved.")
	}
	r = receipt{Digest: job.Digest, State: "running"}
	raw, _ = json.Marshal(r)
	if w.save(name, raw) != nil {
		return failure(runtimecontract.Unavailable, "The operation journal cannot be persisted.")
	}
	execution, cancel := context.WithDeadline(ctx, job.ExpiresAt)
	defer cancel()
	if progress == nil {
		progress = func(core.DeploymentState, string) error { return execution.Err() }
	}
	var health *core.DeploymentHealth
	execution = deploy.WithHealthReporter(execution, func(_ context.Context, id string, record core.DeploymentHealth) error {
		if id != job.Request.Deployment.ID {
			return errors.New("health evidence belongs to another deployment")
		}
		if err := job.Request.ValidateHealth(&record); err != nil {
			return err
		}
		for index := range record.Checks {
			record.Checks[index].Message = redact(record.Checks[index].Message)
		}
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		defer clear(data)
		if len(data) > remoteruntime.MaxResult/2 {
			return errors.New("health evidence exceeds its limit")
		}
		encrypted, err := w.vault.Encrypt("health:"+job.ID, data)
		if err != nil {
			return err
		}
		r.Health = encrypted
		updated, _ := json.Marshal(r)
		if err = w.save(name, updated); err != nil {
			return err
		}
		health = &record
		return nil
	})
	var route *core.ApplicationRoute
	execution = deploy.WithRouteReporter(execution, func(_ context.Context, record core.ApplicationRoute) error {
		if err := job.Request.ValidateRoute(&record); err != nil {
			return err
		}
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		defer clear(data)
		if len(data) > remoteruntime.MaxResult/4 {
			return errors.New("route evidence exceeds its limit")
		}
		encrypted, err := w.vault.Encrypt("route:"+job.ID, data)
		if err != nil {
			return err
		}
		r.Route = encrypted
		updated, _ := json.Marshal(r)
		if err = w.save(name, updated); err != nil {
			return err
		}
		route = &record
		return nil
	})
	result := w.execute(execution, job.Request, func(phase core.DeploymentState, message string) error { return progress(phase, redact(message)) })
	result.Route, result.Health = route, health
	result.Message, result.Logs = redact(result.Message), redact(result.Logs)
	raw, err = json.Marshal(result)
	if err != nil || len(raw) > remoteruntime.MaxResult {
		result = failure(runtimecontract.Uncertain, "The runtime result exceeds its limit; inspect the workload.")
		raw, _ = json.Marshal(result)
	}
	r.Result, err = w.vault.Encrypt("receipt:"+job.ID, raw)
	clear(raw)
	if err != nil {
		return failure(runtimecontract.Uncertain, "The runtime completed but its receipt could not be encrypted.")
	}
	r.State = "complete"
	raw, _ = json.Marshal(r)
	if w.save(name, raw) != nil {
		return failure(runtimecontract.Uncertain, "The runtime completed but its receipt could not be saved.")
	}
	return result
}

func (w *Worker) SaveRuntimeArtifact(ctx context.Context, artifact core.RuntimeArtifact) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !safeID.MatchString(artifact.DeploymentID) {
		return errors.New("invalid artifact identity")
	}
	raw, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	return w.save("artifact-"+artifact.DeploymentID+".json", raw)
}

func (w *Worker) GetRuntimeArtifact(ctx context.Context, id string) (core.RuntimeArtifact, error) {
	var artifact core.RuntimeArtifact
	if ctx.Err() != nil {
		return artifact, ctx.Err()
	}
	if !safeID.MatchString(id) {
		return artifact, errors.New("invalid artifact identity")
	}
	raw, err := os.ReadFile(filepath.Join(w.directory, "artifact-"+id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return artifact, store.ErrNotFound
	}
	if err != nil {
		return artifact, err
	}
	if len(raw) > 48<<20 || json.Unmarshal(raw, &artifact) != nil || artifact.DeploymentID != id {
		return artifact, fmt.Errorf("invalid retained artifact %s", id)
	}
	return artifact, nil
}

func (w *Worker) ListRuntimeArtifacts(ctx context.Context, server string) ([]core.RuntimeArtifact, error) {
	entries, err := os.ReadDir(w.directory)
	if err != nil {
		return nil, err
	}
	out := []core.RuntimeArtifact{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "artifact-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil, errors.New("invalid runtime artifact file")
		}
		id := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "artifact-"), ".json")
		a, err := w.GetRuntimeArtifact(ctx, id)
		if err != nil {
			return nil, err
		}
		if a.ServerID == server {
			out = append(out, a)
		}
		if len(out) > 10000 {
			return nil, errors.New("runtime artifact inventory exceeds its limit")
		}
	}
	return out, nil
}
func (w *Worker) RetireRuntimeArtifact(ctx context.Context, a core.RuntimeArtifact) error {
	existing, err := w.GetRuntimeArtifact(ctx, a.DeploymentID)
	if err != nil {
		return err
	}
	if existing.AppID != a.AppID || existing.ServerID != a.ServerID || existing.ScopeID != a.ScopeID || existing.Ciphertext != a.Ciphertext {
		return errors.New("retained runtime inputs changed")
	}
	a.Metadata.Retired = true
	a.Ciphertext = ""
	return w.SaveRuntimeArtifact(ctx, a)
}
