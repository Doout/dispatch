package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/bcrypt"
)

type identityContextKey struct{}
type impersonatorContextKey struct{}

func withIdentity(ctx context.Context, identity core.Identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, identity)
}

func currentIdentity(ctx context.Context) core.Identity {
	identity, _ := ctx.Value(identityContextKey{}).(core.Identity)
	return identity
}

func withImpersonator(ctx context.Context, identity core.Identity) context.Context {
	return context.WithValue(ctx, impersonatorContextKey{}, identity)
}

func currentImpersonator(ctx context.Context) (core.Identity, bool) {
	identity, ok := ctx.Value(impersonatorContextKey{}).(core.Identity)
	return identity, ok
}

func controllerPermissions() []core.Permission {
	return []core.Permission{
		core.PermissionAccessManage,
		core.PermissionProjectView,
		core.PermissionProjectManage,
		core.PermissionProjectAccess,
		core.PermissionProjectConfigure,
		core.PermissionDeploymentRun,
		core.PermissionDeploymentCancel,
		core.PermissionStageApprove,
		core.PermissionInfrastructureManage,
		core.PermissionSecretsManage,
		core.PermissionConnectionsManage,
	}
}

func controllerIdentity(username string) core.Identity {
	return core.Identity{ID: "controller-owner", Username: username, DisplayName: username, SystemRole: core.UserRoleOwner, Permissions: controllerPermissions()}
}

func identityForUser(user core.User) core.Identity {
	identity := core.Identity{ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, SystemRole: user.SystemRole}
	if user.SystemRole == core.UserRoleOwner {
		identity.Permissions = controllerPermissions()
	}
	return identity
}

func (a *API) requireControllerOwner(w http.ResponseWriter, r *http.Request) bool {
	if currentIdentity(r.Context()).SystemRole == core.UserRoleOwner {
		return true
	}
	problem(w, http.StatusForbidden, "Access denied", "Controller owner access is required.")
	return false
}

func (a *API) requireCredentialOwner(w http.ResponseWriter, r *http.Request, credentialSelected bool) bool {
	if !credentialSelected || currentIdentity(r.Context()).SystemRole == core.UserRoleOwner {
		return true
	}
	problem(w, http.StatusForbidden, "Credential access denied", "A controller owner must attach global credentials.")
	return false
}

func redactAppCredentials(item core.App) core.App {
	item.SourceCredentialID = ""
	item.HookSecretIDs = nil
	return item
}

func redactEventTriggerCredentials(item core.EventTrigger) core.EventTrigger {
	item.GitHubAppID = ""
	item.SecretIDs = nil
	return item
}

func redactPreviewGroupCredentials(item core.PreviewGroup) core.PreviewGroup {
	item.GitHubAppID = ""
	for index := range item.Components {
		item.Components[index].SecretIDs = nil
	}
	return item
}

func redactPreviewGroupRunCredentials(item core.PreviewGroupRun) core.PreviewGroupRun {
	if item.Group != nil {
		redacted := redactPreviewGroupCredentials(*item.Group)
		item.Group = &redacted
	}
	for index := range item.Attempts {
		item.Attempts[index].Configuration = redactPreviewGroupCredentials(item.Attempts[index].Configuration)
	}
	return item
}

func redactConfigSourceCredentials(item core.ConfigSource) core.ConfigSource {
	item.GitHubAppID = ""
	item.CredentialSecretID = ""
	return item
}

func (a *API) effectiveAssignments(ctx context.Context, userID string) ([]core.RoleAssignment, error) {
	assignments, err := a.store.ListRoleAssignments(ctx)
	if err != nil {
		return nil, err
	}
	members, err := a.store.ListTeamMembers(ctx)
	if err != nil {
		return nil, err
	}
	teams := map[string]bool{}
	for _, member := range members {
		if member.UserID == userID {
			teams[member.TeamID] = true
		}
	}
	result := []core.RoleAssignment{}
	for _, assignment := range assignments {
		if assignment.PrincipalType == core.PrincipalUser && assignment.PrincipalID == userID || assignment.PrincipalType == core.PrincipalTeam && teams[assignment.PrincipalID] {
			result = append(result, assignment)
		}
	}
	return result, nil
}

