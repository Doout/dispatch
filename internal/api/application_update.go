package api

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

func (a *API) writeApplicationConfiguration(w http.ResponseWriter, r *http.Request, app core.App) {
	out := core.ApplicationConfiguration{App: app, SpecDigest: app.SpecDigest()}
	if data, ok := a.store.(interface {
		ActiveRuntimeMutation(context.Context, string) (*core.RuntimeJob, error)
	}); ok {
		job, err := data.ActiveRuntimeMutation(r.Context(), app.ID)
		if err != nil {
			a.internal(w, err)
			return
		}
		if job != nil {
			out.BlockingRuntimeJob = &core.BlockingRuntimeJob{ID: job.ID, Operation: job.Operation, State: job.State, DeploymentID: job.DeploymentID,
				Location: "/api/v1/apps/" + app.ID + "/runtime/jobs/" + job.ID}
		}
	}
	if currentIdentity(r.Context()).SystemRole != core.UserRoleOwner {
		out.App = redactAppCredentials(out.App)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (a *API) updateApplicationConfiguration(w http.ResponseWriter, r *http.Request) {
	app, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	if app.Generated || app.HelmProvenance.WorkflowResourceID != "" || app.HelmProvenance.WorkflowRevisionID != "" {
		problem(w, 409, "Application configuration is managed", "Update its application source or workflow configuration instead.")
		return
	}
	var input core.ApplicationUpdate
	if !decode(w, r, &input) {
		return
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(input.ExpectedSpecDigest, "sha256:"))
	if err != nil || !strings.HasPrefix(input.ExpectedSpecDigest, "sha256:") || len(digest) != 32 || !input.HasChanges() {
		problem(w, 422, "Application update required", "Supply expectedSpecDigest from the current application and at least one configuration field.")
		return
	}
	if app.SpecDigest() != input.ExpectedSpecDigest {
		problem(w, 409, "Application configuration changed", "Read the current application before editing its saved configuration.")
		return
	}
	candidate := applyApplicationUpdate(app, input)
	credentialChange := input.SourceAuthType != nil || input.SourceCredentialID != nil || candidate.SourceRepo != app.SourceRepo && candidate.SourceCredentialID != ""
	if !a.requireCredentialOwner(w, r, credentialChange) {
		return
	}
	server, err := a.store.GetServer(r.Context(), app.ServerID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	if err = validateApplicationUpdate(&candidate, server); err != nil {
		problem(w, 422, "Invalid application configuration", err.Error())
		return
	}
	if !a.validateApplicationUpdateCredential(w, r, candidate) {
		return
	}
	data, ok := a.store.(store.ApplicationConfigurationStore)
	if !ok {
		problem(w, 503, "Application updates unavailable", "Application configuration storage is unavailable.")
		return
	}
	if err = data.UpdateAppConfiguration(r.Context(), candidate, input.ExpectedSpecDigest); err != nil {
		switch {
		case errors.Is(err, store.ErrApplicationConfigurationChanged):
			problem(w, 409, "Application configuration changed", "Read the current application before editing its saved configuration.")
		case errors.Is(err, store.ErrApplicationConfigurationManaged):
			problem(w, 409, "Application configuration is managed", "Update its application source or workflow configuration instead.")
		case errors.Is(err, store.ErrAppActive):
			problem(w, 409, "Application busy", "Inspect the application and its blocking operation before editing its configuration.")
		default:
			a.notFoundOrInternal(w, err, "Application")
		}
		return
	}
	a.writeApplicationConfiguration(w, r, candidate)
}

func applyApplicationUpdate(app core.App, input core.ApplicationUpdate) core.App {
	for _, field := range []struct {
		value  *string
		target *string
	}{
		{input.SourceRepo, &app.SourceRepo}, {input.Branch, &app.Branch}, {input.ContextPath, &app.ContextPath},
		{input.DockerfilePath, &app.DockerfilePath}, {input.ComposePath, &app.ComposePath}, {input.ComposeContent, &app.ComposeContent},
		{input.Domain, &app.Domain}, {input.HelmChart, &app.HelmChart}, {input.HelmVersion, &app.HelmVersion},
		{input.HelmRepository, &app.HelmRepository}, {input.SourceAuthType, &app.SourceAuthType}, {input.SourceCredentialID, &app.SourceCredentialID},
	} {
		if field.value != nil {
			*field.target = strings.TrimSpace(*field.value)
		}
	}
	if input.BuildType != nil {
		app.BuildType = *input.BuildType
	}
	if input.ContainerPort != nil {
		app.ContainerPort = *input.ContainerPort
	}
	return app
}

func validateApplicationUpdate(app *core.App, server core.Server) error {
	if app.BuildType != core.BuildTypeDockerfile && app.BuildType != core.BuildTypeCompose && app.BuildType != core.BuildTypeHelm {
		return errors.New("Use dockerfile, compose, or helm.")
	}
	if len(app.ComposeContent) > maxComposeContentBytes {
		return errors.New("Keep Compose content under 512 KB.")
	}
	if app.ComposeContent != "" && app.BuildType != core.BuildTypeCompose {
		return errors.New("Inline Compose content requires the compose build type. Clear it before changing build type.")
	}
	if app.SourceRepo == "" && app.ComposeContent == "" && app.BuildType != core.BuildTypeHelm {
		return errors.New("Supply a repository URL, inline Compose content, or a Helm chart.")
	}
	if detail := validateSourceAuthentication(app.SourceRepo, app.SourceAuthType, app.SourceCredentialID); detail != "" {
		return errors.New(detail)
	}
	if app.ContainerPort < 0 || app.ContainerPort > 65535 {
		return errors.New("Use a container port between 0 and 65535.")
	}
	if app.Domain != "" {
		domain, err := routing.NormalizeHostname(app.Domain)
		if err != nil {
			return err
		}
		app.Domain = domain
	}
	for _, check := range app.HealthPolicy.Checks {
		if check.Scope != "" && check.Scope != "workload" && app.Domain == "" {
			return errors.New("Keep an application domain while route or certificate readiness checks are required.")
		}
	}
	for _, field := range []struct {
		value    *string
		fallback string
	}{
		{&app.ContextPath, "."}, {&app.DockerfilePath, "Dockerfile"}, {&app.ComposePath, "compose.yml"},
	} {
		if *field.value == "" {
			*field.value = field.fallback
		}
		cleaned := path.Clean(*field.value)
		if path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.ContainsRune(*field.value, 0) {
			return errors.New("Build paths must stay within the source repository.")
		}
	}
	if app.ComposeContent != "" {
		var definition map[string]any
		if yaml.Unmarshal([]byte(app.ComposeContent), &definition) != nil {
			return errors.New("Supply a valid Compose YAML object.")
		}
		if _, ok := definition["services"].(map[string]any); !ok {
			return errors.New("Compose content must include a services mapping.")
		}
		app.Branch = ""
	} else if app.SourceRepo == "" && app.BuildType == core.BuildTypeHelm {
		app.Branch = ""
	} else if app.Branch == "" {
		app.Branch = "main"
	}
	if app.BuildType == core.BuildTypeHelm {
		return deploy.ValidateHelmTarget(*app, server)
	}
	if server.Runtime != core.ServerRuntimeDocker {
		return errors.New("Dockerfile and Compose applications require a Docker server.")
	}
	return nil
}

func (a *API) validateApplicationUpdateCredential(w http.ResponseWriter, r *http.Request, app core.App) bool {
	if app.SourceCredentialID == "" {
		return true
	}
	if a.eventConfig.Vault == nil {
		problem(w, 503, "Secret storage is not configured", "Set DISPATCH_MASTER_KEY_FILE before attaching repository credentials.")
		return false
	}
	if app.SourceAuthType == deploy.SourceAuthGitHubApp {
		connection, err := a.store.GetGitHubApp(r.Context(), app.SourceCredentialID)
		if err != nil {
			a.notFoundOrInternal(w, err, "GitHub App")
			return false
		}
		if connection.State != "ready" {
			problem(w, 409, "GitHub App not ready", "Install and verify this GitHub App before using it for a repository.")
			return false
		}
		if err = githubapp.ValidateRepositoryHost(app.SourceRepo, connection.WebURL); err != nil {
			problem(w, 422, "GitHub App host mismatch", err.Error())
			return false
		}
	} else {
		credential, err := a.store.GetSecret(r.Context(), app.SourceCredentialID)
		if err != nil {
			a.notFoundOrInternal(w, err, "Source credential")
			return false
		}
		if err = deploy.ValidateSourceCredentialType(app.SourceAuthType, credential.Type); err != nil {
			problem(w, 422, "Invalid source credential", err.Error())
			return false
		}
	}
	return true
}
