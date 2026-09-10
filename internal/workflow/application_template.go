package workflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
	"gopkg.in/yaml.v3"
)

// ApplicationTemplate discovers values files at the same revision as its own
// configuration and expands them into ordinary, independently managed Applications.
type ApplicationTemplateDocument struct {
	TypeMeta `json:",inline" yaml:",inline"`
	Metadata Metadata                `json:"metadata" yaml:"metadata"`
	Spec     ApplicationTemplateSpec `json:"spec" yaml:"spec"`
}
type ApplicationTemplateSpec struct {
	Files      TemplateFiles       `json:"files" yaml:"files"`
	Parameters map[string]string   `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	Template   ApplicationDocument `json:"template" yaml:"template"`
}
type TemplateFiles struct {
	Path    string `json:"path" yaml:"path"`
	Pattern string `json:"pattern" yaml:"pattern"`
}

var slotParameterPattern = regexp.MustCompile(`\$\{(?:slot|param|config)\.[^{}]+\}`)

func validateApplicationTemplate(d ApplicationTemplateDocument) error {
	if d.APIVersion != APIVersion || !namePattern.MatchString(d.Metadata.Name) {
		return errors.New("invalid ApplicationTemplate apiVersion or name")
	}
	if d.Spec.Template.Kind != KindApplication || d.Spec.Template.APIVersion != APIVersion {
		return errors.New("spec.template must be an Application")
	}
	if d.Spec.Files.Path == "" || d.Spec.Files.Path == "." {
		return errors.New("spec.files.path must name a values directory")
	}
	if _, err := safeJoin("/repository", d.Spec.Files.Path); err != nil {
		return err
	}
	if strings.ContainsAny(d.Spec.Files.Pattern, "/\\") || d.Spec.Files.Pattern == "" {
		return errors.New("spec.files.pattern must be a filename pattern")
	}
	if _, err := path.Match(d.Spec.Files.Pattern, "slot.yaml"); err != nil {
		return fmt.Errorf("spec.files.pattern: %w", err)
	}
	for name := range d.Spec.Parameters {
		if !outputPattern.MatchString(name) {
			return fmt.Errorf("invalid template parameter %q", name)
		}
	}
	return nil
}
func (s *Service) parseConfiguration(ctx context.Context, source core.ConfigSource, revision string, files []githubapp.RepositoryFile) ([]parsedResource, error) {
	return expandConfiguration(files, revision, func(directory string) ([]githubapp.RepositoryFile, error) {
		valuesSource := source
		valuesSource.Path = directory
		values, err := s.repositoryFiles(ctx, valuesSource, revision)
		if errors.Is(err, githubapp.ErrNoConfigurationFiles) {
			return []githubapp.RepositoryFile{}, nil
		}
		return values, err
	})
}
func expandConfiguration(files []githubapp.RepositoryFile, revision string, read func(string) ([]githubapp.RepositoryFile, error)) ([]parsedResource, error) {
	parsed := []parsedResource{}
	cache := map[string][]githubapp.RepositoryFile{}
	for _, file := range files {
		documents, err := Parse(file.Path, file.Contents)
		if err != nil {
			return nil, err
		}
		for _, document := range documents {
			if document.Template == nil {
				parsed = append(parsed, parsedResource{file.Path, document})
				continue
			}
			spec := document.Template
			directory := path.Clean(spec.Files.Path)
			values, ok := cache[directory]
			if !ok {
				values, err = read(directory)
				if err != nil {
					return nil, fmt.Errorf("%s: discover values: %w", file.Path, err)
				}
				cache[directory] = values
			}
			sort.Slice(values, func(i, j int) bool { return values[i].Path < values[j].Path })
			for _, value := range values {
				if path.Dir(value.Path) != path.Clean(spec.Files.Path) {
					continue
				}
				match, _ := path.Match(spec.Files.Pattern, path.Base(value.Path))
				if !match {
					continue
				}
				expanded, err := expandApplication(document, value, revision)
				if err != nil {
					return nil, fmt.Errorf("%s using %s: %w", file.Path, value.Path, err)
				}
				parsed = append(parsed, parsedResource{file.Path, expanded})
				if len(parsed) > 256 {
					return nil, errors.New("configuration expands to more than 256 resources")
				}
			}
		}
	}
	if err := validateResourceSet(parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}
func expandApplication(document Document, file githubapp.RepositoryFile, revision string) (Document, error) {
	spec := document.Template
	var values map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(file.Contents))
	if err := decoder.Decode(&values); err != nil {
		return Document{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Document{}, errors.New("slot values must contain exactly one YAML document")
	}
	if values == nil {
		return Document{}, errors.New("slot values must be a mapping")
	}
	name := strings.TrimSuffix(path.Base(file.Path), path.Ext(file.Path))
	if !namePattern.MatchString(name) {
		return Document{}, fmt.Errorf("invalid slot filename %q", name)
	}
	parameters := map[string]string{"slot.name": name, "slot.path": file.Path, "config.revision": revision}
	for key, value := range spec.Parameters {
		parameters["param."+key] = value
	}
	if raw, ok := values["_pipeline"]; ok {
		overrides, ok := raw.(map[string]any)
		if !ok {
			return Document{}, errors.New("_pipeline must be a mapping of template parameters")
		}
		for key, value := range overrides {
			if _, ok := spec.Parameters[key]; !ok {
				return Document{}, fmt.Errorf("unknown _pipeline parameter %q", key)
			}
			text, ok := value.(string)
			if !ok {
				return Document{}, fmt.Errorf("_pipeline.%s must be a string", key)
			}
			parameters["param."+key] = text
		}
	}
	encoded, err := yaml.Marshal(spec.Template)
	if err != nil {
		return Document{}, err
	}
	var tree yaml.Node
	if err := yaml.Unmarshal(encoded, &tree); err != nil {
		return Document{}, err
	}
	if err := expandTemplateNode(&tree, parameters); err != nil {
		return Document{}, err
	}
	encoded, err = yaml.Marshal(&tree)
	if err != nil {
		return Document{}, err
	}
	result, err := Parse(document.SourcePath, encoded)
	if err != nil {
		return Document{}, err
	}
	if len(result) != 1 || result[0].Spec == nil {
		return Document{}, errors.New("template must generate exactly one Application")
	}
	return result[0], nil
}

// Substitute scalar nodes, including mapping keys, so quotes and newlines in a
// parameter cannot inject YAML. Helm and job expressions are left untouched.
func expandTemplateNode(node *yaml.Node, parameters map[string]string) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
		var unresolved string
		node.Value = slotParameterPattern.ReplaceAllStringFunc(node.Value, func(token string) string {
			key := strings.TrimSuffix(strings.TrimPrefix(token, "${"), "}")
			if value, ok := parameters[key]; ok {
				return value
			}
			unresolved = key
			return token
		})
		if unresolved != "" {
			return fmt.Errorf("unknown template parameter %q", unresolved)
		}
	}
	for _, child := range node.Content {
		if err := expandTemplateNode(child, parameters); err != nil {
			return err
		}
	}
	return nil
}

// A file move must not change a slot's ID, release ownership or run history.
func existingApplication(existing []core.WorkflowResource, file, kind, name string) (core.WorkflowResource, bool, error) {
	candidates := []core.WorkflowResource{}
	var removed *core.WorkflowResource
	for _, item := range existing {
		if item.Kind != kind || item.Name != name {
			continue
		}
		if item.Path == file {
			if item.State != "removed" {
				return item, true, nil
			}
			saved := item
			removed = &saved
		}
		if item.State != "removed" {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) > 1 {
		return core.WorkflowResource{}, false, fmt.Errorf("cannot preserve identity for ambiguous %s/%s", kind, name)
	}
	if len(candidates) == 1 {
		return candidates[0], true, nil
	}
	if removed != nil {
		return *removed, true, nil
	}
	return core.WorkflowResource{}, false, nil
}
