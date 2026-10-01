package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

type destructiveConfirmation struct {
	ResourceID      string `json:"resourceId"`
	Action          string `json:"action"`
	ExpectedVersion string `json:"expectedVersion"`
	ConfirmName     string `json:"confirmName"`
}

type destructiveReview struct {
	ResourceID    string   `json:"resourceId"`
	ResourceType  string   `json:"resourceType"`
	Name          string   `json:"name"`
	Action        string   `json:"action"`
	Version       string   `json:"version"`
	Summary       string   `json:"summary"`
	Resources     []string `json:"resources"`
	BlockedReason string   `json:"blockedReason,omitempty"`
	ProjectID     string   `json:"projectId,omitempty"`
	AppID         string   `json:"appId,omitempty"`
}

type destructiveAudit struct {
	Review  *destructiveReview
	Outcome string
}
type destructiveAuditKey struct{}
type destructiveConfirmationKey struct{}

var errDestructiveReviewChanged = errors.New("The resource changed. Review the action again.")

func (a *API) destructiveRoute(r chi.Router, method, path, kind, action string, handler http.HandlerFunc) {
	previewPath := strings.TrimSuffix(path, "/") + "/delete-preview"
	if method != http.MethodDelete {
		previewPath = path + "-preview"
	}
	r.Post(previewPath, a.previewDestructiveAction(kind, action))
	r.Method(method, path, a.confirmDestructiveAction(kind, action, handler))
}

func (a *API) previewDestructiveAction(kind, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		review, err := a.destructiveReview(r.Context(), r, kind, action)
		if err != nil {
			a.notFoundOrInternal(w, err, "Resource")
			return
		}
		if review.ProjectID != "" {
			permission := core.PermissionProjectConfigure
			if action == "cleanup" {
				permission = core.PermissionDeploymentRun
			}
			if !a.requireProject(w, r, permission, review.ProjectID) {
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, review)
	}
}

func (a *API) confirmDestructiveAction(kind, action string, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil || len(raw) == 0 || json.Unmarshal(raw, &body) != nil {
			problem(w, 422, "Confirmation required", "Review this action and confirm the resource name and current version.")
			return
		}
		var input destructiveConfirmation
		if json.Unmarshal(body["confirmation"], &input) != nil {
			problem(w, 422, "Confirmation required", "Provide the confirmation returned by the action review.")
			return
		}
		review, err := a.destructiveReview(r.Context(), r, kind, action)
		if err != nil {
			a.notFoundOrInternal(w, err, "Resource")
			return
		}
		if review.ProjectID != "" {
			permission := core.PermissionProjectConfigure
			if action == "cleanup" {
				permission = core.PermissionDeploymentRun
			}
			if !a.requireProject(w, r, permission, review.ProjectID) {
				return
			}
		}
		if input.ResourceID != review.ResourceID || input.Action != review.Action || input.ConfirmName != review.Name || input.ExpectedVersion == "" {
			problem(w, 422, "Confirmation does not match", "Confirm the exact resource, action and name shown in the review.")
			return
		}
		if input.ExpectedVersion != review.Version {
			problem(w, 409, "Resource changed", "The resource or its deployment changed. Review the action again before continuing.")
			return
		}
		if review.BlockedReason != "" {
			problem(w, 409, "Resource in use", review.BlockedReason)
			return
		}
		if audit, _ := r.Context().Value(destructiveAuditKey{}).(*destructiveAudit); audit != nil {
			audit.Review = &review
		}
		delete(body, "confirmation")
		raw, _ = json.Marshal(body)
		r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(raw)), int64(len(raw))
		r = r.WithContext(context.WithValue(r.Context(), destructiveConfirmationKey{}, input))
		next(w, r)
	})
}

// Recheck inside an application operation's lock, before cleanup starts.
func (a *API) recheckDestructiveAction(r *http.Request, kind, action string) error {
	input, ok := r.Context().Value(destructiveConfirmationKey{}).(destructiveConfirmation)
	if !ok {
		return errors.New("Review and confirm this action first.")
	}
	review, err := a.destructiveReview(r.Context(), r, kind, action)
	if err != nil {
		return err
	}
	if input.ExpectedVersion != review.Version || input.ConfirmName != review.Name {
		return errDestructiveReviewChanged
	}
	return nil
}

