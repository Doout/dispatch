package events

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
)

const maxPreviewCommentValuesBytes = 64 << 10

var previewValuesDeployment = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type commentFence struct {
	info    string
	content string
	closed  bool
}

func parsePreviewValuesComment(message, arguments string) (string, *core.WorkflowPreviewValuesCommand, error) {
	fields := strings.Fields(arguments)
	explicit := len(fields) > 0 && strings.EqualFold(fields[0], "values")
	_, remainder, _ := strings.Cut(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	fences := previewCommentFences(remainder)
	hasYAML := false
	for _, fence := range fences {
		info := strings.Fields(fence.info)
		if len(info) > 0 && (strings.EqualFold(info[0], "yaml") || strings.EqualFold(info[0], "yml")) {
			hasYAML = true
		}
	}
	if !explicit && !hasYAML {
		return arguments, nil, nil
	}
	command := &core.WorkflowPreviewValuesCommand{}
	if explicit {
		switch {
		case len(fields) == 1:
		case len(fields) == 2 && strings.EqualFold(fields[1], "clear") && !hasYAML:
			command.Clear = true
		case len(fields) == 2:
			command.Deployment = fields[1]
		case len(fields) == 3 && strings.EqualFold(fields[2], "clear"):
			command.Deployment, command.Clear = fields[1], true
		default:
			return arguments, nil, errors.New("use values [deployment] with fenced YAML, or values [deployment] clear")
		}
		if command.Deployment != "" && !previewValuesDeployment.MatchString(command.Deployment) {
			return arguments, nil, errors.New("preview values deployment must be a deployment alias")
		}
	} else if len(fields) > 0 && !strings.EqualFold(fields[0], "with") {
		return arguments, nil, errors.New("fenced Helm values can accompany only a preview deployment or linked deployment command")
	}
	if command.Clear {
		if len(fences) != 0 || strings.TrimSpace(remainder) != "" {
			return arguments, nil, errors.New("values clear cannot include YAML or additional content")
		}
		return "", command, nil
	}
	if len(fences) != 1 || !fences[0].closed || !strings.EqualFold(fences[0].info, "yaml") && !strings.EqualFold(fences[0].info, "yml") {
		return arguments, nil, errors.New("preview values require exactly one closed yaml or yml fence")
	}
	values, err := previewCommentValues(fences[0].content)
	if err != nil {
		return arguments, nil, err
	}
	command.Values = values
	if explicit {
		arguments = ""
	}
	return arguments, command, nil
}

// Recognize Markdown fences rather than interpreting a YAML-looking line inside
// another code block as a deployment override.
func previewCommentFences(message string) []commentFence {
	var fences []commentFence
	var current *commentFence
	var marker byte
	var width int
	var content strings.Builder
	for _, line := range strings.Split(message, "\n") {
		character, count, info, isFence := previewCommentFenceLine(line)
		if current == nil {
			if isFence {
				fences = append(fences, commentFence{info: info})
				current = &fences[len(fences)-1]
				marker, width = character, count
				content.Reset()
			}
			continue
		}
		if isFence && character == marker && count >= width && info == "" {
			current.content, current.closed = content.String(), true
			current = nil
			continue
		}
		content.WriteString(line)
		content.WriteByte('\n')
	}
	if current != nil {
		current.content = content.String()
	}
	return fences
}

func previewCommentFenceLine(line string) (byte, int, string, bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent == len(line) {
		return 0, 0, "", false
	}
	line = line[indent:]
	marker := line[0]
	if marker != '`' && marker != '~' {
		return 0, 0, "", false
	}
	width := 0
	for width < len(line) && line[width] == marker {
		width++
	}
	if width < 3 {
		return 0, 0, "", false
	}
	return marker, width, strings.TrimSpace(line[width:]), true
}

func previewCommentValues(content string) (map[string]any, error) {
	if len(content) > maxPreviewCommentValuesBytes {
		return nil, errors.New("preview Helm values must be no larger than 64 KiB")
	}
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var document yaml.Node
	if decoder.Decode(&document) != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("preview Helm values must be one YAML mapping")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("preview Helm values must contain exactly one YAML document")
	}
	if !previewValuesJSONNode(document.Content[0]) {
		return nil, errors.New("preview Helm values must use unique string keys and JSON-compatible literal values")
	}
	for index := 0; index < len(document.Content[0].Content); index += 2 {
		if document.Content[0].Content[index].Value == "_pipeline" {
			return nil, errors.New("preview Helm values cannot override reserved deployment configuration")
		}
	}
	var values map[string]any
	if document.Content[0].Decode(&values) != nil {
		return nil, errors.New("preview Helm values must be a valid YAML mapping")
	}
	if _, err := json.Marshal(values); err != nil {
		return nil, errors.New("preview Helm values must contain JSON-compatible literal values")
	}
	return values, nil
}

func previewValuesJSONNode(node *yaml.Node) bool {
	switch node.Kind {
	case yaml.MappingNode:
		if node.Tag != "!!map" || len(node.Content)%2 != 0 {
			return false
		}
		keys := map[string]bool{}
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || keys[key.Value] || !previewValuesJSONNode(node.Content[index+1]) {
				return false
			}
			keys[key.Value] = true
		}
		return true
	case yaml.SequenceNode:
		if node.Tag != "!!seq" {
			return false
		}
		for _, value := range node.Content {
			if !previewValuesJSONNode(value) {
				return false
			}
		}
		return true
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!str", "!!null", "!!bool", "!!int", "!!float":
			return true
		}
	}
	return false
}
