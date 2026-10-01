package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

func mutationHash(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func mutationCaller(identity core.Identity) (string, string) {
	kind := identity.Kind
	if kind == "" {
		kind = "user"
	}
	return kind, identity.ID
}

// reserveMutation is called after endpoint authentication, authorization and
// structural validation. With no key, existing endpoint behavior is unchanged.
func (a *API) reserveMutation(w http.ResponseWriter, r *http.Request, project, action string, input any, kind, resource string) (*http.Request, *core.MutationReceipt, bool) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return r, nil, true
	}
	if len(key) < 8 || len(key) > 128 || strings.TrimSpace(key) != key || strings.ContainsFunc(key, func(c rune) bool { return c < 33 || c > 126 }) {
		problem(w, 422, "Invalid idempotency key", "Use 8 to 128 printable ASCII characters without spaces.")
		return r, nil, false
	}
	data, ok := a.store.(store.MutationReceiptStore)
	if !ok {
		problem(w, 503, "Mutation receipts unavailable", "Durable request acceptance is unavailable.")
		return r, nil, false
	}
	callerKind, callerID := mutationCaller(currentIdentity(r.Context()))
	if callerID == "" {
		problem(w, 401, "Authentication required", "A stable caller identity is required.")
		return r, nil, false
	}
	requestBytes, err := json.Marshal(input)
	if err != nil {
		problem(w, 422, "Invalid mutation input", "The request cannot be represented as a durable acceptance.")
		return r, nil, false
	}
	requestDigest := sha256.Sum256(requestBytes)
	receipt := core.MutationReceipt{ID: "mutation-" + mutationHash([]string{callerKind, callerID, project, action, key}), CallerKind: callerKind, CallerID: callerID, CredentialID: currentIdentity(r.Context()).CredentialID, ProjectID: project, Action: action, KeyDigest: mutationHash(key), RequestDigest: hex.EncodeToString(requestDigest[:]), OperationKind: kind, OperationID: ulid.Make().String(), ResourceID: resource, ClaimToken: ulid.Make().String()}
	saved, claimed, err := data.ReserveMutationReceipt(r.Context(), receipt, time.Now().UTC())
	if errors.Is(err, store.ErrMutationConflict) {
		problem(w, 409, "Idempotency key conflict", "This caller already used the key for different parameters in this project and action.")
		return r, nil, false
	}
	if errors.Is(err, store.ErrMutationExpired) {
		w.Header().Set("Location", "/api/v1/mutation-receipts/"+saved.ID)
		w.Header().Set("Idempotency-Retry-Until", saved.RetryUntil.Format(time.RFC3339))
		core.RecordAcceptedOperation(r.Context(), saved.OperationID)
		problem(w, 409, "Idempotency key expired", "The retry window expired. The original request will not run again. Inspect its operation before deciding whether to submit a new request.")
		return r, nil, false
	}
	if err != nil {
		a.internal(w, err)
		return r, nil, false
	}
	w.Header().Set("Location", "/api/v1/mutation-receipts/"+saved.ID)
	w.Header().Set("Idempotency-Retry-Until", saved.RetryUntil.Format(time.RFC3339))
	if !claimed {
		w.Header().Set("Idempotency-Replayed", "true")
		if saved.State != "reserved" {
			core.RecordAcceptedOperation(r.Context(), saved.OperationID)
		}
		a.writeMutationReceipt(w, r, saved, http.StatusAccepted)
		return r, &saved, false
	}
	claim := core.MutationAcceptance{ReceiptID: saved.ID, ClaimToken: saved.ClaimToken, OperationKind: saved.OperationKind, OperationID: saved.OperationID}
	return r.WithContext(core.WithMutationAcceptance(r.Context(), claim)), &saved, true
}

func (a *API) failMutationAcceptance(ctx context.Context, status int, message string) {
	claim, ok := core.MutationAcceptanceFromContext(ctx)
	if !ok {
		return
	}
	data, ok := a.store.(store.MutationReceiptStore)
	if !ok {
		return
	}
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := data.FailMutationReceipt(save, claim, status, message, time.Now().UTC()); err != nil {
		a.logger.Error("mutation rejection could not be saved", "receipt", claim.ReceiptID)
	}
}

type mutationReceiptResponse struct {
	core.MutationReceipt
	OperationURL    string   `json:"operationUrl,omitempty"`
	RecoveryActions []string `json:"recoveryActions"`
	Cancellation    string   `json:"cancellation,omitempty"`
}

