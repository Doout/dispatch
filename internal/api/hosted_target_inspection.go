package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowrunner"
	"github.com/oklog/ulid/v2"
)

// ConfigureHostedTargetInspection keeps Kubernetes discovery on tenant workers.
// Install it after the other broker authorizers and before serving requests.
func (a *API) ConfigureHostedTargetInspection(broker *workflowrunner.Broker) {
	inspector := &hostedTargetInspector{auth: a.auth.Hosted, pending: map[string]pendingTargetInspection{}}
	if broker != nil {
		inspector.runner = broker
		previous := broker.Authorize
		broker.Authorize = func(ctx context.Context, request workflowrunner.Request) error {
			if request.TargetInspection != nil {
				return inspector.authorize(ctx, request)
			}
			if previous == nil {
				return errors.New("worker authorization is unavailable")
			}
			return previous(ctx, request)
		}
	}
	// Even an incomplete hosted configuration must not fall back to discovery
	// from the control plane.
	a.kubernetesTargetValidator = inspector.inspect
}

type pendingTargetInspection struct {
	ctx       context.Context
	digest    [32]byte
	authorize func(context.Context) error
}

type hostedTargetInspector struct {
	auth    *HostedAuth
	runner  workflowrunner.Runner
	mu      sync.Mutex
	pending map[string]pendingTargetInspection
}

func (i *hostedTargetInspector) inspect(ctx context.Context, config core.KubernetesServerConfig) (core.KubernetesTargetEvidence, error) {
	if i.runner == nil || i.auth == nil || i.auth.Authenticate == nil {
		return core.KubernetesTargetEvidence{}, errors.New("tenant worker target inspection is unavailable")
	}
	origin, ok := ctx.Value(targetInspectionContextKey{}).(targetInspectionContext)
	if !ok || origin.request == nil || origin.projectID == "" {
		return core.KubernetesTargetEvidence{}, errors.New("select a project with an enrolled tenant worker")
	}
	identity, err := i.auth.Authenticate(origin.request.WithContext(ctx))
	if err != nil || identity.ID == "" || identity.SystemRole != core.UserRoleOwner {
		return core.KubernetesTargetEvidence{}, errors.New("tenant owner access is required to inspect a target")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	request := workflowrunner.Request{
		Version: workflowrunner.Version, Mode: "tenant", ProjectID: origin.projectID,
		ResourceID: ulid.Make().String(), RevisionID: ulid.Make().String(),
		TargetInspection: &workflowrunner.TargetInspection{Config: config, Kubeconfig: config.KubeconfigData, CertificateAuthority: config.CertificateAuthorityData},
	}
	if err := request.Validate(); err != nil {
		return core.KubernetesTargetEvidence{}, err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return core.KubernetesTargetEvidence{}, errors.New("encode target inspection")
	}
	pending := pendingTargetInspection{ctx: ctx, digest: sha256.Sum256(encoded), authorize: func(checkCtx context.Context) error {
		current, err := i.auth.Authenticate(origin.request.WithContext(checkCtx))
		if err != nil || current.ID != identity.ID || current.SystemRole != core.UserRoleOwner {
			return errors.New("tenant owner access to inspect this target was revoked")
		}
		return nil
	}}
	i.mu.Lock()
	i.pending[request.RevisionID] = pending
	i.mu.Unlock()
	defer func() {
		i.mu.Lock()
		delete(i.pending, request.RevisionID)
		i.mu.Unlock()
	}()
	result, err := i.runner.Run(ctx, request, nil)
	if err != nil {
		return core.KubernetesTargetEvidence{}, errors.New(request.Redact(err.Error()))
	}
	if err := i.authorize(ctx, request); err != nil {
		return core.KubernetesTargetEvidence{}, err
	}
	if result.State != "succeeded" || result.TargetEvidence == nil {
		if result.Error != "" {
			return core.KubernetesTargetEvidence{}, errors.New(request.Redact(result.Error))
		}
		return core.KubernetesTargetEvidence{}, errors.New("tenant worker could not inspect this target")
	}
	evidence := *result.TargetEvidence
	if evidence.ClusterUID == "" || evidence.NamespaceUID == "" || evidence.Version == "" || evidence.CheckedAt.IsZero() {
		return core.KubernetesTargetEvidence{}, errors.New("tenant worker returned incomplete target evidence")
	}
	return evidence, nil
}

func (i *hostedTargetInspector) authorize(ctx context.Context, request workflowrunner.Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.TargetInspection == nil {
		return errors.New("target inspection request is missing")
	}
	i.mu.Lock()
	pending, exists := i.pending[request.RevisionID]
	i.mu.Unlock()
	if !exists || pending.ctx.Err() != nil || ctx.Err() != nil {
		return errors.New("target inspection is no longer pending")
	}
	encoded, err := json.Marshal(request)
	if err != nil || sha256.Sum256(encoded) != pending.digest {
		return errors.New("target inspection request changed")
	}
	return pending.authorize(ctx)
}
