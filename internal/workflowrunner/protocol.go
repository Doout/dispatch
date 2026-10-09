// Package workflowrunner delivers authorized execution to enrolled tenant workers.
package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

const Version = "dispatch.workflow-worker/v1"
const MaxPayload = 4 << 20
const MaxLog = 1 << 20
const LeaseDuration = 45 * time.Second
const MaxDuration = 30 * time.Minute

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type Source struct {
	Revision   core.WorkflowSourceRevision `json:"revision"`
	URL        string                      `json:"url"`
	AuthType   string                      `json:"authType,omitempty"`
	Credential string                      `json:"credential,omitempty"`
}

type Workflow struct {
	ServiceRunID     string            `json:"serviceRunId,omitempty"`
	TemplateDigest   string            `json:"templateDigest,omitempty"`
	ParentRevisionID string            `json:"parentRevisionId,omitempty"`
	Job              json.RawMessage   `json:"job"`
	Sources          map[string]Source `json:"sources"`
	Inputs           map[string]string `json:"inputs,omitempty"`
	Secrets          map[string]string `json:"secrets,omitempty"`
}

type Deployment struct {
	Operation  string          `json:"operation"`
	Deployment core.Deployment `json:"deployment"`
	App        core.App        `json:"app"`
	Server     core.Server     `json:"server"`
	// These fields are intentionally absent from the public App/Server JSON.
	AppSpecDigest        string                       `json:"appSpecDigest"`
	ServerDigest         string                       `json:"serverDigest"`
	HelmValues           string                       `json:"helmValues,omitempty"`
	HelmGeneratedValues  string                       `json:"helmGeneratedValues,omitempty"`
	HelmGroupValues      string                       `json:"helmGroupValues,omitempty"`
	HelmProvenance       core.HelmProvenance          `json:"helmProvenance,omitempty"`
	Snapshot             core.DeploymentSnapshot      `json:"snapshot,omitempty"`
	SourceCredential     string                       `json:"sourceCredential,omitempty"`
	ComposeContent       string                       `json:"composeContent,omitempty"`
	HookEnvironment      map[string]string            `json:"hookEnvironment,omitempty"`
	ServiceRuntime       []core.ServiceRuntimeBinding `json:"serviceRuntime,omitempty"`
	Kubeconfig           string                       `json:"kubeconfig,omitempty"`
	CertificateAuthority string                       `json:"certificateAuthority,omitempty"`
}

type ServiceProvision struct {
	Operation            string                       `json:"operation,omitempty"`
	OperationID          string                       `json:"operationId,omitempty"`
	ExpectedResource     string                       `json:"expectedResource,omitempty"`
	Request              core.ServiceProvisionRequest `json:"request"`
	Spec                 core.HelmServiceProvision    `json:"spec"`
	Server               core.Server                  `json:"server"`
	ServerDigest         string                       `json:"serverDigest"`
	Kubeconfig           string                       `json:"kubeconfig"`
	CertificateAuthority string                       `json:"certificateAuthority,omitempty"`
}

type TargetInspection struct {
	Config               core.KubernetesServerConfig `json:"config"`
	Kubeconfig           string                      `json:"kubeconfig"`
	CertificateAuthority string                      `json:"certificateAuthority,omitempty"`
}

type StorageOperation struct {
	Server               core.Server           `json:"server"`
	ServerDigest         string                `json:"serverDigest"`
	Kubeconfig           string                `json:"kubeconfig"`
	CertificateAuthority string                `json:"certificateAuthority,omitempty"`
	Resource             *core.StorageResource `json:"resource,omitempty"`
}

type Request struct {
	StorageOperation *StorageOperation `json:"storageOperation,omitempty"`
	TargetInspection *TargetInspection `json:"targetInspection,omitempty"`
	ServiceProvision *ServiceProvision `json:"serviceProvision,omitempty"`
	Version          string            `json:"version"`
	ProjectID        string            `json:"projectId"`
	ResourceID       string            `json:"resourceId"`
	RevisionID       string            `json:"revisionId"`
	Workflow         *Workflow         `json:"workflow,omitempty"`
	Deployment       *Deployment       `json:"deployment,omitempty"`
	// Managed execution is opt-in and never falls back to an unrestricted worker.
	Mode string `json:"mode"`
}

type Result struct {
	Storage           []core.StorageObservation       `json:"storage"`
	ServiceInspection *core.ServiceResourceInspection `json:"serviceInspection,omitempty"`
	TargetEvidence    *core.KubernetesTargetEvidence  `json:"targetEvidence,omitempty"`
	Snapshot          *core.DeploymentSnapshot        `json:"snapshot,omitempty"`
	Manifest          string                          `json:"manifest,omitempty"`
	State             string                          `json:"state"`
	Log               string                          `json:"log,omitempty"`
	Error             string                          `json:"error,omitempty"`
	Outputs           map[string]string               `json:"outputs,omitempty"`
	Health            *core.DeploymentHealth          `json:"health,omitempty"`
	Route             *core.ApplicationRoute          `json:"route,omitempty"`
}

type Progress struct {
	LeaseToken string `json:"leaseToken"`
	Log        string `json:"log,omitempty"`
	Phase      string `json:"phase,omitempty"`
}

