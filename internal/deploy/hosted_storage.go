package deploy

import (
	"context"
	"errors"
	"fmt"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowrunner"
	"github.com/oklog/ulid/v2"
)

// HostedStorageBackend inspects Kubernetes volumes on the tenant worker and
// preserves the existing typed runtime for enrolled Docker targets.
type HostedStorageStore interface {
	GetServer(context.Context, string) (core.Server, error)
	GetStorage(context.Context, string) (core.StorageResource, error)
}

type HostedStorageBackend struct {
	Runner workflowrunner.Runner
	Store  HostedStorageStore
	Docker StorageBackend
}

func (b HostedStorageBackend) Inspect(ctx context.Context, server core.Server) ([]core.StorageObservation, error) {
	if !core.IsKubernetesRuntime(server.Runtime) {
		if b.Docker == nil {
			return nil, errors.New("remote storage inspection unavailable")
		}
		return b.Docker.Inspect(ctx, server)
	}
	result, err := b.run(ctx, server, nil)
	if err != nil {
		return nil, err
	}
	if result.Storage == nil {
		return nil, errors.New("worker storage evidence is missing")
	}
	return result.Storage, nil
}
func (b HostedStorageBackend) Delete(ctx context.Context, server core.Server, item core.StorageResource) error {
	if !core.IsKubernetesRuntime(server.Runtime) {
		if b.Docker == nil {
			return errors.New("remote storage deletion unavailable")
		}
		return b.Docker.Delete(ctx, server, item)
	}
	_, err := b.run(ctx, server, &item)
	return err
}
func (b HostedStorageBackend) run(ctx context.Context, server core.Server, item *core.StorageResource) (workflowrunner.Result, error) {
	if b.Runner == nil || server.ProjectID == "" || server.Kubernetes == nil || server.Kubernetes.KubeconfigData == "" {
		return workflowrunner.Result{}, errors.New("Kubernetes storage requires a project and embedded credentials on a tenant worker")
	}
	revision := ulid.Make().String()
	if item != nil {
		revision = fmt.Sprint(item.Revision)
	}
	p := &workflowrunner.StorageOperation{Server: server, ServerDigest: hostedServerDigest(server), Kubeconfig: server.Kubernetes.KubeconfigData, CertificateAuthority: server.Kubernetes.CertificateAuthorityData, Resource: item}
	request := workflowrunner.Request{Version: workflowrunner.Version, ProjectID: server.ProjectID, ResourceID: server.ID, RevisionID: revision, Mode: "tenant", StorageOperation: p}
	if err := b.AuthorizeRequest(ctx, request); err != nil {
		return workflowrunner.Result{}, err
	}
	return b.Runner.Run(ctx, request, nil)
}
func (b HostedStorageBackend) AuthorizeRequest(ctx context.Context, r workflowrunner.Request) error {
	if err := r.Validate(); err != nil {
		return err
	}
	p := r.StorageOperation
	if b.Store == nil || p == nil {
		return errors.New("storage operation authorization unavailable")
	}
	server, err := b.Store.GetServer(ctx, p.Server.ID)
	if err != nil {
		return err
	}
	if server.ProjectID != r.ProjectID || server.Kubernetes == nil || p.Server.Kubernetes == nil {
		return errors.New("storage target ownership changed")
	}
	// The storage manager inspects each workload namespace on this registered
	// cluster. All other connection settings must still match the stored target.
	config := *server.Kubernetes
	config.Namespace = p.Server.Kubernetes.Namespace
	server.Kubernetes = &config
	if hostedServerDigest(server) != p.ServerDigest || hostedServerDigest(hostedInputServer(p.Server, p.Kubeconfig, p.CertificateAuthority)) != p.ServerDigest {
		return errors.New("storage target changed")
	}
	if item := p.Resource; item != nil {
		current, err := b.Store.GetStorage(ctx, item.ID)
		if err != nil {
			return err
		}
		if current.ServerID != server.ID || current.ProjectID != "" && current.ProjectID != r.ProjectID || current.Revision != item.Revision || current.Identity != item.Identity || current.Evidence != item.Evidence || current.Name != item.Name || current.Namespace != item.Namespace || current.Kind != item.Kind || fmt.Sprint(current.Revision) != r.RevisionID {
			return errors.New("storage deletion acceptance changed")
		}
		if reason := current.DeleteBlockedReason(); reason != "" {
			return errors.New(reason)
		}
	}
	return nil
}
func (b HostedStorageBackend) ConfigureBroker(broker *workflowrunner.Broker) {
	previous := broker.Authorize
	broker.Authorize = func(ctx context.Context, r workflowrunner.Request) error {
		if r.StorageOperation != nil {
			return b.AuthorizeRequest(ctx, r)
		}
		if previous == nil {
			return errors.New("worker authorization unavailable")
		}
		return previous(ctx, r)
	}
}
