package automationclient

import (
	"context"
	"encoding/json"
	"time"
)

func serverReadinessResult(id string, out Result, wait bool) (Result, bool) {
	if out.Continuation == nil {
		out.Continuation = &Continuation{Kind: "managed_server", ID: id, ResourceID: id}
	}
	var server struct {
		ID              string `json:"id"`
		ProjectID       string `json:"projectId"`
		AllocationState string `json:"allocationState"`
		EnrollmentState string `json:"enrollmentState"`
		RuntimeState    string `json:"runtimeState"`
		SourceSnapshot  string `json:"sourceSnapshotId"`
		PowerState      string `json:"powerState"`
		PromotionState  string `json:"promotionState"`
		Promotion       *struct {
			OperationID               string `json:"operationId"`
			QuarantineReleased        bool   `json:"quarantineReleased"`
			CopiedWorkloadsDisabled   bool   `json:"copiedWorkloadsDisabled"`
			ProductionBindingsCleared bool   `json:"productionBindingsCleared"`
		} `json:"promotionEvidence"`
		WaitState       string `json:"waitState"`
		Deployable      *bool  `json:"deployable"`
		LatestOperation *struct {
			ID       string `json:"id"`
			ServerID string `json:"serverId"`
		} `json:"latestOperation"`
	}
	fail := func(code, title string) (Result, bool) {
		out.OK = false
		out.Error = &Problem{Code: code, Title: title}
		return out, true
	}
	if json.Unmarshal(out.Data, &server) != nil || server.ID != id || !identifier.MatchString(server.ProjectID) || server.AllocationState == "" || server.EnrollmentState == "" || server.RuntimeState == "" || server.Deployable == nil {
		return fail("invalid_response", "The controller did not return readiness for the original managed server")
	}
	out.Continuation.ProjectID = server.ProjectID
	if op := server.LatestOperation; op != nil {
		if !identifier.MatchString(op.ID) || op.ServerID != id {
			return fail("invalid_response", "The controller returned an operation for another server")
		}
		out.Continuation.OperationID = op.ID
	}
	promoted := server.SourceSnapshot != "" && server.PromotionState == "promoted" && server.Promotion != nil && identifier.MatchString(server.Promotion.OperationID) && server.Promotion.QuarantineReleased && server.Promotion.CopiedWorkloadsDisabled && server.Promotion.ProductionBindingsCleared
	if *server.Deployable && (server.WaitState != "ready" || server.AllocationState != "allocated" || server.EnrollmentState != "enrolled" || server.RuntimeState != "ready" || server.SourceSnapshot != "" && !promoted || server.PowerState != "" && server.PowerState != "running") {
		return fail("invalid_response", "The controller returned inconsistent deployment readiness")
	}
	switch server.WaitState {
	case "ready":
		if !*server.Deployable {
			return fail("invalid_response", "Ready server has no verified workload target")
		}
		return out, true
	case "verified-isolated":
		if *server.Deployable || server.SourceSnapshot == "" || server.PromotionState == "promoted" || server.AllocationState != "allocated" || server.EnrollmentState != "enrolled" || server.RuntimeState != "verified-isolated" || server.PowerState != "" && server.PowerState != "running" {
			return fail("invalid_response", "The controller returned inconsistent isolated clone evidence")
		}
		return out, true
	case "stopped":
		if *server.Deployable || server.PowerState != "stopped" {
			return fail("invalid_response", "The controller returned inconsistent stopped machine evidence")
		}
		if wait {
			return fail("operation_stopped", "The machine is stopped. Inspect its power operation before explicitly starting it. No automatic start occurred.")
		}
		return out, true
	case "pending_approval":
		return out, true
	case "paused":
		if wait {
			return fail("operation_paused", "The original server operation paused after transport failures. Inspect it before requesting an explicit retry.")
		}
		return out, true
	case "failed", "expired", "revoked", "deleted":
		if wait {
			return fail("operation_failed", "Server readiness stopped. Inspect enrollment, installation and the original operation before recovery.")
		}
		return out, true
	case "cancelled":
		if wait {
			return fail("operation_cancelled", "The original server operation was cancelled")
		}
		return out, true
	case "unknown", "unresolved":
		if wait {
			return fail("outcome_unknown", "Server readiness is uncertain. Inspect the original machine and operation before recovery.")
		}
		return out, true
	case "waiting", "deleting", "cancelling":
		return out, false
	default:
		return fail("invalid_response", "The controller returned an unsupported readiness state")
	}
}

func (c *Client) waitPoll(ctx context.Context, out *Result) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		out.OK = false
		code := "timeout"
		if ctx.Err() == context.Canceled {
			code = "cancelled"
		}
		out.Error = &Problem{Code: code, Title: "Client stopped waiting. Resume with the saved server ID; accepted work was not cancelled."}
		return false
	case <-timer.C:
		return true
	}
}
