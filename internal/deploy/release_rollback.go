package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
	"helm.sh/helm/v3/pkg/action"
	helmrelease "helm.sh/helm/v3/pkg/release"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RollbackPreview struct {
	Available           bool                         `json:"available"`
	Message             string                       `json:"message"`
	DeploymentID        string                       `json:"deploymentId"`
	CurrentDeploymentID string                       `json:"currentDeploymentId"`
	HelmRevision        int                          `json:"helmRevision,omitempty"`
	Bindings            []core.AppliedServiceBinding `json:"bindings"`
	Resources           []ReleaseResource            `json:"resources"`
}

type ReleaseCapture func(context.Context, core.Deployment, core.App, core.Server, string) error

type rollbackPrepared struct {
	source         core.Deployment
	app            core.App
	server         core.Server
	client         *sdkHelmClient
	release        *helmrelease.Release
	secretManifest string
	cleanup        func()
}

func (s *Service) prepareRollback(ctx context.Context, id string) (rollbackPrepared, error) {
	out := rollbackPrepared{cleanup: func() {}}
	d, err := s.store.GetDeployment(ctx, id)
	if err != nil {
		return out, errors.New("The retained deployment is unavailable.")
	}
	out.source = d
	app, err := s.store.GetApp(ctx, d.AppID)
	if err != nil {
		return out, errors.New("The application is unavailable.")
	}
	if d.State != core.DeploymentSucceeded {
		return out, errors.New("Select a successful deployment to roll back to.")
	}
	if app.BuildType != core.BuildTypeHelm || d.Snapshot.Runtime == "" || d.Snapshot.Chart == "" {
		return out, errors.New("Historical rollback requires a retained Helm release. Docker and Compose image and runtime artifacts are not retained for rollback.")
	}
	if d.Snapshot.TargetID != app.ServerID || d.Snapshot.Namespace == "" || d.Snapshot.Release == "" {
		return out, errors.New("The target changed or historical target inputs are missing. Deploy to the intended target first.")
	}
	server, err := s.store.GetServer(ctx, d.Snapshot.TargetID)
	if err != nil || server.Kubernetes == nil {
		return out, errors.New("The retained Kubernetes target is unavailable.")
	}
	if helmNamespace(app, server) != d.Snapshot.Namespace || helmReleaseName(app) != d.Snapshot.Release {
		return out, errors.New("The application's release name or namespace changed. Historical rollback cannot change targets.")
	}
	prepared, cleanup, err := prepareKubernetesServer(server)
	if err != nil {
		return out, errors.New("Target credentials are unavailable.")
	}
	dir, err := os.MkdirTemp("", "dispatch-rollback-")
	if err != nil {
		cleanup()
		return out, errors.New("Cannot prepare release validation.")
	}
	out.cleanup = func() { cleanup(); _ = os.RemoveAll(dir) }
	genericClient, err := newSDKHelmClient(prepared, d.Snapshot.Namespace, dir)
	if err != nil {
		return out, errors.New("Cannot initialize retained release access.")
	}
	client := genericClient.(*sdkHelmClient)
	getter := driftRESTGetter{RESTClientGetter: client.settings.RESTClientGetter(), ctx: ctx}
	if client.configuration.Init(getter, d.Snapshot.Namespace, os.Getenv("HELM_DRIVER"), func(string, ...any) {}) != nil {
		return out, errors.New("Cannot read retained Helm releases.")
	}
	history, err := action.NewHistory(client.configuration).Run(d.Snapshot.Release)
	if err != nil {
		return out, errors.New("Retained Helm history is unavailable. Check target access and retention.")
	}
	bindings, err := s.store.GetDeploymentServiceBindings(ctx, d.ID)
	if err != nil {
		return out, errors.New("Captured service binding metadata is unavailable.")
	}
	matched, err := selectRollbackRelease(history, d, bindings)
	if err != nil || !matched.Matched {
		return out, errors.New("No unique retained Helm revision matches this deployment's saved inputs and completion time. Rollback cannot reconstruct credentials from redacted history.")
	}
	var selected *helmrelease.Release
	for _, item := range history {
		if item.Version == matched.Revision {
			selected = item
			break
		}
	}
	if selected == nil || selected.Chart == nil {
		return out, errors.New("The original chart artifact is no longer retained.")
	}
	if len(bindings) != len(d.Snapshot.ServiceBindings) {
		return out, errors.New("Captured service binding inputs are incomplete.")
	}
	if len(bindings) > 0 {
		kubeClient, err := serviceKubeClient(prepared)
		if err != nil {
			return out, errors.New("Cannot validate retained service credentials.")
		}
		for _, binding := range bindings {
			if binding.Service.ProjectID != app.ProjectID {
				return out, errors.New("A retained service is outside this application's project.")
			}
			name := retainedServiceSecretName(selected.Config, binding.Binding)
			if name == "" {
				return out, errors.New("The original service Secret reference is unavailable.")
			}
			secret, err := kubeClient.CoreV1().Secrets(d.Snapshot.Namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil || secret.Labels["dispatch.app"] != app.ID || secret.Labels["dispatch.release"] != d.Snapshot.Release || secret.Labels["dispatch.service-binding"] != "true" || secret.Immutable == nil || !*secret.Immutable {
				return out, errors.New("An original immutable service Secret is missing or no longer owned by this application. Restore it securely before rollback.")
			}
			for key := range binding.Binding.Helm.Keys {
				if _, ok := secret.Data[key]; !ok {
					return out, errors.New("An original service Secret is missing a required key.")
				}
			}
			secret.TypeMeta.APIVersion, secret.TypeMeta.Kind = "v1", "Secret"
			secret.ResourceVersion = ""
			secret.UID = ""
			secret.ManagedFields = nil
			raw, err := json.Marshal(secret)
			if err != nil {
				return out, errors.New("Cannot retain service credential identities.")
			}
			out.secretManifest += "\n---\n" + string(raw)
		}
	}
	out.app, out.server, out.client, out.release = app, server, client, selected
	return out, nil
}

// Service reference destinations override supplied values. Older snapshots were
// taken before those references were injected, so compare the remaining captured
// values and validate each retained immutable Secret separately.
func selectRollbackRelease(history []*helmrelease.Release, d core.Deployment, bindings []core.CapturedServiceBinding) (HelmDriftRelease, error) {
	var selected *helmrelease.Release
	matches := 0
	normalize := func(values map[string]any) []byte {
		raw, _ := json.Marshal(redactSnapshotValues("", values))
		var clean map[string]any
		_ = json.Unmarshal(raw, &clean)
		for _, binding := range bindings {
			if binding.Binding.Helm == nil {
				continue
			}
			paths := append([]string{}, binding.Binding.Helm.SecretNameValues...)
			for path := range binding.Binding.Helm.KeyValues {
				paths = append(paths, path)
			}
			for _, path := range paths {
				removeReleaseValue(clean, strings.Split(path, "."))
			}
		}
		raw, _ = json.Marshal(clean)
		return raw
	}
	expected := normalize(d.Snapshot.Values)
	for _, candidate := range history {
		if candidate == nil || candidate.Info == nil || candidate.Name != d.Snapshot.Release || candidate.Namespace != d.Snapshot.Namespace || d.FinishedAt == nil {
			continue
		}
		if candidate.Info.Status != helmrelease.StatusDeployed && candidate.Info.Status != helmrelease.StatusSuperseded {
			continue
		}
		if candidate.Info.LastDeployed.Time.Before(d.CreatedAt) || candidate.Info.LastDeployed.Time.After(*d.FinishedAt) {
			continue
		}
		if bytes.Equal(normalize(candidate.Config), expected) {
			selected = candidate
			matches++
		}
	}
	if matches != 1 || selected.Manifest == "" {
		return HelmDriftRelease{}, errors.New("No unique retained release matches the captured deployment.")
	}
	return HelmDriftRelease{Manifest: selected.Manifest, Revision: selected.Version, Matched: true}, nil
}
func removeReleaseValue(values map[string]any, parts []string) {
	if len(parts) == 0 || values == nil {
		return
	}
	if len(parts) == 1 {
		delete(values, parts[0])
		return
	}
	if nested, ok := values[parts[0]].(map[string]any); ok {
		removeReleaseValue(nested, parts[1:])
		if len(nested) == 0 {
			delete(values, parts[0])
		}
	}
}

func retainedServiceSecretName(values map[string]any, binding core.ServiceBinding) string {
	if binding.Helm == nil || len(binding.Helm.SecretNameValues) == 0 {
		return ""
	}
	name := ""
	for _, path := range binding.Helm.SecretNameValues {
		var value any = values
		for _, key := range strings.Split(path, ".") {
			object, ok := value.(map[string]any)
			if !ok {
				return ""
			}
			value = object[key]
		}
		current, ok := value.(string)
		if !ok || !strings.HasPrefix(current, "dispatch-svc-") || name != "" && name != current {
			return ""
		}
		name = current
	}
	return name
}

func (s *Service) PreviewRollback(ctx context.Context, id string) (RollbackPreview, error) {
	d, err := s.store.GetDeployment(ctx, id)
	if err != nil {
		return RollbackPreview{}, err
	}
	out := RollbackPreview{DeploymentID: id, Bindings: d.Snapshot.ServiceBindings, Resources: []ReleaseResource{}, Message: "Rollback does not undo database migrations or external side effects. Deployment and chart hooks will not run."}
	err = s.WithIdleApplication(ctx, d.AppID, func() error {
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		current, err := s.store.LatestSuccessfulDeployment(ctx, d.AppID)
		if err != nil {
			return errors.New("The currently running release is unavailable.")
		}
		out.CurrentDeploymentID = current.ID
		prepared, err := s.prepareRollback(ctx, id)
		defer prepared.cleanup()
		if err != nil {
			out.Message = err.Error()
			return nil
		}
		out.HelmRevision = prepared.release.Version
		out.Resources, err = dryRunReleaseManifest(ctx, prepared.client, prepared.source.Snapshot.Namespace, prepared.release.Manifest)
		if err != nil {
			out.Message = err.Error()
			return nil
		}
		out.Available = true
		return nil
	})
	return out, err
}

// StartRollback reserves the application before validation or runtime writes.
// Native Helm rollback reuses the original retained chart/config and Secrets.
func (s *Service) StartRollback(ctx context.Context, id, expectedCurrent, actor string, capture ReleaseCapture) (core.Deployment, error) {
	source, err := s.store.GetDeployment(ctx, id)
	if err != nil {
		return core.Deployment{}, err
	}
	unlock := s.lockApp(source.AppID)
	defer unlock()
	active, err := s.store.ActiveDeploymentForApp(ctx, source.AppID)
	if err != nil {
		return core.Deployment{}, err
	}
	if active != nil {
		return core.Deployment{}, ErrDeploymentActive
	}
	current, err := s.store.LatestSuccessfulDeployment(ctx, source.AppID)
	if err != nil || expectedCurrent == "" || current.ID != expectedCurrent {
		return core.Deployment{}, errors.New("The running release changed. Review the rollback again.")
	}
	data, ok := s.store.(store.ReleaseStore)
	if !ok {
		return core.Deployment{}, errors.New("Release history storage is unavailable.")
	}
	validation, cancel := context.WithTimeout(ctx, 45*time.Second)
	prepared, err := s.prepareRollback(validation, id)
	if err == nil {
		_, err = dryRunReleaseManifest(validation, prepared.client, source.Snapshot.Namespace, prepared.release.Manifest)
	}
	prepared.cleanup()
	cancel()
	if err != nil {
		return core.Deployment{}, err
	}
	now := time.Now().UTC()
	lease := now.Add(10 * time.Minute)
	d := core.Deployment{ID: ulid.Make().String(), AppID: source.AppID, CommitSHA: source.CommitSHA, SpecDigest: source.SpecDigest, Snapshot: source.Snapshot, State: core.DeploymentQueued, Message: "Historical rollback accepted", CreatedAt: now, LeaseUntil: &lease}
	action := core.ReleaseAction{ID: ulid.Make().String(), ProjectID: prepared.app.ProjectID, AppID: source.AppID, DeploymentID: d.ID, SourceDeploymentID: source.ID, Actor: actor, Action: "deployment.rollback", Message: "Rollback accepted from retained successful deployment " + source.ID, CreatedAt: now}
	if err = data.CreateRollbackDeployment(ctx, d, source, action); err != nil {
		return core.Deployment{}, err
	}
	jobCtx, jobCancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancels[d.ID] = jobCancel
	s.mu.Unlock()
	go s.runRollback(jobCtx, d, source.ID, capture)
	return s.store.GetDeployment(ctx, d.ID)
}

func (s *Service) runRollback(ctx context.Context, d core.Deployment, sourceID string, capture ReleaseCapture) {
	defer func() { s.mu.Lock(); delete(s.cancels, d.ID); s.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, helmOperationTimeout+time.Minute)
	defer cancel()
	prepared, err := s.prepareRollback(ctx, sourceID)
	defer prepared.cleanup()
	if err != nil {
		s.fail(d, err)
		return
	}
	if _, err = dryRunReleaseManifest(ctx, prepared.client, prepared.source.Snapshot.Namespace, prepared.release.Manifest); err != nil {
		s.fail(d, err)
		return
	}
	now := time.Now().UTC()
	d.StartedAt = &now
	if err = s.transition(ctx, &d, core.DeploymentStarting, "Restoring retained Helm release; hooks are disabled"); err != nil {
		return
	}
	rollback := action.NewRollback(prepared.client.configuration)
	rollback.Version = prepared.release.Version
	rollback.DisableHooks = true
	rollback.Wait = true
	rollback.WaitForJobs = true
	rollback.Timeout = helmOperationTimeout
	if err = rollback.Run(prepared.source.Snapshot.Release); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			s.finish(&d, core.DeploymentCancelled, "Rollback cancelled; inspect release status before retrying")
		} else {
			s.fail(d, errors.New("Helm rollback failed. Inspect workload readiness and Helm history before retrying; original retained credentials were preserved."))
		}
		return
	}
	if capture != nil {
		if err = capture(ctx, d, prepared.app, prepared.server, prepared.release.Manifest+prepared.secretManifest); err != nil {
			_ = s.log(ctx, d.ID, "warning", "Rollback applied; the drift baseline could not be saved. Check target access and encrypted storage.")
		}
	}
	s.finish(&d, core.DeploymentSucceeded, "Retained release restored; database migrations and external side effects were not reverted")
}
