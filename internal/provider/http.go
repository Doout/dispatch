package provider

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

const MaxMessageBytes = 1 << 20

// Problem is the provider's RFC 9457 error. Details must never contain credentials.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func (p *Problem) Error() string { return p.Title }

func NewProblem(status int, title, detail string) *Problem {
	return &Problem{Type: "about:blank", Title: title, Status: status, Detail: detail}
}

type validationRequest struct {
	Config map[string]any `json:"config"`
}
type validationResponse struct {
	Valid bool `json:"valid"`
}
type optionsResponse struct {
	Items []Option `json:"items"`
}

// Handler makes an adapter available over HTTP. An empty bearerToken is intended
// for isolated local mocks; real deployments should authenticate every endpoint.
func Handler(adapter Provider, bearerToken string) http.Handler {
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, status int, value any, err error) {
		if err != nil {
			writeProblem(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	mux.HandleFunc("GET /v1/manifest", func(w http.ResponseWriter, r *http.Request) {
		value, err := adapter.Manifest(r.Context())
		respond(w, http.StatusOK, value, err)
	})
	mux.HandleFunc("POST /v1/validate", func(w http.ResponseWriter, r *http.Request) {
		var input validationRequest
		if !decodeRequest(w, r, &input) {
			return
		}
		err := adapter.Validate(r.Context(), input.Config)
		respond(w, http.StatusOK, validationResponse{Valid: err == nil}, err)
	})
	mux.HandleFunc("POST /v1/options", func(w http.ResponseWriter, r *http.Request) {
		var input OptionRequest
		if !decodeRequest(w, r, &input) {
			return
		}
		value, err := adapter.Options(r.Context(), input)
		if value == nil {
			value = []Option{}
		}
		respond(w, http.StatusOK, optionsResponse{Items: value}, err)
	})
	mux.HandleFunc("POST /v1/servers", func(w http.ResponseWriter, r *http.Request) {
		key, ok := mutationKey(w, r)
		if !ok {
			return
		}
		var input CreateServerRequest
		if !decodeRequest(w, r, &input) {
			return
		}
		value, err := adapter.CreateServer(r.Context(), key, input)
		respond(w, http.StatusAccepted, value, err)
	})
	mux.HandleFunc("GET /v1/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !pathID(w, r) {
			return
		}
		value, err := adapter.Operation(r.Context(), r.PathValue("id"))
		respond(w, http.StatusOK, value, err)
	})
	mux.HandleFunc("GET /v1/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !pathID(w, r) {
			return
		}
		value, err := adapter.Server(r.Context(), r.PathValue("id"))
		respond(w, http.StatusOK, value, err)
	})
	mux.HandleFunc("DELETE /v1/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		key, ok := mutationKey(w, r)
		if !ok || !pathID(w, r) {
			return
		}
		value, err := adapter.DeleteServer(r.Context(), key, r.PathValue("id"))
		respond(w, http.StatusAccepted, value, err)
	})
	registerSnapshotRoutes(mux, adapter, respond)
	registerServerActionRoutes(mux, adapter, respond)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, NewProblem(http.StatusNotFound, "Provider endpoint not found", "Use a documented v1 endpoint."))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if bearerToken != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+bearerToken)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, NewProblem(http.StatusUnauthorized, "Provider authentication required", "Supply the configured bearer credential."))
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func mutationKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if !ValidID(key) || len(r.Header.Values("Idempotency-Key")) != 1 {
		writeProblem(w, NewProblem(http.StatusBadRequest, "Idempotency key required", "Use one 1-128 character key containing letters, digits, dots, underscores, colons or hyphens."))
		return "", false
	}
	return key, true
}

func pathID(w http.ResponseWriter, r *http.Request) bool {
	if !ValidID(r.PathValue("id")) {
		writeProblem(w, NewProblem(http.StatusBadRequest, "Resource identity invalid", "Use the identity returned by the provider."))
		return false
	}
	return true
}

func decodeRequest(w http.ResponseWriter, r *http.Request, output any) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeProblem(w, NewProblem(http.StatusUnsupportedMediaType, "JSON required", "Send application/json."))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxMessageBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 || strings.TrimSpace(string(raw))[0] != '{' {
		writeProblem(w, NewProblem(http.StatusBadRequest, "Provider request invalid", "Send one JSON object within the request size limit."))
		return false
	}
	defer clear(raw)
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		writeProblem(w, NewProblem(http.StatusBadRequest, "Provider request invalid", "Send one valid JSON object within the request size limit."))
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeProblem(w, NewProblem(http.StatusBadRequest, "Provider request invalid", "Send one JSON object."))
		return false
	}
	return true
}

func writeProblem(w http.ResponseWriter, err error) {
	problem := NewProblem(http.StatusInternalServerError, "Provider operation failed", "Inspect adapter diagnostics and reconcile the operation before retrying.")
	var known *Problem
	if errors.As(err, &known) && known.Status >= 400 && known.Status <= 599 && strings.TrimSpace(known.Title) != "" {
		problem = known
	}
	if errors.Is(err, context.DeadlineExceeded) {
		problem = NewProblem(http.StatusGatewayTimeout, "Provider deadline exceeded", "Reconcile the operation before retrying.")
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(problem.Status)
	_ = json.NewEncoder(w).Encode(problem)
}