type LeasedJob struct {
	ID         string    `json:"id"`
	Digest     string    `json:"digest"`
	Attempt    int       `json:"attempt"`
	LeaseToken string    `json:"leaseToken"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Request    Request   `json:"request"`
}

type Completion struct {
	LeaseToken string `json:"leaseToken"`
	Result     Result `json:"result"`
}

type Runner interface {
	Run(context.Context, Request, func(string)) (Result, error)
}

func (r Request) Kind() string {
	if p := r.StorageOperation; p != nil {
		if p.Resource != nil {
			return "storage_delete"
		}
		return "storage_inspect"
	}
	if r.TargetInspection != nil {
		return "target_inspect"
	}
	if r.ServiceProvision != nil {
		return "service_" + r.ServiceProvision.Action()
	}
	if r.Workflow != nil {
		return "workflow"
	}
	return "deployment"
}
func (r Request) Validate() error {
	if p := r.StorageOperation; p != nil {
		if r.Mode != "tenant" || r.ResourceID != p.Server.ID || r.ProjectID != p.Server.ProjectID || !core.IsKubernetesRuntime(p.Server.Runtime) || p.Server.Kubernetes == nil || p.Kubeconfig == "" {
			return errors.New("invalid worker storage ownership")
		}
		if p.Resource != nil && (p.Resource.ServerID != p.Server.ID || p.Resource.Kind != "kubernetes_pvc") {
			return errors.New("invalid worker storage resource")
		}
	}
	if r.Version != Version || !identifier.MatchString(r.ProjectID) || !identifier.MatchString(r.ResourceID) || !identifier.MatchString(r.RevisionID) {
		return errors.New("invalid worker request identity")
	}
	if r.Mode != "tenant" && r.Mode != "managed" {
		return errors.New("invalid worker execution mode")
	}
	variants := 0
	if r.StorageOperation != nil {
		variants++
	}
	if r.Workflow != nil {
		variants++
	}
	if r.Deployment != nil {
		variants++
	}
	if r.ServiceProvision != nil {
		variants++
	}
	if r.TargetInspection != nil {
		variants++
	}
	if variants != 1 {
		return errors.New("worker request must contain one execution operation")
	}
	if r.Workflow != nil {
		if len(r.Workflow.Job) == 0 || !json.Valid(r.Workflow.Job) || len(r.Workflow.Sources) > 32 {
			return errors.New("invalid workflow job")
		}
		if r.Mode == "managed" && len(r.Workflow.Sources) != 0 {
			return errors.New("managed jobs cannot access repositories; select a tenant worker")
		}
	}
	if d := r.Deployment; d != nil {
		if r.Mode != "tenant" || d.App.ID != r.ResourceID || d.App.ProjectID != r.ProjectID || d.App.ServerID != d.Server.ID || (d.Operation != "deploy" && d.Operation != "destroy") {
			return errors.New("invalid worker deployment ownership")
		}
		if d.Operation == "deploy" && (d.Deployment.ID != r.RevisionID || d.Deployment.AppID != d.App.ID) {
			return errors.New("invalid worker deployment revision")
		}
	}
	if p := r.ServiceProvision; p != nil {
		if r.Mode != "tenant" || !identifier.MatchString(p.Request.Run.ID) || p.Request.Run.TemplateID != r.ResourceID || p.Request.Run.ProjectID != r.ProjectID || p.Spec.ServerRef != p.Server.ID || !core.IsKubernetesRuntime(p.Server.Runtime) {
			return errors.New("invalid worker service provisioning ownership")
		}
		switch p.Action() {
		case "provision":
			if p.Request.Run.ID != r.RevisionID {
				return errors.New("invalid service provision revision")
			}
		case "inspect":
		case "delete":
			if p.OperationID != r.RevisionID {
				return errors.New("invalid service cleanup operation")
			}
		default:
			return errors.New("unsupported worker service operation")
		}
	}
	if p := r.TargetInspection; p != nil {
		if r.Mode != "tenant" || p.Kubeconfig == "" || p.Config.Namespace == "" {
			return errors.New("target inspection requires a tenant worker and stored Kubernetes credentials")
		}
	}
	return nil
}

func (p ServiceProvision) Action() string {
	if p.Operation == "" {
		return "provision"
	}
	return p.Operation
}

func (r Request) Redact(text string) string {
	secrets := []string{}
	if p := r.StorageOperation; p != nil {
		secrets = append(secrets, p.Kubeconfig)
	}
	if p := r.TargetInspection; p != nil {
		secrets = append(secrets, p.Kubeconfig)
	}
	if p := r.ServiceProvision; p != nil {
		secrets = append(secrets, p.Request.Password, p.Kubeconfig)
		for _, value := range p.Request.Inputs {
			secrets = append(secrets, value)
		}
	}
	if w := r.Workflow; w != nil {
		for _, value := range w.Secrets {
			secrets = append(secrets, value)
		}
		for _, src := range w.Sources {
			secrets = append(secrets, src.Credential)
		}
	}
	if d := r.Deployment; d != nil {
		secrets = append(secrets, d.SourceCredential, d.Kubeconfig)
		for _, binding := range d.ServiceRuntime {
			secrets = append(secrets, binding.SensitiveValues...)
		}
		for _, value := range d.HookEnvironment {
			secrets = append(secrets, value)
		}
	}
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
			// A later progress update may append the remainder of this credential.
			for n := min(len(secret)-1, len(text)); n > 0; n-- {
				if strings.HasSuffix(text, secret[:n]) {
					text = text[:len(text)-n]
					break
				}
			}
		}
	}
	if len(text) > MaxLog {
		text = text[len(text)-MaxLog:]
	}
	return text
}
