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

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowfile"
	"gopkg.in/yaml.v3"
)

const APIVersion = workflowfile.APIVersion

const (
	KindApplication         = "Application"
	KindWorkflowTemplate    = "WorkflowTemplate"
	KindApplicationTemplate = "ApplicationTemplate"
	KindPipeline            = "Pipeline"
	KindServiceTemplate     = "ServiceTemplate"
)

type TypeMeta = workflowfile.TypeMeta

type Metadata = workflowfile.Metadata

type Document struct {
	TypeMeta        `json:",inline" yaml:",inline"`
	Metadata        Metadata                 `json:"metadata" yaml:"metadata"`
	Spec            *ApplicationSpec         `json:"spec,omitempty" yaml:"spec,omitempty"`
	Pipeline        *PipelineSpec            `json:"-" yaml:"-"`
	ServiceTemplate *ServiceTemplateSpec     `json:"-" yaml:"-"`
	SourcePath      string                   `json:"-" yaml:"-"`
	Template        *ApplicationTemplateSpec `json:"-" yaml:"-"`
}

type ApplicationDocument = workflowfile.ApplicationDocument

type PipelineDocument struct {
	TypeMeta `json:",inline" yaml:",inline"`
	Metadata Metadata     `json:"metadata" yaml:"metadata"`
	Spec     PipelineSpec `json:"spec" yaml:"spec"`
}

type ServiceTemplateDocument struct {
	TypeMeta `json:",inline" yaml:",inline"`
	Metadata Metadata            `json:"metadata" yaml:"metadata"`
	Spec     ServiceTemplateSpec `json:"spec" yaml:"spec"`
}

// A ServiceTemplate returns the same connection fields from built-in
// Docker or Helm provisioning, or from an optional script.
type ServiceTemplateSpec struct {
	Description string                               `json:"description,omitempty" yaml:"description,omitempty"`
	ServiceType string                               `json:"serviceType" yaml:"serviceType"`
	Inputs      map[string]ServiceTemplateInputSpec  `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Sources     map[string]SourceSpec                `json:"sources,omitempty" yaml:"sources,omitempty"`
	Provision   ServiceProvisionSpec                 `json:"provision" yaml:"provision"`
	Outputs     map[string]ServiceTemplateOutputSpec `json:"outputs" yaml:"outputs"`
}

type ServiceProvisionSpec struct {
	JobSpec `yaml:",inline"`
	Docker  *core.DockerServiceProvision `json:"docker,omitempty" yaml:"docker,omitempty"`
	Helm    *core.HelmServiceProvision   `json:"helm,omitempty" yaml:"helm,omitempty"`
	Neon    *core.NeonServiceProvision   `json:"neon,omitempty" yaml:"neon,omitempty"`
}

func (p ServiceProvisionSpec) MarshalYAML() (any, error) {
	if p.Docker != nil || p.Helm != nil || p.Neon != nil {
		return struct {
			Docker *core.DockerServiceProvision `yaml:"docker,omitempty"`
			Helm   *core.HelmServiceProvision   `yaml:"helm,omitempty"`
			Neon   *core.NeonServiceProvision   `yaml:"neon,omitempty"`
		}{Docker: p.Docker, Helm: p.Helm, Neon: p.Neon}, nil
	}
	return p.JobSpec, nil
}

func (p ServiceProvisionSpec) Provider() string {
	if p.Neon != nil {
		return "neon"
	}
	if p.Docker != nil {
		return "docker"
	}
	if p.Helm != nil {
		return "helm"
	}
	return "script"
}

type ServiceTemplateInputSpec struct {
	Label       string `json:"label,omitempty" yaml:"label,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Type        string `json:"type,omitempty" yaml:"type,omitempty"`
	Required    bool   `json:"required,omitempty" yaml:"required,omitempty"`
	ServiceType string `json:"serviceType,omitempty" yaml:"serviceType,omitempty"`
}

type ServiceTemplateOutputSpec struct {
	Sensitive bool `json:"sensitive,omitempty" yaml:"sensitive,omitempty"`
}

type ApplicationSpec = workflowfile.ApplicationSpec

type PipelineSpec struct {
	MaxParallelJobs int                   `json:"maxParallelJobs,omitempty" yaml:"maxParallelJobs,omitempty"`
	Inputs          map[string]InputSpec  `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Sources         map[string]SourceSpec `json:"sources,omitempty" yaml:"sources,omitempty"`
	Jobs            map[string]JobSpec    `json:"jobs" yaml:"jobs"`
	Finally         map[string]JobSpec    `json:"finally,omitempty" yaml:"finally,omitempty"`
}

type InputSpec = workflowfile.InputSpec

type SourceSpec = workflowfile.SourceSpec

type JobSpec = workflowfile.JobSpec

type SecretBinding = workflowfile.SecretBinding

type DeploymentSpec = workflowfile.DeploymentSpec

type HelmValuesFile = workflowfile.HelmValuesFile

type HelmDeploymentSpec = workflowfile.HelmDeploymentSpec

type OutputBinding = workflowfile.OutputBinding

type StageSpec = workflowfile.StageSpec

type CheckSpec = workflowfile.CheckSpec

var (
	namePattern     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?$`)
	aliasPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	outputPattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
	templatePattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
)

