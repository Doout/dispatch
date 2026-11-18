package workflowfile

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const APIVersion = "dispatch/v1alpha1"

var (
	namePattern        = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?$`)
	aliasPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	outputPattern      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
	jsonKeyPathPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z_][A-Za-z0-9_-]*)*$`)
	templatePattern    = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
)

func ApplyApplicationDefaults(spec *ApplicationSpec) {
	ApplySourceDefaults(spec.Sources)
	ApplyJobDefaults(spec.Jobs)
	ApplyJobDefaults(spec.Finally)
	for index := range spec.Stages {
		if spec.Stages[index].Approval == "" {
			spec.Stages[index].Approval = "automatic"
		}
	}
}

func ApplySourceDefaults(sources map[string]SourceSpec) {
	for name, source := range sources {
		if source.Branch == "" && source.Ref == "" {
			source.Branch = "main"
		}
		source.Path = strings.Trim(strings.TrimSpace(source.Path), "/")
		source.Connection = strings.TrimSpace(source.Connection)
		sources[name] = source
	}
}

func ApplyJobDefaults(jobs map[string]JobSpec) {
	for name, job := range jobs {
		if job.Reuse == "" {
			job.Reuse = "onInputMatch"
		}
		job.Sources = UniqueStrings(job.Sources)
		for alias, paths := range job.SourcePaths {
			job.SourcePaths[alias] = UniqueStrings(paths)
		}
		jobs[name] = job
	}
}

func ValidateHeader(meta TypeMeta, metadata Metadata) error {
	if meta.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %s", APIVersion)
	}
	if !namePattern.MatchString(metadata.Name) {
		return errors.New("metadata.name must be a lowercase DNS name")
	}
	return nil
}

func ValidateSources(sources map[string]SourceSpec) error {
	for alias, source := range sources {
		if !aliasPattern.MatchString(alias) {
			return fmt.Errorf("spec.sources.%s has an invalid alias", alias)
		}
		if strings.TrimSpace(source.Repository) == "" {
			return fmt.Errorf("spec.sources.%s.repository is required", alias)
		}
		if len(source.Connection) > 80 || strings.ContainsAny(source.Connection, "\r\n\t") {
			return fmt.Errorf("spec.sources.%s.connection must be a GitHub App connection name", alias)
		}
		if source.Branch != "" && source.Ref != "" {
			return fmt.Errorf("spec.sources.%s must use branch or ref, not both", alias)
		}
		ref := SourceRevisionRef(source)
		if strings.ContainsAny(ref, " \t\r\n~^:?*[\\") || strings.Contains(ref, "..") || strings.HasPrefix(ref, "-") {
			return fmt.Errorf("spec.sources.%s has an invalid Git ref", alias)
		}
		if strings.TrimSpace(ref) == "" {
			return fmt.Errorf("spec.sources.%s.branch is required", alias)
		}
		if source.Path == ".." || strings.HasPrefix(source.Path, "../") || strings.Contains(source.Path, "/../") {
			return fmt.Errorf("spec.sources.%s.path must remain inside the repository", alias)
		}
	}
	return nil
}

