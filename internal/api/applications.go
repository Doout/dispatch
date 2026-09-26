package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
	"gopkg.in/yaml.v3"
)

func (a *API) listApps(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListApps(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	visible, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	filtered := items[:0]
	member := currentIdentity(r.Context()).SystemRole != core.UserRoleOwner
	for _, item := range items {
		if visible[item.ProjectID] {
			if member {
				item = redactAppCredentials(item)
			}
			filtered = append(filtered, item)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

type createAppRequest struct {
	ProjectID, ServerID, Name, SourceRepo, Branch, BuildType, ContextPath, DockerfilePath, ComposePath, ComposeContent, Domain string
	SourceAuthType, SourceCredentialID                                                                                         string
	HelmChart, HelmVersion, HelmRepository, HelmValues, HelmNamespace, HelmRelease                                             string
	HelmValueOverrides                                                                                                         map[string]interface{}
	PreDeployHook, PostDeployHook                                                                                              string
	ContainerPort                                                                                                              int
	Template                                                                                                                   bool
}

const maxComposeContentBytes = 512 * 1024

const maxHookBytes = 64 * 1024

func (a *API) createApp(w http.ResponseWriter, r *http.Request) {
	var input createAppRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.SourceRepo = strings.TrimSpace(input.SourceRepo)
	input.SourceAuthType = strings.TrimSpace(input.SourceAuthType)
	input.SourceCredentialID = strings.TrimSpace(input.SourceCredentialID)
	input.ComposeContent = strings.TrimSpace(input.ComposeContent)
	input.HelmChart = strings.TrimSpace(input.HelmChart)
	input.HelmVersion = strings.TrimSpace(input.HelmVersion)
	input.HelmRepository = strings.TrimSpace(input.HelmRepository)
	input.HelmNamespace = strings.TrimSpace(input.HelmNamespace)
	input.HelmRelease = strings.TrimSpace(input.HelmRelease)
	input.PreDeployHook = strings.TrimSpace(input.PreDeployHook)
	input.PostDeployHook = strings.TrimSpace(input.PostDeployHook)
	directCompose := input.ComposeContent != ""
	helmApplication := input.BuildType == string(core.BuildTypeHelm)
	if input.HelmValueOverrides != nil {
		if strings.TrimSpace(input.HelmValues) != "" {
			problem(w, http.StatusBadRequest, "Choose one Helm values format", "Send structured Helm value overrides or raw Helm values, not both.")
			return
		}
		encoded, err := encodeHelmValueOverrides(input.HelmValueOverrides)
		if err != nil {
			problem(w, http.StatusBadRequest, "Helm values unavailable", err.Error())
			return
		}
		input.HelmValues = encoded
	}
	if input.ProjectID == "" || input.ServerID == "" || input.Name == "" {
		problem(w, http.StatusBadRequest, "Application details required", "Choose a project and server, then enter an application name.")
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectConfigure, input.ProjectID) {
		return
	}
	if !a.requireCredentialOwner(w, r, input.SourceCredentialID != "") {
		return
	}
	if input.SourceRepo == "" && !directCompose && !helmApplication {
		problem(w, http.StatusBadRequest, "Application source required", "Enter a repository URL, paste a Docker Compose file, or configure a Helm chart.")
		return
	}
	if detail := validateSourceAuthentication(input.SourceRepo, input.SourceAuthType, input.SourceCredentialID); detail != "" {
		problem(w, http.StatusBadRequest, "Invalid source authentication", detail)
		return
	}
	if input.SourceCredentialID != "" {
		if a.eventConfig.Vault == nil {
			problem(w, http.StatusServiceUnavailable, "Secret storage is not configured", "Set DISPATCH_MASTER_KEY_FILE before attaching repository credentials.")
			return
		}
		if input.SourceAuthType == deploy.SourceAuthGitHubApp {
			connection, err := a.store.GetGitHubApp(r.Context(), input.SourceCredentialID)
			if err != nil {
				a.notFoundOrInternal(w, err, "GitHub App")
				return
			}
			if connection.State != "ready" {
				problem(w, http.StatusConflict, "GitHub App not ready", "Install and verify this GitHub App before using it for a repository.")
				return
			}
			if err := githubapp.ValidateRepositoryHost(input.SourceRepo, connection.WebURL); err != nil {
				problem(w, http.StatusBadRequest, "GitHub App host mismatch", err.Error())
				return
			}
		} else {
			secret, err := a.store.GetSecret(r.Context(), input.SourceCredentialID)
			if err != nil {
				a.notFoundOrInternal(w, err, "Source credential")
				return
			}
			if err := deploy.ValidateSourceCredentialType(input.SourceAuthType, secret.Type); err != nil {
				problem(w, http.StatusBadRequest, "Invalid source credential", err.Error())
				return
			}
		}
	}
	if len(input.ComposeContent) > maxComposeContentBytes || len(input.HelmValues) > maxComposeContentBytes {
		problem(w, http.StatusRequestEntityTooLarge, "Application definition too large", "Keep Compose content and Helm values under 512 KB.")
		return
	}
	if len(input.PreDeployHook) > maxHookBytes || len(input.PostDeployHook) > maxHookBytes || strings.ContainsRune(input.PreDeployHook+input.PostDeployHook, 0) {
		problem(w, http.StatusRequestEntityTooLarge, "Deployment hook too large", "Keep each deployment hook under 64 KB and use plain shell text.")
		return
	}
	if _, err := a.store.GetProject(r.Context(), input.ProjectID); err != nil {
		a.notFoundOrInternal(w, err, "Project")
		return
	}
	server, err := a.store.GetServer(r.Context(), input.ServerID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	if server.State != "ready" {
		problem(w, http.StatusConflict, "Server not ready", "Choose a ready server or complete its enrollment before defining an application.")
		return
	}
	if helmApplication {
		candidate := core.App{BuildType: core.BuildTypeHelm, SourceRepo: input.SourceRepo, SourceAuthType: input.SourceAuthType,
			HelmChart: input.HelmChart, HelmRepository: input.HelmRepository}
		if err := deploy.ValidateHelmTarget(candidate, server); err != nil {
			problem(w, http.StatusBadRequest, "Helm configuration unavailable", err.Error())
			return
		}
	} else if server.Runtime != core.ServerRuntimeDocker {
		problem(w, http.StatusBadRequest, "Docker server required", "Dockerfile and Compose applications require a Docker server.")
		return
	}
	if directCompose {
		input.BuildType = string(core.BuildTypeCompose)
		input.Branch = ""
		input.ComposePath = "compose.yml"
	} else if helmApplication && input.SourceRepo == "" {
		input.Branch = ""
	} else if input.Branch == "" {
		input.Branch = "main"
	}
	if input.BuildType == "" {
		input.BuildType = string(core.BuildTypeDockerfile)
	}
	if input.BuildType != string(core.BuildTypeDockerfile) && input.BuildType != string(core.BuildTypeCompose) && input.BuildType != string(core.BuildTypeHelm) {
		problem(w, http.StatusBadRequest, "Build type unavailable", "Use dockerfile, compose, or helm.")
		return
	}
	if input.ContextPath == "" {
		input.ContextPath = "."
	}
	if input.DockerfilePath == "" {
		input.DockerfilePath = "Dockerfile"
	}
	if input.ComposePath == "" {
		input.ComposePath = "compose.yml"
	}
	state := "ready"
	if input.Template {
		state = "template"
	}
	item := core.App{ID: ulid.Make().String(), ProjectID: input.ProjectID, ServerID: input.ServerID, Name: input.Name,
		SourceRepo: input.SourceRepo, Branch: input.Branch, SourceAuthType: input.SourceAuthType, SourceCredentialID: input.SourceCredentialID,
		BuildType: core.BuildType(input.BuildType), ContextPath: input.ContextPath,
		DockerfilePath: input.DockerfilePath, ComposePath: input.ComposePath, ComposeContent: input.ComposeContent, ContainerPort: input.ContainerPort,
		HelmChart: input.HelmChart, HelmVersion: input.HelmVersion, HelmRepository: input.HelmRepository, HelmValues: input.HelmValues,
		HelmNamespace: input.HelmNamespace, HelmRelease: input.HelmRelease, PreDeployHook: input.PreDeployHook,
		PostDeployHook: input.PostDeployHook, Domain: strings.TrimSpace(input.Domain), Template: input.Template, State: state, CreatedAt: time.Now().UTC()}
	if err := a.store.CreateApp(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

type updateAppHooksRequest struct {
	PreDeployHook  string   `json:"preDeployHook"`
	PostDeployHook string   `json:"postDeployHook"`
	SecretIDs      []string `json:"secretIds"`
}

func (a *API) updateAppHooks(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	if item.Generated {
		problem(w, http.StatusConflict, "Generated application cannot be edited", "Update hooks on its application source or event rule instead.")
		return
	}
	var input updateAppHooksRequest
	if !decode(w, r, &input) {
		return
	}
	if currentIdentity(r.Context()).SystemRole != core.UserRoleOwner {
		if len(input.SecretIDs) > 0 {
			problem(w, http.StatusForbidden, "Credential access denied", "A controller owner must change hook credentials.")
			return
		}
		input.SecretIDs = append([]string(nil), item.HookSecretIDs...)
	}
	if err := validateEventHooks(input.PreDeployHook, input.PostDeployHook); err != nil {
		problem(w, http.StatusBadRequest, "Invalid deployment hook", err.Error())
		return
	}
	if err := a.validateSecretIDs(r.Context(), input.SecretIDs); err != nil {
		problem(w, http.StatusBadRequest, "Invalid secret binding", err.Error())
		return
	}
	item.PreDeployHook = strings.TrimSpace(input.PreDeployHook)
	item.PostDeployHook = strings.TrimSpace(input.PostDeployHook)
	item.HookEnvironment = make(map[string]string, len(input.SecretIDs))
	item.HookSecretIDs = append([]string(nil), input.SecretIDs...)
	for _, id := range input.SecretIDs {
		secret, err := a.store.GetSecret(r.Context(), id)
		if err != nil {
			a.notFoundOrInternal(w, err, "Build credential")
			return
		}
		item.HookEnvironment[core.SecretEnvironmentKey(secret.ID, secret.EnvironmentVariable)] = secret.EncryptedValue
	}
	if err := a.store.UpdateApp(r.Context(), item); err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

type inspectHelmSourceRequest struct {
	ProjectID          string `json:"projectId"`
	SourceRepo         string `json:"sourceRepo"`
	Branch             string `json:"branch"`
	ChartPath          string `json:"chartPath"`
	SourceAuthType     string `json:"sourceAuthType"`
	SourceCredentialID string `json:"sourceCredentialId"`
}

func (a *API) inspectHelmSource(w http.ResponseWriter, r *http.Request) {
	var input inspectHelmSourceRequest
	if !decode(w, r, &input) {
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectConfigure, strings.TrimSpace(input.ProjectID)) {
		return
	}
	if !a.requireCredentialOwner(w, r, strings.TrimSpace(input.SourceCredentialID) != "") {
		return
	}
	input.SourceRepo, input.Branch, input.ChartPath = deploy.NormalizeGitHelmSource(input.SourceRepo, input.Branch, input.ChartPath)
	input.SourceAuthType = strings.TrimSpace(input.SourceAuthType)
	input.SourceCredentialID = strings.TrimSpace(input.SourceCredentialID)
	input.SourceRepo = deploy.RepositoryForSourceAuth(input.SourceRepo, input.SourceAuthType)
	if input.Branch == "" {
		input.Branch = "main"
	}
	if input.SourceRepo == "" || input.ChartPath == "" {
		problem(w, http.StatusBadRequest, "Helm source required", "Enter a repository URL and chart directory, or paste a GitHub folder URL.")
		return
	}
	if detail := validateSourceAuthentication(input.SourceRepo, input.SourceAuthType, input.SourceCredentialID); detail != "" {
		problem(w, http.StatusBadRequest, "Invalid source authentication", detail)
		return
	}
	app := core.App{SourceRepo: input.SourceRepo, Branch: input.Branch, SourceAuthType: input.SourceAuthType,
		SourceCredentialID: input.SourceCredentialID, BuildType: core.BuildTypeHelm, HelmChart: input.ChartPath}
	if input.SourceCredentialID != "" {
		if a.eventConfig.Vault == nil {
			problem(w, http.StatusServiceUnavailable, "Secret storage is not configured", "Set DISPATCH_MASTER_KEY_FILE before inspecting private repositories.")
			return
		}
		if input.SourceAuthType == deploy.SourceAuthGitHubApp {
			connection, err := a.store.GetGitHubApp(r.Context(), input.SourceCredentialID)
			if err != nil {
				a.notFoundOrInternal(w, err, "GitHub App")
				return
			}
			if connection.State != "ready" {
				problem(w, http.StatusConflict, "GitHub App not ready", "Install and verify this GitHub App before loading the chart.")
				return
			}
			if err := githubapp.ValidateRepositoryHost(input.SourceRepo, connection.WebURL); err != nil {
				problem(w, http.StatusBadRequest, "GitHub App host mismatch", err.Error())
				return
			}
		}
		resolved, err := (deploy.SourceAuthExecutor{Secrets: a.store, Vault: a.eventConfig.Vault, Resolver: a.secretResolver, GitHubApps: a.eventConfig.GitHubApps}).Resolve(r.Context(), app)
		if err != nil {
			problem(w, http.StatusBadRequest, "Repository credential unavailable", err.Error())
			return
		}
		app = resolved
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	inspection, err := deploy.InspectGitHelmSource(ctx, app)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		problem(w, status, "Helm chart could not be loaded", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, inspection)
}

func encodeHelmValueOverrides(values map[string]interface{}) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	encoded, err := yaml.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode structured Helm values: %w", err)
	}
	if len(encoded) > maxComposeContentBytes {
		return "", errors.New("structured Helm values exceed 512 KB")
	}
	return string(encoded), nil
}

type appHelmValuesResponse struct {
	deploy.HelmChartInspection
	Overrides map[string]interface{} `json:"overrides"`
}

func (a *API) getAppHelmValues(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	if item.BuildType != core.BuildTypeHelm {
		problem(w, http.StatusConflict, "Helm values unavailable", "This application does not deploy a Helm chart.")
		return
	}
	item.SourceRepo = deploy.RepositoryForSourceAuth(item.SourceRepo, item.SourceAuthType)
	resolved, err := (deploy.SourceAuthExecutor{Secrets: a.store, Vault: a.eventConfig.Vault, Resolver: a.secretResolver, GitHubApps: a.eventConfig.GitHubApps}).Resolve(r.Context(), item)
	if err != nil {
		problem(w, http.StatusBadRequest, "Repository credential unavailable", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	inspection, err := deploy.InspectHelmSource(ctx, resolved)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		problem(w, status, "Helm chart could not be loaded", err.Error())
		return
	}
	overrides := map[string]interface{}{}
	if strings.TrimSpace(item.HelmValues) != "" {
		if err := yaml.Unmarshal([]byte(item.HelmValues), &overrides); err != nil {
			problem(w, http.StatusUnprocessableEntity, "Saved Helm values are invalid", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, appHelmValuesResponse{HelmChartInspection: inspection, Overrides: overrides})
}

type updateAppHelmValuesRequest struct {
	Overrides map[string]interface{} `json:"overrides"`
}

func (a *API) updateAppHelmValues(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	if item.BuildType != core.BuildTypeHelm {
		problem(w, http.StatusConflict, "Helm values unavailable", "This application does not deploy a Helm chart.")
		return
	}
	if item.Generated {
		problem(w, http.StatusConflict, "Generated application cannot be edited", "Update values on its Helm source or preview group instead.")
		return
	}
	var input updateAppHelmValuesRequest
	if !decode(w, r, &input) {
		return
	}
	encoded, err := encodeHelmValueOverrides(input.Overrides)
	if err != nil {
		problem(w, http.StatusBadRequest, "Helm values unavailable", err.Error())
		return
	}
	item.HelmValues = encoded
	if err := a.store.UpdateApp(r.Context(), item); err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"overrides": input.Overrides})
}

func validateSourceAuthentication(repository, authType, credentialID string) string {
	if repository != "" {
		switch {
		case strings.HasPrefix(repository, "https://"):
			parsed, err := url.Parse(repository)
			if err != nil || parsed.Host == "" {
				return "Enter a valid HTTPS repository URL."
			}
			if parsed.User != nil {
				return "Do not embed credentials in the repository URL; attach a stored credential instead."
			}
		case strings.HasPrefix(repository, "file://"):
			if authType != "" || credentialID != "" {
				return "Local file repositories do not use stored credentials."
			}
		case strings.HasPrefix(repository, "ssh://"), strings.Contains(repository, "@"):
			if authType == "" && credentialID == "" {
				return "SSH repositories require a stored SSH private key."
			}
		default:
			return "Use an HTTPS repository URL, or SSH with a stored private key."
		}
	}
	if authType == "" && credentialID == "" {
		return ""
	}
	if repository == "" {
		return "Repository credentials require a source repository."
	}
	if authType == "" || credentialID == "" {
		return "Choose both an authentication method and a stored credential."
	}
	switch authType {
	case deploy.SourceAuthGitHubApp:
		if !strings.HasPrefix(repository, "https://") {
			return "GitHub Apps require an HTTPS repository URL."
		}
	case deploy.SourceAuthGitHubToken:
		if !strings.HasPrefix(repository, "https://") {
			return "GitHub tokens require an HTTPS repository URL."
		}
	case deploy.SourceAuthSSHKey:
		if !strings.HasPrefix(repository, "ssh://") && !strings.Contains(repository, "@") {
			return "SSH keys require an ssh:// or git@host:path repository URL."
		}
	default:
		return "Use github_app, github_token, or ssh_key."
	}
	return ""
}

func (a *API) deleteApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := a.store.GetApp(r.Context(), id); err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	previewGroups, err := a.store.ListPreviewGroups(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, group := range previewGroups {
		for _, component := range group.Components {
			if component.AppID == id {
				problem(w, http.StatusConflict, "Application is in a preview group", "Remove this application from every preview group before deleting it.")
				return
			}
		}
	}
	active, err := a.store.ActiveDeploymentForApp(r.Context(), id)
	if err != nil {
		a.internal(w, err)
		return
	}
	if active != nil {
		problem(w, http.StatusConflict, "Deployment active", "Wait for the active deployment to finish before deleting this application.")
		return
	}
	previews, err := a.store.ListPreviewEnvironments(r.Context(), id)
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, preview := range previews {
		if preview.TemplateAppID == id && preview.State != core.PreviewClosed {
			problem(w, http.StatusConflict, "Preview active", "Close and clean every preview created from this application before deleting it.")
			return
		}
	}
	hasDeployments, err := a.store.AppHasDeployments(r.Context(), id)
	if err != nil {
		a.internal(w, err)
		return
	}
	if hasDeployments {
		if err := a.deploy.Cleanup(r.Context(), id, nil); err != nil {
			switch {
			case errors.Is(err, deploy.ErrDeploymentActive):
				problem(w, http.StatusConflict, "Deployment active", "Wait for the active deployment to finish before deleting this application.")
			case errors.Is(err, deploy.ErrCleanupUnsupported):
				problem(w, http.StatusConflict, "Cleanup unavailable", "This application cannot be deleted until its deployed resources can be cleaned up safely.")
			case errors.Is(err, store.ErrNotFound):
				problem(w, http.StatusNotFound, "Application not found", "Refresh the application inventory and try again.")
			default:
				a.internal(w, err)
			}
			return
		}
	}
	if err := a.store.DeleteApp(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrAppPreviewGroup) {
			problem(w, http.StatusConflict, "Application is in a preview group", "Remove this application from every preview group before deleting it.")
			return
		}
		if errors.Is(err, store.ErrAppActive) {
			problem(w, http.StatusConflict, "Deployment active", "A deployment started while the application was being deleted. Wait for it to finish and retry.")
			return
		}
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) cleanupApp(w http.ResponseWriter, r *http.Request) {
	err := a.deploy.Cleanup(r.Context(), chi.URLParam(r, "id"), nil)
	if errors.Is(err, deploy.ErrDeploymentActive) {
		problem(w, http.StatusConflict, "Deployment active", "Wait for the active deployment to finish before cleaning up the application.")
		return
	}
	if errors.Is(err, deploy.ErrCleanupUnsupported) {
		problem(w, http.StatusConflict, "Cleanup unavailable", err.Error())
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "Application not found", "Refresh the application inventory and try again.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