func (a *API) canProject(ctx context.Context, permission core.Permission, projectID string) (bool, error) {
	identity := currentIdentity(ctx)
	if identity.SystemRole == core.UserRoleOwner {
		return true, nil
	}
	assignments, err := a.effectiveAssignments(ctx, identity.ID)
	if err != nil {
		return false, err
	}
	for _, assignment := range assignments {
		if assignment.ScopeType == core.ScopeProject && assignment.ScopeID == projectID && core.RoleAllows(assignment.Role, permission) {
			return true, nil
		}
	}
	return false, nil
}

func (a *API) visibleProjectIDs(ctx context.Context) (map[string]bool, error) {
	identity := currentIdentity(ctx)
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	visible := map[string]bool{}
	if identity.SystemRole == core.UserRoleOwner {
		for _, project := range projects {
			visible[project.ID] = true
		}
		return visible, nil
	}
	assignments, err := a.effectiveAssignments(ctx, identity.ID)
	if err != nil {
		return nil, err
	}
	for _, assignment := range assignments {
		if assignment.ScopeType == core.ScopeProject && core.RoleAllows(assignment.Role, core.PermissionProjectView) {
			visible[assignment.ScopeID] = true
		}
	}
	return visible, nil
}

func (a *API) visibleAppIDs(ctx context.Context) (map[string]bool, error) {
	projects, err := a.visibleProjectIDs(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := a.store.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	visible := map[string]bool{}
	for _, app := range apps {
		if projects[app.ProjectID] {
			visible[app.ID] = true
		}
	}
	return visible, nil
}

func (a *API) requireProject(w http.ResponseWriter, r *http.Request, permission core.Permission, projectID string) bool {
	allowed, err := a.canProject(r.Context(), permission, projectID)
	if err != nil {
		a.internal(w, err)
		return false
	}
	if allowed {
		return true
	}
	problem(w, http.StatusForbidden, "Access denied", "Your project role does not allow this action.")
	return false
}

func (a *API) ownerOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.requireControllerOwner(w, r) {
			next(w, r)
		}
	}
}

func (a *API) directUserOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, impersonating := currentImpersonator(r.Context()); impersonating {
			problem(w, http.StatusForbidden, "Unavailable while viewing as another user", "Return to your account before changing sign-in credentials.")
			return
		}
		next(w, r)
	}
}

func (a *API) projectPermission(permission core.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.requireProject(w, r, permission, chi.URLParam(r, "id")) {
			next(w, r)
		}
	}
}

func (a *API) appPermission(permission core.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Application not found", "This application no longer exists.")
			return
		}
		if err != nil {
			a.internal(w, err)
			return
		}
		if a.requireProject(w, r, permission, app.ProjectID) {
			next(w, r)
		}
	}
}

func (a *API) requireApps(w http.ResponseWriter, r *http.Request, permission core.Permission, appIDs []string) bool {
	checked := map[string]bool{}
	if len(appIDs) == 0 {
		problem(w, http.StatusBadRequest, "Application required", "Choose at least one application.")
		return false
	}
	for _, appID := range appIDs {
		appID = strings.TrimSpace(appID)
		if appID == "" || checked[appID] {
			continue
		}
		app, err := a.store.GetApp(r.Context(), appID)
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusBadRequest, "Application unavailable", "Choose an application you can access.")
			return false
		}
		if err != nil {
			a.internal(w, err)
			return false
		}
		if !a.requireProject(w, r, permission, app.ProjectID) {
			return false
		}
		checked[appID] = true
	}
	return len(checked) > 0
}

func previewGroupAppIDs(group core.PreviewGroup) []string {
	appIDs := make([]string, 0, len(group.Components))
	for _, component := range group.Components {
		appIDs = append(appIDs, component.AppID)
	}
	return appIDs
}

func (a *API) previewGroupPermission(permission core.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		group, err := a.store.GetPreviewGroup(r.Context(), chi.URLParam(r, "id"))
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Preview group not found", "This preview group no longer exists.")
			return
		}
		if err != nil {
			a.internal(w, err)
			return
		}
		if a.requireApps(w, r, permission, previewGroupAppIDs(group)) {
			next(w, r)
		}
	}
}

func (a *API) previewGroupRunPermission(permission core.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		run, err := a.store.GetPreviewGroupRun(r.Context(), chi.URLParam(r, "id"))
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Preview group run not found", "This preview run no longer exists.")
			return
		}
		if err != nil {
			a.internal(w, err)
			return
		}
		group, err := a.store.GetPreviewGroup(r.Context(), run.GroupID)
		if err != nil {
			a.internal(w, err)
			return
		}
		if a.requireApps(w, r, permission, previewGroupAppIDs(group)) {
			next(w, r)
		}
	}
}