func ValidateJobs(prefix string, jobs map[string]JobSpec, sources map[string]SourceSpec, inputs map[string]InputSpec, sourceFree bool) error {
	if err := ValidateJobDependencies(prefix, jobs); err != nil {
		return err
	}
	for name, job := range jobs {
		if prefix == "spec.finally" && len(job.Needs) > 0 {
			return fmt.Errorf("%s.%s.needs is not supported; finally jobs run in name order", prefix, name)
		}
		if !aliasPattern.MatchString(name) {
			return fmt.Errorf("%s.%s has an invalid name", prefix, name)
		}
		if _, ok := sources[job.RunFrom]; !ok && (!sourceFree && job.WorkerMode != "managed" || job.RunFrom != "") {
			return fmt.Errorf("%s.%s.runFrom references unknown source %q", prefix, name, job.RunFrom)
		}
		if strings.TrimSpace(job.Run) == "" {
			return fmt.Errorf("%s.%s.run is required", prefix, name)
		}
		if job.WorkerMode != "" && job.WorkerMode != "tenant" && job.WorkerMode != "managed" {
			return fmt.Errorf("job %s: workerMode must be tenant or managed", name)
		}
		if job.WorkerMode == "managed" && (job.Builder != "" || job.RunFrom != "" || len(job.Sources) != 0 || len(job.SourcePaths) != 0) {
			return fmt.Errorf("job %s: managed workers cannot use repositories or Docker", name)
		}
		if job.Builder != "" && job.Builder != "docker" {
			return fmt.Errorf("%s.%s.builder must be docker", prefix, name)
		}
		for _, alias := range job.Sources {
			if _, ok := sources[alias]; !ok {
				return fmt.Errorf("%s.%s.sources references unknown source %q", prefix, name, alias)
			}
		}
		for alias, paths := range job.SourcePaths {
			if !slices.Contains(JobSourceAliases(job), alias) || len(paths) == 0 {
				return fmt.Errorf("%s.%s.sourcePaths.%s requires a declared source and at least one path", prefix, name, alias)
			}
			for _, path := range paths {
				if err := ValidateInputPath(path); err != nil {
					return fmt.Errorf("%s.%s.sourcePaths.%s: %w", prefix, name, alias, err)
				}
			}
		}
		if err := ValidateTemplates(job.Run, sources, inputs, false); err != nil {
			return fmt.Errorf("%s.%s.run: %w", prefix, name, err)
		}
		for environment, binding := range job.Secrets {
			if !outputPattern.MatchString(environment) || (strings.TrimSpace(binding.SecretRef) == "") == (strings.TrimSpace(binding.ServiceRef) == "") {
				return fmt.Errorf("%s.%s.secrets.%s requires secretRef", prefix, name, environment)
			}
			if binding.Key != "" && !jsonKeyPathPattern.MatchString(binding.Key) {
				return fmt.Errorf("%s.%s.secrets.%s has an invalid JSON key path", prefix, name, environment)
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

func ValidateTemplates(value string, sources map[string]SourceSpec, inputs map[string]InputSpec, allowStage bool) error {
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

func UniqueStrings(values []string) []string {
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

func SourceRevisionRef(source SourceSpec) string {
	if source.Ref != "" {
		return source.Ref
	}
	return source.Branch
}

func ValidateApplication(document ApplicationDocument, validateServiceDestinations func(DeploymentSpec) error) error {
	if err := ValidateJobConcurrency(document.Spec.MaxParallelJobs); err != nil {
		return err
	}
	if document.Spec.Reporting != nil {
		policy := document.Spec.Reporting
		if policy.StatusContext != "" && (len(policy.StatusContext) > 100 || strings.ContainsAny(policy.StatusContext, "\r\n") || strings.TrimSpace(policy.StatusContext) != policy.StatusContext || strings.EqualFold(policy.StatusContext, "Dispatch/deployment")) {
			return errors.New("spec.reporting.statusContext must be 1-100 characters and separate from Dispatch/deployment")
		}
		if policy.ReviewOnSuccess != "" && policy.ReviewOnSuccess != "none" && policy.ReviewOnSuccess != "approve" {
			return errors.New("spec.reporting.reviewOnSuccess must be none or approve")
		}
		if policy.ReviewOnFailure != "" && policy.ReviewOnFailure != "none" && policy.ReviewOnFailure != "requestChanges" {
			return errors.New("spec.reporting.reviewOnFailure must be none or requestChanges")
		}
	}
	if err := ValidateHeader(document.TypeMeta, document.Metadata); err != nil {
		return err
	}
	if len(document.Spec.Sources) == 0 {
		return errors.New("spec.sources must contain at least one source")
	}
	if err := ValidateSources(document.Spec.Sources); err != nil {
		return err
	}
	if err := ValidateJobs("spec.jobs", document.Spec.Jobs, document.Spec.Sources, nil, false); err != nil {
		return err
	}
	if err := ValidateJobs("spec.finally", document.Spec.Finally, document.Spec.Sources, nil, false); err != nil {
		return err
	}
	for name, deployment := range document.Spec.Deployments {
		if err := validateServiceDestinations(deployment); err != nil {
			return fmt.Errorf("deployment %s: %w", name, err)
		}
		if !aliasPattern.MatchString(name) {
			return fmt.Errorf("spec.deployments.%s has an invalid name", name)
		}
		if _, ok := document.Spec.Sources[deployment.Helm.SourceRef]; !ok {
			return fmt.Errorf("spec.deployments.%s.helm.sourceRef references unknown source %q", name, deployment.Helm.SourceRef)
		}
		if _, err := SafeJoin("/repository", deployment.Helm.ChartPath); err != nil {
			return fmt.Errorf("deployment %s chartPath: %w", name, err)
		}
		for _, file := range deployment.Helm.ValuesFiles {
			if _, ok := document.Spec.Sources[file.SourceRef]; !ok {
				return fmt.Errorf("deployment %s values file references unknown source %q", name, file.SourceRef)
			}
			if strings.TrimSpace(file.Path) == "" {
				return fmt.Errorf("deployment %s values file path is required", name)
			}
			if _, err := SafeJoin("/repository", file.Path); err != nil {
				return fmt.Errorf("deployment %s values file: %w", name, err)
			}
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
			if check.When != "" && check.When != "automatic" && check.When != "onDemand" {
				return fmt.Errorf("spec.stages[%d].checks.%s.when must be automatic or onDemand", index, name)
			}
			for key, value := range check.With {
				if !aliasPattern.MatchString(key) {
					return fmt.Errorf("spec.stages[%d].checks.%s.with.%s has an invalid input name", index, name, key)
				}
				if err := ValidateTemplates(value, document.Spec.Sources, nil, true); err != nil {
					return fmt.Errorf("spec.stages[%d].checks.%s.with.%s: %w", index, name, key, err)
				}
			}
		}
	}
	return nil
}

func ValidateJobConcurrency(limit int) error {
	if limit < 0 || limit > 32 {
		return errors.New("spec.maxParallelJobs must be between 1 and 32 when set")
	}
	return nil
}

func ValidateJobDependencies(prefix string, jobs map[string]JobSpec) error {
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return fmt.Errorf("%s.%s.needs contains a dependency cycle", prefix, name)
		}
		if visited[name] {
			return nil
		}
		visiting[name] = true
		seen := map[string]bool{}
		for _, dependency := range jobs[name].Needs {
			if _, ok := jobs[dependency]; !ok || seen[dependency] {
				return fmt.Errorf("%s.%s.needs contains unknown or duplicate job %q", prefix, name, dependency)
			}
			seen[dependency] = true
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[name], visited[name] = false, true
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(jobs)) {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func JobSourceAliases(job JobSpec) []string {
	aliases := append([]string{}, job.Sources...)
	if job.RunFrom != "" {
		aliases = append(aliases, job.RunFrom)
	}
	slices.Sort(aliases)
	return UniqueStrings(aliases)
}

func SafeJoin(root, value string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if value == "." || value == "" {
		return root, nil
	}
	if filepath.IsAbs(value) || value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return "", errors.New("source path must remain inside the repository")
	}
	joined := filepath.Join(root, value)
	relative, err := filepath.Rel(root, joined)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("source path must remain inside the repository")
	}
	return joined, nil
}

func ValidateInputPath(value string) error {
	if strings.TrimSpace(value) == "" || path.IsAbs(value) || strings.ContainsAny(value, "\\\x00*?[") {
		return fmt.Errorf("input path %q must be a relative file or directory without wildcards", value)
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return fmt.Errorf("input path %q must not traverse parent directories", value)
		}
	}
	return nil
}
