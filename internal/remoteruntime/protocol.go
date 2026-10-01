package remoteruntime

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
)

const (
	ProvisionService runtimecontract.Operation = "provision_service"
	APIVersion                                 = "dispatch.agent.runtime/v1"
	MaxPayload                                 = 2 << 20
	MaxResult                                  = 1 << 20
	LeaseDuration                              = 45 * time.Second
	MaxDuration                                = 30 * time.Minute
)

var commitPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type Request struct {
	Retention          *RetentionRequest         `json:"retention,omitempty"`
	Storage            *core.StorageResource     `json:"storage,omitempty"`
	APIVersion         string                    `json:"apiVersion"`
	Operation          runtimecontract.Operation `json:"operation"`
	Deployment         core.Deployment           `json:"deployment"`
	Snapshot           core.DeploymentSnapshot   `json:"snapshot"`
	Application        core.App                  `json:"application"`
	Server             core.Server               `json:"server"`
	Inputs             Inputs                    `json:"inputs"`
	Service            *ServiceRequest           `json:"service,omitempty"`
	SourceDeploymentID string                    `json:"sourceDeploymentId,omitempty"`
	ExpectedRuntime    string                    `json:"expectedRuntime,omitempty"`
	LogLimit           int                       `json:"logLimit,omitempty"`
}

// Inputs exists only inside the encrypted job and the authenticated agent
// request. App's public JSON representation deliberately omits these fields.
type Inputs struct {
	ComposeContent   string                       `json:"composeContent,omitempty"`
	SourceCredential string                       `json:"sourceCredential,omitempty"`
	Services         []core.ServiceRuntimeBinding `json:"services,omitempty"`
}

type LeasedJob struct {
	ID              string    `json:"id"`
	Attempt         int       `json:"attempt"`
	Digest          string    `json:"digest"`
	LeaseToken      string    `json:"leaseToken"`
	ExpiresAt       time.Time `json:"expiresAt"`
	CancelRequested bool      `json:"cancelRequested"`
	Request         Request   `json:"request"`
}

type Result struct {
	Retention *RetentionResult       `json:"retention,omitempty"`
	Route     *core.ApplicationRoute `json:"route,omitempty"`

	Health         *core.DeploymentHealth       `json:"health,omitempty"`
	Storage        []core.StorageObservation    `json:"storage"`
	State          string                       `json:"state"`
	Code           runtimecontract.Code         `json:"code,omitempty"`
	Message        string                       `json:"message,omitempty"`
	Resources      []Resource                   `json:"resources,omitempty"`
	ServiceOutputs map[string]string            `json:"serviceOutputs,omitempty"`
	Rollback       *core.RuntimeRollbackPreview `json:"rollback,omitempty"`
	RuntimeDigest  string                       `json:"runtimeDigest,omitempty"`
	Logs           string                       `json:"logs,omitempty"`
}

type Resource struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Image         string `json:"image"`
	State         string `json:"state"`
	ApplicationID string `json:"applicationId"`
	DeploymentID  string `json:"deploymentId,omitempty"`
}

type Heartbeat struct {
	LeaseToken string `json:"leaseToken"`
	Phase      string `json:"phase,omitempty"`
	Message    string `json:"message,omitempty"`
}

type Completion struct {
	LeaseToken string `json:"leaseToken"`
	Result     Result `json:"result"`
}

func NewRequest(op runtimecontract.Operation, d core.Deployment, app core.App, server core.Server) Request {
	inputs := Inputs{ComposeContent: app.ComposeContent, SourceCredential: app.SourceCredential, Services: app.ServiceRuntime}
	// Hooks run at the controller's checked execution boundary, never as a
	// second agent command path. Deployment subrecords cannot carry credentials.
	app.PreDeployHook, app.PostDeployHook, app.HookEnvironment = "", "", nil
	d.App, d.Server = nil, nil
	return Request{APIVersion: APIVersion, Operation: op, Deployment: d, Snapshot: d.Snapshot, Application: app, Server: server, Inputs: inputs, LogLimit: 200}
}

func (r Request) Validate() error {
	if r.APIVersion != APIVersion {
		return errors.New("unsupported agent runtime version")
	}
	if IsStorageOperation(r.Operation) {
		return r.validateStorage()
	}
	if r.Storage != nil {
		return errors.New("workload requests cannot carry storage deletion inputs")
	}
	if !identityPattern.MatchString(r.Application.ID) || !identityPattern.MatchString(r.Application.ProjectID) || !identityPattern.MatchString(r.Server.ID) || !identityPattern.MatchString(r.Server.AgentNodeID) {
		return errors.New("runtime request lacks a valid ownership identity")
	}
	if r.Application.ServerID != r.Server.ID || r.Server.Runtime != core.ServerRuntimeDocker {
		return errors.New("runtime target ownership does not match the application")
	}
	if r.Application.BuildType != core.BuildTypeDockerfile && r.Application.BuildType != core.BuildTypeCompose {
		return errors.New("remote runtime supports Dockerfile and Compose workloads")
	}
	if r.Operation != RetentionInspect && r.Operation != RetentionPrune && r.Retention != nil {
		return errors.New("unexpected retention inputs")
	}
	switch r.Operation {
	case RetentionInspect, RetentionPrune:
		return r.validateRetention()
	case ProvisionService:
		if err := r.validateService(); err != nil {
			return err
		}
	case runtimecontract.Deploy:
		if r.Inputs.ComposeContent == "" && !commitPattern.MatchString(r.Deployment.CommitSHA) {
			return errors.New("remote deployments require an immutable source commit")
		}
		if !identityPattern.MatchString(r.Deployment.ID) || r.Deployment.AppID != r.Application.ID || r.Deployment.SpecDigest == "" {
			return errors.New("deployment evidence is incomplete")
		}
	case runtimecontract.Rollback:
		if !identityPattern.MatchString(r.SourceDeploymentID) || !identityPattern.MatchString(r.Deployment.ID) || r.Deployment.AppID != r.Application.ID || r.ExpectedRuntime == "" {
			return errors.New("rollback requires retained source and reviewed runtime evidence")
		}
	case runtimecontract.Inspect, runtimecontract.Logs, runtimecontract.Start, runtimecontract.Stop, runtimecontract.Destroy:
	default:
		return errors.New("unsupported remote runtime operation")
	}
	if r.SourceDeploymentID != "" && !identityPattern.MatchString(r.SourceDeploymentID) {
		return errors.New("invalid retained deployment identity")
	}
	if r.LogLimit < 0 || r.LogLimit > 1000 {
		return errors.New("runtime log limit must be between 0 and 1000 lines")
	}
	return nil
}

func (r Request) App() core.App {
	app := r.Application
	app.ComposeContent, app.SourceCredential, app.ServiceRuntime = r.Inputs.ComposeContent, r.Inputs.SourceCredential, r.Inputs.Services
	return app
}

func (r Request) Redact(value string) string {
	secrets := r.SecretValues()

	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}