func (a *API) eventTriggerPermission(permission core.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := a.store.ListEventTriggers(r.Context(), "")
		if err != nil {
			a.internal(w, err)
			return
		}
		for _, item := range items {
			if item.ID != chi.URLParam(r, "id") {
				continue
			}
			app, appErr := a.store.GetApp(r.Context(), item.AppID)
			if appErr != nil {
				a.internal(w, appErr)
				return
			}
			if a.requireProject(w, r, permission, app.ProjectID) {
				next(w, r)
			}
			return
		}
		problem(w, http.StatusNotFound, "Event trigger not found", "This event trigger no longer exists.")
	}
}

func (a *API) deploymentPermission(permission core.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deployment, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Deployment not found", "This deployment no longer exists.")
			return
		}
		if err != nil {
			a.internal(w, err)
			return
		}
		app, err := a.store.GetApp(r.Context(), deployment.AppID)
		if err != nil {
			a.internal(w, err)
			return
		}
		if a.requireProject(w, r, permission, app.ProjectID) {
			next(w, r)
		}
	}
}

func (a *API) configSourcePermission(permission core.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		source, err := a.store.GetConfigSource(r.Context(), chi.URLParam(r, "id"))
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Configuration source not found", "This source no longer exists.")
			return
		}
		if err != nil {
			a.internal(w, err)
			return
		}
		if a.requireProject(w, r, permission, source.ProjectID) {
			next(w, r)
		}
	}
}

func (a *API) workflowResourcePermission(permission core.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resource, err := a.store.GetWorkflowResource(r.Context(), chi.URLParam(r, "id"))
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Workflow resource not found", "This resource no longer exists.")
			return
		}
		if err != nil {
			a.internal(w, err)
			return
		}
		source, err := a.store.GetConfigSource(r.Context(), resource.ConfigSourceID)
		if err != nil {
			a.internal(w, err)
			return
		}
		if a.requireProject(w, r, permission, source.ProjectID) {
			next(w, r)
		}
	}
}

func (a *API) workflowRevisionPermission(permission core.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		revision, err := a.store.GetWorkflowRevision(r.Context(), chi.URLParam(r, "id"))
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Workflow revision not found", "This revision no longer exists.")
			return
		}
		if err != nil {
			a.internal(w, err)
			return
		}
		resource, err := a.store.GetWorkflowResource(r.Context(), revision.ResourceID)
		if err != nil {
			a.internal(w, err)
			return
		}
		source, err := a.store.GetConfigSource(r.Context(), resource.ConfigSourceID)
		if err != nil {
			a.internal(w, err)
			return
		}
		if a.requireProject(w, r, permission, source.ProjectID) {
			next(w, r)
		}
	}
}

func (a *API) workflowStagePermission(permission core.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stage, err := a.store.GetWorkflowStageRun(r.Context(), chi.URLParam(r, "id"))
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Stage not found", "This stage no longer exists.")
			return
		}
		if err != nil {
			a.internal(w, err)
			return
		}
		revision, err := a.store.GetWorkflowRevision(r.Context(), stage.RevisionID)
		if err != nil {
			a.internal(w, err)
			return
		}
		resource, err := a.store.GetWorkflowResource(r.Context(), revision.ResourceID)
		if err != nil {
			a.internal(w, err)
			return
		}
		source, err := a.store.GetConfigSource(r.Context(), resource.ConfigSourceID)
		if err != nil {
			a.internal(w, err)
			return
		}
		if a.requireProject(w, r, permission, source.ProjectID) {
			next(w, r)
		}
	}
}

func (a *API) serverPermission(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity := currentIdentity(r.Context())
		if identity.SystemRole == core.UserRoleOwner {
			next(w, r)
			return
		}
		serverID := chi.URLParam(r, "id")
		projects, err := a.visibleProjectIDs(r.Context())
		if err != nil {
			a.internal(w, err)
			return
		}
		apps, err := a.store.ListApps(r.Context())
		if err != nil {
			a.internal(w, err)
			return
		}
		for _, app := range apps {
			if app.ServerID == serverID && projects[app.ProjectID] {
				next(w, r)
				return
			}
		}
		problem(w, http.StatusForbidden, "Access denied", "This server is not used by one of your projects.")
	}
}

