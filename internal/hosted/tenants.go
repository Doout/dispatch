package hosted

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/tenancy"
	"github.com/go-chi/chi/v5"
)

func (s *Server) listTenants(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.admin(w, r); !ok {
		return
	}
	items, err := s.Catalog.ListTenants(r.Context())
	if err != nil {
		catalogError(w, err)
		return
	}
	respond(w, http.StatusOK, items)
}

func (s *Server) createTenant(w http.ResponseWriter, r *http.Request) {
	user, ok := s.admin(w, r)
	if !ok {
		return
	}
	var input struct {
		Slug       string `json:"slug"`
		Name       string `json:"name"`
		OwnerEmail string `json:"ownerEmail"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Slug = strings.ToLower(strings.TrimSpace(input.Slug))
	create := tenancy.CreateTenantInput{Slug: input.Slug, Name: input.Name, CreatorID: user.ID}
	owner, err := s.Catalog.UserByEmail(r.Context(), input.OwnerEmail)
	if err == nil && owner.EmailVerified && owner.State == tenancy.StateActive {
		create.InitialOwnerID = owner.ID
	} else if err == nil || errors.Is(err, tenancy.ErrNotFound) {
		create.InvitationEmail = cleanEmail(input.OwnerEmail)
	} else {
		catalogError(w, err)
		return
	}
	for _, ns := range s.Config.Nameservers {
		if input.Slug+"."+s.Config.RootDomain == ns {
			problem(w, http.StatusConflict, "That name is reserved.")
			return
		}
	}
	tenant, err := s.Catalog.CreateTenant(r.Context(), create)
	if err != nil {
		catalogError(w, err)
		return
	}
	// The durable provisioning record is retried by Run, including after restart.
	respond(w, http.StatusAccepted, tenant)
}

func (s *Server) tenantMetadata(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.admin(w, r); !ok {
		return
	}
	tenant, err := s.Catalog.Tenant(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		catalogError(w, err)
		return
	}
	respond(w, http.StatusOK, tenant)
}

func (s *Server) tenantUsage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.admin(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.Catalog.Tenant(r.Context(), id); err != nil {
		catalogError(w, err)
		return
	}
	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 90 {
			problem(w, http.StatusBadRequest, "Choose between 1 and 90 days.")
			return
		}
		days = parsed
	}
	end := time.Now().UTC()
	usage, err := s.Catalog.TenantUsage(r.Context(), id, end.Add(-time.Duration(days)*24*time.Hour), end)
	if err != nil {
		catalogError(w, err)
		return
	}
	respond(w, http.StatusOK, usage)
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	items, err := s.Catalog.ListTenantMembers(r.Context(), user.ID, chi.URLParam(r, "id"))
	if err != nil {
		catalogError(w, err)
		return
	}
	respond(w, http.StatusOK, items)
}

func (s *Server) addMember(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.user(w, r)
	if !ok {
		return
	}
	var input struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !decode(w, r, &input) {
		return
	}
	tenantID := chi.URLParam(r, "id")
	// Check authority before resolving another global identity by email.
	m, err := s.Catalog.Membership(r.Context(), tenantID, actor.ID)
	if err != nil || m.Role != tenancy.RoleOwner {
		problem(w, http.StatusForbidden, "Tenant owner access required.")
		return
	}
	user, err := s.Catalog.UserByEmail(r.Context(), input.Email)
	if err != nil {
		catalogError(w, err)
		return
	}
	membership := tenancy.Membership{TenantID: tenantID, UserID: user.ID, Role: input.Role, State: tenancy.StateActive}
	if err = s.Catalog.SetMembership(r.Context(), actor.ID, membership); err != nil {
		catalogError(w, err)
		return
	}
	if err = s.syncMember(r, tenantID, user.ID); err != nil {
		catalogError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setMember(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.user(w, r)
	if !ok {
		return
	}
	var input struct {
		Role  string `json:"role"`
		State string `json:"state"`
	}
	if !decode(w, r, &input) {
		return
	}
	m := tenancy.Membership{TenantID: chi.URLParam(r, "id"), UserID: chi.URLParam(r, "userId"), Role: input.Role, State: input.State}
	if err := s.Catalog.SetMembership(r.Context(), actor.ID, m); err != nil {
		catalogError(w, err)
		return
	}
	if err := s.syncMember(r, m.TenantID, m.UserID); err != nil {
		catalogError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteMember(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.user(w, r)
	if !ok {
		return
	}
	tenantID, userID := chi.URLParam(r, "id"), chi.URLParam(r, "userId")
	if err := s.Catalog.DeleteMembership(r.Context(), actor.ID, tenantID, userID); err != nil {
		catalogError(w, err)
		return
	}
	if err := s.syncMember(r, tenantID, userID); err != nil {
		catalogError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) syncMember(r *http.Request, tenantID, userID string) error {
	tenant, err := s.Catalog.Tenant(r.Context(), tenantID)
	if err != nil {
		return err
	}
	runtime, err := s.runtime(tenant)
	if err != nil {
		return err
	}
	return s.syncUser(r.Context(), runtime.Store, tenantID, userID)
}

func (s *Server) syncUser(ctx context.Context, data *store.SQLStore, tenantID, userID string) error {
	u, err := s.Catalog.User(ctx, userID)
	if err != nil {
		return err
	}
	membership, memberErr := s.Catalog.Membership(ctx, tenantID, userID)
	if memberErr != nil && !errors.Is(memberErr, tenancy.ErrDenied) && !errors.Is(memberErr, tenancy.ErrNotFound) {
		return memberErr
	}
	state := core.UserStateActive
	role := core.UserRoleMember
	if memberErr != nil {
		state = core.UserStateDisabled
	} else if membership.Role == tenancy.RoleOwner || membership.Role == tenancy.RoleAdmin {
		role = core.UserRoleOwner
	}
	local, err := data.GetUser(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return data.CreateUser(ctx, core.User{ID: u.ID, Username: u.Email, Email: u.Email, DisplayName: u.Name, SystemRole: role, State: state, CreatedAt: u.CreatedAt, UpdatedAt: time.Now().UTC()})
	}
	if err != nil {
		return err
	}
	if local.Username == u.Email && local.DisplayName == u.Name && local.SystemRole == role && local.State == state && local.PasswordHash == "" {
		return nil
	}
	local.Username, local.Email, local.DisplayName, local.SystemRole, local.State, local.PasswordHash = u.Email, u.Email, u.Name, role, state, ""
	local.UpdatedAt = time.Now().UTC()
	return data.UpdateUser(ctx, local)
}
