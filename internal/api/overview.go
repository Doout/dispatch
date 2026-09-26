package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/doout/dispatch/internal/core"
	workflowservice "github.com/doout/dispatch/internal/workflow"
)

func (a *API) overview(w http.ResponseWriter, r *http.Request) {
	overview, err := a.overviewData(r)
	if err != nil {
		a.internal(w, err)
		return
	}
	data, err := json.Marshal(overview)
	if err != nil {
		a.internal(w, err)
		return
	}
	w.Header().Set("X-Overview-Version", a.overviewSnapshots.remember(overviewScope(r), data))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (a *API) overviewData(r *http.Request) (core.Overview, error) {
	projects, err := a.store.ListProjects(r.Context())
	if err != nil {
		return core.Overview{}, err
	}
	servers, err := a.store.ListServers(r.Context())
	if err != nil {
		return core.Overview{}, err
	}
	apps, err := a.store.ListApps(r.Context())
	if err != nil {
		return core.Overview{}, err
	}
	deployments, err := a.store.ListDeploymentSummaries(r.Context(), 100)
	if err != nil {
		return core.Overview{}, err
	}
	eventTriggers, err := a.store.ListEventTriggers(r.Context(), "")
	if err != nil {
		return core.Overview{}, err
	}
	previews, err := a.store.ListPreviewEnvironments(r.Context(), "")
	if err != nil {
		return core.Overview{}, err
	}
	previewGroups, err := a.store.ListPreviewGroups(r.Context())
	if err != nil {
		return core.Overview{}, err
	}
	previewGroupRuns, err := a.store.ListPreviewGroupRuns(r.Context(), "")
	if err != nil {
		return core.Overview{}, err
	}
	secrets, err := a.store.ListSecrets(r.Context())
	if err != nil {
		return core.Overview{}, err
	}
	secretStores, err := a.store.ListSecretStores(r.Context())
	if err != nil {
		return core.Overview{}, err
	}
	privateNetworks, err := a.store.ListPrivateNetworks(r.Context())
	if err != nil {
		return core.Overview{}, err
	}
	githubApps, err := a.store.ListGitHubApps(r.Context())
	if err != nil {
		return core.Overview{}, err
	}
	relayWebhooks, err := a.store.ListRelayWebhooks(r.Context(), "")
	if err != nil {
		return core.Overview{}, err
	}
	configSources, err := a.store.ListConfigSources(r.Context())
	if err != nil {
		return core.Overview{}, err
	}
	workflowPreviewTemplates, err := a.store.ListWorkflowPreviewTemplates(r.Context())
	if err != nil {
		return core.Overview{}, err
	}
	workflowResources, err := a.store.ListWorkflowResources(r.Context(), "")
	if err != nil {
		return core.Overview{}, err
	}
	currentWorkflowResources := workflowResources[:0]
	for _, resource := range workflowResources {
		if resource.State != "removed" {
			currentWorkflowResources = append(currentWorkflowResources, resource)
		}
	}
	workflowResources = currentWorkflowResources
	for index := range workflowResources {
		documents, parseErr := workflowservice.Parse(workflowResources[index].Path, []byte(workflowResources[index].Document))
		if parseErr != nil || len(documents) != 1 {
			continue
		}
		if documents[0].Spec != nil {
			workflowResources[index].SourceCount = len(documents[0].Spec.Sources)
			workflowResources[index].JobCount = len(documents[0].Spec.Jobs)
			for _, stage := range documents[0].Spec.Stages {
				workflowResources[index].StageNames = append(workflowResources[index].StageNames, stage.Name)
				if !slices.Contains(workflowResources[index].TargetRefs, stage.TargetRef) {
					workflowResources[index].TargetRefs = append(workflowResources[index].TargetRefs, stage.TargetRef)
				}
			}
		} else if documents[0].Pipeline != nil {
			workflowResources[index].SourceCount = len(documents[0].Pipeline.Sources)
			workflowResources[index].JobCount = len(documents[0].Pipeline.Jobs)
		}
	}
	workflowRevisions, err := a.store.ListWorkflowRevisions(r.Context(), "", 100)
	if err != nil {
		return core.Overview{}, err
	}
	workflowStageRuns, err := a.store.ListWorkflowStageRuns(r.Context(), "")
	if err != nil {
		return core.Overview{}, err
	}
	serviceItems, err := a.store.ListServices(r.Context(), "")
	if err != nil {
		return core.Overview{}, err
	}
	services := []core.ServiceOverview{}
	for _, item := range serviceItems {
		value, err := a.serviceResponse(r.Context(), item, currentIdentity(r.Context()).SystemRole == core.UserRoleOwner)
		if err != nil {
			return core.Overview{}, err
		}
		services = append(services, value)
	}
	settings, err := a.store.GetControllerSettings(r.Context())
	if err != nil {
		return core.Overview{}, err
	}
	overview := core.Overview{ControllerSettings: settings, Services: services, Demo: a.demo, SecretStorageConfigured: a.eventConfig.Vault != nil, Identity: currentIdentity(r.Context()), Projects: projects, Servers: servers, Apps: apps, Deployments: deployments,
		EventTriggers: eventTriggers, Previews: previews, PreviewGroups: previewGroups, PreviewGroupRuns: previewGroupRuns, Secrets: secrets, SecretStores: secretStores, PrivateNetworks: privateNetworks, GitHubApps: githubApps, RelayWebhooks: relayWebhooks,
		ConfigSources: configSources, WorkflowPreviewTemplates: workflowPreviewTemplates, WorkflowResources: workflowResources, WorkflowRevisions: workflowRevisions, WorkflowStageRuns: workflowStageRuns}
	if impersonator, ok := currentImpersonator(r.Context()); ok {
		overview.Impersonator = &impersonator
	}
	overview, err = a.filterOverview(r.Context(), overview)
	if err != nil {
		return core.Overview{}, err
	}
	if err := a.enrichWorkflowEvaluations(r.Context(), overview.WorkflowResources); err != nil {
		return core.Overview{}, err
	}
	if err := a.enrichWorkflowPreviewPullRequests(r.Context(), overview.WorkflowResources); err != nil {
		return core.Overview{}, err
	}
	return overview, nil
}

func (a *API) RunWorkflowPoller(ctx context.Context) {
	if a.workflows != nil {
		a.workflows.RunPoller(ctx)
	}
}
