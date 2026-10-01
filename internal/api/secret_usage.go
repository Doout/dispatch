package api

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// Counts describe configured consumers, not executions or secret reads.
// Only reference metadata is inspected. Values are never resolved here.
type secretUsage struct {
	SecretID  string           `json:"secretId"`
	Consumers []secretConsumer `json:"consumers"`
	Archived  []secretConsumer `json:"archived"`
	Warnings  []string         `json:"warnings"`
}
type secretConsumer struct {
	ID           string                   `json:"id"`
	Kind         string                   `json:"kind"`
	Name         string                   `json:"name"`
	State        string                   `json:"state,omitempty"`
	References   []string                 `json:"references"`
	Applications []secretUsageApplication `json:"applications"`
}
type secretUsageApplication struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Target   string `json:"target"`
	Archived bool   `json:"archived"`
}

// Read the supported reference fields even when another part of a saved
// workflow is invalid. This also covers templates before their expansion.
type usageDocument struct {
	Spec struct {
		Jobs      map[string]usageJob `yaml:"jobs"`
		Finally   map[string]usageJob `yaml:"finally"`
		Provision usageJob            `yaml:"provision"`
		Stages    []struct {
			Checks map[string]struct {
				PipelineRef string `yaml:"pipelineRef"`
			} `yaml:"checks"`
		} `yaml:"stages"`
		Template    *usageDocument `yaml:"template"`
		Application *usageDocument `yaml:"application"`
	} `yaml:"spec"`
}
type usageJob struct {
	Secrets map[string]struct {
		SecretRef string `yaml:"secretRef"`
		Key       string `yaml:"key"`
	} `yaml:"secrets"`
}

func (d usageDocument) references() map[string][]string {
	result := map[string][]string{}
	add := func(label string, job usageJob) {
		for environment, binding := range job.Secrets {
			reference := label + " → " + environment
			if binding.Key != "" {
				reference += " · JSON key " + binding.Key
			}
			result[binding.SecretRef] = append(result[binding.SecretRef], reference)
		}
	}
	for name, job := range d.Spec.Jobs {
		add("Job "+name, job)
	}
	for name, job := range d.Spec.Finally {
		add("Finally "+name, job)
	}
	add("Provision", d.Spec.Provision)
	for _, nested := range []*usageDocument{d.Spec.Template, d.Spec.Application} {
		if nested != nil {
			for ref, locations := range nested.references() {
				result[ref] = append(result[ref], locations...)
			}
		}
	}
	return result
}

