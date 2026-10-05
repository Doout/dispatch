package events

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func previewValuesEvent(t *testing.T, message string) core.IncomingEvent {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"action": "created", "repository": map[string]any{"full_name": "Acme/Checkout"},
		"issue":   map[string]any{"number": 42, "pull_request": map[string]any{"url": "https://example.test/pulls/42"}},
		"comment": map[string]any{"id": 987, "body": message, "author_association": "MEMBER", "user": map[string]any{"login": "octo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := ParseGitHubEvent("issue_comment", "delivery-values", payload, time.Now())
	if err != nil {
		t.Fatalf("comment parsing must preserve the event for failure activity: %v", err)
	}
	return event
}

func TestPreviewValuesCommentCarriesLiteralHelmValues(t *testing.T) {
	event := previewValuesEvent(t, "/preview values server\n```yaml\nwxo_optimization:\n  configMap:\n    data:\n      AGENT_GATEWAY_URL: http://agent-gateway.archer-server.svc.cluster.local\n      LITERAL: '{{ jobs.build.outputs.image }}'\nreplicas: 2\nenabled: true\noptional: null\nports: [8080, 9090]\n```")
	if event.PreviewValuesError != "" || event.PreviewValues == nil {
		t.Fatalf("expected typed values, got %#v", event)
	}
	if event.Arguments != "" || event.PreviewValues.Deployment != "server" || event.PreviewValues.Clear {
		t.Fatalf("unexpected command: %#v", event)
	}
	expected := map[string]any{
		"wxo_optimization": map[string]any{"configMap": map[string]any{"data": map[string]any{
			"AGENT_GATEWAY_URL": "http://agent-gateway.archer-server.svc.cluster.local",
			"LITERAL":           "{{ jobs.build.outputs.image }}",
		}}},
		"replicas": 2, "enabled": true, "optional": nil, "ports": []any{8080, 9090},
	}
	if !reflect.DeepEqual(event.PreviewValues.Values, expected) {
		t.Fatalf("literal values changed: %#v", event.PreviewValues.Values)
	}
	if !event.TrustedActor || event.Command != "/preview" || event.SourceCommentID != "987" || event.Repository != "acme/checkout" {
		t.Fatalf("event metadata changed: %#v", event)
	}
}

func TestPreviewValuesCommentDeployAndClearGrammar(t *testing.T) {
	tests := []struct {
		name, message, arguments, deployment string
		clear                                bool
	}{
		{name: "explicit unqualified", message: "/preview values\n```yaml\nreplicas: 2\n```"},
		{name: "bare shorthand", message: "/preview\n```yaml\nreplicas: 2\n```"},
		{name: "linked shorthand", message: "/preview with ui=#84\n```yaml\nreplicas: 2\n```", arguments: "with ui=#84"},
		{name: "custom command CRLF and tilde", message: "/deploy values api-v2\r\n  ~~~YML\r\nreplicas: 2\r\n  ~~~", deployment: "api-v2"},
		{name: "wide backtick fence", message: "/preview values\n````yaml\nreplicas: 2\n````"},
		{name: "empty mapping", message: "/preview values\n```yml\n{}\n```"},
		{name: "deployment named clear", message: "/preview values clear\n```yaml\nreplicas: 2\n```", deployment: "clear"},
		{name: "clear all", message: "/preview values clear", clear: true},
		{name: "clear target", message: "/preview values api-v2 clear\n", deployment: "api-v2", clear: true},
		{name: "clear deployment named clear", message: "/preview values clear clear", deployment: "clear", clear: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := previewValuesEvent(t, test.message)
			if event.PreviewValuesError != "" || event.PreviewValues == nil || event.Arguments != test.arguments || event.PreviewValues.Deployment != test.deployment || event.PreviewValues.Clear != test.clear {
				t.Fatalf("unexpected values command: %#v", event)
			}
			if test.clear && event.PreviewValues.Values != nil {
				t.Fatalf("clear must not carry values: %#v", event.PreviewValues)
			}
		})
	}
}

func TestPreviewValuesCommentPreservesExistingPlainComments(t *testing.T) {
	for _, message := range []string{
		"/preview image.tag=abc\nignore this",
		"/preview\nordinary prose",
		"/preview test\nordinary prose",
		"/preview\n```python\nprint('hello')\n```",
		"/preview\n````python\n```yaml\nvalue: text\n```\n````",
		"ordinary prose\n```yaml\nreplicas: 2\n```",
	} {
		event := previewValuesEvent(t, message)
		command, arguments := ParseCommand(message)
		if event.Command != command || event.Arguments != arguments || event.PreviewValues != nil || event.PreviewValuesError != "" {
			t.Fatalf("ordinary comment changed: message=%q event=%#v", message, event)
		}
	}
}

func TestPreviewValuesCommentRejectsMalformedOverridesWithoutDroppingEvent(t *testing.T) {
	fence := func(value string) string { return "/preview values\n```yaml\n" + value + "\n```" }
	tests := map[string]string{
		"invalid YAML":          fence("private-marker: [broken"),
		"sequence root":         fence("[one, two]"),
		"scalar root":           fence("private-marker"),
		"null root":             fence("null"),
		"empty document":        fence(""),
		"multiple documents":    fence("replicas: 2\n---\nreplicas: 3"),
		"duplicate root":        fence("replicas: 2\nreplicas: 3"),
		"duplicate nested":      fence("config:\n  key: one\n  key: two"),
		"numeric key":           fence("1: private-marker"),
		"complex key":           fence("? [one, two]\n: private-marker"),
		"custom tag":            fence("config: !private-marker value"),
		"binary data":           fence("config: !!binary cHJpdmF0ZS1tYXJrZXI="),
		"timestamp":             fence("config: 2026-10-05"),
		"alias":                 fence("one: &value private-marker\ntwo: *value"),
		"merge key":             fence("config:\n  <<: {one: private-marker}"),
		"nan":                   fence("config: .nan"),
		"infinity":              fence("config: .inf"),
		"reserved pipeline":     fence("_pipeline: {name: private-marker}"),
		"no fence":              "/preview values\nreplicas: 2",
		"unclosed fence":        "/preview values\n```yaml\nreplicas: 2",
		"multiple fences":       fence("replicas: 2") + "\n```yml\nreplicas: 3\n```",
		"extra non YAML":        fence("replicas: 2") + "\n```python\npass\n```",
		"wrong fence info":      "/preview values\n```json\n{}\n```",
		"extra fence info":      "/preview values\n```yaml private-marker\nreplicas: 2\n```",
		"named clear with YAML": "/preview values clear clear\n```yaml\nreplicas: 2\n```",
		"clear with prose":      "/preview values clear\nprivate-marker",
		"invalid alias":         "/preview values Bad_Alias\n```yaml\nreplicas: 2\n```",
		"too many arguments":    "/preview values api extra\n```yaml\nreplicas: 2\n```",
		"too large":             fence("config: '" + strings.Repeat("x", maxPreviewCommentValuesBytes) + "'"),
	}
	for name, message := range tests {
		t.Run(name, func(t *testing.T) {
			event := previewValuesEvent(t, message)
			if event.PreviewValuesError == "" || event.PreviewValues != nil || event.Kind != core.EventKindPullRequestComment || event.SourceCommentID != "987" {
				t.Fatalf("expected a rejected override on the retained event: %#v", event)
			}
			if strings.Contains(event.PreviewValuesError, "private-marker") {
				t.Fatalf("validation error disclosed values: %q", event.PreviewValuesError)
			}
		})
	}
}

func TestPreviewValuesCommentRejectsLifecycleOverrides(t *testing.T) {
	for _, arguments := range []string{"test", "live", "ttl 2h", "extend 2h", "without ui"} {
		event := previewValuesEvent(t, "/preview "+arguments+"\n```yaml\nreplicas: 2\n```")
		if event.PreviewValuesError == "" || event.PreviewValues != nil || event.Arguments != arguments {
			t.Fatalf("lifecycle comment must not accept a values override: %#v", event)
		}
	}
}

func TestPreviewValuesCommentPayloadDoesNotEnterEventMetadata(t *testing.T) {
	event := previewValuesEvent(t, "/preview values\n```yaml\nconfig: private-marker\n```")
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-marker") || strings.Contains(string(encoded), "previewValues") {
		t.Fatalf("values disclosed in event JSON: %s", encoded)
	}
	for key, value := range event.HookEnvironment() {
		if strings.Contains(value, "private-marker") {
			t.Fatalf("values disclosed in hook metadata %s=%s", key, value)
		}
	}
	event.PreviewValuesError = "private-error-marker"
	encoded, err = json.Marshal(event)
	if err != nil || strings.Contains(string(encoded), "private-error-marker") {
		t.Fatalf("internal values error disclosed in event JSON: %s, %v", encoded, err)
	}
}