// Parse decodes every YAML document in a file. JSON is accepted because it is
// a strict subset of YAML. Unknown fields fail instead of being ignored.
func Parse(path string, contents []byte) ([]Document, error) {
	return parseDocuments(path, contents, false)
}

func parseDocuments(path string, contents []byte, importing bool) ([]Document, error) {
	skippedPreview := false
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
		// A workflow definition is instantiated by its configured trigger,
		// never by the ordinary Application importer.
		if meta.Kind == KindWorkflowTemplate {
			if importing && meta.APIVersion == APIVersion {
				skippedPreview = true
				continue
			}
			return nil, fmt.Errorf("%s document %d: WorkflowTemplate must be instantiated through a configured trigger", path, index)
		}
		var item Document
		switch meta.Kind {
		case KindApplicationTemplate:
			var value ApplicationTemplateDocument
			if err := strictDecode(raw, &value); err != nil {
				return nil, fmt.Errorf("%s document %d: %w", path, index, err)
			}
			if err := validateApplicationTemplate(value); err != nil {
				return nil, fmt.Errorf("%s document %d: %w", path, index, err)
			}
			item = Document{TypeMeta: value.TypeMeta, Metadata: value.Metadata, Template: &value.Spec, SourcePath: path}
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
		case KindServiceTemplate:
			var value ServiceTemplateDocument
			if err := strictDecode(raw, &value); err != nil {
				return nil, fmt.Errorf("%s document %d: %w", path, index, err)
			}
			applyServiceTemplateDefaults(&value.Spec)
			if err := validateServiceTemplate(value); err != nil {
				return nil, fmt.Errorf("%s document %d: %w", path, index, err)
			}
			item = Document{TypeMeta: value.TypeMeta, Metadata: value.Metadata, ServiceTemplate: &value.Spec, SourcePath: path}
		default:
			return nil, fmt.Errorf("%s document %d: unsupported kind %q", path, index, meta.Kind)
		}
		items = append(items, item)
	}
	if len(items) == 0 && !skippedPreview {
		return nil, fmt.Errorf("%s does not contain a Doodship resource", path)
	}
	return items, nil
}

func strictDecode(contents []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	return decoder.Decode(target)
}

