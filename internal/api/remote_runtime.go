package api

import (
	"encoding/hex"
	"errors"
	"github.com/doout/dispatch/internal/runtimecontract"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) runtimeBroker() *remoteruntime.Broker {
	data, ok := a.store.(store.RuntimeJobStore)
	if !ok || a.eventConfig.Vault == nil {
		return nil
	}
	return &remoteruntime.Broker{Store: data, Vault: a.eventConfig.Vault}
}

func (a *API) runtimeNode(w http.ResponseWriter, r *http.Request) (core.PrivateNetwork, *remoteruntime.Broker, bool) {
	w.Header().Set("Cache-Control", "no-store")
	node, ok := a.authenticateEdge(r)
	if !ok {
		problem(w, 401, "Authentication required", "The enrolled runtime session is invalid.")
		return node, nil, false
	}
	data, ok := a.edgeCredentials()
	if !ok {
		problem(w, 503, "Runtime enrollment unavailable", "Credential storage is unavailable.")
		return node, nil, false
	}
	credential, err := data.GetEdgeCredential(r.Context(), node.ID)
	if err != nil || credential.PublicKey == "" || credential.Revoked {
		problem(w, 401, "Enrollment required", "Runtime operations require a current key-bound enrolled identity.")
		return node, nil, false
	}
	broker := a.runtimeBroker()
	if broker == nil {
		problem(w, 503, "Runtime unavailable", "Encrypted runtime storage is unavailable.")
		return node, nil, false
	}
	return node, broker, true
}

