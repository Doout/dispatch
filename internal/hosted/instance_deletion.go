package hosted

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/doout/dispatch/internal/tenancy"
	"github.com/go-chi/chi/v5"
)

func (s *Server) deleteInstance(w http.ResponseWriter, r *http.Request) {
	u, ok := s.user(w, r)
	if !ok {
		return
	}
	var in struct {
		Slug string `json:"slug"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := chi.URLParam(r, "id")
	externalID := ""
	if s.workos != nil {
		externalID = s.Config.RootDomain + ":" + id
	}
	// Organization sync must not recreate a deleted instance from a stale
	// reconciliation list. The catalog transaction serializes membership writes.
	s.workosMu.Lock()
	s.dnsMu.Lock()
	remove := s.Catalog.DeleteConnectedInstance
	if s.inactiveInstanceRemovalAllowed(id) {
		remove = s.Catalog.DeleteInactiveInstance
	}
	connection, err := remove(r.Context(), u.ID, id, in.Slug, externalID)
	s.dnsMu.Unlock()
	s.workosMu.Unlock()
	if err != nil {
		catalogError(w, err)
		return
	}
	s.disconnectInstanceStreams(connectionKey(connection))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) inactiveInstanceRemovalAllowed(id string) bool {
	if s.Config.BackendMode != tenancy.BackendConnected {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.runtimes[id] == nil && s.initializing[id] == nil
}

func (s *Server) reconcileOrganizationDeletions(ctx context.Context) error {
	if s.workos == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pending, err := s.Catalog.PendingOrganizationDeletions(ctx)
	if err != nil {
		return err
	}
	var joined error
	for _, deletion := range pending {
		err := s.workos.DeleteOrganization(ctx, deletion.OrganizationID, deletion.ExternalID)
		if err == nil {
			err = s.Catalog.CompleteOrganizationDeletion(ctx, deletion.TenantID)
		}
		if err != nil && ctx.Err() == nil {
			s.Logger.Error("WorkOS organization deletion failed", "instance", deletion.TenantID)
		}
		joined = errors.Join(joined, err)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return joined
}
