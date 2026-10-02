package remoteruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/doout/dispatch/internal/core"
)

type ServiceRequest struct {
	ExpectedResourceID string                       `json:"expectedResourceId,omitempty"`
	Request            core.ServiceProvisionRequest `json:"request"`
	Docker             core.DockerServiceProvision  `json:"docker"`
}

func ServiceSubject(project, server, name string) string {
	sum := sha256.Sum256([]byte(project + ":" + server + ":" + name))
	return "service-" + hex.EncodeToString(sum[:])
}
func NewServiceRequest(request core.ServiceProvisionRequest, spec core.DockerServiceProvision, server core.Server) Request {
	app := core.App{ID: ServiceSubject(request.Run.ProjectID, server.ID, request.Run.ServiceName), ProjectID: request.Run.ProjectID, ServerID: server.ID, Name: request.Run.ServiceName, BuildType: core.BuildTypeDockerfile}
	r := NewRequest(ProvisionService, core.Deployment{}, app, server)
	r.Service = &ServiceRequest{Request: request, Docker: spec}
	return r
}
func (r Request) validateService() error {
	if r.Service == nil {
		return errors.New("service provisioning inputs required")
	}
	run := r.Service.Request.Run
	if !identityPattern.MatchString(run.ID) || !identityPattern.MatchString(run.TemplateID) || run.ProjectID != r.Application.ProjectID || run.Target == nil || run.Target.ServerID != r.Server.ID || run.Target.Provider != "docker" || r.Service.Docker.ServerRef != r.Server.ID || r.Application.ID != ServiceSubject(run.ProjectID, r.Server.ID, run.ServiceName) {
		return errors.New("service ownership does not match runtime target")
	}
	if r.Service.Request.ServiceType != "postgresql" {
		return errors.New("remote built-in provisioning currently supports PostgreSQL")
	}
	return nil
}
func (r Request) SecretValues() []string {
	secrets := []string{r.Inputs.SourceCredential}
	if r.WorkloadBackup != nil {
		secrets = append(secrets, r.WorkloadBackup.Key, r.WorkloadBackup.Source.Password)
		for _, value := range r.WorkloadBackup.Source.Inputs {
			secrets = append(secrets, value)
		}
		if r.WorkloadBackup.Destination != nil {
			secrets = append(secrets, r.WorkloadBackup.Destination.Password)
		}
		for _, check := range r.WorkloadBackup.Checks {
			secrets = append(secrets, check.Query, check.Expected)
		}
	}
	for _, binding := range r.Inputs.Services {
		secrets = append(secrets, binding.SensitiveValues...)
		for _, v := range binding.Values {
			secrets = append(secrets, v)
		}
	}
	if r.Service != nil {
		secrets = append(secrets, r.Service.Request.Password)
		for _, v := range r.Service.Request.Inputs {
			secrets = append(secrets, v)
		}
		for _, v := range r.Service.Docker.Environment {
			secrets = append(secrets, v)
		}
	}
	return secrets
}

func (r Request) ValidateServiceResult(result Result) error {
	if r.Operation != ProvisionService && len(result.ServiceOutputs) != 0 {
		return errors.New("unexpected service credentials in runtime result")
	}
	if r.Operation != ServiceInspect {
		if result.ServiceResource != nil {
			return errors.New("unexpected service inspection result")
		}
		return nil
	}
	if result.State != "succeeded" && result.ServiceResource == nil {
		return nil
	}
	item := result.ServiceResource
	if item == nil || r.Service == nil || item.RunID != r.Service.Request.Run.ID || item.ProjectID != r.Application.ProjectID || item.ServerID != r.Server.ID || item.Provider != "docker" || item.State != "ready" && item.State != "absent" && item.State != "unready" || item.State != "absent" && item.ResourceID == "" || !item.StorageRetained {
		return errors.New("service inspection does not match the owned resource")
	}
	return nil
}
