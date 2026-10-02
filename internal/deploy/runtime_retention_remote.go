package deploy

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/oklog/ulid/v2"
	"time"
)

type RemoteRetentionBackend struct {
	Local  RuntimeRetentionBackend
	Broker *remoteruntime.Broker
}

func (b RemoteRetentionBackend) InspectRetention(ctx context.Context, app core.App, server core.Server) ([]core.RuntimeRetentionItem, error) {
	if server.AgentNodeID == "" {
		if b.Local == nil {
			return nil, errors.New("Local runtime retention is unavailable")
		}
		return b.Local.InspectRetention(ctx, app, server)
	}
	if b.Broker == nil {
		return nil, errors.New("Remote runtime retention is unavailable")
	}
	job, err := b.Broker.Submit(ctx, "retention-inspect-"+ulid.Make().String(), remoteruntime.NewRetentionRequest(app, server, nil))
	if err != nil {
		return nil, err
	}
	result, err := b.Broker.Wait(ctx, job.ID, nil)
	if err != nil {
		return nil, err
	}
	if result.Retention == nil {
		return nil, errors.New("Remote retention inventory evidence is missing")
	}
	active, err := b.Broker.Store.ActiveRuntimeMutation(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	if active != nil && active.Operation == string(remoteruntime.RetentionPrune) && active.State == "unknown" {
		if err = b.Broker.Store.AcknowledgeRuntimeJob(ctx, app.ID, active.ID, job.ID, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	return result.Retention.Items, nil
}

type retentionReviewContextKey struct{}

func (b RemoteRetentionBackend) PruneRetention(ctx context.Context, app core.App, server core.Server, item core.RuntimeRetentionItem) (core.RuntimeRetentionOutcome, error) {
	if server.AgentNodeID == "" {
		if b.Local == nil {
			return core.RuntimeRetentionOutcome{}, errors.New("Local runtime retention is unavailable")
		}
		return b.Local.PruneRetention(ctx, app, server, item)
	}
	review, ok := ctx.Value(retentionReviewContextKey{}).(core.RuntimeRetentionReview)
	if !ok || b.Broker == nil {
		return core.RuntimeRetentionOutcome{}, errors.New("Remote cleanup requires an accepted review")
	}
	job, err := b.Broker.Submit(ctx, "retention-prune-"+ulid.Make().String(), remoteruntime.NewRetentionRequest(app, server, &remoteruntime.RetentionRequest{Attempt: review.Attempt, ReviewID: review.ID, Digest: review.Digest, Item: item}))
	if err != nil {
		return core.RuntimeRetentionOutcome{}, err
	}
	result, err := b.Broker.Wait(ctx, job.ID, nil)
	if result.Retention != nil && result.Retention.Outcome != nil {
		return *result.Retention.Outcome, err
	}
	if err == nil {
		err = errors.New("Remote cleanup receipt is missing")
	}
	return core.RuntimeRetentionOutcome{Key: item.Key, State: "failed", Message: "Remote outcome is unresolved; retry after a fresh target inspection"}, err
}
