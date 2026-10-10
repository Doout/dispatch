package hosted

import (
	"net/http"
	"strings"

	"github.com/doout/dispatch/internal/tenancy"
)

func certificateBearer(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return token
}

func (s *Server) tenantCertificateToken(w http.ResponseWriter, r *http.Request, tenant tenancy.Tenant) {
	user, err := s.Catalog.AuthenticateSession(r.Context(), requestToken(r, tenantCookie), tenancy.TenantAudience(tenant.ID))
	if err != nil || !s.tenantOwner(r, tenant) {
		problem(w, http.StatusForbidden, "Tenant administrator access required.")
		return
	}
	switch r.Method {
	case http.MethodPost:
		token, expires, err := s.Catalog.RotateCertificateToken(r.Context(), tenant.ID, user.ID)
		if err != nil {
			catalogError(w, err)
			return
		}
		respond(w, http.StatusCreated, map[string]any{"token": token, "expiresAt": expires})
	case http.MethodDelete:
		if err := s.Catalog.RevokeCertificateToken(r.Context(), tenant.ID, user.ID); err != nil {
			catalogError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		problem(w, http.StatusMethodNotAllowed, "Method not allowed.")
	}
}
