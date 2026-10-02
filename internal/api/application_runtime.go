package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/go-chi/chi/v5"
)

// Runtime operations are asynchronous. The caller chooses an idempotency key
// and polls the scoped receipt; HTTP disconnects do not authorize another job.
func (a *API) startApplicationRuntime(w http.ResponseWriter, r *http.Request) {
	app, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	op := runtimecontract.Operation(chi.URLParam(r, "operation"))
	switch op {
	case runtimecontract.Inspect, runtimecontract.Logs:
	case runtimecontract.Start, runtimecontract.Stop:
		if !a.requireProject(w, r, core.PermissionDeploymentRun, app.ProjectID) {
			return
		}
	default:
		problem(w, 422, "Operation unsupported", "Use inspect, logs, start, or stop. Deployment and destruction use their reviewed application actions.")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		problem(w, 422, "Operation identity required", "Supply an Idempotency-Key header between 8 and 128 characters; reuse it only for identical requests.")
		return
	}
	server, err := a.store.GetServer(r.Context(), app.ServerID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	broker := a.runtimeBroker()
	if server.AgentNodeID == "" || broker == nil {
		problem(w, 422, "Remote target required", "This operation requires an enrolled Docker runtime target.")
		return
	}
	if err = a.deploy.RuntimeCapabilities(app, server).Check(r.Context(), op); err != nil {
		problem(w, 422, "Runtime capability unavailable", err.Error())
		return
	}
	digest := sha256.Sum256([]byte(currentIdentity(r.Context()).Kind + ":" + currentIdentity(r.Context()).ID + ":" + app.ID + ":" + key))
	id := "runtime-" + hex.EncodeToString(digest[:])
	request := remoteruntime.NewRequest(op, core.Deployment{}, app, server)
	var input struct {
		LogLimit int `json:"logLimit,omitempty"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.LogLimit != 0 {
		request.LogLimit = input.LogLimit
	}
	var job core.RuntimeJob
	submit := func() error { var e error; job, e = broker.Submit(r.Context(), id, request); return e }
	// A receipt replay is allowed while its own mutation is active.
	if existing, e := broker.Store.GetRuntimeJob(r.Context(), id); e == nil && existing.AppID == app.ID {
		err = submit()
	} else if op == runtimecontract.Start || op == runtimecontract.Stop {
		err = a.deploy.WithIdleApplication(r.Context(), app.ID, submit)
	} else {
		err = submit()
	}
	if errors.Is(err, deploy.ErrDeploymentActive) {
		problem(w, 409, "Application busy", "Wait for the operation to finish, or inspect and acknowledge an unknown outcome.")
		return
	}
	if err != nil {
		a.runtimeJobProblem(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/apps/"+app.ID+"/runtime/jobs/"+job.ID)
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) getApplicationRuntime(w http.ResponseWriter, r *http.Request) {
	broker := a.runtimeBroker()
	if broker == nil {
		problem(w, 503, "Runtime unavailable", "Runtime storage is unavailable.")
		return
	}
	job, err := broker.Store.GetRuntimeJob(r.Context(), chi.URLParam(r, "jobId"))
	if err != nil || job.AppID != chi.URLParam(r, "id") {
		problem(w, 404, "Runtime job not found", "The operation does not belong to this application.")
		return
	}
	var result *remoteruntime.Result
	if job.Terminal() && job.EncryptedResult != "" {
		value, e := broker.Result(job)
		if e != nil {
			a.internal(w, e)
			return
		}
		result = &value
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"job": job, "result": result})
}

func (a *API) acknowledgeApplicationRuntime(w http.ResponseWriter, r *http.Request) {
	app, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	var input struct {
		InspectionID string `json:"inspectionId"`
		ConfirmName  string `json:"confirmName"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.ConfirmName != app.Name || input.InspectionID == "" {
		problem(w, 422, "Runtime inspection required", "Confirm the application name and provide a successful inspection created after the unknown outcome.")
		return
	}
	broker := a.runtimeBroker()
	if broker == nil {
		problem(w, 503, "Runtime unavailable", "Runtime storage is unavailable.")
		return
	}
	if err = broker.Store.AcknowledgeRuntimeJob(r.Context(), app.ID, chi.URLParam(r, "jobId"), input.InspectionID, time.Now().UTC()); err != nil {
		a.runtimeJobProblem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
