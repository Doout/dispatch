// Package workflowfile contains the portable parts of Dispatch workflow documents.
package workflowfile

import "github.com/doout/dispatch/internal/core"

type SourceSpec struct {
	Ref        string `json:"ref,omitempty" yaml:"ref,omitempty"`
	Repository string `json:"repository" yaml:"repository"`
	Branch     string `json:"branch,omitempty" yaml:"branch,omitempty"`
	Path       string `json:"path,omitempty" yaml:"path,omitempty"`
	GitHubApp  string `json:"githubApp,omitempty" yaml:"githubApp,omitempty"`
}

type JobSpec struct {
	WorkerMode  string                   `json:"workerMode,omitempty" yaml:"workerMode,omitempty"`
	Needs       []string                 `json:"needs,omitempty" yaml:"needs,omitempty"`
	Builder     string                   `json:"builder,omitempty" yaml:"builder,omitempty"`
	SourcePaths map[string][]string      `json:"sourcePaths,omitempty" yaml:"sourcePaths,omitempty"`
	RunFrom     string                   `json:"runFrom" yaml:"runFrom"`
	Sources     []string                 `json:"sources,omitempty" yaml:"sources,omitempty"`
	Run         string                   `json:"run" yaml:"run"`
	Secrets     map[string]SecretBinding `json:"secrets,omitempty" yaml:"secrets,omitempty"`
	Outputs     []string                 `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	Reuse       string                   `json:"reuse,omitempty" yaml:"reuse,omitempty"`
}

type SecretBinding struct {
	ServiceRef string `json:"serviceRef,omitempty" yaml:"serviceRef,omitempty"`
	SecretRef  string `json:"secretRef,omitempty" yaml:"secretRef,omitempty"`
	Key        string `json:"key,omitempty" yaml:"key,omitempty"`
}

type HelmDeploymentSpec struct {
	ChartPath   string                   `json:"chartPath,omitempty" yaml:"chartPath,omitempty"`
	ValuesFiles []HelmValuesFile         `json:"valuesFiles,omitempty" yaml:"valuesFiles,omitempty"`
	SourceRef   string                   `json:"sourceRef" yaml:"sourceRef"`
	Namespace   string                   `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	ReleaseName string                   `json:"releaseName,omitempty" yaml:"releaseName,omitempty"`
	Values      map[string]any           `json:"values,omitempty" yaml:"values,omitempty"`
	Bindings    map[string]OutputBinding `json:"bindings,omitempty" yaml:"bindings,omitempty"`
}

type HelmValuesFile struct {
	SourceRef string `json:"sourceRef" yaml:"sourceRef"`
	Path      string `json:"path" yaml:"path"`
}

type OutputBinding struct {
	OutputRef string `json:"outputRef" yaml:"outputRef"`
}

type TypeMeta struct {
	APIVersion string `json:"apiVersion" yaml:"apiVersion"`
	Kind       string `json:"kind" yaml:"kind"`
}

type Metadata struct {
	Name string `json:"name" yaml:"name"`
}

type ApplicationDocument struct {
	TypeMeta `json:",inline" yaml:",inline"`
	Metadata Metadata        `json:"metadata" yaml:"metadata"`
	Spec     ApplicationSpec `json:"spec" yaml:"spec"`
}

type ApplicationSpec struct {
	GitHubLogs      bool                          `json:"githubLogs,omitempty" yaml:"githubLogs,omitempty"`
	PreviewServices map[string]PreviewServiceSpec `json:"previewServices,omitempty" yaml:"previewServices,omitempty"`
	MaxParallelJobs int                           `json:"maxParallelJobs,omitempty" yaml:"maxParallelJobs,omitempty"`
	Reporting       *core.WorkflowReporting       `json:"reporting,omitempty" yaml:"reporting,omitempty"`
	Sources         map[string]SourceSpec         `json:"sources" yaml:"sources"`
	Jobs            map[string]JobSpec            `json:"jobs,omitempty" yaml:"jobs,omitempty"`
	Deployments     map[string]DeploymentSpec     `json:"deployments,omitempty" yaml:"deployments,omitempty"`
	Stages          []StageSpec                   `json:"stages,omitempty" yaml:"stages,omitempty"`
	Finally         map[string]JobSpec            `json:"finally,omitempty" yaml:"finally,omitempty"`
}

type InputSpec struct {
	Type     string `json:"type,omitempty" yaml:"type,omitempty"`
	Required bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Default  any    `json:"default,omitempty" yaml:"default,omitempty"`
}

type StageSpec struct {
	ServiceBindings map[string]string    `json:"serviceBindings,omitempty" yaml:"serviceBindings,omitempty"`
	Name            string               `json:"name" yaml:"name"`
	TargetRef       string               `json:"targetRef" yaml:"targetRef"`
	Deploy          []string             `json:"deploy" yaml:"deploy"`
	URL             string               `json:"url,omitempty" yaml:"url,omitempty"`
	Checks          map[string]CheckSpec `json:"checks,omitempty" yaml:"checks,omitempty"`
	Approval        string               `json:"approval,omitempty" yaml:"approval,omitempty"`
}

type CheckSpec struct {
	PipelineRef string            `json:"pipelineRef" yaml:"pipelineRef"`
	With        map[string]string `json:"with,omitempty" yaml:"with,omitempty"`
	When        string            `json:"when,omitempty" yaml:"when,omitempty"`
}

type DeploymentSpec struct {
	ServiceBindings []core.ServiceBinding `json:"serviceBindings,omitempty" yaml:"serviceBindings,omitempty"`
	Helm            HelmDeploymentSpec    `json:"helm" yaml:"helm"`
}

type PreviewServiceSpec struct {
	TemplateRef   string `json:"templateRef" yaml:"templateRef"`
	CleanupPolicy string `json:"cleanupPolicy,omitempty" yaml:"cleanupPolicy,omitempty"`
}