func (a *API) destructiveReview(ctx context.Context, r *http.Request, kind, action string) (destructiveReview, error) {
	id := chi.URLParam(r, "id")
	if kind == "relay-webhook" {
		id = chi.URLParam(r, "webhookId")
	}
	out := destructiveReview{ResourceID: id, ResourceType: kind, Action: action, Resources: []string{}, Summary: "Remove this registration and its access from Dispatch. This action cannot be undone."}
	var record, extra any
	var err error
	switch kind {
	case "application":
		var app core.App
		app, err = a.store.GetApp(ctx, id)
		record, out.Name, out.ProjectID, out.AppID = app, app.Name, app.ProjectID, app.ID
		if err != nil {
			break
		}
		bindings, e := a.store.GetAppServiceBindings(ctx, id)
		if e != nil {
			return out, e
		}
		history, e := a.store.ListApplicationHistory(ctx, id, "", 1)
		if e != nil {
			return out, e
		}
		server, e := a.store.GetServer(ctx, app.ServerID)
		if e != nil {
			return out, e
		}
		validation, cancel := context.WithTimeout(ctx, 15*time.Second)
		runtimeIdentity, e := a.deploy.CleanupRuntimeIdentity(validation, app, server)
		cancel()
		if e != nil {
			return out, e
		}
		extra = struct {
			RuntimeIdentity string
			Spec            string
			Bindings        []core.ServiceBinding
			History         []core.Deployment
			Server          core.Server
		}{runtimeIdentity, app.SpecDigest(), bindings, history, server}
		if app.BuildType == core.BuildTypeHelm {
			namespace := app.HelmNamespace
			if namespace == "" && server.Kubernetes != nil {
				namespace = server.Kubernetes.Namespace
			}
			if namespace == "" {
				namespace = "default"
			}
			release := app.HelmRelease
			if release == "" {
				release = app.Name
			}
			out.Resources = append(out.Resources, "Helm release "+release+" in "+namespace+" on "+server.Name)
		} else {
			out.Resources = append(out.Resources, "Docker resources dispatch-"+strings.ToLower(app.ID)+" on "+server.Name)
		}
		out.Summary = "Remove this application's runtime resources. Named volumes and external database data are preserved. Helm charts control which chart resources uninstall removes."
		if action == "delete" {
			out.Summary += " Application configuration and deployment history will also be deleted."
		} else {
			out.Summary += " Application configuration and deployment history remain."
		}
		if app.Generated && app.HelmProvenance.WorkflowResourceID != "" {
			resource, e := a.store.GetWorkflowResource(ctx, app.HelmProvenance.WorkflowResourceID)
			if e != nil && !errors.Is(e, store.ErrNotFound) {
				return out, e
			}
			if e == nil && resource.Temporary {
				preview, resources, e := a.workflowCleanupReview(ctx, resource, app.ProjectID)
				if e != nil {
					return out, e
				}
				extra = struct{ Application, Preview any }{extra, preview}
				out.Resources = resources
				out.Summary = "Remove the entire preview " + resource.Name + ", including all listed applications, and close its PR triggers. Application configuration and deployment history remain. Retained data follows the runtime's storage policy."
			}
		}
	case "project":
		var item core.Project
		item, err = a.store.GetProject(ctx, id)
		record, out.Name, out.ProjectID = item, item.Name, item.ID
	case "server":
		var item core.Server
		item, err = a.store.GetServer(ctx, id)
		record, out.Name = item, item.Name
		if strings.EqualFold(item.Address, "local") {
			out.BlockedReason = "Managed server cannot be deleted. The controller target is reconciled automatically."
		}
		out.Summary = "Remove this target registration. The server and its stored application data remain. Applications must be detached first."
	case "service":
		var item core.Service
		item, err = a.store.GetService(ctx, id)
		record, out.Name, out.ProjectID = item, item.Name, item.ProjectID
		out.Summary = "Remove this service registration. The provisioned or external service and its data remain. Remove configured consumers first."
	case "service-template":
		var resource core.WorkflowResource
		var project string
		var revision int64
		resource, project, revision, err = a.serviceTemplateResource(ctx, id)
		record, out.Name, out.ProjectID, extra = resource, resource.Name, project, revision
		out.Summary = "Delete this saved service template. Existing services remain. Repository templates must be removed in Git."
	case "config-source":
		var item core.ConfigSource
		item, err = a.store.GetConfigSource(ctx, id)
		record, out.Name, out.ProjectID = item, item.Name, item.ProjectID
		out.Summary = "Remove this repository configuration and its synced workflow definitions. This does not uninstall deployed runtime resources."
	case "preview-template":
		var item core.WorkflowPreviewTemplate
		item, err = a.store.GetWorkflowPreviewTemplate(ctx, id)
		record, out.Name = item, item.Name
		out.Summary = "Delete this reusable preview template and stop creating previews from it. Existing preview instances remain."
	case "temporary-workflow":
		var item core.WorkflowResource
		item, err = a.store.GetWorkflowResource(ctx, id)
		record, out.Name = item, item.Name
		if err == nil {
			extra, err = a.store.ListWorkflowRevisions(ctx, id, 0)
		}
		out.Summary = "Remove this preview's owned deployments and close its PR triggers. Retained database data follows the runtime's storage policy."
	case "preview-group":
		var item core.PreviewGroup
		item, err = a.store.GetPreviewGroup(ctx, id)
		record, out.Name = item, item.Name
		out.Summary = "Delete this preview group configuration after its active runs are cleaned up."
	case "preview-run":
		var item core.PreviewGroupRun
		item, err = a.store.GetPreviewGroupRun(ctx, id)
		record, out.Name = item, id
		out.Summary = "Remove the runtime resources for this preview run. Run history remains; database data follows the runtime's storage policy."
	case "event-trigger":
		items, e := a.store.ListEventTriggers(ctx, "")
		err = e
		if err == nil {
			err = store.ErrNotFound
			for _, item := range items {
				if item.ID == id {
					record, out.Name, out.AppID, err = item, item.Repository+" "+item.Command, item.AppID, nil
					break
				}
			}
		}
	case "secret":
		var item core.Secret
		item, err = a.store.GetSecret(ctx, id)
		record, out.Name = item, item.Name
		extra = item.EncryptedValue
		out.Summary = "Delete this saved value. Applications or integrations that still reference it will lose access to it."
	case "secret-store":
		var item core.SecretStore
		item, err = a.store.GetSecretStore(ctx, id)
		record, out.Name = item, item.Name
		extra = item.EncryptedCredentials
	case "private-network":
		var item core.PrivateNetwork
		item, err = a.store.GetPrivateNetwork(ctx, id)
		item.LastVerifiedAt = nil
		item.UpdatedAt = item.CreatedAt
		item.Details = nil
		record, out.Name = item, item.Name
		extra = struct{ Token, Credentials string }{item.TokenHash, item.EncryptedCredentials}
		if action == "rotate-token" {
			out.Summary = "Replace this node's enrollment credentials. Current credentials stop working and the node must enroll again."
		}
		if action == "revoke" {
			out.Summary = "Revoke this node's credentials. It stops receiving private requests immediately."
		}
	case "github-app":
		var item core.GitHubAppConnection
		item, err = a.store.GetGitHubApp(ctx, id)
		record, out.Name = item, item.Name
		extra = struct{ Key, Webhook string }{item.EncryptedPrivateKey, item.EncryptedWebhookSecret}
		out.Summary = "Remove this GitHub App connection and its saved credentials. Repository workflows using it will lose access. The App registration on GitHub remains."
	case "relay-webhook":
		var item core.RelayWebhook
		item, err = a.store.GetRelayWebhook(ctx, id)
		record, out.Name = item, item.Name
	case "auth-provider", "auth-link":
		var item core.AuthProvider
		item, err = a.store.GetAuthProvider(ctx, id)
		record, out.Name = item, item.Name
		if kind == "auth-link" {
			extra = currentIdentity(ctx).ID
			out.Summary = "Unlink this sign-in provider from your account. Make sure you have another working sign-in method."
		}
	case "user":
		var item core.User
		item, err = a.store.GetUser(ctx, id)
		record, out.Name = item, item.Username
	case "team":
		var item core.Team
		item, err = a.store.GetTeam(ctx, id)
		record, out.Name = item, item.Name
	case "role-assignment":
		items, e := a.store.ListRoleAssignments(ctx)
		err = e
		if err == nil {
			err = store.ErrNotFound
			for _, item := range items {
				if item.ID == id {
					record, out.Name, err = item, item.PrincipalID+" "+item.Role, nil
					break
				}
			}
		}
	case "team-mapping":
		data, ok := a.store.(operationsStore)
		if !ok {
			return out, errors.New("Operations storage is unavailable.")
		}
		items, e := data.ListIdentityTeamMappings(ctx)
		err = e
		if err == nil {
			err = store.ErrNotFound
			for _, item := range items {
				if item.ID == id {
					record, out.Name, err = item, item.ExternalGroup, nil
					break
				}
			}
		}
	default:
		return out, errors.New("Unsupported destructive resource type.")
	}
	if err != nil {
		return out, err
	}
	if out.Name == "" {
		out.Name = id
	}
	if action == "delete" {
		dependencies, dependencyErr := a.destructiveDependencies(ctx, kind, id)
		if dependencyErr != nil {
			return out, dependencyErr
		}
		if len(dependencies) > 0 {
			if out.BlockedReason == "" {
				out.BlockedReason = "Remove these references before deleting this resource."
			}
			out.Resources = append(out.Resources, dependencies...)
		}
		extra = struct {
			Inputs       any
			Dependencies []string
		}{extra, dependencies}
	}
	// Canonical JSON dereferences target configuration instead of hashing memory addresses.
	raw, err := json.Marshal(struct {
		Kind, Action  string
		Record, Extra any
	}{kind, action, record, extra})
	if err != nil {
		return out, err
	}
	out.Version = fmt.Sprintf("%x", sha256.Sum256(raw))
	return out, nil
}

