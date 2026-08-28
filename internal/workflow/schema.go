package workflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const APIVersion = "dispatch/v1alpha1"

const (
	KindApplication = "Application"
	KindPipeline    = "Pipeline"
)

type TypeMeta struct {
	APIVersion string `json:"apiVersion" yaml:"apiVersion"`
	Kind       string `json:"kind" yaml:"kind"`
}

type Metadata struct {
	Name string `json:"name" yaml:"name"`
}

type Document struct {
	TypeMeta   `json:",inline" yaml:",inline"`
	Metadata   Metadata         `json:"metadata" yaml:"metadata"`
	Spec       *ApplicationSpec `json:"spec,omitempty" yaml:"spec,omitempty"`
	Pipeline   *PipelineSpec    `json:"-" yaml:"-"`
	SourcePath string           `json:"-" yaml:"-"`
}

type ApplicationDocument struct {
	TypeMeta `json:",inline" yaml:",inline"`
	Metadata Metadata        `json:"metadata" yaml:"metadata"`
	Spec     ApplicationSpec `json:"spec" yaml:"spec"`
}

type PipelineDocument struct {
	TypeMeta `json:",inline" yaml:",inline"`
	Metadata Metadata     `json:"metadata" yaml:"metadata"`
	Spec     PipelineSpec `json:"spec" yaml:"spec"`
}

type ApplicationSpec struct {
	Sources     map[string]SourceSpec     `json:"sources" yaml:"sources"`
	Jobs        map[string]JobSpec        `json:"jobs,omitempty" yaml:"jobs,omitempty"`
	Deployments map[string]DeploymentSpec `json:"deployments,omitempty" yaml:"deployments,omitempty"`
	Stages      []StageSpec               `json:"stages,omitempty" yaml:"stages,omitempty"`
	Finally     map[string]JobSpec        `json:"finally,omitempty" yaml:"finally,omitempty"`
}

type PipelineSpec struct {
	Inputs  map[string]InputSpec  `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Sources map[string]SourceSpec `json:"sources,omitempty" yaml:"sources,omitempty"`
	Jobs    map[string]JobSpec    `json:"jobs" yaml:"jobs"`
	Finally map[string]JobSpec    `json:"finally,omitempty" yaml:"finally,omitempty"`
}

type InputSpec struct {
	Type     string `json:"type,omitempty" yaml:"type,omitempty"`
	Required bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Default  any    `json:"default,omitempty" yaml:"default,omitempty"`
}

type SourceSpec struct {
	Repository string `json:"repository" yaml:"repository"`
	Branch     string `json:"branch,omitempty" yaml:"branch,omitempty"`
	Path       string `json:"path,omitempty" yaml:"path,omitempty"`
}

type JobSpec struct {
	RunFrom string                   `json:"runFrom" yaml:"runFrom"`
	Sources []string                 `json:"sources,omitempty" yaml:"sources,omitempty"`
	Run     string                   `json:"run" yaml:"run"`
	Secrets map[string]SecretBinding `json:"secrets,omitempty" yaml:"secrets,omitempty"`
	Outputs []string                 `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	Reuse   string                   `json:"reuse,omitempty" yaml:"reuse,omitempty"`
}

type SecretBinding struct {
	SecretRef string `json:"secretRef" yaml:"secretRef"`
}

type DeploymentSpec struct {
	Helm HelmDeploymentSpec `json:"helm" yaml:"helm"`
}

type HelmDeploymentSpec struct {
	SourceRef   string                   `json:"sourceRef" yaml:"sourceRef"`
	Namespace   string                   `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	ReleaseName string                   `json:"releaseName,omitempty" yaml:"releaseName,omitempty"`
	Values      map[string]any           `json:"values,omitempty" yaml:"values,omitempty"`
	Bindings    map[string]OutputBinding `json:"bindings,omitempty" yaml:"bindings,omitempty"`
}

type OutputBinding struct {
	OutputRef string `json:"outputRef" yaml:"outputRef"`
}

type StageSpec struct {
	Name      string               `json:"name" yaml:"name"`
	TargetRef string               `json:"targetRef" yaml:"targetRef"`
	Deploy    []string             `json:"deploy" yaml:"deploy"`
	URL       string               `json:"url,omitempty" yaml:"url,omitempty"`
	Checks    map[string]CheckSpec `json:"checks,omitempty" yaml:"checks,omitempty"`
	Approval  string               `json:"approval,omitempty" yaml:"approval,omitempty"`
}

type CheckSpec struct {
	PipelineRef string            `json:"pipelineRef" yaml:"pipelineRef"`
	With        map[string]string `json:"with,omitempty" yaml:"with,omitempty"`
}

var (
	namePattern     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?$`)
	aliasPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	outputPattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
	templatePattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
)

