package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// loadForCheck can recover old deployment inputs from Helm storage. It never
// adopts the current live object configuration as the desired baseline.
func (s *Service) loadForCheck(ctx context.Context, appID string) (core.DriftBaseline, core.Server, []*unstructured.Unstructured, bool, string, error) {
	d, err := s.Store.LatestSuccessfulDeployment(ctx, appID)
	if err != nil {
		return core.DriftBaseline{}, core.Server{}, nil, false, "", errors.New("No successful deployment is available.")
	}
	b := core.DriftBaseline{DeploymentID: d.ID, AppID: appID}
	if _, err = s.Store.GetDriftBaseline(ctx, d.ID); err == nil {
		baseline, server, objects, err := s.load(ctx, appID)
		return baseline, server, objects, true, "Compared fields saved with the last successful deployment.", err
	} else if !errors.Is(err, store.ErrNotFound) {
		return b, core.Server{}, nil, false, "", errors.New("The saved baseline could not be read.")
	}
	app, err := s.Store.GetApp(ctx, appID)
	if err != nil {
		return b, core.Server{}, nil, false, "", errors.New("The application is unavailable.")
	}
	if d.Snapshot.TargetID != "" && d.Snapshot.TargetID != app.ServerID {
		return b, core.Server{}, nil, false, "", errors.New("The application target changed. Deploy to the new target first.")
	}
	server, err := s.Store.GetServer(ctx, app.ServerID)
	if err != nil || server.Kubernetes == nil {
		return b, server, nil, false, "", errors.New("The Kubernetes deployment target is unavailable.")
	}
	b.ServerID = server.ID
	b.Namespace = d.Snapshot.Namespace
	b.Release = d.Snapshot.Release
	if b.Namespace == "" {
		b.Namespace = app.HelmNamespace
	}
	if b.Namespace == "" {
		b.Namespace = server.Kubernetes.Namespace
	}
	if b.Namespace == "" {
		b.Namespace = "default"
	}
	if b.Release == "" {
		b.Release = app.HelmRelease
	}
	if b.Release == "" {
		b.Release = app.Name
	}
	reader := s.ReadRelease
	if reader == nil {
		reader = deploy.ReadHelmDriftRelease
	}
	release, err := reader(ctx, server, b.Namespace, b.Release, d)
	if err != nil {
		return b, server, nil, false, "", err
	}
	objects, err := Parse(release.Manifest, b.Namespace)
	if err != nil {
		return b, server, nil, false, "", errors.New("Stored Helm resources could not be parsed for this check.")
	}
	if len(objects) == 0 {
		return b, server, nil, false, "", errors.New("The retained Helm release contains no runtime resources.")
	}
	for _, obj := range objects {
		stampHelmOwnership(obj, b.Release, b.Namespace)
	}
	message := fmt.Sprintf("Compared with Helm revision %d retained for the last successful deployment.", release.Revision)
	comparable := release.Matched && len(d.Snapshot.ServiceBindings) == 0
	if !release.Matched {
		message = "Health checked from retained Helm resources. Drift is unavailable because no Helm revision could be uniquely matched to this deployment's saved inputs and completion time."
	}
	if len(d.Snapshot.ServiceBindings) > 0 {
		message = "Health checked from retained Helm resources. Drift needs a new deployment because the original service credential baseline is unavailable."
	}
	if comparable && s.Vault != nil {
		raw, marshalErr := json.Marshal(objects)
		if marshalErr != nil {
			return b, server, nil, false, "", errors.New("The retained Helm baseline could not be encoded.")
		}
		b.Ciphertext, err = s.Vault.Encrypt(scope(d.ID), raw)
		if err != nil {
			return b, server, nil, false, "", errors.New("The retained Helm baseline could not be encrypted.")
		}
		if err = s.Store.SaveDriftBaseline(ctx, b); err != nil {
			return b, server, nil, false, "", errors.New("The retained Helm baseline could not be saved.")
		}
	}
	return b, server, objects, comparable, message, nil
}
func stampHelmOwnership(obj *unstructured.Unstructured, release, namespace string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["meta.helm.sh/release-name"] = release
	annotations["meta.helm.sh/release-namespace"] = namespace
	obj.SetAnnotations(annotations)
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["app.kubernetes.io/managed-by"] = "Helm"
	obj.SetLabels(labels)
}