// A generated application can remove its whole preview, including applications
// connected only through older workflow stages. Review exactly that cleanup set.
func (a *API) workflowCleanupReview(ctx context.Context, resource core.WorkflowResource, projectID string) (any, []string, error) {
	source, err := a.store.GetConfigSource(ctx, resource.ConfigSourceID)
	if err != nil {
		return nil, nil, err
	}
	if source.ProjectID != projectID {
		return nil, nil, errors.New("Preview ownership no longer matches this application's project. Repair the preview links before cleanup.")
	}
	apps, err := a.workflowPreviewCleanupApps(ctx, resource)
	if err != nil {
		return nil, nil, err
	}
	type applicationInputs struct {
		App             core.App
		Server          core.Server
		Bindings        []core.ServiceBinding
		History         []core.Deployment
		RuntimeIdentity string
	}
	inputs := []applicationInputs{}
	resources := []string{}
	for _, app := range apps {
		if app.ProjectID != projectID {
			return nil, nil, errors.New("Preview application ownership no longer matches its project. Repair the preview links before cleanup.")
		}
		server, err := a.store.GetServer(ctx, app.ServerID)
		if err != nil {
			return nil, nil, err
		}
		bindings, err := a.store.GetAppServiceBindings(ctx, app.ID)
		if err != nil {
			return nil, nil, err
		}
		history, err := a.store.ListApplicationHistory(ctx, app.ID, "", 1)
		if err != nil {
			return nil, nil, err
		}
		validation, cancel := context.WithTimeout(ctx, 15*time.Second)
		identity, err := a.deploy.CleanupRuntimeIdentity(validation, app, server)
		cancel()
		if err != nil {
			return nil, nil, err
		}
		inputs = append(inputs, applicationInputs{app, server, bindings, history, identity})
		resources = append(resources, "Application "+app.Name+" on "+server.Name)
	}
	revisions, err := a.store.ListWorkflowRevisions(ctx, resource.ID, 0)
	if err != nil {
		return nil, nil, err
	}
	return struct {
		Resource     core.WorkflowResource
		Applications []applicationInputs
		Revisions    []core.WorkflowRevision
	}{resource, inputs, revisions}, resources, nil
}

