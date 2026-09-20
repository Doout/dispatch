package api

import (
	"context"
	"sort"

	"github.com/doout/dispatch/internal/core"
)

// Saved service-bound deployment digests contain service revisions as well as
// application fields. Compare with its captured revisions so credential rotation
// does not look like an unrelated application edit.
func (a *API) deploymentAppInputsMatch(ctx context.Context, app core.App, d core.Deployment) (bool, error) {
	captured, err := a.store.GetDeploymentServiceBindings(ctx, d.ID)
	if err != nil {
		return false, err
	}
	sort.Slice(captured, func(i, j int) bool {
		if captured[i].Binding.ServiceRef == captured[j].Binding.ServiceRef {
			return captured[i].Binding.Alias < captured[j].Binding.Alias
		}
		return captured[i].Binding.ServiceRef < captured[j].Binding.ServiceRef
	})
	bindings := []core.ServiceBinding{}
	applied := []core.AppliedServiceBinding{}
	for _, c := range captured {
		bindings = append(bindings, c.Binding)
		applied = append(applied, core.AppliedServiceBinding{Alias: c.Binding.Alias, ServiceID: c.Service.ID, ServiceName: c.Service.Name, Revision: c.Service.Revision})
	}
	if d.SpecDigest != core.BoundDeploymentSpecDigest(app.SpecDigest(), bindings, applied) {
		return false, nil
	}
	current, err := a.store.GetAppServiceBindings(ctx, app.ID)
	if err != nil {
		return false, err
	}
	return core.ServiceBindingConfigurationDigest(current) == core.ServiceBindingConfigurationDigest(bindings), nil
}
func (a *API) reviewServiceConsumer(ctx context.Context, app core.App) (core.DeploymentReview, error) {
	bindings, err := a.store.GetAppServiceBindings(ctx, app.ID)
	if err != nil {
		return core.DeploymentReview{}, err
	}
	review := core.DeploymentReview{ProjectID: app.ProjectID, AppSpecDigest: app.SpecDigest(), BindingsDigest: core.ServiceBindingConfigurationDigest(bindings), ServiceRevisions: map[string]int64{}}
	for _, b := range bindings {
		service, err := a.store.GetService(ctx, b.ServiceRef)
		if err != nil {
			return review, err
		}
		review.ServiceRevisions[service.ID] = service.Revision
	}
	return review, nil
}
