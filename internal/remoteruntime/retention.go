package remoteruntime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
	"regexp"
	"time"
)

const (
	RetentionInspect runtimecontract.Operation = "retention_inspect"
	RetentionPrune   runtimecontract.Operation = "retention_prune"
)

type RetentionRequest struct {
	Attempt  string                    `json:"attempt"`
	ReviewID string                    `json:"reviewId"`
	Digest   string                    `json:"digest"`
	Item     core.RuntimeRetentionItem `json:"item"`
}
type RetentionResult struct {
	Items   []core.RuntimeRetentionItem   `json:"items,omitempty"`
	Outcome *core.RuntimeRetentionOutcome `json:"outcome,omitempty"`
}

func NewRetentionRequest(app core.App, server core.Server, input *RetentionRequest) Request {
	app = core.App{ID: app.ID, ProjectID: app.ProjectID, ServerID: app.ServerID, BuildType: app.BuildType}
	op := RetentionInspect
	if input != nil {
		op = RetentionPrune
	}
	r := NewRequest(op, core.Deployment{}, app, server)
	r.Retention = input
	return r
}

var retentionHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (r Request) validateRetention() error {
	if r.Service != nil || r.Storage != nil || r.Deployment.ID != "" || r.SourceDeploymentID != "" || r.Inputs.ComposeContent != "" || r.Inputs.SourceCredential != "" || len(r.Inputs.Services) > 0 {
		return errors.New("retention requests cannot carry workload execution inputs")
	}
	if r.Operation == RetentionInspect {
		if r.Retention != nil {
			return errors.New("retention inspection cannot carry deletion inputs")
		}
		return nil
	}
	input := r.Retention
	if input == nil || !identityPattern.MatchString(input.Attempt) || !identityPattern.MatchString(input.ReviewID) || !retentionHash.MatchString(input.Digest) {
		return errors.New("runtime cleanup requires an accepted immutable review")
	}
	item := input.Item
	if item.AppID != r.Application.ID || item.ServerID != r.Server.ID || len(item.Protected) > 0 || len(item.Containers) > 1000 {
		return errors.New("retention item ownership is invalid")
	}
	if item.Kind == "revision" {
		if !identityPattern.MatchString(item.DeploymentID) || !retentionHash.MatchString(item.Identity) || item.Key != "revision:"+r.Server.ID+":"+item.DeploymentID {
			return errors.New("invalid retained revision identity")
		}
	} else if item.Kind == "image" {
		if len(item.Identity) != 71 || item.Identity[:7] != "sha256:" || !retentionHash.MatchString(item.Identity[7:]) || item.Key != "image:"+r.Server.ID+":"+item.Identity || item.DeploymentID != "" {
			return errors.New("invalid immutable image identity")
		}
	} else {
		return errors.New("unsupported retention item")
	}
	for _, id := range item.Containers {
		if !retentionHash.MatchString(id) {
			return errors.New("invalid retained container identity")
		}
	}
	return nil
}
func (b *Broker) validateRetentionOwner(ctx context.Context, j core.RuntimeJob, r Request) error {
	if r.Operation != RetentionPrune {
		return nil
	}
	data, ok := b.Store.(interface {
		ValidateRuntimeRetentionMutation(context.Context, string, string, string, core.RuntimeRetentionItem, time.Time) error
	})
	if !ok {
		return errors.New("runtime retention review storage is unavailable")
	}
	return data.ValidateRuntimeRetentionMutation(ctx, r.Retention.ReviewID, r.Retention.Digest, r.Retention.Attempt, r.Retention.Item, time.Now().UTC())
}
func (r Request) ValidateRetentionResult(result Result) error {
	if r.Operation != RetentionInspect && r.Operation != RetentionPrune {
		if result.Retention != nil {
			return errors.New("unexpected retention evidence")
		}
		return nil
	}
	if result.State == "succeeded" && result.Retention == nil {
		return errors.New("retention result evidence is missing")
	}
	if result.Retention == nil {
		return nil
	}
	if len(result.Retention.Items) > 1000 {
		return errors.New("retention inventory exceeds its limit")
	}
	seen := map[string]bool{}
	for _, item := range result.Retention.Items {
		if item.AppID != r.Application.ID || item.ServerID != r.Server.ID || seen[item.Key] || len(item.Protected) > 20 || len(item.Containers) > 1000 {
			return errors.New("retention inventory ownership is invalid")
		}
		seen[item.Key] = true
		if item.Kind != "revision" && item.Kind != "image" {
			return errors.New("unsupported retention inventory kind")
		}
	}
	if r.Operation == RetentionPrune {
		if len(result.Retention.Items) > 0 {
			return errors.New("prune cannot return inventory")
		}
		out := result.Retention.Outcome
		if out == nil || out.Key != r.Retention.Item.Key {
			return errors.New("retention receipt does not match reviewed item")
		}
		switch out.State {
		case "removed", "absent", "protected", "failed":
		default:
			return errors.New("invalid retention receipt")
		}
	} else if result.Retention.Outcome != nil {
		return errors.New("inspection cannot return a deletion receipt")
	}
	raw, _ := json.Marshal(result.Retention)
	if len(raw) > MaxResult/2 {
		return errors.New("retention evidence exceeds its limit")
	}
	return nil
}