func (a *API) idleAppMutation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := a.deploy.WithIdleApplication(r.Context(), chi.URLParam(r, "id"), func() error { next.ServeHTTP(w, r); return nil })
		if errors.Is(err, deploy.ErrDeploymentActive) {
			problem(w, 409, "Application busy", "Wait for the application operation to finish before changing its runtime inputs.")
		} else if err != nil {
			a.internal(w, err)
		}
	})
}

func destructiveOutcome(r *http.Request, outcome string) {
	if r.Context().Err() != nil {
		outcome = "cancelled"
	}
	if audit, _ := r.Context().Value(destructiveAuditKey{}).(*destructiveAudit); audit != nil {
		audit.Outcome = outcome
	}
}

func (a *API) destructiveDependencies(ctx context.Context, kind, id string) ([]string, error) {
	dependencies := []string{}
	if kind != "server" && kind != "project" && kind != "secret" && kind != "service" {
		return dependencies, nil
	}
	if kind == "service" {
		consumers, err := a.store.ListServiceConsumers(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, consumer := range consumers {
			dependencies = append(dependencies, "Application "+consumer.AppName)
		}
	} else {
		apps, err := a.store.ListApps(ctx)
		if err != nil {
			return nil, err
		}
		for _, app := range apps {
			if kind == "server" && app.ServerID == id || kind == "project" && app.ProjectID == id || kind == "secret" && (app.SourceCredentialID == id || slices.Contains(app.HookSecretIDs, id)) {
				dependencies = append(dependencies, "Application "+app.Name)
			}
		}
	}
	if kind == "secret" {
		servers, err := a.store.ListServers(ctx)
		if err != nil {
			return nil, err
		}
		for _, server := range servers {
			if server.Builder != nil && server.Builder.SSHSecretID == id {
				dependencies = append(dependencies, "Docker builder "+server.Name)
			}
		}
		triggers, err := a.store.ListEventTriggers(ctx, "")
		if err != nil {
			return nil, err
		}
		for _, trigger := range triggers {
			if slices.Contains(trigger.SecretIDs, id) {
				dependencies = append(dependencies, "Event rule "+trigger.Repository+" "+trigger.Command)
			}
		}
		groups, err := a.store.ListPreviewGroups(ctx)
		if err != nil {
			return nil, err
		}
		for _, group := range groups {
			for _, component := range group.Components {
				if slices.Contains(component.SecretIDs, id) {
					dependencies = append(dependencies, "Preview group "+group.Name)
				}
			}
		}
	}
	sort.Strings(dependencies)
	return slices.Compact(dependencies), nil
}