func (a *API) writeMutationReceipt(w http.ResponseWriter, r *http.Request, receipt core.MutationReceipt, status int) {
	response := mutationReceiptResponse{MutationReceipt: receipt, RecoveryActions: []string{}}
	switch receipt.OperationKind {
	case "service_provision":
		response.OperationURL = "/api/v1/service-provision-runs/" + receipt.OperationID
	case "service_resource":
		response.OperationURL = "/api/v1/service-provision-runs/" + receipt.ResourceID + "/resource"
	case "deployment":
		response.OperationURL = "/api/v1/deployments/" + receipt.OperationID
	case "infrastructure_operation":
		response.OperationURL = "/api/v1/infrastructure/servers/" + receipt.ResourceID + "/operations"
	}
	switch receipt.State {
	case "reserved":
		response.State = "accepted"
		response.Message = "Request identity reserved; operation acceptance is still being prepared."
		response.RecoveryActions = []string{"retry_same_request"}
	case "cancelled", "canceled":
		response.RecoveryActions = []string{"inspect_operation", "inspect_runtime"}
		response.Cancellation = "Execution stopped; cancellation does not undo external changes."
	case "failed":
		if receipt.FailureStatus != 0 {
			response.OperationURL = ""
		} else {
			response.RecoveryActions = []string{"inspect_operation"}
		}
		if status == http.StatusAccepted && receipt.FailureStatus >= 400 {
			status = receipt.FailureStatus
		}
	case "unresolved":
		response.RecoveryActions = []string{"inspect_operation", "inspect_runtime"}
	case "accepted":
		data, ok := a.store.(interface {
			MutationOperationState(context.Context, string, string) (string, bool, error)
		})
		var state string
		var cancelled bool
		var err error
		if ok {
			state, cancelled, err = data.MutationOperationState(r.Context(), receipt.OperationKind, receipt.OperationID)
		} else {
			err = store.ErrNotFound
		}
		if err != nil {
			response.State = "unresolved"
			response.Message = "The accepted operation cannot currently be inspected. Its request identity remains protected."
			response.RecoveryActions = []string{"inspect_operation", "inspect_runtime"}
		} else {
			switch state {
			case "queued", "pending", "accepted":
				response.State = "accepted"
				response.Message = "Operation accepted."
			case "paused":
				response.State = "accepted"
				response.Message = "Operation is paused; inspect the original operation before resuming."
				response.RecoveryActions = []string{"inspect_operation"}
			case "adopted":
				response.State = "succeeded"
				response.Message = "Owned resource inspected and adopted."
			case "succeeded":
				response.State = "succeeded"
				response.Message = "Operation completed."
			case "failed":
				response.State = "failed"
				response.Message = "Operation failed; inspect its sanitized diagnostics."
				response.RecoveryActions = []string{"inspect_operation"}
			case "unknown", "unresolved":
				response.State = "unresolved"
				response.Message = "The external outcome is uncertain; inspect or reconcile the original operation."
				response.RecoveryActions = []string{"inspect_operation", "inspect_runtime"}
			case "cancelled", "canceled":
				response.State = "cancelled"
				response.Message = "Execution stopped after cancellation; external changes may remain."
				response.RecoveryActions = []string{"inspect_operation", "inspect_runtime"}
				response.Cancellation = "Execution stopped; cancellation does not undo external changes."
			default:
				response.State = "running"
				response.Message = "Operation is running."
			}
			if cancelled && response.State != "cancelled" {
				response.Cancellation = "Cancellation was requested; inspect the operation for its actual effect."
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/api/v1/mutation-receipts/"+receipt.ID)
	writeJSON(w, status, response)
}

func (a *API) getMutationReceipt(w http.ResponseWriter, r *http.Request) {
	data, ok := a.store.(store.MutationReceiptStore)
	if !ok {
		problem(w, 503, "Mutation receipts unavailable", "Durable request acceptance is unavailable.")
		return
	}
	receipt, err := data.GetMutationReceipt(r.Context(), chi.URLParam(r, "id"))
	kind, id := mutationCaller(currentIdentity(r.Context()))
	if errors.Is(err, store.ErrNotFound) || err == nil && (receipt.CallerKind != kind || receipt.CallerID != id) {
		problem(w, 404, "Mutation receipt not found", "The receipt is not available to this caller.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	if receipt.ProjectID != "" && !a.requireProject(w, r, core.PermissionProjectView, receipt.ProjectID) {
		return
	}
	if receipt.ProjectID == "" && !a.requireControllerOwner(w, r) {
		return
	}
	a.writeMutationReceipt(w, r, receipt, http.StatusOK)
}