// Parse decodes every YAML document in a file. JSON is accepted because it is
// a strict subset of YAML. Unknown fields fail instead of being ignored.
func Parse(path string, contents []byte) ([]Document, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	items := []Document{}
	for index := 1; ; index++ {
		var node yaml.Node
		err := decoder.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s document %d: %w", path, index, err)
		}
		if len(node.Content) == 0 || node.Content[0].Kind == 0 {
			continue
		}
		raw, err := yaml.Marshal(&node)
		if err != nil {
			return nil, fmt.Errorf("%s document %d: %w", path, index, err)
		}
		var meta TypeMeta
		if err := yaml.Unmarshal(raw, &meta); err != nil {
			return nil, fmt.Errorf("%s document %d: %w", path, index, err)
		}
		var item Document
		switch meta.Kind {
		case KindApplication:
			var value ApplicationDocument
			if err := strictDecode(raw, &value); err != nil {
				return nil, fmt.Errorf("%s document %d: %w", path, index, err)
			}
			applyApplicationDefaults(&value.Spec)
			if err := validateApplication(value); err != nil {
				return nil, fmt.Errorf("%s document %d: %w", path, index, err)
			}
			item = Document{TypeMeta: value.TypeMeta, Metadata: value.Metadata, Spec: &value.Spec, SourcePath: path}
		case KindPipeline:
			var value PipelineDocument
			if err := strictDecode(raw, &value); err != nil {
				return nil, fmt.Errorf("%s document %d: %w", path, index, err)
			}
			applyPipelineDefaults(&value.Spec)
			if err := validatePipeline(value); err != nil {
				return nil, fmt.Errorf("%s document %d: %w", path, index, err)
			}
			item = Document{TypeMeta: value.TypeMeta, Metadata: value.Metadata, Pipeline: &value.Spec, SourcePath: path}
		default:
			return nil, fmt.Errorf("%s document %d: kind must be Application or Pipeline", path, index)
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%s does not contain a Dispatch resource", path)
	}
	return items, nil
}

func strictDecode(contents []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	return decoder.Decode(target)
}

func applyApplicationDefaults(spec *ApplicationSpec) {
	applySourceDefaults(spec.Sources)
	applyJobDefaults(spec.Jobs)
	applyJobDefaults(spec.Finally)
	for index := range spec.Stages {
		if spec.Stages[index].Approval == "" {
			spec.Stages[index].Approval = "automatic"
		}
	}
}

func applyPipelineDefaults(spec *PipelineSpec) {
	applySourceDefaults(spec.Sources)
	applyJobDefaults(spec.Jobs)
	applyJobDefaults(spec.Finally)
	for name, input := range spec.Inputs {
		if input.Type == "" {
			input.Type = "string"
		}
		spec.Inputs[name] = input
	}
}

func applySourceDefaults(sources map[string]SourceSpec) {
	for name, source := range sources {
		if source.Branch == "" {
			source.Branch = "main"
		}
		source.Path = strings.Trim(strings.TrimSpace(source.Path), "/")
		sources[name] = source
	}
}

func applyJobDefaults(jobs map[string]JobSpec) {
	for name, job := range jobs {
		if job.Reuse == "" {
			job.Reuse = "onInputMatch"
		}
		job.Sources = uniqueStrings(job.Sources)
		jobs[name] = job
	}
}

