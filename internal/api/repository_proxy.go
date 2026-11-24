package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/doout/dispatch/internal/repositoryproxy"
)

func (a *API) getRepositoryProxy(w http.ResponseWriter, r *http.Request) {
	if a.auth.Hosted != nil {
		problem(w, http.StatusForbidden, "Proxy settings unavailable", "Configure proxy settings on a self-hosted controller.")
		return
	}
	settings, err := a.store.GetRepositoryProxy(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		URL           string `json:"url"`
		NoProxy       string `json:"noProxy"`
		Authenticated bool   `json:"authenticated"`
	}{settings.URL, settings.NoProxy, settings.EncryptedUserinfo != ""})
}

func (a *API) saveRepositoryProxy(w http.ResponseWriter, r *http.Request) {
	if a.auth.Hosted != nil {
		problem(w, http.StatusForbidden, "Proxy settings unavailable", "Configure proxy settings on a self-hosted controller.")
		return
	}
	var input struct {
		URL     *string `json:"url"`
		NoProxy *string `json:"noProxy"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.NoProxy == nil || decoder.Decode(new(any)) != io.EOF {
		problem(w, http.StatusBadRequest, "Invalid proxy settings", "Send a bypass list and an optional proxy URL.")
		return
	}
	var address string
	if input.URL != nil {
		address = *input.URL
	}
	settings, err := a.repositoryProxy.Prepare("", address, *input.NoProxy)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, repositoryproxy.ErrEncryptionUnavailable) {
			status = http.StatusServiceUnavailable
		}
		problem(w, status, "Proxy settings could not be saved", err.Error())
		return
	}
	if input.URL != nil && settings.URL == "" {
		settings.NoProxy = ""
	}
	if err := a.store.SaveRepositoryProxy(r.Context(), settings, input.URL != nil); err != nil {
		a.internal(w, err)
		return
	}
	a.getRepositoryProxy(w, r)
}