func applyApplicationDefaults(spec *ApplicationSpec) {
	workflowfile.ApplyApplicationDefaults(spec)
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

func applyServiceTemplateDefaults(spec *ServiceTemplateSpec) {
	applySourceDefaults(spec.Sources)
	if (spec.Provision.Docker != nil || spec.Provision.Helm != nil || spec.Provision.Neon != nil) && spec.ServiceType == "postgresql" && spec.Outputs == nil {
		spec.Outputs = map[string]ServiceTemplateOutputSpec{"connectionUrl": {Sensitive: true}}
	}
	if spec.Provision.Outputs == nil {
		for name := range spec.Outputs {
			spec.Provision.Outputs = append(spec.Provision.Outputs, name)
		}
		slices.Sort(spec.Provision.Outputs)
	}
	spec.Provision.Reuse = "never"
	for name, input := range spec.Inputs {
		if input.Type == "" {
			input.Type = "string"
		}
		spec.Inputs[name] = input
	}
}

func validateServiceTemplate(document ServiceTemplateDocument) error {
	if err := validateHeader(document.TypeMeta, document.Metadata); err != nil {
		return err
	}
	spec := document.Spec
	if spec.ServiceType != "postgresql" && spec.ServiceType != "generic" {
		return errors.New("spec.serviceType must be postgresql or generic")
	}
	if len(spec.Outputs) == 0 {
		return errors.New("spec.outputs must declare at least one service field")
	}
	if spec.ServiceType == "postgresql" {
		_, connectionURL := spec.Outputs["connectionUrl"]
		if connectionURL {
			for name := range spec.Outputs {
				if name != "connectionUrl" && name != "caCert" {
					return errors.New("spec.outputs.connectionUrl cannot be combined with individual PostgreSQL fields")
				}
			}
		} else {
			for _, name := range []string{"host", "database", "username"} {
				if _, ok := spec.Outputs[name]; !ok {
					return fmt.Errorf("spec.outputs.%s is required for PostgreSQL", name)
				}
			}
		}
	}
	inputs := map[string]InputSpec{}
	for name, input := range spec.Inputs {
		if !aliasPattern.MatchString(name) {
			return fmt.Errorf("spec.inputs.%s has an invalid name", name)
		}
		if input.Type != "string" && input.Type != "secret" && input.Type != "service" {
			return fmt.Errorf("spec.inputs.%s.type must be string, secret, or service", name)
		}
		if input.Type == "service" {
			if input.ServiceType != "postgresql" {
				return fmt.Errorf("spec.inputs.%s.serviceType must be postgresql", name)
			}
		} else if input.ServiceType != "" {
			return fmt.Errorf("spec.inputs.%s.serviceType requires a service input", name)
		}
		inputs[name] = InputSpec{Type: "string", Required: input.Required}
	}
	if err := validateSources(spec.Sources); err != nil {
		return err
	}
	if spec.Provision.Docker != nil || spec.Provision.Helm != nil || spec.Provision.Neon != nil {
		if err := validateBuiltinProvision(spec); err != nil {
			return err
		}
	} else if err := validateJobs("spec.provision", map[string]JobSpec{"job": spec.Provision.JobSpec}, spec.Sources, inputs, true); err != nil {
		return err
	}
	for _, match := range templatePattern.FindAllStringSubmatch(spec.Provision.Run, -1) {
		if strings.HasPrefix(strings.TrimSpace(match[1]), "inputs.") {
			return errors.New("spec.provision.run must read DISPATCH_INPUT_* environment variables instead of interpolating inputs")
		}
	}
	if len(spec.Provision.Outputs) != len(spec.Outputs) {
		return errors.New("spec.provision.outputs must match spec.outputs")
	}
	for name := range spec.Outputs {
		if !outputPattern.MatchString(name) || !slices.Contains(spec.Provision.Outputs, name) {
			return fmt.Errorf("spec.outputs.%s must be declared by the provision job", name)
		}
		if spec.ServiceType == "postgresql" && !slices.Contains([]string{"connectionUrl", "host", "port", "database", "username", "password", "sslmode", "caCert"}, name) {
			return fmt.Errorf("spec.outputs.%s is not a PostgreSQL connection field", name)
		}
	}
	return nil
}

func applySourceDefaults(sources map[string]SourceSpec) {
	workflowfile.ApplySourceDefaults(sources)
}

func applyJobDefaults(jobs map[string]JobSpec) {
	workflowfile.ApplyJobDefaults(jobs)
}

func validateApplication(document ApplicationDocument) error {
	if err := validatePreviewServiceReferences(document.Spec); err != nil {
		return err
	}
	return workflowfile.ValidateApplication(document, validateServiceDestinations)
}

func validatePipeline(document PipelineDocument) error {
	if err := validateJobConcurrency(document.Spec.MaxParallelJobs); err != nil {
		return err
	}
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
	if err := validateJobs("spec.jobs", document.Spec.Jobs, document.Spec.Sources, document.Spec.Inputs, true); err != nil {
		return err
	}
	return validateJobs("spec.finally", document.Spec.Finally, document.Spec.Sources, document.Spec.Inputs, true)
}

func validateHeader(meta TypeMeta, metadata Metadata) error {
	return workflowfile.ValidateHeader(meta, metadata)
}

func validateSources(sources map[string]SourceSpec) error {
	return workflowfile.ValidateSources(sources)
}

func validateJobs(prefix string, jobs map[string]JobSpec, sources map[string]SourceSpec, inputs map[string]InputSpec, sourceFree bool) error {
	return workflowfile.ValidateJobs(prefix, jobs, sources, inputs, sourceFree)
}

func uniqueStrings(values []string) []string {
	return workflowfile.UniqueStrings(values)
}

func (d Document) CanonicalJSON() ([]byte, error) {
	if d.ServiceTemplate != nil {
		return json.Marshal(ServiceTemplateDocument{TypeMeta: d.TypeMeta, Metadata: d.Metadata, Spec: *d.ServiceTemplate})
	}
	if d.Template != nil {
		return json.Marshal(ApplicationTemplateDocument{TypeMeta: d.TypeMeta, Metadata: d.Metadata, Spec: *d.Template})
	}
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
	if d.ServiceTemplate != nil {
		return yaml.Marshal(ServiceTemplateDocument{TypeMeta: d.TypeMeta, Metadata: d.Metadata, Spec: *d.ServiceTemplate})
	}
	if d.Template != nil {
		return yaml.Marshal(ApplicationTemplateDocument{TypeMeta: d.TypeMeta, Metadata: d.Metadata, Spec: *d.Template})
	}
	if d.Spec != nil {
		return yaml.Marshal(ApplicationDocument{TypeMeta: d.TypeMeta, Metadata: d.Metadata, Spec: *d.Spec})
	}
	if d.Pipeline != nil {
		return yaml.Marshal(PipelineDocument{TypeMeta: d.TypeMeta, Metadata: d.Metadata, Spec: *d.Pipeline})
	}
	return nil, errors.New("document has no spec")
}

func sourceRevisionRef(source SourceSpec) string {
	return workflowfile.SourceRevisionRef(source)
}