func validateApplication(document ApplicationDocument) error {
	if err := validateHeader(document.TypeMeta, document.Metadata); err != nil {
		return err
	}
	if len(document.Spec.Sources) == 0 {
		return errors.New("spec.sources must contain at least one source")
	}
	if err := validateSources(document.Spec.Sources); err != nil {
		return err
	}
	if err := validateJobs("spec.jobs", document.Spec.Jobs, document.Spec.Sources, nil); err != nil {
		return err
	}
	if err := validateJobs("spec.finally", document.Spec.Finally, document.Spec.Sources, nil); err != nil {
		return err
	}
	for name, deployment := range document.Spec.Deployments {
		if !aliasPattern.MatchString(name) {
			return fmt.Errorf("spec.deployments.%s has an invalid name", name)
		}
		if _, ok := document.Spec.Sources[deployment.Helm.SourceRef]; !ok {
			return fmt.Errorf("spec.deployments.%s.helm.sourceRef references unknown source %q", name, deployment.Helm.SourceRef)
		}
		for path, binding := range deployment.Helm.Bindings {
			jobName, outputName, ok := strings.Cut(binding.OutputRef, ".")
			if strings.TrimSpace(path) == "" || !ok || jobName == "" || outputName == "" {
				return fmt.Errorf("spec.deployments.%s.helm.bindings must use job.output references", name)
			}
			job, ok := document.Spec.Jobs[jobName]
			if !ok || !slices.Contains(job.Outputs, outputName) {
				return fmt.Errorf("spec.deployments.%s.helm.bindings.%s references unknown output %q", name, path, binding.OutputRef)
			}
		}
	}
	stages := map[string]bool{}
	for index, stage := range document.Spec.Stages {
		if !aliasPattern.MatchString(stage.Name) || stages[stage.Name] {
			return fmt.Errorf("spec.stages[%d].name must be unique and contain lowercase letters, numbers, or hyphens", index)
		}
		stages[stage.Name] = true
		if strings.TrimSpace(stage.TargetRef) == "" {
			return fmt.Errorf("spec.stages[%d].targetRef is required", index)
		}
		if len(stage.Deploy) == 0 {
			return fmt.Errorf("spec.stages[%d].deploy must contain at least one deployment", index)
		}
		for _, name := range stage.Deploy {
			if _, ok := document.Spec.Deployments[name]; !ok {
				return fmt.Errorf("spec.stages[%d].deploy references unknown deployment %q", index, name)
			}
		}
		if stage.Approval != "automatic" && stage.Approval != "required" {
			return fmt.Errorf("spec.stages[%d].approval must be automatic or required", index)
		}
		for name, check := range stage.Checks {
			if !aliasPattern.MatchString(name) || strings.TrimSpace(check.PipelineRef) == "" {
				return fmt.Errorf("spec.stages[%d].checks.%s requires pipelineRef", index, name)
			}
			for key, value := range check.With {
				if !aliasPattern.MatchString(key) {
					return fmt.Errorf("spec.stages[%d].checks.%s.with.%s has an invalid input name", index, name, key)
				}
				if err := validateTemplates(value, document.Spec.Sources, nil, true); err != nil {
					return fmt.Errorf("spec.stages[%d].checks.%s.with.%s: %w", index, name, key, err)
				}
			}
		}
	}
	return nil
}

func validatePipeline(document PipelineDocument) error {
	if err := validateHeader(document.TypeMeta, document.Metadata); err != nil {
		return err
	}
	if len(document.Spec.Jobs) == 0 {
		return errors.New("spec.jobs must contain at least one job")
	}
	for name, input := range document.Spec.Inputs {
		if !aliasPattern.MatchString(name) {
			return fmt.Errorf("spec.inputs.%s has an invalid name", name)
		}
		if input.Type != "string" && input.Type != "number" && input.Type != "boolean" {
			return fmt.Errorf("spec.inputs.%s.type must be string, number, or boolean", name)
		}
	}
	if err := validateSources(document.Spec.Sources); err != nil {
		return err
	}
	if err := validateJobs("spec.jobs", document.Spec.Jobs, document.Spec.Sources, document.Spec.Inputs); err != nil {
		return err
	}
	return validateJobs("spec.finally", document.Spec.Finally, document.Spec.Sources, document.Spec.Inputs)
}

func validateHeader(meta TypeMeta, metadata Metadata) error {
	if meta.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %s", APIVersion)
	}
	if !namePattern.MatchString(metadata.Name) {
		return errors.New("metadata.name must be a lowercase DNS name")
	}
	return nil
}

