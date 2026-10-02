package remoteruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/doout/dispatch/internal/core"
)

type ServiceRequest struct {
	Request core.ServiceProvisionRequest `json:"request"`
	Docker  core.DockerServiceProvision  `json:"docker"`
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
	for _, binding := range r.Inputs.Services {
		secrets = append(secrets, binding.SensitiveValues...)
		for _, v := range binding.Values {
			secrets = append(secrets, v)
		}
	}
	if r.Service != nil {
		for _, v := range r.Service.Request.Inputs {
			secrets = append(secrets, v)
		}
		for _, v := range r.Service.Docker.Environment {
			secrets = append(secrets, v)
		}
	}
	return secrets
}
