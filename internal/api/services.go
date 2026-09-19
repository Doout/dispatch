package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/serviceconn"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type serviceFieldInput struct {
	Value     *string `json:"value"`
	Sensitive *bool   `json:"sensitive"`
	SecretRef *string `json:"secretRef"`
	Remove    bool    `json:"remove"`
}
type serviceRequest struct {
	ProjectID     string                       `json:"projectId"`
	Name          string                       `json:"name"`
	Description   string                       `json:"description"`
	Type          string                       `json:"type"`
	ConnectionURL *string                      `json:"connectionUrl"`
	Fields        map[string]serviceFieldInput `json:"fields"`
	ProbeHost     string                       `json:"probeHost"`
	ProbePort     int                          `json:"probePort"`
	Revision      int64                        `json:"revision"`
}
type serviceResponse = core.ServiceOverview

func publicService(item core.Service, owner bool) core.Service {
	fields := map[string]core.ServiceField{}
	for key, f := range item.Fields {
		if f.Sensitive || f.SecretRef != "" {
			f.Value = ""
		}
		f.EncryptedValue = ""
		if !owner {
			f.SecretRef = ""
		}
		fields[key] = f
	}
	item.Fields = fields
	return item
}
func (a *API) serviceResponse(ctx context.Context, item core.Service, owner bool) (serviceResponse, error) {
	consumers, err := a.store.ListServiceConsumers(ctx, item.ID)
	if err != nil {
		return serviceResponse{}, err
	}
	fields := []string{}
	for key := range item.Fields {
		if serviceconn.HasField(item, key) {
			fields = append(fields, key)
		}
	}
	if item.Type == "postgresql" {
		fields = append(fields, "connectionUrl")
	}
	sort.Strings(fields)
	return serviceResponse{Service: publicService(item, owner), Consumers: consumers, AvailableFields: fields}, nil
}
func (a *API) listServices(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListServices(r.Context(), strings.TrimSpace(r.URL.Query().Get("projectId")))
	if err != nil {
		a.internal(w, err)
		return
	}
	visible, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	result := []serviceResponse{}
	for _, item := range items {
		if !visible[item.ProjectID] {
			continue
		}
		value, err := a.serviceResponse(r.Context(), item, currentIdentity(r.Context()).SystemRole == core.UserRoleOwner)
		if err != nil {
			a.internal(w, err)
			return
		}
		result = append(result, value)
	}
	writeJSON(w, http.StatusOK, result)
}
func (a *API) loadService(w http.ResponseWriter, r *http.Request, permission core.Permission) (core.Service, bool) {
	item, err := a.store.GetService(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Service")
		return item, false
	}
	return item, a.requireProject(w, r, permission, item.ProjectID)
}
func (a *API) getService(w http.ResponseWriter, r *http.Request) {
	item, ok := a.loadService(w, r, core.PermissionProjectView)
	if !ok {
		return
	}
	result, err := a.serviceResponse(r.Context(), item, currentIdentity(r.Context()).SystemRole == core.UserRoleOwner)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *API) serviceInput(r *http.Request, in serviceRequest, item core.Service) (core.Service, error) {
	if in.ProjectID != "" && item.ProjectID != "" && in.ProjectID != item.ProjectID || in.Name != "" && item.Name != "" && in.Name != item.Name || in.Type != "" && item.Type != "" && in.Type != item.Type {
		return item, errors.New("service project, name and type cannot be changed")
	}
	if item.ProjectID == "" {
		item.ProjectID = strings.TrimSpace(in.ProjectID)
		item.Name = strings.TrimSpace(in.Name)
		item.Type = in.Type
	}
	item.Description = in.Description
	item.ProbeHost = strings.TrimSpace(in.ProbeHost)
	item.ProbePort = in.ProbePort
	fields := map[string]core.ServiceField{}
	for key, f := range item.Fields {
		fields[key] = f
	}
	item.Fields = fields
	if in.ConnectionURL != nil {
		if item.Type != "postgresql" {
			return item, errors.New("connection URL input is only available for PostgreSQL")
		}
		parsed, err := serviceconn.ParsePostgresURL(*in.ConnectionURL)
		if err != nil {
			return item, err
		}
		if in.Fields == nil {
			in.Fields = map[string]serviceFieldInput{}
		}
		for key, value := range parsed {
			if _, exists := in.Fields[key]; exists {
				return item, errors.New("supply either a connection URL or individual connection fields")
			}
			v := value
			in.Fields[key] = serviceFieldInput{Value: &v}
		}
	}
	for key, input := range in.Fields {
		f := item.Fields[key]
		if input.Remove {
			delete(item.Fields, key)
			continue
		}
		if input.Sensitive != nil && *input.Sensitive != f.Sensitive {
			if !*input.Sensitive && (f.EncryptedValue != "" || f.SecretRef != "") {
				return item, errors.New("remove a sensitive field before replacing it with an ordinary field")
			}
			f.Sensitive = *input.Sensitive
		}
		if item.Type == "postgresql" && key == "password" {
			f.Sensitive = true
		}
		if input.SecretRef != nil && *input.SecretRef != "" && *input.SecretRef != f.SecretRef {
			if currentIdentity(r.Context()).SystemRole != core.UserRoleOwner {
				return item, errors.New("a controller owner must attach global secret references")
			}
			if _, err := a.store.GetSecret(r.Context(), *input.SecretRef); err != nil {
				return item, errors.New("secret reference is unavailable")
			}
		}
		if input.SecretRef != nil {
			f.SecretRef = *input.SecretRef
			if f.SecretRef != "" {
				if input.Value != nil {
					return item, errors.New("choose a value or a secret reference")
				}
				f.Sensitive = true
				f.EncryptedValue = ""
				f.Value = ""
				f.Configured = true
			}
		}
		if input.Value != nil {
			if u, err := url.Parse(*input.Value); err == nil && u.User != nil {
				if _, hasPassword := u.User.Password(); hasPassword {
					f.Sensitive = true
				}
			}
			if len(*input.Value) > 65536 {
				return item, errors.New("service values must be at most 64 KiB")
			}
			f.SecretRef = ""
			f.Configured = true
			if f.Sensitive {
				if a.eventConfig.Vault == nil {
					return item, errors.New("configure the controller master key before saving credentials")
				}
				// Preserve ciphertext when the supplied value did not change.
				unchanged := false
				if f.EncryptedValue != "" {
					plain, err := a.eventConfig.Vault.Decrypt(serviceconn.FieldAAD(item.ID, key), f.EncryptedValue)
					unchanged = err == nil && string(plain) == *input.Value
					clear(plain)
				}
				if !unchanged {
					encrypted, err := a.eventConfig.Vault.Encrypt(serviceconn.FieldAAD(item.ID, key), []byte(*input.Value))
					if err != nil {
						return item, errors.New("cannot encrypt service credential")
					}
					f.EncryptedValue = encrypted
				}
				f.Value = ""
			} else {
				f.Value = *input.Value
				f.EncryptedValue = ""
			}
		} else if f.Sensitive && f.Value != "" {
			return item, errors.New("supply a replacement value when making a field sensitive")
		}
		item.Fields[key] = f
	}
	return item, serviceconn.Validate(&item)
}
func (a *API) createService(w http.ResponseWriter, r *http.Request) {
	var input serviceRequest
	if !decode(w, r, &input) {
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectConfigure, input.ProjectID) {
		return
	}
	now := time.Now().UTC()
	item, err := a.serviceInput(r, input, core.Service{ID: ulid.Make().String(), Revision: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		problem(w, 400, "Invalid service", err.Error())
		return
	}
	if err = a.store.CreateService(r.Context(), item); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			problem(w, 409, "Service already exists", "Choose a unique service name in this project.")
		} else {
			a.internal(w, err)
		}
		return
	}
	result, _ := a.serviceResponse(r.Context(), item, currentIdentity(r.Context()).SystemRole == core.UserRoleOwner)
	writeJSON(w, 201, result)
}
func (a *API) updateService(w http.ResponseWriter, r *http.Request) {
	item, ok := a.loadService(w, r, core.PermissionProjectConfigure)
	if !ok {
		return
	}
	var input serviceRequest
	if !decode(w, r, &input) {
		return
	}
	if input.Revision != 0 && input.Revision != item.Revision {
		problem(w, 409, "Service changed", "Reload the service before saving.")
		return
	}
	updated, err := a.serviceInput(r, input, item)
	if err != nil {
		problem(w, 400, "Invalid service", err.Error())
		return
	}
	if !reflect.DeepEqual(item.Fields, updated.Fields) || item.ProbeHost != updated.ProbeHost || item.ProbePort != updated.ProbePort {
		updated.Revision++
		updated.Check = nil
	}
	updated.UpdatedAt = time.Now().UTC()
	if err = a.store.UpdateService(r.Context(), updated, item.Revision); err != nil {
		a.serviceStoreError(w, err)
		return
	}
	result, _ := a.serviceResponse(r.Context(), updated, currentIdentity(r.Context()).SystemRole == core.UserRoleOwner)
	writeJSON(w, 200, result)
}
func (a *API) serviceStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrServiceConflict):
		problem(w, 409, "Service changed", "Reload and retry.")
	case errors.Is(err, store.ErrServiceInUse):
		problem(w, 409, "Service in use", "Remove configured bindings and wait for active deployments before removing this registration.")
	default:
		a.notFoundOrInternal(w, err, "Service")
	}
}
func (a *API) deleteService(w http.ResponseWriter, r *http.Request) {
	item, ok := a.loadService(w, r, core.PermissionProjectConfigure)
	if !ok {
		return
	}
	// Include configured stages that have not generated an application yet.
	used, err := a.workflows.ServiceReferenced(r.Context(), item.ProjectID, item.ID, item.Name)
	if err != nil {
		a.internal(w, err)
		return
	}
	if used {
		a.serviceStoreError(w, store.ErrServiceInUse)
		return
	}
	if err = a.store.DeleteService(r.Context(), item.ID); err != nil {
		a.serviceStoreError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *API) verifyService(w http.ResponseWriter, r *http.Request) {
	item, ok := a.loadService(w, r, core.PermissionProjectConfigure)
	if !ok {
		return
	}
	result := (serviceconn.Resolver{Vault: a.eventConfig.Vault, Secrets: a.secretResolver}).Check(r.Context(), item)
	item.Check = &result
	if err := a.store.UpdateService(r.Context(), item, item.Revision); err != nil {
		a.serviceStoreError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *API) getAppServiceBindings(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.GetAppServiceBindings(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (a *API) updateAppServiceBindings(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	if item.Generated || item.Template {
		problem(w, 409, "Repository-managed bindings", "Edit service bindings in the source configuration.")
		return
	}
	var bindings []core.ServiceBinding
	if !decode(w, r, &bindings) {
		return
	}
	if err = a.store.ReplaceAppServiceBindings(r.Context(), item.ID, bindings); err != nil {
		problem(w, 400, "Invalid service bindings", err.Error())
		return
	}
	writeJSON(w, 200, bindings)
}
