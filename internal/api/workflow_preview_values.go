package api

import (
	"fmt"
	"sort"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflow"
)

func prepareWorkflowPreviewValues(trigger core.WorkflowPreviewTrigger, command *core.WorkflowPreviewValuesCommand, spec *workflow.ApplicationSpec) (core.WorkflowPreviewTrigger, error) {
	if command == nil {
		return trigger, nil
	}
	if command.Clear && command.Deployment == "" {
		trigger.PreviewValues = nil
		return trigger, nil
	}
	deployment := command.Deployment
	if deployment == "" && len(spec.Deployments) == 1 {
		for name := range spec.Deployments {
			deployment = name
		}
	}
	if _, ok := spec.Deployments[deployment]; !ok {
		names := make([]string, 0, len(spec.Deployments))
		for name := range spec.Deployments {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return trigger, fmt.Errorf("this preview has no Helm deployments to override")
		}
		if deployment == "" {
			return trigger, fmt.Errorf("select a Helm deployment with %s values <deployment>; available deployments: %s", trigger.Command, strings.Join(names, ", "))
		}
		return trigger, fmt.Errorf("unknown Helm deployment %q; available deployments: %s", deployment, strings.Join(names, ", "))
	}
	// A new comment replaces only this deployment's overrides. Keep other
	// deployment overrides and never mutate the cached trigger's maps.
	next := make(map[string]map[string]any, len(trigger.PreviewValues)+1)
	for name, values := range trigger.PreviewValues {
		next[name] = values
	}
	if command.Clear || len(command.Values) == 0 {
		delete(next, deployment)
	} else {
		copy, err := core.CloneWorkflowPreviewValues(map[string]map[string]any{deployment: command.Values})
		if err != nil {
			return trigger, fmt.Errorf("invalid preview Helm values")
		}
		next[deployment] = copy[deployment]
	}
	trigger.PreviewValues = next
	return trigger, nil
}
