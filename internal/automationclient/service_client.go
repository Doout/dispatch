package automationclient

import (
	"errors"
	"strings"
)

type ServiceProvisionInput struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Inputs      map[string]string `json:"inputs"`
}

var serviceClientOperations = []Operation{
	{Name: "service_templates_list", Method: "GET", Path: "/service-templates", Description: "List visible service templates, optionally filtered by project. An owner must approve the exact template digest before an automation account can provision it.", Fields: []string{"projectId"}},
	{Name: "service_template_get", Method: "GET", Path: "/service-templates/{templateId}", Description: "Inspect a visible service template's inputs, target configuration and immutable digest before requesting provisioning.", Fields: []string{"templateId"}, Required: []string{"templateId"}},
	{Name: "service_provision", Method: "POST", Path: "/service-templates/{templateId}/runs", Description: "Provision an explicitly approved built-in service template with supplied inputs and a stable retry key. Requires service.provision, project.view, assigned target and quota. Cannot run custom scripts or copy production data.", Fields: []string{"templateId", "key", "input"}, Required: []string{"templateId", "key", "input"}, Mutation: true, InputSchema: "ServiceProvisionInput", Response: "receipt_or_service_provision"},
	{Name: "service_runs_list", Method: "GET", Path: "/service-provision-runs", Description: "List visible service provision runs, optionally filtered by project. Reuse original run IDs for inspection and recovery.", Fields: []string{"projectId"}},
	{Name: "service_run_get", Method: "GET", Path: "/service-provision-runs/{runId}", Description: "Inspect the original service provision run and its execution state without requesting another service.", Fields: []string{"runId"}, Required: []string{"runId"}, Response: "service_provision_run"},
}

func serviceClientInput(op Operation, args Arguments) (any, error) {
	var input ServiceProvisionInput
	if decodeStrict(args.Input, &input) != nil || strings.TrimSpace(input.Name) == "" || input.Inputs == nil {
		return nil, errors.New("Supply the approved service name and explicit input values; use {} for templates without inputs")
	}
	return input, nil
}
