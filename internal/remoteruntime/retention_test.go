package remoteruntime

import (
	"context"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"strings"
	"testing"
	"time"
)

func retentionRequestFixture(t *testing.T) (*Broker, Request, core.RuntimeRetentionReview) {
	t.Helper()
	b, r := brokerFixture(t)
	ctx := context.Background()
	data := b.Store.(*store.SQLStore)
	now := time.Now().UTC()
	p := core.RetentionPolicy{ProjectID: r.Application.ProjectID, LogDays: 30, RunDays: 90, KeepRuns: 5, StoppedRevisionDays: 7, KeepRollbackRevisions: 2}
	if err := data.SaveRetentionPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	d := core.Deployment{ID: "old-failed", AppID: r.Application.ID, State: core.DeploymentFailed, CreatedAt: now.AddDate(0, 0, -30)}
	if err := data.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	item := core.RuntimeRetentionItem{Key: "revision:" + r.Server.ID + ":" + d.ID, Kind: "revision", AppID: r.Application.ID, ServerID: r.Server.ID, DeploymentID: d.ID, Identity: strings.Repeat("a", 64), Protected: []string{}}
	review := core.RuntimeRetentionReview{Attempt: "retention-attempt", ID: "retention-review", ProjectID: p.ProjectID, Digest: strings.Repeat("b", 64), State: "planned", CreatedAt: now, ExpiresAt: now.Add(time.Minute), Policy: p, Items: []core.RuntimeRetentionItem{item}, Results: []core.RuntimeRetentionOutcome{}}
	if err := data.SaveRuntimeRetentionReview(ctx, review); err != nil {
		t.Fatal(err)
	}
	if err := data.ClaimRuntimeRetentionReview(ctx, review, now); err != nil {
		t.Fatal(err)
	}
	if err := data.BeginRuntimeArtifactRetirement(ctx, review, item); err != nil {
		t.Fatal(err)
	}
	return b, NewRetentionRequest(r.Application, r.Server, &RetentionRequest{Attempt: review.Attempt, ReviewID: review.ID, Digest: review.Digest, Item: item}), review
}
func TestRemoteRetentionRequiresExactAcceptedReviewAtEveryBoundary(t *testing.T) {
	b, r, review := retentionRequestFixture(t)
	ctx := context.Background()
	changed := r
	input := *r.Retention
	input.Item.Identity = strings.Repeat("c", 64)
	changed.Retention = &input
	if _, err := b.Submit(ctx, "altered-retention", changed); err == nil {
		t.Fatal("altered candidate accepted")
	}
	r.Inputs.SourceCredential = "private"
	if r.Validate() == nil {
		t.Fatal("retention carried credential")
	}
	r.Inputs.SourceCredential = ""
	job, err := b.Submit(ctx, "retention-job", r)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := b.Lease(ctx, r.Server.AgentNodeID)
	if err != nil || lease == nil {
		t.Fatalf("lease %+v %v", lease, err)
	}
	missing := Result{State: "succeeded"}
	if err = b.Complete(ctx, r.Server.AgentNodeID, job.ID, Completion{LeaseToken: lease.LeaseToken, Result: missing}); err == nil {
		t.Fatal("missing deletion receipt accepted")
	}
	review.State = "partial"
	if err = b.Store.(*store.SQLStore).UpdateRuntimeRetentionReview(ctx, review, true); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Renew(ctx, r.Server.AgentNodeID, job.ID, Heartbeat{LeaseToken: lease.LeaseToken}); err == nil {
		t.Fatal("released review renewed mutation")
	}
}
func TestRemoteRetentionInspectionCannotUnlockAnotherMutation(t *testing.T) {
	b, r, _ := retentionRequestFixture(t)
	ctx := context.Background()
	inspect := NewRetentionRequest(r.Application, r.Server, nil)
	if inspect.Inputs.SourceCredential != "" || inspect.Inputs.ComposeContent != "" {
		t.Fatal("inspection included execution inputs")
	}
	job, err := b.Submit(ctx, "inspect-retention", inspect)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := b.Lease(ctx, r.Server.AgentNodeID)
	if err != nil || lease == nil {
		t.Fatal(err)
	}
	item := r.Retention.Item
	item.AppID = "another-app"
	result := Result{State: "succeeded", Retention: &RetentionResult{Items: []core.RuntimeRetentionItem{item}}}
	if err = b.Complete(ctx, r.Server.AgentNodeID, job.ID, Completion{LeaseToken: lease.LeaseToken, Result: result}); err == nil {
		t.Fatal("cross-app inventory accepted")
	}
	result.Retention.Items = []core.RuntimeRetentionItem{}
	if err = b.Complete(ctx, r.Server.AgentNodeID, job.ID, Completion{LeaseToken: lease.LeaseToken, Result: result}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Wait(ctx, job.ID, nil); err != nil {
		t.Fatal(err)
	}
}
