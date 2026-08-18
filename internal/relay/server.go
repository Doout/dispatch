package relay

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const maxWebhookBody = 4 << 20

type Server struct {
	store     *Store
	token     string
	publicURL string
	lease     time.Duration
}

func NewServer(store *Store, token, publicURL string) http.Handler {
	s := &Server{store: store, token: token, publicURL: strings.TrimRight(publicURL, "/"), lease: 90 * time.Second}
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Post("/hooks/{id}", s.receive)
	r.Group(func(r chi.Router) {
		r.Use(s.authorize)
		r.Get("/api/v1/status", s.status)
		r.Get("/api/v1/hooks", s.listHooks)
		r.Post("/api/v1/hooks", s.createHook)
		r.Delete("/api/v1/hooks/{id}", s.deleteHook)
		r.Get("/api/v1/deliveries/next", s.next)
		r.Post("/api/v1/deliveries/{id}/ack", s.ack)
	})
	return r
}

func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) receive(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "payload exceeds 4 MiB"})
		return
	}
	headers := make(map[string][]string, len(r.Header))
	for key, values := range r.Header {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Cookie") {
			continue
		}
		headers[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
	}
	delivery, err := s.store.Enqueue(r.Context(), chi.URLParam(r, "id"), r.Method, headers, body, time.Now().UTC())
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "event could not be stored"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"deliveryId": delivery.ID, "sequence": delivery.Sequence})
}

func (s *Server) createHook(w http.ResponseWriter, r *http.Request) {
	var input hookRequest
	if json.NewDecoder(r.Body).Decode(&input) != nil || strings.TrimSpace(input.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	hook, err := s.store.CreateHook(r.Context(), strings.TrimSpace(input.Name), time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "hook could not be created"})
		return
	}
	hook.URL = s.publicURL + "/hooks/" + url.PathEscape(hook.ID)
	writeJSON(w, http.StatusCreated, hook)
}

func (s *Server) listHooks(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListHooks(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "hooks could not be listed"})
		return
	}
	for i := range items {
		items[i].URL = s.publicURL + "/hooks/" + url.PathEscape(items[i].ID)
	}
	writeJSON(w, 200, items)
}
func (s *Server) deleteHook(w http.ResponseWriter, r *http.Request) {
	if s.store.DeleteHook(r.Context(), chi.URLParam(r, "id")) != nil {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	value, err := s.store.Status(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "status unavailable"})
		return
	}
	writeJSON(w, 200, value)
}

func (s *Server) next(w http.ResponseWriter, r *http.Request) {
	deadline := time.Now().Add(25 * time.Second)
	for {
		item, err := s.store.LeaseNext(r.Context(), s.lease, time.Now().UTC())
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": "delivery unavailable"})
			return
		}
		if item != nil {
			writeJSON(w, 200, item)
			return
		}
		if time.Now().After(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *Server) ack(w http.ResponseWriter, r *http.Request) {
	var input ackRequest
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.LeaseToken == "" {
		writeJSON(w, 400, map[string]string{"error": "lease token is required"})
		return
	}
	switch input.Disposition {
	case "", "ack":
		input.Disposition = "ack"
	case "retry", "dead":
	default:
		writeJSON(w, 400, map[string]string{"error": "invalid disposition"})
		return
	}
	if err := s.store.Acknowledge(r.Context(), chi.URLParam(r, "id"), input.LeaseToken, input.Disposition, input.Error, time.Now().UTC()); err != nil {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
