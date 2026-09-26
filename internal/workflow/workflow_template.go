package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const InstanceIDVariable = "{{ instance.id }}"

var templateVariablePattern = regexp.MustCompile(`\{\{\s*((?:instance|trigger)\.[^{}]*?)\s*\}\}`)

type TemplateVariables struct {
	ID         string
	PRNumber   int
	Repository string
	PRURL      string
}

func HasInstanceID(value string) bool {
	if strings.Contains(value, "__PREVIEW_ID__") {
		return true
	}
	for _, match := range templateVariablePattern.FindAllStringSubmatch(value, -1) {
		if strings.TrimSpace(match[1]) == "instance.id" {
			return true
		}
	}
	return false
}

// RenderTemplateText resolves only the instance and trigger namespaces. Source/job expressions
// remain available for the workflow engine to resolve at execution time.
func RenderTemplateText(value string, variables TemplateVariables) (string, error) {
	var renderErr error
	result := templateVariablePattern.ReplaceAllStringFunc(value, func(token string) string {
		name := strings.TrimSpace(templateVariablePattern.FindStringSubmatch(token)[1])
		replacement := ""
		switch name {
		case "instance.id":
			replacement = variables.ID
		case "trigger.pullRequest.number":
			if variables.PRNumber > 0 {
				replacement = strconv.Itoa(variables.PRNumber)
			}
		case "trigger.repository":
			replacement = variables.Repository
		case "trigger.pullRequest.url":
			replacement = variables.PRURL
		default:
			renderErr = fmt.Errorf("unknown template variable %q; use instance.id, trigger.pullRequest.number, trigger.repository, or trigger.pullRequest.url", name)
			return token
		}
		if replacement == "" {
			renderErr = fmt.Errorf("template variable %q is unavailable in this context", name)
			return token
		}
		return replacement
	})
	// Retain compatibility with previously saved definitions.
	result = strings.ReplaceAll(result, "__PREVIEW_ID__", variables.ID)
	return result, renderErr
}

// RenderWorkflowTemplate converts the reusable resource into a concrete
// Application. Legacy Application templates remain supported for saved records.
func RenderWorkflowTemplate(contents []byte, variables TemplateVariables) ([]byte, error) {
	rendered, err := RenderTemplateText(string(contents), variables)
	if err != nil {
		return nil, err
	}
	contents = []byte(rendered)
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("provide exactly one WorkflowTemplate document")
	}
	var meta TypeMeta
	if err := node.Decode(&meta); err != nil {
		return nil, err
	}
	if meta.APIVersion != APIVersion || (meta.Kind != KindWorkflowTemplate && meta.Kind != KindApplication) {
		return nil, errors.New("use dispatch/v1alpha1 and kind WorkflowTemplate")
	}
	if meta.Kind == KindWorkflowTemplate {
		if _, _, err := ReadWorkflowTemplateTrigger(contents); err != nil {
			return nil, err
		}
		root := node.Content[0]
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == "kind" {
				root.Content[i+1].Value = KindApplication
			}
		}
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value != "spec" {
				continue
			}
			spec := root.Content[i+1]
			for j := 0; j+1 < len(spec.Content); j += 2 {
				if spec.Content[j].Value == "triggers" {
					spec.Content = append(spec.Content[:j], spec.Content[j+2:]...)
					break
				}
			}
		}
		contents, err = yaml.Marshal(&node)
		if err != nil {
			return nil, err
		}
	}
	return contents, nil
}

// Template triggers select which sources can instantiate the reusable workflow.
// The other sources remain dependencies and can still receive linked PR overrides.
type WorkflowTemplateTriggers struct {
	PullRequestComment *PullRequestCommentTrigger `json:"pullRequestComment,omitempty" yaml:"pullRequestComment,omitempty"`
}
type PullRequestCommentTrigger struct {
	Sources []string `json:"sources" yaml:"sources"`
	Command string   `json:"command" yaml:"command"`
}

func ReadWorkflowTemplateTrigger(contents []byte) (*PullRequestCommentTrigger, map[string]SourceSpec, error) {
	var header struct {
		TypeMeta `yaml:",inline"`
		Spec     struct {
			Sources  map[string]SourceSpec `yaml:"sources"`
			Triggers yaml.Node             `yaml:"triggers"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(contents, &header); err != nil {
		return nil, nil, err
	}
	if header.Kind != KindWorkflowTemplate || header.Spec.Triggers.Kind == 0 {
		return nil, nil, nil
	}
	raw, err := yaml.Marshal(&header.Spec.Triggers)
	if err != nil {
		return nil, nil, err
	}
	var triggers WorkflowTemplateTriggers
	if err := strictDecode(raw, &triggers); err != nil {
		return nil, nil, fmt.Errorf("template triggers: %w", err)
	}
	trigger := triggers.PullRequestComment
	if trigger == nil || len(trigger.Sources) == 0 {
		return nil, nil, errors.New("spec.triggers.pullRequestComment.sources must name at least one source alias")
	}
	seen := map[string]bool{}
	for _, alias := range trigger.Sources {
		if _, ok := header.Spec.Sources[alias]; !ok || seen[alias] {
			return nil, nil, fmt.Errorf("trigger source %q must name a unique source alias", alias)
		}
		seen[alias] = true
	}
	if trigger.Command == "" {
		trigger.Command = "/preview"
	}
	return trigger, header.Spec.Sources, nil
}
