package workflow

import (
	"testing"
	"time"
)

func TestPreviewLifetimeYAML(t *testing.T) {
	for _, value := range []string{"0", "1d", "24h", "30m"} {
		document := "apiVersion: dispatch/v1alpha1\nkind: WorkflowTemplate\nspec:\n  sources:\n    service:\n      repository: example/service\n  triggers:\n    pullRequestComment:\n      sources: [service]\n      ttl: " + value + "\n"
		trigger, _, err := ReadWorkflowTemplateTrigger([]byte(document))
		if err != nil || trigger.TTL != value {
			t.Fatalf("ttl %s: %+v, %v", value, trigger, err)
		}
	}
	for _, value := range []string{"-1d", "0d", "-24h", "garbage", "1ms", "1.5d", "99999999999999999d", "0h"} {
		if _, err := ParsePreviewTTL(value); err == nil {
			t.Errorf("accepted invalid lifetime %q", value)
		}
	}
	if duration, err := ParsePreviewTTL("1d"); err != nil || duration != 24*time.Hour {
		t.Fatalf("one day: %s, %v", duration, err)
	}
	if duration, err := ParsePreviewTTL(""); err != nil || duration != 0 {
		t.Fatalf("missing lifetime should be unlimited: %s, %v", duration, err)
	}
}
