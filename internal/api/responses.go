package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/doout/dispatch/internal/store"
)

func (a *API) list(w http.ResponseWriter, value any, err error) {
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (a *API) notFoundOrInternal(w http.ResponseWriter, err error, resource string) {
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, resource+" not found", "Refresh the inventory and try again.")
		return
	}
	a.internal(w, err)
}

func (a *API) internal(w http.ResponseWriter, err error) {
	a.logger.Error("api request failed", "error", err)
	problem(w, http.StatusInternalServerError, "Request failed", "Dispatch could not complete the request. Check the controller log and retry.")
}

func (a *API) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		a.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		problem(w, http.StatusBadRequest, "Invalid request", "Send valid JSON containing only supported fields.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func problem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "title": title, "detail": detail})
}
