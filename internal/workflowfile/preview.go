package workflowfile

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

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