func (a *API) leaseRuntimeJob(w http.ResponseWriter, r *http.Request) {
	node, broker, ok := a.runtimeNode(w, r)
	if !ok {
		return
	}
	if r.Header.Get("X-Dispatch-Runtime-Version") != remoteruntime.APIVersion {
		problem(w, 422, "Unsupported runtime version", "The runtime agent must advertise the supported typed protocol.")
		return
	}
	if node.Details == nil {
		node.Details = map[string]string{}
	}
	artifact := r.Header.Get("X-Dispatch-Agent-Artifact")
	if decoded, err := hex.DecodeString(artifact); artifact != "" && (err != nil || len(decoded) != 32) {
		problem(w, 422, "Invalid agent artifact", "Provide the installed artifact SHA-256.")
		return
	}
	node.Details["agentArtifactSHA256"] = artifact
	node.Details["runtimeVersion"] = remoteruntime.APIVersion
	node.Details["runtimeCheckedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	advertised := strings.Split(r.Header.Get("X-Dispatch-Runtime-Capabilities"), ",")
	if len(advertised) > 20 || len(r.Header.Get("X-Dispatch-Runtime-Capabilities")) > 512 {
		problem(w, 422, "Invalid capabilities", "The capability list exceeds its limit.")
		return
	}
	node.Details["runtimeCapabilities"] = strings.Join(advertised, ",")
	a.touchEdgeNode(r.Context(), node, r)
	job, err := broker.Lease(r.Context(), node.ID)
	if err != nil {
		a.runtimeJobProblem(w, err)
		return
	}
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	supported := false
	for _, op := range advertised {
		if op == string(job.Request.Operation) {
			supported = true
		}
	}
	if !supported {
		_ = broker.Complete(r.Context(), node.ID, job.ID, remoteruntime.Completion{LeaseToken: job.LeaseToken, Result: remoteruntime.Result{State: "failed", Code: runtimecontract.Unsupported, Message: "The enrolled agent does not advertise this runtime capability."}})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.checkTemporaryRuntimeAuthority(r.Context(), job.ID); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.checkBackupPolicyRuntimeAuthority(r.Context(), job.ID); err != nil {
		// No first-attempt payload has left the controller. A later attempt may
		// already have effects, so stop delivery without claiming those effects absent.
		if job.Attempt == 1 {
			result := remoteruntime.Result{State: "failed", Code: runtimecontract.OwnershipConflict, Message: "Scheduled backup authority changed before runtime dispatch."}
			if request := job.Request.WorkloadBackup; request != nil {
				result.WorkloadBackup = &core.WorkloadBackupResult{BackupID: request.Backup.ID, ProjectID: request.Backup.ProjectID, OperationID: request.OperationID, ArtifactID: request.Backup.ArtifactID, State: "failed", CleanupState: "complete", Message: result.Message}
			}
			err = broker.Complete(r.Context(), node.ID, job.ID, remoteruntime.Completion{LeaseToken: job.LeaseToken, Result: result})
		}
		if job.Attempt > 1 || err != nil {
			_ = broker.Store.FenceRuntimeJob(r.Context(), node.ID, job.ID, job.LeaseToken, time.Now().UTC())
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Recheck source authorization at the credential-release boundary. A queued
	// job cannot outlive its preview approval or a moved pull request head.
	if job.Request.Operation == runtimecontract.Deploy || job.Request.Operation == runtimecontract.Rollback || job.Request.Operation == runtimecontract.Start || job.Request.SourceDeploymentID != "" {
		app, readErr := a.store.GetApp(r.Context(), job.Request.Application.ID)
		commit := job.Request.Deployment.CommitSHA
		if readErr == nil && job.Request.SourceDeploymentID != "" {
			source, sourceErr := a.store.GetDeployment(r.Context(), job.Request.SourceDeploymentID)
			if sourceErr != nil || source.AppID != app.ID {
				readErr = errors.New("retained deployment ownership changed")
			} else {
				commit = source.CommitSHA
			}
		}
		if readErr == nil && a.workflows != nil {
			readErr = a.workflows.CheckDeploymentTrust(r.Context(), app, commit)
		}
		if readErr != nil {
			_ = broker.Complete(r.Context(), node.ID, job.ID, remoteruntime.Completion{LeaseToken: job.LeaseToken, Result: remoteruntime.Result{State: "failed", Code: runtimecontract.OwnershipConflict, Message: "Runtime source authorization changed before dispatch."}})
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	// Recheck enrollment immediately before releasing the encrypted job payload.
	if _, _, valid := a.runtimeNode(w, r); !valid {
		return
	}
	writeJSON(w, 200, job)
}

func (a *API) renewRuntimeJob(w http.ResponseWriter, r *http.Request) {
	node, broker, ok := a.runtimeNode(w, r)
	if !ok {
		return
	}
	var input remoteruntime.Heartbeat
	if !decode(w, r, &input) {
		return
	}
	jobID := chi.URLParam(r, "jobId")
	job, err := broker.Store.GetRuntimeJob(r.Context(), jobID)
	if err != nil || job.NodeID != node.ID {
		a.runtimeJobProblem(w, store.ErrNotFound)
		return
	}
	now := time.Now().UTC()
	if job.State != "running" || input.LeaseToken == "" || job.LeaseToken != input.LeaseToken || !job.LeaseUntil.After(now) || !job.ExpiresAt.After(now) {
		a.runtimeJobProblem(w, store.ErrRuntimeJobConflict)
		return
	}
	if err := a.checkTemporaryRuntimeAuthority(r.Context(), chi.URLParam(r, "jobId")); err != nil {
		a.runtimeJobProblem(w, err)
		return
	}
	if err := a.checkBackupPolicyRuntimeAuthority(r.Context(), chi.URLParam(r, "jobId")); err != nil {
		if cancelErr := broker.Store.CancelLeasedRuntimeJob(r.Context(), node.ID, jobID, input.LeaseToken, time.Now().UTC()); cancelErr != nil {
			a.runtimeJobProblem(w, cancelErr)
			return
		}
		a.runtimeJobProblem(w, err)
		return
	}
	cancelled, err := broker.Renew(r.Context(), node.ID, chi.URLParam(r, "jobId"), input)
	if err != nil {
		a.runtimeJobProblem(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"cancelRequested": cancelled, "leaseSeconds": int(remoteruntime.LeaseDuration / time.Second)})
}

func (a *API) completeRuntimeJob(w http.ResponseWriter, r *http.Request) {
	node, broker, ok := a.runtimeNode(w, r)
	if !ok {
		return
	}
	var input remoteruntime.Completion
	if !decode(w, r, &input) {
		return
	}
	if err := broker.Complete(r.Context(), node.ID, chi.URLParam(r, "jobId"), input); err != nil {
		a.runtimeJobProblem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) runtimeJobProblem(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		problem(w, 404, "Runtime job not found", "The operation is not available to this target.")
		return
	}
	if errors.Is(err, store.ErrRuntimeJobConflict) {
		problem(w, 409, "Runtime lease changed", "Poll for the current operation before submitting another result.")
		return
	}
	problem(w, 422, "Runtime operation rejected", "The runtime request, result or ownership evidence is invalid.")
}

func (a *API) bindRuntimeNode(w http.ResponseWriter, r *http.Request, server *core.Server, id string) bool {
	data, ok := a.edgeCredentials()
	if !ok || a.runtimeBroker() == nil {
		problem(w, 503, "Runtime storage unavailable", "Configure encrypted runtime storage first.")
		return false
	}
	credential, err := data.GetEdgeCredential(r.Context(), id)
	if err != nil || credential.PublicKey == "" || credential.Revoked {
		problem(w, 422, "Enrolled node required", "Bind a current key-bound enrolled node to this Docker target.")
		return false
	}
	servers, err := a.store.ListServers(r.Context())
	if err != nil {
		a.internal(w, err)
		return false
	}
	for _, item := range servers {
		if item.AgentNodeID == id {
			problem(w, 409, "Node already bound", "Each runtime node belongs to one server.")
			return false
		}
	}
	server.AgentNodeID = id
	return true
}