func TestLegacyPreviewServiceRejectsValuesBeforeAnyDeployment(t *testing.T) {
	for _, message := range []string{
		"/preview values\n```yaml\nreplicas: 2\n```",
		"/preview values clear",
		"/preview values\n```yaml\nreplicas: [broken\n```",
	} {
		event := previewValuesEvent(t, message)
		// No dependencies: a resolver, store, or deployment call would fail.
		result, err := New(nil, nil, nil, nil).Process(context.Background(), event)
		if err == nil || result.Event.DeliveryID != event.DeliveryID || result.Event.SourceCommentID != event.SourceCommentID {
			t.Fatalf("legacy preview must reject before processing: result=%#v err=%v", result, err)
		}
		if event.PreviewValuesError != "" && err.Error() != event.PreviewValuesError {
			t.Fatalf("legacy preview lost validation error: %v", err)
		}
	}
}

func TestLegacyPreviewServiceIgnoresUntrustedValuesWithoutDependencies(t *testing.T) {
	for _, message := range []string{
		"/preview values\n```yaml\nreplicas: 2\n```",
		"/preview values clear",
		"/preview values\n```yaml\nreplicas: [broken\n```",
	} {
		event := previewValuesEvent(t, message)
		event.TrustedActor = false
		result, err := New(nil, nil, nil, nil).Process(context.Background(), event)
		if err != nil || !result.Ignored || result.Event.DeliveryID != event.DeliveryID {
			t.Fatalf("untrusted values comment must be ignored before dependencies: result=%#v err=%v", result, err)
		}
	}
}