func validateSources(sources map[string]SourceSpec) error {
	for alias, source := range sources {
		if !aliasPattern.MatchString(alias) {
			return fmt.Errorf("spec.sources.%s has an invalid alias", alias)
		}
		if strings.TrimSpace(source.Repository) == "" {
			return fmt.Errorf("spec.sources.%s.repository is required", alias)
		}
		if strings.TrimSpace(source.Branch) == "" {
			return fmt.Errorf("spec.sources.%s.branch is required", alias)
		}
		if source.Path == ".." || strings.HasPrefix(source.Path, "../") || strings.Contains(source.Path, "/../") {
			return fmt.Errorf("spec.sources.%s.path must remain inside the repository", alias)
		}
	}
	return nil
}

func validateJobs(prefix string, jobs map[string]JobSpec, sources map[string]SourceSpec, inputs map[string]InputSpec) error {
	for name, job := range jobs {
		if !aliasPattern.MatchString(name) {
			return fmt.Errorf("%s.%s has an invalid name", prefix, name)
		}
		if _, ok := sources[job.RunFrom]; !ok {
			return fmt.Errorf("%s.%s.runFrom references unknown source %q", prefix, name, job.RunFrom)
		}
		if strings.TrimSpace(job.Run) == "" {
			return fmt.Errorf("%s.%s.run is required", prefix, name)
		}
		for _, alias := range job.Sources {
			if _, ok := sources[alias]; !ok {
				return fmt.Errorf("%s.%s.sources references unknown source %q", prefix, name, alias)
			}
		}
		if err := validateTemplates(job.Run, sources, inputs, false); err != nil {
			return fmt.Errorf("%s.%s.run: %w", prefix, name, err)
		}
		for environment, binding := range job.Secrets {
			if !outputPattern.MatchString(environment) || strings.TrimSpace(binding.SecretRef) == "" {
				return fmt.Errorf("%s.%s.secrets.%s requires secretRef", prefix, name, environment)
			}
		}
		seen := map[string]bool{}
		for _, output := range job.Outputs {
			if !outputPattern.MatchString(output) || seen[output] {
				return fmt.Errorf("%s.%s.outputs contains invalid or duplicate output %q", prefix, name, output)
			}
			seen[output] = true
		}
		if job.Reuse != "" && job.Reuse != "onInputMatch" && job.Reuse != "never" {
			return fmt.Errorf("%s.%s.reuse must be onInputMatch or never", prefix, name)
		}
	}
	return nil
}

func validateTemplates(value string, sources map[string]SourceSpec, inputs map[string]InputSpec, allowStage bool) error {
	for _, match := range templatePattern.FindAllStringSubmatch(value, -1) {
		path := strings.TrimSpace(match[1])
		parts := strings.Split(path, ".")
		switch {
		case len(parts) == 3 && parts[0] == "sources":
			if _, ok := sources[parts[1]]; !ok || (parts[2] != "path" && parts[2] != "commit" && parts[2] != "branch") {
				return fmt.Errorf("unknown template %q", path)
			}
		case len(parts) == 2 && parts[0] == "inputs":
			if _, ok := inputs[parts[1]]; !ok {
				return fmt.Errorf("unknown template %q", path)
			}
		case allowStage && len(parts) == 2 && parts[0] == "stage" && (parts[1] == "name" || parts[1] == "url"):
		default:
			return fmt.Errorf("unknown template %q", path)
		}
	}
	without := templatePattern.ReplaceAllString(value, "")
	if strings.Contains(without, "{{") || strings.Contains(without, "}}") {
		return errors.New("template braces are incomplete")
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func (d Document) CanonicalJSON() ([]byte, error) {
	if d.Spec != nil {
		return json.Marshal(ApplicationDocument{TypeMeta: d.TypeMeta, Metadata: d.Metadata, Spec: *d.Spec})
	}
	if d.Pipeline != nil {
		return json.Marshal(PipelineDocument{TypeMeta: d.TypeMeta, Metadata: d.Metadata, Spec: *d.Pipeline})
	}
	return nil, errors.New("document has no spec")
}

func (d Document) Digest() (string, error) {
	contents, err := d.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (d Document) MarshalYAML() ([]byte, error) {
	if d.Spec != nil {
		return yaml.Marshal(ApplicationDocument{TypeMeta: d.TypeMeta, Metadata: d.Metadata, Spec: *d.Spec})
	}
	if d.Pipeline != nil {
		return yaml.Marshal(PipelineDocument{TypeMeta: d.TypeMeta, Metadata: d.Metadata, Spec: *d.Pipeline})
	}
	return nil, errors.New("document has no spec")
}
