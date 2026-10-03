package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/serviceconn"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

var temporaryName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)
var temporarySHA = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

func (a *API) temporaryStore(w http.ResponseWriter) (*store.SQLStore, bool) {
	d, ok := a.store.(*store.SQLStore)
	if !ok {
		problem(w, 503, "Temporary environments unavailable", "Durable SQL storage is required.")
	}
	return d, ok
}
func (a *API) temporaryEnvironmentRoutes(r chi.Router) {
	r.Post("/temporary-environments/review", a.reviewTemporaryEnvironment)
	r.Post("/temporary-environments", a.acceptTemporaryEnvironment)
	r.Get("/temporary-environments/{environmentId}", a.getTemporaryEnvironment)
	r.Post("/temporary-environments/{environmentId}/extend", a.extendTemporaryEnvironment)
	r.Post("/temporary-environments/{environmentId}/cleanup-review", a.reviewTemporaryCleanup)
	r.Post("/temporary-environments/{environmentId}/destroy", a.destroyTemporaryEnvironment)
}
func (a *API) temporaryEnvironmentProjectRoutes(r chi.Router) {
	r.Get("/temporary-environments", a.listTemporaryEnvironments)
	r.Get("/temporary-environments/options", a.temporaryEnvironmentOptions)
}
func (a *API) temporaryTarget(ctx context.Context, project, id string) error {
	assigned, err := a.assignedInfrastructure(ctx, project, "target", id)
	if err != nil {
		return err
	}
	if !assigned {
		return errors.New("target is not assigned to this project")
	}
	s, err := a.store.GetServer(ctx, id)
	if err != nil {
		return err
	}
	if s.Runtime != core.ServerRuntimeDocker || s.AgentNodeID == "" || s.State != "ready" {
		return errors.New("choose an assigned ready outbound Docker target")
	}
	manifest := a.deploy.RuntimeCapabilities(core.App{BuildType: core.BuildTypeDockerfile}, s)
	if manifest.Driver != "docker-agent" || manifest.TargetMode != "outbound" {
		return errors.New("controller requires the outbound Docker executor")
	}
	n, err := a.store.GetPrivateNetwork(ctx, s.AgentNodeID)
	if err != nil {
		return err
	}
	checked, err := time.Parse(time.RFC3339Nano, n.Details["runtimeCheckedAt"])
	if err != nil || time.Since(checked) > 2*time.Minute || checked.After(time.Now().Add(5*time.Second)) || n.Details["runtimeVersion"] != remoteruntime.APIVersion {
		return errors.New("target needs fresh authenticated Docker runtime evidence")
	}
	caps := "," + n.Details["runtimeCapabilities"] + ","
	for _, op := range []string{"deploy", "inspect", "destroy"} {
		if !strings.Contains(caps, ","+op+",") {
			return errors.New("target lacks deployment, inspection or cleanup capability")
		}
	}
	return nil
}
func (a *API) temporaryPermission(w http.ResponseWriter, r *http.Request, project string, run bool) bool {
	if !a.requireProject(w, r, core.PermissionProjectView, project) {
		return false
	}
	if !run {
		return true
	}
	return a.requireProject(w, r, core.PermissionProjectConfigure, project) && a.requireProject(w, r, core.PermissionDeploymentRun, project)
}
func (a *API) temporaryEnvironmentOptions(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "id")
	if !a.temporaryPermission(w, r, project, true) {
		return
	}
	apps, err := a.store.ListApps(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	templates := []core.App{}
	for _, app := range apps {
		if app.ProjectID == project && app.Template && app.BuildType == core.BuildTypeDockerfile {
			templates = append(templates, redactAppCredentials(app))
		}
	}
	servers, err := a.store.ListServers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	targets := []map[string]string{}
	for _, s := range servers {
		if a.temporaryTarget(r.Context(), project, s.ID) == nil {
			targets = append(targets, map[string]string{"id": s.ID, "name": s.Name})
		}
	}
	writeJSON(w, 200, map[string]any{"templates": templates, "targets": targets, "runtime": "outbound Dockerfile", "omissions": temporaryOmissions()})
}
func temporaryOmissions() []string {
	return []string{"Deployment hooks and hook variables are not copied.", "Template service bindings and data are not copied. Only selected services are bound and remain shared.", "Production domains are not copied. A configured target routing zone supplies a reviewed unique hostname; workload health checks are preserved.", "The assigned server and selected services are shared and retained. Named volumes and backup archives are retained.", "The approved source revision is immutable. Local Docker, Compose, Helm and snapshot restores are not supported by this environment path."}
}
func (a *API) reviewTemporaryEnvironment(w http.ResponseWriter, r *http.Request) {
	d, ok := a.temporaryStore(w)
	if !ok {
		return
	}
	var input core.TemporaryEnvironmentInput
	if !decode(w, r, &input) {
		return
	}
	if !a.temporaryPermission(w, r, input.ProjectID, true) {
		return
	}
	if !temporaryName.MatchString(input.Name) || !temporarySHA.MatchString(input.SourceSHA) || input.LifetimeSeconds < 1 || input.LifetimeSeconds > 365*24*3600 {
		problem(w, 422, "Invalid environment input", "Choose a 2–40 character lowercase name, full Git commit SHA, and finite lifetime in seconds.")
		return
	}
	template, err := a.store.GetApp(r.Context(), input.TemplateID)
	if err != nil || template.ProjectID != input.ProjectID || !template.Template || template.BuildType != core.BuildTypeDockerfile {
		problem(w, 422, "Unsupported template", "Choose an existing same-project Dockerfile Application template.")
		return
	}
	if !a.requireCredentialOwner(w, r, template.SourceCredentialID != "") {
		return
	}
	if err = a.temporaryTarget(r.Context(), input.ProjectID, input.ServerID); err != nil {
		problem(w, 422, "Target unavailable", err.Error())
		return
	}
	p, err := d.GetInfrastructureQuotaPolicy(r.Context(), input.ProjectID)
	if err != nil {
		a.internal(w, err)
		return
	}
	if input.LifetimeSeconds > p.MaxTemporaryLifetimeSeconds || p.MaxTemporaryEnvironments == 0 {
		problem(w, 422, "Resource policy required", "The project must allow temporary environments and the requested finite lifetime.")
		return
	}
	health, err := core.NormalizeHealthPolicy(template.HealthPolicy)
	if err != nil {
		problem(w, 422, "Invalid template health policy", err.Error())
		return
	}
	for _, check := range health.Checks {
		if check.Scope != "workload" {
			problem(w, 422, "Unsupported route policy", "This path preserves workload health checks and does not copy production routing. Choose a template with workload-only health checks.")
			return
		}
	}
	now := time.Now().UTC()
	clone := core.App{ID: "environment-app-" + ulid.Make().String(), ProjectID: input.ProjectID, ServerID: input.ServerID, Name: input.Name, SourceRepo: template.SourceRepo, Branch: input.SourceSHA, SourceAuthType: template.SourceAuthType, SourceCredentialID: template.SourceCredentialID, BuildType: core.BuildTypeDockerfile, ContextPath: template.ContextPath, DockerfilePath: template.DockerfilePath, ContainerPort: template.ContainerPort, Generated: true, State: "pending", CreatedAt: now, HealthPolicy: health}
	target, err := a.store.GetServer(r.Context(), input.ServerID)
	if err != nil {
		a.internal(w, err)
		return
	}
	if len(input.ServiceBindings) > 32 {
		problem(w, 422, "Too many service bindings", "Select at most 32 same-project service bindings.")
		return
	}
	services := map[string]core.Service{}
	serviceRevisions := map[string]int64{}
	for _, binding := range input.ServiceBindings {
		service, err := a.store.GetService(r.Context(), binding.ServiceRef)
		if err != nil || service.ProjectID != input.ProjectID {
			problem(w, 422, "Service unavailable", "Select a service in this project.")
			return
		}
		if service.ProvisionTarget != nil && service.ProvisionTarget.ServerID != "" && service.ProvisionTarget.ServerID != input.ServerID {
			problem(w, 422, "Service target unavailable", "Built-in service bindings require the environment's target. Register an externally reachable connection separately.")
			return
		}
		for _, field := range service.Fields {
			if field.SecretRef == "" {
				continue
			}
			secret, err := a.store.GetSecret(r.Context(), field.SecretRef)
			if err != nil {
				problem(w, 422, "Service credential unavailable", "A selected service credential cannot be captured.")
				return
			}
			if secret.Source != "" && secret.Source != core.SecretSourceLocal {
				problem(w, 422, "Immutable service credential required", "External secret-store references need immutable version support. Use encrypted service fields or local secret references for this environment.")
				return
			}
		}
		services[service.ID], serviceRevisions[service.ID] = service, service.Revision
	}
	if err := serviceconn.ValidateBindings(input.ServiceBindings, core.BuildTypeDockerfile, services); err != nil {
		problem(w, 422, "Invalid service bindings", err.Error())
		return
	}
	plan, err := routing.Plan(core.Deployment{}, clone, target)
	if err != nil {
		problem(w, 422, "Managed route unavailable", err.Error())
		return
	}
	if plan != nil {
		clone.Domain = plan.Hostname
	}
	credential, err := d.GetEdgeCredential(r.Context(), target.AgentNodeID)
	if err != nil || credential.Revoked || credential.PublicKey == "" {
		problem(w, 422, "Target enrollment unavailable", "Choose a current enrolled runtime identity.")
		return
	}
	review := core.TemporaryEnvironmentReview{ServiceRevisions: serviceRevisions, Routing: target.Routing, Route: plan, TargetNodeID: target.AgentNodeID, TargetGeneration: credential.Generation, ID: ulid.Make().String(), EnvironmentID: ulid.Make().String(), Input: input, TemplateDigest: template.SpecDigest(), Clone: clone, Omissions: temporaryOmissions(), State: "prepared", CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	review.Digest = mutationHash(review)
	if err = d.CreateTemporaryEnvironmentReview(r.Context(), review); err != nil {
		a.internal(w, err)
		return
	}
	review.Clone = redactAppCredentials(review.Clone)
	writeJSON(w, 201, review)
}

type temporaryAcceptance struct {
	ReviewID    string `json:"reviewId"`
	Digest      string `json:"digest"`
	ConfirmName string `json:"confirmName"`
}

func (a *API) acceptTemporaryEnvironment(w http.ResponseWriter, r *http.Request) {
	d, ok := a.temporaryStore(w)
	if !ok {
		return
	}
	var input temporaryAcceptance
	if !decode(w, r, &input) {
		return
	}
	review, err := d.GetTemporaryEnvironmentReview(r.Context(), input.ReviewID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Environment review")
		return
	}
	if !a.temporaryPermission(w, r, review.Input.ProjectID, true) || !a.requireCredentialOwner(w, r, review.Clone.SourceCredentialID != "") {
		return
	}
	if input.Digest != review.Digest || input.ConfirmName != review.Input.Name {
		problem(w, 409, "Review changed", "Confirm the exact reviewed environment and name.")
		return
	}
	if r.Header.Get("Idempotency-Key") == "" {
		problem(w, 422, "Idempotency key required", "Supply an Idempotency-Key and reuse it for retries.")
		return
	}
	markTemporaryAudit(r.Context(), review.Input.ProjectID, review.Clone.ID, review.EnvironmentID, review.Input.Name, "environment.create", input.Digest)
	r, receipt, proceed := a.reserveMutation(w, r, review.Input.ProjectID, "environment.create", input, "deployment", review.EnvironmentID)
	if !proceed {
		return
	}
	if err = a.temporaryTarget(r.Context(), review.Input.ProjectID, review.Input.ServerID); err != nil {
		a.failMutationAcceptance(r.Context(), 409, "Target readiness or assignment changed.")
		problem(w, 409, "Target unavailable", err.Error())
		return
	}
	e, err := d.AcceptTemporaryEnvironment(r.Context(), review, currentIdentity(r.Context()), receipt.OperationID, time.Now().UTC())
	if err != nil {
		a.failMutationAcceptance(r.Context(), 409, "Environment review or quota changed.")
		problem(w, 409, "Environment acceptance rejected", err.Error())
		return
	}
	core.RecordAcceptedOperation(r.Context(), e.DeploymentID)
	saved, err := d.GetMutationReceipt(r.Context(), receipt.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	a.writeMutationReceipt(w, r, saved, 202)
}
func (a *API) listTemporaryEnvironments(w http.ResponseWriter, r *http.Request) {
	d, ok := a.temporaryStore(w)
	if !ok {
		return
	}
	project := chi.URLParam(r, "id")
	if !a.temporaryPermission(w, r, project, false) {
		return
	}
	items, err := d.ListTemporaryEnvironments(r.Context(), project)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (a *API) temporaryRecord(w http.ResponseWriter, r *http.Request, run bool) (*store.SQLStore, core.TemporaryEnvironment, bool) {
	d, ok := a.temporaryStore(w)
	if !ok {
		return nil, core.TemporaryEnvironment{}, false
	}
	e, err := d.GetTemporaryEnvironment(r.Context(), chi.URLParam(r, "environmentId"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Temporary environment")
		return d, e, false
	}
	return d, e, a.temporaryPermission(w, r, e.ProjectID, run)
}
func (a *API) getTemporaryEnvironment(w http.ResponseWriter, r *http.Request) {
	_, e, ok := a.temporaryRecord(w, r, false)
	if ok {
		writeJSON(w, 200, e)
	}
}
func (a *API) extendTemporaryEnvironment(w http.ResponseWriter, r *http.Request) {
	d, e, ok := a.temporaryRecord(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Revision  int64     `json:"revision"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := a.temporaryTarget(r.Context(), e.ProjectID, e.ServerID); err != nil {
		problem(w, 409, "Target unavailable", err.Error())
		return
	}
	markTemporaryAudit(r.Context(), e.ProjectID, e.AppID, e.ID, e.Name, "environment.extend", mutationHash(input))
	if err := a.temporaryAuthority(r.Context(), e); err != nil {
		problem(w, 409, "Environment authority changed", "The accepted actor or target identity is no longer authorized.")
		return
	}
	if err := d.ExtendTemporaryEnvironment(r.Context(), e.ID, input.Revision, input.ExpiresAt, time.Now().UTC()); err != nil {
		problem(w, 409, "Extension rejected", "The revision, deadline or maximum total lifetime changed.")
		return
	}
	e, _ = d.GetTemporaryEnvironment(r.Context(), e.ID)
	writeJSON(w, 200, e)
}

type temporaryCleanupReview struct {
	EnvironmentID string                   `json:"environmentId"`
	Revision      int64                    `json:"revision"`
	Name          string                   `json:"name"`
	Digest        string                   `json:"digest"`
	Resources     []core.TemporaryResource `json:"resources"`
	Summary       string                   `json:"summary"`
}

func (a *API) temporaryCleanupReview(ctx context.Context, d *store.SQLStore, e core.TemporaryEnvironment) (temporaryCleanupReview, error) {
	resources := append([]core.TemporaryResource{}, e.Resources...)
	volumes, err := d.ListStorage(ctx, e.ServerID)
	if err != nil {
		return temporaryCleanupReview{}, err
	}
	seen := map[string]bool{}
	for _, resource := range resources {
		seen[resource.Kind+":"+resource.ID] = true
	}
	for _, v := range volumes {
		if v.OwnerID == e.AppID && !seen[v.Kind+":"+v.ID] {
			resources = append(resources, core.TemporaryResource{Kind: v.Kind, ID: v.ID, Ownership: "retained"})
		}
	}
	review := temporaryCleanupReview{EnvironmentID: e.ID, Revision: e.Revision, Name: e.Name, Resources: resources, Summary: "Stop new work, settle or inspect active operations, then remove the owned workload and route. Retain the shared server, named volumes, backups and execution history. Uncertain operations block cleanup until inspected."}
	review.Digest = mutationHash(review)
	return review, nil
}
func (a *API) reviewTemporaryCleanup(w http.ResponseWriter, r *http.Request) {
	d, e, ok := a.temporaryRecord(w, r, true)
	if !ok {
		return
	}
	review, err := a.temporaryCleanupReview(r.Context(), d, e)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, review)
}
func (a *API) destroyTemporaryEnvironment(w http.ResponseWriter, r *http.Request) {
	data, e, ok := a.temporaryRecord(w, r, true)
	if !ok || !a.requireProject(w, r, core.PermissionDeploymentCancel, e.ProjectID) {
		return
	}
	var input struct {
		Revision    int64  `json:"revision"`
		Digest      string `json:"digest"`
		ConfirmName string `json:"confirmName"`
	}
	if !decode(w, r, &input) {
		return
	}
	if r.Header.Get("Idempotency-Key") == "" {
		problem(w, 422, "Idempotency key required", "Supply an Idempotency-Key and reuse it for retries.")
		return
	}
	markTemporaryAudit(r.Context(), e.ProjectID, e.AppID, e.ID, e.Name, "environment.destroy", input.Digest)
	r, receipt, proceed := a.reserveMutation(w, r, e.ProjectID, "environment.destroy", input, "temporary_environment", e.ID)
	if !proceed {
		return
	}
	review, err := a.temporaryCleanupReview(r.Context(), data, e)
	if err == nil && (input.ConfirmName != e.Name || input.Digest != review.Digest || input.Revision != e.Revision) {
		err = store.ErrTemporaryEnvironmentChanged
	}
	if err == nil {
		err = data.StopTemporaryEnvironment(r.Context(), e.ID, input.Revision, time.Now().UTC())
	}
	if err != nil {
		a.failMutationAcceptance(r.Context(), 409, "Cleanup review or operation changed.")
		problem(w, 409, "Cleanup review changed", "Inspect the current operation and review cleanup again. A pending or unknown cleanup cannot be replaced.")
		return
	}
	_ = a.deploy.Cancel(r.Context(), e.DeploymentID)
	core.RecordAcceptedOperation(r.Context(), receipt.OperationID)
	saved, err := data.GetMutationReceipt(r.Context(), receipt.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	a.writeMutationReceipt(w, r, saved, 202)
}

func markTemporaryAudit(ctx context.Context, project, app, id, name, action, digest string) {
	if audit, ok := ctx.Value(destructiveAuditKey{}).(*destructiveAudit); ok {
		audit.Review = &destructiveReview{ResourceID: id, ResourceType: "temporary_environment", ProjectID: project, AppID: app, Name: name, Action: action, Version: digest, StoragePolicy: "retain"}
	}
}