func (a *API) authMe(w http.ResponseWriter, r *http.Request) {
	identity := currentIdentity(r.Context())
	assignments := []core.RoleAssignment{}
	if identity.SystemRole != core.UserRoleOwner {
		var err error
		assignments, err = a.effectiveAssignments(r.Context(), identity.ID)
		if err != nil {
			a.internal(w, err)
			return
		}
	}
	result := map[string]any{"identity": identity, "assignments": assignments}
	if impersonator, ok := currentImpersonator(r.Context()); ok {
		result["impersonator"] = impersonator
	}
	writeJSON(w, http.StatusOK, result)
}

type passwordChangeRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	identity := currentIdentity(r.Context())
	if identity.ID == "" || identity.ID == "controller-owner" {
		problem(w, http.StatusConflict, "Password managed outside Dispatch", "Change the controller credentials in the server configuration.")
		return
	}
	var input passwordChangeRequest
	if !decode(w, r, &input) {
		return
	}
	if len([]byte(input.NewPassword)) < 12 || len([]byte(input.NewPassword)) > 72 {
		problem(w, http.StatusBadRequest, "Invalid password", "Use a password between 12 and 72 bytes.")
		return
	}
	user, err := a.store.GetUser(r.Context(), identity.ID)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "User not found", "Your account no longer exists.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.CurrentPassword)) != nil {
		problem(w, http.StatusBadRequest, "Current password is incorrect", "Enter your current password and try again.")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.NewPassword)) == nil {
		problem(w, http.StatusBadRequest, "Password unchanged", "Choose a different password.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		a.internal(w, err)
		return
	}
	user.PasswordHash = string(hash)
	user.UpdatedAt = time.Now().UTC()
	if err := a.store.UpdateUser(r.Context(), user); err != nil {
		a.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) accessOverview(w http.ResponseWriter, r *http.Request) {
	if !a.requireControllerOwner(w, r) {
		return
	}
	users, err := a.store.ListUsers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	teams, err := a.store.ListTeams(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	members, err := a.store.ListTeamMembers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	assignments, err := a.store.ListRoleAssignments(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	projects, err := a.store.ListProjects(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	providers, err := a.store.ListAuthProviders(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	identities, err := a.store.ListExternalIdentities(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, core.AccessOverview{Users: users, Teams: teams, Members: members, Assignments: assignments, Roles: core.ProjectRoles(), Projects: projects, Providers: providers, Identities: identities})
}

type userRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	SystemRole  string `json:"systemRole"`
	State       string `json:"state"`
}

func validSystemRole(role string) bool {
	return role == core.UserRoleOwner || role == core.UserRoleMember
}
func validUserState(state string) bool {
	return state == core.UserStateActive || state == core.UserStatePending || state == core.UserStateDisabled
}

func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	if !a.requireControllerOwner(w, r) {
		return
	}
	var input userRequest
	if !decode(w, r, &input) {
		return
	}
	input.Username, input.DisplayName, input.Email = strings.TrimSpace(input.Username), strings.TrimSpace(input.DisplayName), strings.TrimSpace(input.Email)
	if input.DisplayName == "" {
		input.DisplayName = input.Username
	}
	if input.SystemRole == "" {
		input.SystemRole = core.UserRoleMember
	}
	if input.State == "" {
		input.State = core.UserStateActive
	}
	if detail := validateCredentials(input.Username, input.Password); detail != "" || len(input.DisplayName) > 100 || !validSystemRole(input.SystemRole) || !validUserState(input.State) {
		if detail == "" {
			detail = "Check the name, role, and account status."
		}
		problem(w, http.StatusBadRequest, "Invalid user", detail)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		a.internal(w, err)
		return
	}
	now := time.Now().UTC()
	user := core.User{ID: ulid.Make().String(), Username: input.Username, DisplayName: input.DisplayName, Email: input.Email, PasswordHash: string(hash), PasswordConfigured: true, SystemRole: input.SystemRole, State: input.State, CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(r.Context(), user); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			problem(w, http.StatusConflict, "Username already used", "Choose another username.")
			return
		}
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (a *API) updateUser(w http.ResponseWriter, r *http.Request) {
	if !a.requireControllerOwner(w, r) {
		return
	}
	user, err := a.store.GetUser(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "User not found", "This user no longer exists.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	var input userRequest
	if !decode(w, r, &input) {
		return
	}
	if input.Password != "" {
		problem(w, http.StatusBadRequest, "Password change not allowed", "Users can only change their own password.")
		return
	}
	input.Username, input.DisplayName, input.Email = strings.TrimSpace(input.Username), strings.TrimSpace(input.DisplayName), strings.TrimSpace(input.Email)
	if input.Username == "" {
		input.Username = user.Username
	}
	if input.DisplayName == "" {
		input.DisplayName = input.Username
	}
	if input.SystemRole == "" {
		input.SystemRole = user.SystemRole
	}
	if input.State == "" {
		input.State = user.State
	}
	if !validSystemRole(input.SystemRole) || !validUserState(input.State) || len(input.Username) < 3 || len(input.Username) > 64 || len(input.DisplayName) > 100 {
		problem(w, http.StatusBadRequest, "Invalid user", "Check the name, role, and account status.")
		return
	}
	if user.SystemRole == core.UserRoleOwner && user.State == core.UserStateActive && (input.SystemRole != core.UserRoleOwner || input.State != core.UserStateActive) {
		owners, countErr := a.store.CountOwners(r.Context())
		if countErr != nil {
			a.internal(w, countErr)
			return
		}
		if owners <= 1 {
			problem(w, http.StatusConflict, "Owner required", "Keep at least one active controller owner.")
			return
		}
	}
	user.Username, user.DisplayName, user.Email, user.SystemRole, user.State, user.UpdatedAt = input.Username, input.DisplayName, input.Email, input.SystemRole, input.State, time.Now().UTC()
	user.PasswordHash = ""
	if err := a.store.UpdateUser(r.Context(), user); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			problem(w, http.StatusConflict, "Username already used", "Choose another username.")
			return
		}
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (a *API) deleteUser(w http.ResponseWriter, r *http.Request) {
	if !a.requireControllerOwner(w, r) {
		return
	}
	user, err := a.store.GetUser(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "User not found", "This user no longer exists.")
			return
		}
		a.internal(w, err)
		return
	}
	if user.ID == currentIdentity(r.Context()).ID {
		problem(w, http.StatusConflict, "Cannot remove current user", "Sign in as another owner first.")
		return
	}
	if user.SystemRole == core.UserRoleOwner && user.State == core.UserStateActive {
		owners, countErr := a.store.CountOwners(r.Context())
		if countErr != nil {
			a.internal(w, countErr)
			return
		}
		if owners <= 1 {
			problem(w, http.StatusConflict, "Owner required", "Keep at least one active controller owner.")
			return
		}
	}
	if err := a.store.DeleteUser(r.Context(), user.ID); err != nil {
		a.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type teamRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	MemberIDs   []string `json:"memberIds"`
}

func (a *API) createTeam(w http.ResponseWriter, r *http.Request) {
	if !a.requireControllerOwner(w, r) {
		return
	}
	var input teamRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name, input.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	if input.Name == "" || len(input.Name) > 80 || len(input.Description) > 240 {
		problem(w, http.StatusBadRequest, "Invalid team", "Enter a team name no longer than 80 characters.")
		return
	}
	now := time.Now().UTC()
	team := core.Team{ID: ulid.Make().String(), Name: input.Name, Description: input.Description, CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateTeam(r.Context(), team); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			problem(w, http.StatusConflict, "Team name already used", "Choose another team name.")
			return
		}
		a.internal(w, err)
		return
	}
	if err := a.replaceMembers(r.Context(), team.ID, input.MemberIDs, now); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, team)
}

func (a *API) updateTeam(w http.ResponseWriter, r *http.Request) {
	if !a.requireControllerOwner(w, r) {
		return
	}
	team, err := a.store.GetTeam(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Team not found", "This team no longer exists.")
			return
		}
		a.internal(w, err)
		return
	}
	var input teamRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name, input.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	if input.Name == "" || len(input.Name) > 80 || len(input.Description) > 240 {
		problem(w, http.StatusBadRequest, "Invalid team", "Enter a team name no longer than 80 characters.")
		return
	}
	team.Name, team.Description, team.UpdatedAt = input.Name, input.Description, time.Now().UTC()
	if err := a.store.UpdateTeam(r.Context(), team); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			problem(w, http.StatusConflict, "Team name already used", "Choose another team name.")
			return
		}
		a.internal(w, err)
		return
	}
	if err := a.replaceMembers(r.Context(), team.ID, input.MemberIDs, team.UpdatedAt); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, team)
}

