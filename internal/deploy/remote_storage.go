package deploy

import (
	"context"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/oklog/ulid/v2"
)

// RemoteStorageBackend never uses the controller daemon for an enrolled target.
// The public storage API retains its ownership, policy and confirmation gates.
type RemoteStorageBackend struct {
	Local  StorageBackend
	Broker *remoteruntime.Broker
}

func (b RemoteStorageBackend) Inspect(ctx context.Context, server core.Server) ([]core.StorageObservation, error) {
	if server.AgentNodeID == "" {
		if b.Local == nil {
			return nil, errors.New("storage inspection is unavailable")
		}
		return b.Local.Inspect(ctx, server)
	}
	if b.Broker == nil {
		return nil, errors.New("remote storage broker is unavailable")
	}
	job, err := b.Broker.Submit(ctx, "storage-inspect-"+ulid.Make().String(), remoteruntime.NewStorageRequest(server, nil))
	if err != nil {
		return nil, err
	}
	result, err := b.Broker.Wait(ctx, job.ID, nil)
	if err != nil {
		return nil, err
	}
	if result.Storage == nil {
		return nil, errors.New("remote inventory evidence is missing; absence was not confirmed")
	}
	if err := b.Broker.Store.ReconcileStorageRuntimeJobs(ctx, server.ID, job.ID, time.Now().UTC()); err != nil {
		return nil, err
	}
	return result.Storage, nil
}
func (b RemoteStorageBackend) Delete(ctx context.Context, server core.Server, item core.StorageResource) error {
	if reason := item.DeleteBlockedReason(); reason != "" {
		return errors.New(reason)
	}
	if server.AgentNodeID == "" {
		if b.Local == nil {
			return errors.New("storage deletion is unavailable")
		}
		return b.Local.Delete(ctx, server, item)
	}
	if b.Broker == nil {
		return errors.New("remote storage broker is unavailable")
	}
	job, err := b.Broker.Submit(ctx, "storage-delete-"+ulid.Make().String(), remoteruntime.NewStorageRequest(server, &item))
	if err != nil {
		return err
	}
	_, err = b.Broker.Wait(ctx, job.ID, nil)
	return err
}
