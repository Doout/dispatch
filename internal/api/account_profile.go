package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

type accountProfileTeam struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type accountProfileAccess struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Role        string `json:"role"`
	Source      string `json:"source"`
	SourceName  string `json:"sourceName,omitempty"`
}

type accountProfileResponse struct {
	User          core.User              `json:"user"`
	Managed       bool                   `json:"managed"`
	Links         []accountAuthLink      `json:"links"`
	Teams         []accountProfileTeam   `json:"teams"`
	ProjectAccess []accountProfileAccess `json:"projectAccess"`
}

func (a *API) accountProfile(w http.ResponseWriter, r *http.Request) {
	identity := currentIdentity(r.Context())
	if identity.ID == "" {
		problem(w, http.StatusUnauthorized, "Sign in required", "Sign in to view your profile.")
		return
	}
	if identity.ID == "controller-owner" {
		writeJSON(w, http.StatusOK, accountProfileResponse{
			User: core.User{
				ID:                 identity.ID,
				Username:           identity.Username,
				DisplayName:        identity.DisplayName,
				PasswordConfigured: true,
				SystemRole:         identity.SystemRole,
				State:              core.UserStateActive,
			},
			Managed:       true,
			Links:         []accountAuthLink{},
			Teams:         []accountProfileTeam{},
			ProjectAccess: []accountProfileAccess{},
		})
		return
	}
	profile, err := a.profileForUser(r.Context(), identity.ID)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "User not found", "Your account no longer exists.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (a *API) userProfile(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(chi.URLParam(r, "id"))
	profile, err := a.profileForUser(r.Context(), userID)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "User not found", "This user no longer exists.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (a *API) profileForUser(ctx context.Context, userID string) (accountProfileResponse, error) {
	user, err := a.store.GetUser(ctx, userID)
	if err != nil {
		return accountProfileResponse{}, err
	}
	links, err := a.accountLinksForUser(ctx, userID)
	if err != nil {
		return accountProfileResponse{}, err
	}
	members, err := a.store.ListTeamMembers(ctx)
	if err != nil {
		return accountProfileResponse{}, err
	}
	allTeams, err := a.store.ListTeams(ctx)
	if err != nil {
		return accountProfileResponse{}, err
	}
	assignments, err := a.store.ListRoleAssignments(ctx)
	if err != nil {
		return accountProfileResponse{}, err
	}
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		return accountProfileResponse{}, err
	}

	teamByID := make(map[string]core.Team, len(allTeams))
	for _, team := range allTeams {
		teamByID[team.ID] = team
	}
	projectByID := make(map[string]core.Project, len(projects))
	for _, project := range projects {
		projectByID[project.ID] = project
	}
	teamMemberships := map[string]accountProfileTeam{}
	teams := []accountProfileTeam{}
	for _, member := range members {
		if member.UserID != userID {
			continue
		}
		team, ok := teamByID[member.TeamID]
		if !ok {
			continue
		}
		item := accountProfileTeam{ID: team.ID, Name: team.Name, Role: member.Role}
		teamMemberships[team.ID] = item
		teams = append(teams, item)
	}

	access := []accountProfileAccess{}
	for _, assignment := range assignments {
		if assignment.ScopeType != core.ScopeProject {
			continue
		}
		item := accountProfileAccess{
			ProjectID: assignment.ScopeID,
			Role:      assignment.Role,
		}
		if project, ok := projectByID[assignment.ScopeID]; ok {
			item.ProjectName = project.Name
		}
		if assignment.PrincipalType == core.PrincipalUser && assignment.PrincipalID == userID {
			item.Source = core.PrincipalUser
			access = append(access, item)
			continue
		}
		if assignment.PrincipalType == core.PrincipalTeam {
			team, ok := teamMemberships[assignment.PrincipalID]
			if !ok {
				continue
			}
			item.Source = core.PrincipalTeam
			item.SourceName = team.Name
			access = append(access, item)
		}
	}

	return accountProfileResponse{
		User:          user,
		Links:         links,
		Teams:         teams,
		ProjectAccess: access,
	}, nil
}