func (a *API) replaceMembers(ctx context.Context, teamID string, userIDs []string, now time.Time) error {
	members := make([]core.TeamMember, 0, len(userIDs))
	seen := map[string]bool{}
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID == "" || seen[userID] {
			continue
		}
		if _, err := a.store.GetUser(ctx, userID); err != nil {
			return err
		}
		seen[userID] = true
		members = append(members, core.TeamMember{TeamID: teamID, UserID: userID, Role: core.TeamMemberRoleMember, CreatedAt: now})
	}
	return a.store.ReplaceTeamMembers(ctx, teamID, members)
}

func (a *API) deleteTeam(w http.ResponseWriter, r *http.Request) {
	if !a.requireControllerOwner(w, r) {
		return
	}
	if err := a.store.DeleteTeam(r.Context(), chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Team not found", "This team no longer exists.")
			return
		}
		a.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type roleAssignmentRequest struct {
	PrincipalType string `json:"principalType"`
	PrincipalID   string `json:"principalId"`
	ProjectID     string `json:"projectId"`
	Role          string `json:"role"`
}

func validProjectRole(role string) bool {
	for _, definition := range core.ProjectRoles() {
		if definition.ID == role {
			return true
		}
	}
	return false
}

func (a *API) upsertRoleAssignment(w http.ResponseWriter, r *http.Request) {
	if !a.requireControllerOwner(w, r) {
		return
	}
	var input roleAssignmentRequest
	if !decode(w, r, &input) {
		return
	}
	if input.PrincipalType != core.PrincipalUser && input.PrincipalType != core.PrincipalTeam || !validProjectRole(input.Role) {
		problem(w, http.StatusBadRequest, "Invalid access grant", "Choose a user or team, a project, and a project role.")
		return
	}
	if _, err := a.store.GetProject(r.Context(), input.ProjectID); err != nil {
		problem(w, http.StatusBadRequest, "Invalid access grant", "Choose an available project.")
		return
	}
	if input.PrincipalType == core.PrincipalUser {
		_, err := a.store.GetUser(r.Context(), input.PrincipalID)
		if err != nil {
			problem(w, http.StatusBadRequest, "Invalid access grant", "Choose an available user.")
			return
		}
	} else {
		_, err := a.store.GetTeam(r.Context(), input.PrincipalID)
		if err != nil {
			problem(w, http.StatusBadRequest, "Invalid access grant", "Choose an available team.")
			return
		}
	}
	now := time.Now().UTC()
	assignment := core.RoleAssignment{ID: ulid.Make().String(), PrincipalType: input.PrincipalType, PrincipalID: input.PrincipalID, ScopeType: core.ScopeProject, ScopeID: input.ProjectID, Role: input.Role, CreatedAt: now, UpdatedAt: now}
	if err := a.store.UpsertRoleAssignment(r.Context(), assignment); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, assignment)
}

