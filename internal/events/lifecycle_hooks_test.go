package events

import (
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestPreviewApplicationUsesSnapshottedEventHooks(t *testing.T) {
	template := core.App{Name: "Checkout", HelmRelease: "checkout", PreDeployHook: "old pre", PostDeployHook: "old post"}
	preview := core.PreviewEnvironment{PullRequestNumber: 12, HeadRef: "feature/cart", PreDeployHook: "event pre", PostDeployHook: "event post",
		HookEnvironment: map[string]string{"DISPATCH_EVENT_REPOSITORY": "acme/checkout"}}
	instance := previewApplication(template, preview)
	if instance.PreDeployHook != "event pre" || instance.PostDeployHook != "event post" || instance.HookEnvironment["DISPATCH_EVENT_REPOSITORY"] != "acme/checkout" {
		t.Fatalf("preview did not use event hook snapshot: %#v", instance)
	}
}
