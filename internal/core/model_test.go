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
