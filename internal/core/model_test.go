package core

import "testing"

func TestAppSpecDigestIsStableAndSensitive(t *testing.T) {
	app := App{
		ServerID: "server-1", SourceRepo: "https://example.test/app.git", Branch: "main",
		BuildType: BuildTypeDockerfile, ContextPath: ".", DockerfilePath: "Dockerfile",
		ContainerPort: 8080, Domain: "app.example.test",
	}
	first := app.SpecDigest()
	if second := app.SpecDigest(); first != second {
		t.Fatalf("digest changed without a spec change: %q != %q", first, second)
	}
	app.Branch = "release"
	if changed := app.SpecDigest(); first == changed {
		t.Fatal("digest did not change with the application specification")
	}
	app.ComposeContent = "services:\n  app:\n    image: example/app:latest"
	if changed := app.SpecDigest(); first == changed {
		t.Fatal("digest did not change with pasted Compose content")
	}
	app.HelmValues = "image:\n  tag: preview"
	if changed := app.SpecDigest(); first == changed {
		t.Fatal("digest did not change with Helm values")
	}
	beforeHook := app.SpecDigest()
	app.PreDeployHook = "docker build ."
	if changed := app.SpecDigest(); beforeHook == changed {
		t.Fatal("digest did not change with a deployment hook")
	}
}

func TestDeploymentTerminalStates(t *testing.T) {
	for _, state := range []DeploymentState{DeploymentSucceeded, DeploymentFailed, DeploymentCancelled} {
		if !state.Terminal() {
			t.Fatalf("expected %q to be terminal", state)
		}
	}
	if DeploymentBuilding.Terminal() {
		t.Fatal("building must remain active")
	}
}

func TestIncomingEventHookEnvironmentUsesNormalizedEventVariables(t *testing.T) {
	event := IncomingEvent{Provider: EventProviderGitHub, Kind: EventKindPullRequestComment, Action: "created", Repository: "acme/app",
		PullRequestNumber: 42, HeadRef: "feature/cart", HeadSHA: "abc123", BaseRef: "main", Actor: "octo",
		Command: "/preview", Arguments: "with ui=#8", DeliveryID: "delivery-1", SourceCommentID: "501"}
	values := event.HookEnvironment()
	for name, expected := range map[string]string{
		"DISPATCH_EVENT_REPOSITORY": "acme/app", "DISPATCH_EVENT_PULL_REQUEST_NUMBER": "42",
		"DISPATCH_EVENT_HEAD_REF": "feature/cart", "DISPATCH_EVENT_COMMAND": "/preview",
		"DISPATCH_EVENT_ARGUMENTS": "with ui=#8", "DISPATCH_EVENT_ACTOR": "octo",
	} {
		if values[name] != expected {
			t.Fatalf("expected %s=%q, got %#v", name, expected, values)
		}
	}
	if _, found := values["DISPATCH_EVENT_SOURCE_COMMENT_ID"]; !found {
		t.Fatalf("expected source comment ID in %#v", values)
	}
}
