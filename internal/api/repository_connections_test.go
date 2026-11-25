package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func TestRepositoryConnectionSwitchesCredentialByStableProjectName(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := t.Context()
	now := time.Now().UTC()
	for _, project := range []string{"slots", "second-project"} {
		if err := a.store.CreateProject(ctx, core.Project{ID: project, Name: project, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	rawToken := "token-supplied-only-to-the-variable-form"
	var secret core.Secret
	response := serviceRequestTest(t, a, "POST", "/api/v1/secrets", map[string]string{"name": "PAT", "environmentVariable": "SLOT_TOKEN", "type": "github_token", "value": rawToken}, 201)
	if err := json.Unmarshal(response, &secret); err != nil {
		t.Fatal(err)
	}
	app := core.GitHubAppConnection{ID: "installed", Name: "Installed app", AppID: 7, InstallationID: 17, State: "ready",
		WebURL: "https://github.example.test", APIURL: "https://github.example.test/api/v3", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateGitHubApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	const endpoint = "/api/v1/repository-connections"
	input := map[string]string{"projectId": "slots", "name": "gitops-read", "repositoryUrl": "https://GitHub.Example.Test/Org/Repo", "credentialSecretId": secret.ID}
	var binding core.RepositoryConnection
	res := serviceRequestTest(t, a, "POST", endpoint, input, 201)
	if err := json.Unmarshal(res, &binding); err != nil || binding.ID == "" || binding.RepositoryURL != "https://github.example.test/org/repo.git" || binding.CredentialSecretID != secret.ID || strings.Contains(string(res), rawToken) {
		t.Fatalf("invalid binding response: %v, %s", err, res)
	}
	if usage := serviceRequestTest(t, a, "GET", "/api/v1/secrets/usage", nil, 200); !strings.Contains(string(usage), `"repository_connection"`) {
		t.Fatalf("missing PAT consumer: %s", usage)
	}
	path := endpoint + "/" + binding.ID
	serviceRequestTest(t, a, "POST", endpoint, input, 409)
	input["projectId"] = "second-project"
	serviceRequestTest(t, a, "POST", endpoint, input, 201)
	input["name"] = app.Name
	serviceRequestTest(t, a, "POST", endpoint, input, 409)
	otherApp := app
	otherApp.ID, otherApp.AppID, otherApp.Name = "other", 8, binding.Name
	if err := a.store.CreateGitHubApp(ctx, otherApp); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("legacy App sharing a workflow name: %v", err)
	}
	for _, bad := range []map[string]string{
		{"name": "renamed", "githubAppId": app.ID},
		{"projectId": "second-project", "githubAppId": app.ID},
		{"repositoryUrl": "https://other.test/org/repo", "credentialSecretId": secret.ID},
		{"githubAppId": app.ID, "credentialSecretId": secret.ID},
		{"token": rawToken},
	} {
		serviceRequestTest(t, a, "PUT", path, bad, 400)
	}
	input["name"], input["repositoryUrl"], input["githubAppId"] = "other-host", "https://github.com/org/repo", app.ID
	delete(input, "credentialSecretId")
	serviceRequestTest(t, a, "POST", endpoint, input, 400)
	serviceRequestTest(t, a, "PUT", path, map[string]string{"githubAppId": app.ID}, 200)
	got, err := a.store.GetRepositoryConnectionByName(ctx, "slots", "gitops-read")
	if err != nil || got.ID != binding.ID || got.CredentialSecretID != "" || got.GitHubAppID != app.ID {
		t.Fatalf("switch by name: %+v, %v", got, err)
	}
	if _, err := a.store.GetRepositoryConnectionByName(ctx, "another-project", binding.Name); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-project lookup: %v", err)
	}
	if contents := serviceRequestTest(t, a, "GET", endpoint, nil, 200); strings.Contains(string(contents), rawToken) {
		t.Fatal("listed connection exposed token")
	}
}