func (a *API) secretUsageIndex(ctx context.Context) ([]secretUsage, error) {
	secrets, err := a.store.ListSecrets(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := a.store.ListAppsForUsage(ctx)
	if err != nil {
		return nil, err
	}
	resources, err := a.store.ListWorkflowResources(ctx, "")
	if err != nil {
		return nil, err
	}
	sources, err := a.store.ListConfigSources(ctx)
	if err != nil {
		return nil, err
	}
	templates, err := a.store.ListWorkflowPreviewTemplates(ctx)
	if err != nil {
		return nil, err
	}
	serviceTemplates, err := a.store.ListSavedServiceTemplates(ctx)
	if err != nil {
		return nil, err
	}
	services, err := a.store.ListServices(ctx, "")
	if err != nil {
		return nil, err
	}
	servers, err := a.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	triggers, err := a.store.ListEventTriggers(ctx, "")
	if err != nil {
		return nil, err
	}
	groups, err := a.store.ListPreviewGroups(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]secretUsage, len(secrets))
	targets := map[string]string{}
	for _, server := range servers {
		targets[server.ID] = server.Name
	}
	resourceByID := map[string]core.WorkflowResource{}
	for _, resource := range resources {
		resourceByID[resource.ID] = resource
	}
	application := func(app core.App) secretUsageApplication {
		return secretUsageApplication{ID: app.ID, Name: app.Name, Target: targets[app.ServerID], Archived: app.State == "closed" || app.State == "removed" || resourceByID[app.HelmProvenance.WorkflowResourceID].State == "removed"}
	}
	appsByResource := map[string][]secretUsageApplication{}
	appsByID := map[string]secretUsageApplication{}
	for _, app := range apps {
		appsByID[app.ID] = application(app)
		if id := app.HelmProvenance.WorkflowResourceID; id != "" {
			appsByResource[id] = append(appsByResource[id], application(app))
		}
	}
	// A check pipeline is also a dependency of the application's deployments.
	pipelines := map[string][]string{}
	for _, resource := range resources {
		if resource.Kind == "Pipeline" {
			key := resource.ConfigSourceID + "/" + resource.Name
			pipelines[key] = append(pipelines[key], resource.ID)
		}
	}
	for _, resource := range resources {
		var document usageDocument
		if yaml.Unmarshal([]byte(resource.Document), &document) != nil {
			continue
		}
		for _, stage := range document.Spec.Stages {
			for _, check := range stage.Checks {
				for _, id := range pipelines[resource.ConfigSourceID+"/"+check.PipelineRef] {
					appsByResource[id] = append(appsByResource[id], appsByResource[resource.ID]...)
				}
			}
		}
	}
	byID := map[string]int{}
	for i, secret := range secrets {
		result[i] = secretUsage{SecretID: secret.ID, Consumers: []secretConsumer{}, Archived: []secretConsumer{}, Warnings: []string{}}
		byID[secret.ID] = i
	}
	resolve := func(ref string) (int, bool) {
		// Match the workflow runtime's ID or case-insensitive name resolution.
		for i, secret := range secrets {
			if secret.ID == ref || strings.EqualFold(secret.Name, ref) {
				return i, true
			}
		}
		return 0, false
	}
	add := func(ref string, named bool, consumer secretConsumer, archived bool, locations ...string) {
		i, ok := byID[ref]
		if named {
			i, ok = resolve(ref)
		}
		if !ok {
			return
		}
		list := &result[i].Consumers
		if archived {
			list = &result[i].Archived
		}
		for n := range *list {
			existing := &(*list)[n]
			if existing.ID == consumer.ID && existing.Kind == consumer.Kind {
				existing.References = append(existing.References, locations...)
				existing.Applications = append(existing.Applications, consumer.Applications...)
				return
			}
		}
		consumer.References = locations
		if consumer.Applications == nil {
			consumer.Applications = []secretUsageApplication{}
		}
		*list = append(*list, consumer)
	}
	document := func(raw string, consumer secretConsumer, archived bool) {
		var parsed usageDocument
		if err := yaml.Unmarshal([]byte(raw), &parsed); err != nil {
			// A parse failure must not look like a confirmed zero.
			for i := range result {
				result[i].Warnings = append(result[i].Warnings, "Could not inspect "+consumer.Name+" because its YAML is invalid.")
			}
			return
		}
		for ref, locations := range parsed.references() {
			add(ref, true, consumer, archived, locations...)
		}
	}
	for _, app := range apps {
		consumer := secretConsumer{ID: app.ID, Kind: "application", Name: app.Name, State: app.State, Applications: []secretUsageApplication{application(app)}}
		if app.SourceAuthType != deploy.SourceAuthGitHubApp {
			add(app.SourceCredentialID, false, consumer, application(app).Archived, "Repository checkout")
		}
		for _, id := range app.HookSecretIDs {
			add(id, false, consumer, application(app).Archived, "Deployment hooks")
		}
	}
	for _, resource := range resources {
		consumer := secretConsumer{ID: resource.ID, Kind: "workflow", Name: resource.Name, State: resource.State, Applications: appsByResource[resource.ID]}
		document(resource.Document, consumer, resource.State == "removed")
	}
	for _, template := range templates {
		document(template.Document, secretConsumer{ID: template.ID, Kind: "preview_template", Name: template.Name}, false)
	}
	for _, template := range serviceTemplates {
		document(template.Document, secretConsumer{ID: template.ID, Kind: "service_template", Name: template.Name}, false)
	}
	for _, source := range sources {
		add(source.CredentialSecretID, false, secretConsumer{ID: source.ID, Kind: "configuration", Name: source.Name, State: source.State}, false, "Configuration repository checkout")
	}
	if manager := a.infrastructureManager(); manager != nil {
		providers, err := manager.Store.ListInfrastructureProviders(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range providers {
			add(item.CredentialSecretID, false, secretConsumer{ID: item.ID, Kind: "infrastructure_provider", Name: item.Name, State: item.State}, false, "Provider authentication")
		}
	}
	for _, server := range servers {
		if server.Builder != nil {
			add(server.Builder.SSHSecretID, false, secretConsumer{ID: server.ID, Kind: "builder", Name: server.Name, State: server.State}, false, "Builder SSH authentication")
		}
	}
	for _, service := range services {
		bound, err := a.store.ListServiceConsumers(ctx, service.ID)
		if err != nil {
			return nil, err
		}
		consumer := secretConsumer{ID: service.ID, Kind: "service", Name: service.Name}
		for _, binding := range bound {
			if app, ok := appsByID[binding.AppID]; ok {
				consumer.Applications = append(consumer.Applications, app)
			}
		}
		for key, field := range service.Fields {
			add(field.SecretRef, false, consumer, false, "Connection field "+key)
		}
	}
	for _, trigger := range triggers {
		consumer := secretConsumer{ID: trigger.ID, Kind: "event_rule", Name: trigger.Repository + " " + trigger.Command}
		if app, ok := appsByID[trigger.AppID]; ok {
			consumer.Applications = []secretUsageApplication{app}
		}
		for _, id := range trigger.SecretIDs {
			add(id, false, consumer, false, "Preview hooks")
		}
	}
	for _, group := range groups {
		consumer := secretConsumer{ID: group.ID, Kind: "preview_group", Name: group.Name}
		for _, component := range group.Components {
			consumer.Applications = nil
			if app, ok := appsByID[component.AppID]; ok {
				consumer.Applications = []secretUsageApplication{app}
			}
			for _, id := range component.SecretIDs {
				add(id, false, consumer, false, "Component "+component.Alias+" hooks")
			}
		}
	}
	for i := range result {
		for _, list := range [][]secretConsumer{result[i].Consumers, result[i].Archived} {
			for n := range list {
				sort.Strings(list[n].References)
				list[n].References = uniqueStrings(list[n].References)
				sort.Slice(list[n].Applications, func(a, b int) bool { return list[n].Applications[a].ID < list[n].Applications[b].ID })
				applications := list[n].Applications[:0]
				for _, app := range list[n].Applications {
					if len(applications) == 0 || applications[len(applications)-1].ID != app.ID {
						applications = append(applications, app)
					}
				}
				list[n].Applications = applications
			}
			sort.Slice(list, func(a, b int) bool {
				if list[a].Name != list[b].Name {
					return list[a].Name < list[b].Name
				}
				return list[a].Kind+list[a].ID < list[b].Kind+list[b].ID
			})
		}
	}
	return result, nil
}
func uniqueStrings(items []string) []string {
	result := items[:0]
	for _, item := range items {
		if len(result) == 0 || result[len(result)-1] != item {
			result = append(result, item)
		}
	}
	return result
}
func (a *API) listSecretUsage(w http.ResponseWriter, r *http.Request) {
	items, err := a.secretUsageIndex(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, items)
}
func (a *API) secretUsageDeployments(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := a.store.GetSecret(r.Context(), id); err != nil {
		a.notFoundOrInternal(w, err, "Variable")
		return
	}
	usages, err := a.secretUsageIndex(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	appIDs := []string{}
	seen := map[string]bool{}
	for _, usage := range usages {
		if usage.SecretID == id {
			for _, consumers := range [][]secretConsumer{usage.Consumers, usage.Archived} {
				for _, consumer := range consumers {
					for _, app := range consumer.Applications {
						if !seen[app.ID] {
							appIDs = append(appIDs, app.ID)
							seen[app.ID] = true
						}
					}
				}
			}
		}
	}
	items, err := a.store.SearchDeploymentHistory(r.Context(), core.DeploymentSearch{AppIDs: appIDs, Before: r.URL.Query().Get("before"), Limit: 51})
	if err != nil {
		a.internal(w, err)
		return
	}
	next := ""
	if len(items) > 50 {
		items = items[:50]
		next = items[len(items)-1].ID
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		Items []core.Deployment `json:"items"`
		Next  string            `json:"next,omitempty"`
	}{items, next})
}
