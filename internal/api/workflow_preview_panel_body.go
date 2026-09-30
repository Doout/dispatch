package api

import (
	"fmt"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

const (
	previewRedeployLabel = "Deploy latest commits"
	previewTestLabel     = "Run configured checks"
	previewLiveLabel     = "Live reload"
	previewExtendLabel   = "Extend lifetime by one day"
)

func previewPanelControls(trigger core.WorkflowPreviewTrigger) string {
	live := " "
	if trigger.LiveReload {
		live = "x"
	}
	body := fmt.Sprintf("\n- [ ] %s\n- [ ] %s\n- [%s] %s\n", previewRedeployLabel, previewTestLabel, live, previewLiveLabel)
	if trigger.TTL != "" && trigger.TTL != "0" {
		body += fmt.Sprintf("- [ ] %s\n", previewExtendLabel)
	}
	return body + "\nCheck an action to run it. Actions reset after acceptance. Live reload stays checked while enabled. Repository write access is required.\n"
}

// Only the known checkbox states are editable. Ignore changes to headings,
// markers, links or source text; none of them can select a workflow or command.
func previewPanelCheckboxes(body string) map[string]bool {
	states := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		for _, label := range []string{previewRedeployLabel, previewTestLabel, previewLiveLabel, previewExtendLabel} {
			if line == "- [ ] "+label {
				states[label] = false
			}
			if line == "- [x] "+label || line == "- [X] "+label {
				states[label] = true
			}
		}
	}
	return states
}

func normalizedPreviewPanel(body string) string {
	for _, label := range []string{previewRedeployLabel, previewTestLabel, previewLiveLabel, previewExtendLabel} {
		body = strings.ReplaceAll(body, "- [x] "+label, "- [ ] "+label)
		body = strings.ReplaceAll(body, "- [X] "+label, "- [ ] "+label)
	}
	return strings.ReplaceAll(body, "\r\n", "\n")
}

func previewPanelActions(before, after string) []string {
	if normalizedPreviewPanel(before) != normalizedPreviewPanel(after) {
		return nil
	}
	old, next := previewPanelCheckboxes(before), previewPanelCheckboxes(after)
	actions := []string{}
	if next[previewLiveLabel] != old[previewLiveLabel] {
		action := "live off"
		if next[previewLiveLabel] {
			action = "live on"
		}
		actions = append(actions, action)
	}
	if next[previewExtendLabel] && !old[previewExtendLabel] {
		actions = append(actions, "extend 1d")
	}
	if next[previewRedeployLabel] && !old[previewRedeployLabel] {
		actions = append(actions, "")
	}
	if next[previewTestLabel] && !old[previewTestLabel] {
		actions = append(actions, "test")
	}
	return actions
}

type previewPanelRuns struct {
	Latest         core.WorkflowRevision
	Stages         []core.WorkflowStageRun
	Deployed       core.WorkflowRevision
	DeployedStages []core.WorkflowStageRun
	Checks         core.WorkflowRevision
}

func previewPanelBody(panel core.WorkflowPreviewPanel, template core.WorkflowPreviewTemplate, trigger *core.WorkflowPreviewTrigger, resource core.WorkflowResource, runs previewPanelRuns, webURL string, linked bool) string {
	revision, stages := runs.Latest, runs.Stages
	marker := fmt.Sprintf("<!-- dispatch-preview-panel:%s -->\n", panel.ID)
	if panel.ActionError != "" {
		marker += "\n" + panel.ActionError + "\n\n"
	}
	if trigger == nil {
		t := core.WorkflowPreviewTrigger{Command: template.Command, Repository: panel.Repository, TTL: template.TTL}
		return marker + "### Preview\n\nAvailable. No preview is deployed.\n" + "\n- [ ] " + previewRedeployLabel + "\n\nRepository write access is required. Check to deploy, or post `" + t.Command + "`.\n" + workflowPreviewCommandHelp(revision, t)
	}
	if !linked {
		return marker + "### Preview\n\nThis PR is no longer linked to this preview. Post `" + trigger.Command + "` to create its own preview.\n"
	}
	if previewRemoved(resource) {
		return marker + "### " + resource.Name + " preview\n\nRemoved. The preview deployment has been cleaned up.\n\nPost `" + trigger.Command + "` on an open PR to create a new preview.\n"
	}
	status := "Not deployed"
	if resource.State == "expired" || resource.State == "expiring" {
		status = "Stopped. Preview lifetime ended"
	} else if resource.State == "paused" {
		status = "Paused"
	} else if revision.State != "" {
		status = revision.State
		for _, stage := range stages {
			if stage.State != "succeeded" && stage.State != "skipped" {
				status = stage.StageName + ": " + stage.State
				break
			}
		}
	}
	body := marker + "### " + resource.Name + " preview\n\nStatus: " + status + "\n"
	if trigger.PreviewURL != "" && resource.State != "expired" && resource.State != "expiring" && runs.Deployed.State == "succeeded" {
		body += "\n[Open preview](" + trigger.PreviewURL + ")\n"
	}
	body += "\nLifetime: " + workflowPreviewLifetimeText(*trigger) + "\n"
	body += previewPanelControls(*trigger)
	if revision.Error != "" {
		body += "\nThe latest run failed. Open the workflow run in Dispatch for the error and logs.\n"
	}
	if runs.Deployed.ID != "" {
		body += "\n<details>\n<summary>Deployment details</summary>\n\n" + workflowPreviewReportForTrigger(runs.Deployed, resource, runs.DeployedStages, trigger.PreviewURL, webURL, *trigger) + "\n</details>\n"
	}
	if revision.ID != "" && revision.ID != runs.Deployed.ID {
		body += "\n<details>\n<summary>Current run</summary>\n\n" + workflowPreviewReportForTrigger(revision, resource, stages, "", webURL, *trigger) + "\n</details>\n"
	} else if runs.Deployed.ID == "" {
		body += workflowPreviewCommandHelp(revision, *trigger)
	}
	if runs.Checks.ID != "" {
		body += "\nLatest checks: " + runs.Checks.State + ". Open the workflow run in Dispatch for results and logs.\n"
	}
	return body
}