func (a *API) deleteRoleAssignment(w http.ResponseWriter, r *http.Request) {
	if !a.requireControllerOwner(w, r) {
		return
	}
	if err := a.store.DeleteRoleAssignment(r.Context(), chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Access grant not found", "This grant no longer exists.")
			return
		}
		a.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) filterOverview(ctx context.Context, overview core.Overview) (core.Overview, error) {
	identity := currentIdentity(ctx)
	overview.ProjectPermissions = make(map[string][]core.Permission)
	if identity.SystemRole == core.UserRoleOwner {
		for _, project := range overview.Projects {
			overview.ProjectPermissions[project.ID] = controllerPermissions()
		}
		return overview, nil
	}
	assignments, err := a.effectiveAssignments(ctx, identity.ID)
	if err != nil {
		return overview, err
	}
	projectIDs := map[string]bool{}
	projectPermissionSets := map[string]map[core.Permission]bool{}
	for _, assignment := range assignments {
		if assignment.ScopeType == core.ScopeProject && core.RoleAllows(assignment.Role, core.PermissionProjectView) {
			projectIDs[assignment.ScopeID] = true
		}
		if assignment.ScopeType != core.ScopeProject {
			continue
		}
		if projectPermissionSets[assignment.ScopeID] == nil {
			projectPermissionSets[assignment.ScopeID] = make(map[core.Permission]bool)
		}
		for _, role := range core.ProjectRoles() {
			if role.ID != assignment.Role {
				continue
			}
			for _, permission := range role.Permissions {
				projectPermissionSets[assignment.ScopeID][permission] = true
			}
		}
	}
	for projectID, permissionSet := range projectPermissionSets {
		for _, permission := range controllerPermissions() {
			if permissionSet[permission] {
				overview.ProjectPermissions[projectID] = append(overview.ProjectPermissions[projectID], permission)
			}
		}
	}
	projects := overview.Projects[:0]
	for _, project := range overview.Projects {
		if projectIDs[project.ID] {
			projects = append(projects, project)
		}
	}
	overview.Projects = projects

	appIDs, serverIDs := map[string]bool{}, map[string]bool{}
	apps := overview.Apps[:0]
	for _, app := range overview.Apps {
		if projectIDs[app.ProjectID] {
			apps = append(apps, redactAppCredentials(app))
			appIDs[app.ID] = true
			serverIDs[app.ServerID] = true
		}
	}
	overview.Apps = apps
	deployments := overview.Deployments[:0]
	for _, deployment := range overview.Deployments {
		if appIDs[deployment.AppID] {
			if deployment.App != nil {
				redacted := redactAppCredentials(*deployment.App)
				deployment.App = &redacted
			}
			deployments = append(deployments, deployment)
		}
	}
	overview.Deployments = deployments
	servers := overview.Servers[:0]
	for _, server := range overview.Servers {
		if serverIDs[server.ID] {
			servers = append(servers, server)
		}
	}
	overview.Servers = servers
	eventTriggers := overview.EventTriggers[:0]
	for _, trigger := range overview.EventTriggers {
		if appIDs[trigger.AppID] {
			eventTriggers = append(eventTriggers, redactEventTriggerCredentials(trigger))
		}
	}
	overview.EventTriggers = eventTriggers
	previews := overview.Previews[:0]
	for _, preview := range overview.Previews {
		if appIDs[preview.AppID] {
			previews = append(previews, preview)
		}
	}
	overview.Previews = previews

	groupIDs := map[string]bool{}
	groups := overview.PreviewGroups[:0]
	for _, group := range overview.PreviewGroups {
		if previewGroupAppsVisible(group, appIDs) {
			groups = append(groups, redactPreviewGroupCredentials(group))
			groupIDs[group.ID] = true
		}
	}
	overview.PreviewGroups = groups
	groupRuns := overview.PreviewGroupRuns[:0]
	for _, run := range overview.PreviewGroupRuns {
		if groupIDs[run.GroupID] {
			groupRuns = append(groupRuns, redactPreviewGroupRunCredentials(run))
		}
	}
	overview.PreviewGroupRuns = groupRuns

	configSourceIDs := map[string]bool{}
	configSources := overview.ConfigSources[:0]
	for _, source := range overview.ConfigSources {
		if projectIDs[source.ProjectID] {
			configSources = append(configSources, redactConfigSourceCredentials(source))
			configSourceIDs[source.ID] = true
		}
	}
	overview.ConfigSources = configSources
	resourceIDs := map[string]bool{}
	resources := overview.WorkflowResources[:0]
	for _, resource := range overview.WorkflowResources {
		if configSourceIDs[resource.ConfigSourceID] {
			resources = append(resources, resource)
			resourceIDs[resource.ID] = true
		}
	}
	overview.WorkflowResources = resources
	revisionIDs := map[string]bool{}
	revisions := overview.WorkflowRevisions[:0]
	for _, revision := range overview.WorkflowRevisions {
		if resourceIDs[revision.ResourceID] {
			revisions = append(revisions, revision)
			revisionIDs[revision.ID] = true
		}
	}
	overview.WorkflowRevisions = revisions
	stageRuns := overview.WorkflowStageRuns[:0]
	for _, run := range overview.WorkflowStageRuns {
		if revisionIDs[run.RevisionID] {
			stageRuns = append(stageRuns, run)
		}
	}
	overview.WorkflowStageRuns = stageRuns

	// Controller-level credentials and connections are not project resources.
	overview.Secrets = []core.Secret{}
	overview.SecretStores = []core.SecretStore{}
	overview.PrivateNetworks = []core.PrivateNetwork{}
	overview.GitHubApps = []core.GitHubAppConnection{}
	overview.RelayWebhooks = []core.RelayWebhook{}
	return overview, nil
}
