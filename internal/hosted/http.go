package hosted

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/doout/dispatch/internal/tenancy"
)

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}

func problem(w http.ResponseWriter, status int, detail string) {
	respond(w, status, map[string]any{"status": status, "title": http.StatusText(status), "detail": detail})
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		problem(w, http.StatusUnsupportedMediaType, "Send a JSON request.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		problem(w, http.StatusBadRequest, "Invalid request.")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		problem(w, http.StatusBadRequest, "Send one JSON object.")
		return false
	}
	return true
}

func catalogError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tenancy.ErrDenied), errors.Is(err, tenancy.ErrExpired):
		problem(w, http.StatusForbidden, "Access denied.")
	case errors.Is(err, tenancy.ErrNotFound):
		problem(w, http.StatusNotFound, "Not found.")
	case errors.Is(err, tenancy.ErrConflict), errors.Is(err, tenancy.ErrLastOwner):
		problem(w, http.StatusConflict, "The request conflicts with the current state.")
	case errors.Is(err, tenancy.ErrInvalid):
		problem(w, http.StatusBadRequest, "Invalid request.")
	default:
		problem(w, http.StatusInternalServerError, "The request could not be completed.")
	}
}

func requestToken(r *http.Request, cookieName string) string {
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && strings.TrimSpace(token) != "" {
		return token
	}
	if cookie, err := r.Cookie(cookieName); err == nil {
		return cookie.Value
	}
	return ""
}

func setSession(w http.ResponseWriter, name, token string, seconds int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: token, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: seconds})
}

func originAllowed(r *http.Request, expected string) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return validOrigin(origin, expected)
	}
	// Requests carrying browser cookies must establish their exact origin before
	// changing state. CLI credentials have no ambient browser authority.
	return r.Method == http.MethodGet || r.Method == http.MethodHead || r.Header.Get("Cookie") == ""
}
