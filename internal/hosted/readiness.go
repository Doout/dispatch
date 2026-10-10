package hosted

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) readiness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if s.ctx.Err() != nil || s.Catalog.Ready(ctx) != nil {
		respond(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	respond(w, http.StatusOK, map[string]string{"status": "ready"})
}
