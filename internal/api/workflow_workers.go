package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/doout/dispatch/internal/workflowrunner"
	"github.com/go-chi/chi/v5"
)

// ConfigureWorkflowRunner makes local workflow command execution unavailable.
func (a *API) ConfigureWorkflowRunner(b *workflowrunner.Broker) {
	if a.workflows == nil {
		return
	}
	a.workflows.RemoteJobs = b
	a.workflows.RequireRemote = true
	if b != nil {
		previous := b.Authorize
		b.Authorize = func(ctx context.Context, request workflowrunner.Request) error {
			if request.Workflow != nil {
				return a.workflows.AuthorizeWorkerRequest(ctx, request)
			}
			if previous != nil {
				return previous(ctx, request)
			}
			return errors.New("deployment worker authorization is not configured")
		}
	}
}

// MountWorkflowRunnerRoutes mounts beneath /api/v1 on the tenant's router. These
// endpoints accept only key-bound enrolled sessions, never browser credentials.
func (a *API) MountWorkflowRunnerRoutes(r chi.Router, b *workflowrunner.Broker) {
	authenticate := func(w http.ResponseWriter, r *http.Request) (string, int64, string, bool) {
		w.Header().Set("Cache-Control", "no-store")
		node, ok := a.authenticateEdge(r)
		if !ok {
			problem(w, 401, "Worker authentication required", "Enroll this worker before requesting jobs.")
			return "", 0, "", false
		}
		data, ok := a.edgeCredentials()
		if !ok || b == nil {
			problem(w, 503, "Worker unavailable", "Workflow execution is not configured.")
			return "", 0, "", false
		}
		credential, err := data.GetEdgeCredential(r.Context(), node.ID)
		mode := workflowrunner.NodeMode(node)
		if err != nil || credential.Revoked || credential.PublicKey == "" || (mode != "tenant" && mode != "managed") {
			problem(w, 403, "Worker unavailable", "This enrolled node is not enabled for workflow execution.")
			return "", 0, "", false
		}
		if r.Header.Get("X-Dispatch-Worker-Version") != workflowrunner.Version || r.Header.Get("X-Dispatch-Worker-Mode") != mode {
			problem(w, 422, "Worker configuration mismatch", "The worker must advertise its assigned protocol and execution mode.")
			return "", 0, "", false
		}
		details := map[string]string{"workerDocker": "false", "workerVersion": workflowrunner.Version, "workerCheckedAt": time.Now().UTC().Format(time.RFC3339Nano)}
		if mode == "tenant" && r.Header.Get("X-Dispatch-Worker-Docker") == "true" {
			details["workerDocker"] = "true"
		}
		a.touchEdgeNode(r.Context(), node, r, details)
		return node.ID, credential.Generation, mode, true
	}
	read := func(w http.ResponseWriter, r *http.Request, input any) bool {
		body := http.MaxBytesReader(w, r.Body, workflowrunner.MaxPayload)
		defer body.Close()
		decoder := json.NewDecoder(body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(input); err != nil {
			problem(w, 400, "Invalid worker response", "The response must fit the supported worker protocol.")
			return false
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			problem(w, 400, "Invalid worker response", "Send one JSON response.")
			return false
		}
		return true
	}
	r.Get("/edge/nodes/{id}/workflow/jobs/next", func(w http.ResponseWriter, r *http.Request) {
		node, generation, mode, ok := authenticate(w, r)
		if !ok {
			return
		}
		job, err := b.Lease(r.Context(), node, generation, mode)
		if err != nil {
			problem(w, 409, "Worker job unavailable", "The operation or its authorization changed.")
			return
		}
		if job == nil {
			w.WriteHeader(204)
			return
		}
		// Session/enrollment may have changed while the queue was inspected.
		if _, _, _, ok = authenticate(w, r); !ok {
			return
		}
		writeJSON(w, 200, job)
	})
	r.Post("/edge/nodes/{id}/workflow/jobs/{jobId}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		node, _, _, ok := authenticate(w, r)
		if !ok {
			return
		}
		var input workflowrunner.Progress
		if !read(w, r, &input) {
			return
		}
		cancel, err := b.Renew(r.Context(), node, chi.URLParam(r, "jobId"), input)
		if err != nil {
			problem(w, 409, "Worker lease changed", "Stop this operation before accepting another job.")
			return
		}
		writeJSON(w, 200, map[string]bool{"cancelRequested": cancel})
	})
	r.Post("/edge/nodes/{id}/workflow/jobs/{jobId}/complete", func(w http.ResponseWriter, r *http.Request) {
		node, _, _, ok := authenticate(w, r)
		if !ok {
			return
		}
		var input workflowrunner.Completion
		if !read(w, r, &input) {
			return
		}
		if err := b.Complete(r.Context(), node, chi.URLParam(r, "jobId"), input); err != nil {
			problem(w, 409, "Worker completion refused", "The operation or its authorization changed.")
			return
		}
		writeJSON(w, 200, map[string]bool{"accepted": true})
	})
}
