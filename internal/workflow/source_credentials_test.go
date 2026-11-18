package workflow

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/store"
)

func TestWorkflowSourcesUseTheirSelectedGitHubApps(t *testing.T) {
	ctx := t.Context()
	data, config, resource := workflowRunnerFixture(t, "mixed-apps")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	chartRepo := t.TempDir()
	gitOutput(t, chartRepo, "init", "-b", "helm-resources")
	must(os.Mkdir(filepath.Join(chartRepo, "chart"), 0700))
	must(os.WriteFile(filepath.Join(chartRepo, "chart", "Chart.yaml"), []byte("apiVersion: v2\nname: fixture\nversion: 1.0.0\n"), 0600))
	must(os.WriteFile(filepath.Join(chartRepo, "values.yaml"), []byte("replicaCount: 3\n"), 0600))
	gitOutput(t, chartRepo, "add", ".")
	gitOutput(t, chartRepo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Add chart")
	chartSHA := gitOutput(t, chartRepo, "rev-parse", "HEAD")
	serviceSHA := strings.Repeat("b", 40)
	chartURL := "https://github.example.com/example/gitops.git"
	gitConfig := filepath.Join(t.TempDir(), "gitconfig")
	gitOutput(t, chartRepo, "config", "--file", gitConfig, "url.file://"+chartRepo+".insteadOf", chartURL)
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		appID, installation, commit := 1, 2, serviceSHA
		if connection == "charts-reader" {
			appID, installation, commit = 11, 12, chartSHA
		}
		if strings.HasPrefix(path, "repos/") && (strings.HasPrefix(path, "repos/example/gitops/") != (connection == "charts-reader")) {
			t.Errorf("wrong GitHub App for %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		respond := func(body map[string]any) { _ = json.NewEncoder(w).Encode(body) }
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/installation"):
			respond(map[string]any{"id": installation, "app_id": appID})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/access_tokens"):
			if connection == "charts-reader" {
				var request struct{ Permissions map[string]string }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Permissions["contents"] != "read" {
					t.Error("checkout did not request a read-only token")
				}
			}
			respond(map[string]any{"token": connection + "-fixture", "expires_at": time.Now().Add(time.Hour)})
		case r.Method == http.MethodGet && strings.Contains(path, "/commits/"):
			if r.Header.Get("Authorization") != "Bearer "+connection+"-fixture" {
				t.Errorf("wrong token for %s", r.URL.Path)
			}
			respond(map[string]any{"sha": commit})
		default:
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(github.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	must(err)
	appKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	masterKey := filepath.Join(t.TempDir(), "key")
	must(os.WriteFile(masterKey, []byte("0123456789abcdef0123456789abcde!"), 0600))
	vault, err := secretcrypto.OpenFile(masterKey)
	must(err)
	manager := githubapp.New(data, vault)
	defaultApp, err := data.GetGitHubApp(ctx, config.GitHubAppID)
	must(err)
	defaultApp.Name, defaultApp.APIURL = "dispatch-dev", github.URL+"/dispatch-dev"
	defaultApp.EncryptedPrivateKey, defaultApp.EncryptedWebhookSecret, err = manager.EncryptCredentials(defaultApp.ID, appKey, "fixture-webhook-secret")
	must(err)
	must(data.UpdateGitHubApp(ctx, defaultApp))
	chartApp := defaultApp
	chartApp.ID, chartApp.Name, chartApp.APIURL = "chart-reader", "charts-reader", github.URL+"/charts-reader"
	chartApp.AppID, chartApp.InstallationID, chartApp.ReadOnly = 11, 12, true
	chartApp.EncryptedPrivateKey, chartApp.EncryptedWebhookSecret, err = manager.EncryptCredentials(chartApp.ID, appKey, "fixture-webhook-secret")
	must(err)
	must(data.CreateGitHubApp(ctx, chartApp))
	service := NewService(data, manager, nil, nil, nil, t.TempDir())
	resource.Document = `apiVersion: dispatch/v1alpha1
kind: Application
metadata: {name: mixed-apps}
spec:
  sources:
    service: {repository: example/service, branch: develop}
    ui: {repository: example/ui, branch: develop}
    preview-config: {repository: example/config}
    gitops:
      repository: example/gitops
      ref: helm-resources
      connection: charts-reader
  deployments:
    release:
      helm:
        sourceRef: gitops
        chartPath: chart
        valuesFiles: [{sourceRef: gitops, path: values.yaml}]
  stages:
    - name: development
      targetRef: test
      deploy: [release]
`
	snapshot, err := service.resolveResourceSources(ctx, config, resource)
	must(err)
	if snapshot["gitops"].GitHubAppID != chartApp.ID || snapshot["gitops"].CloneURL != chartURL ||
		snapshot["gitops"].CommitSHA != chartSHA || len(snapshot["gitops"].ContentHashes) != 2 {
		t.Fatalf("chart override and content inputs were not captured: %+v", snapshot["gitops"])
	}
	for _, alias := range []string{"service", "ui", "preview-config"} {
		if snapshot[alias].GitHubAppID != "" || snapshot[alias].CommitSHA != serviceSHA {
			t.Fatalf("%s changed its default connection: %+v", alias, snapshot[alias])
		}
	}
	worker, err := service.workerSource(ctx, config, snapshot["gitops"])
	must(err)
	if worker.URL != chartURL || worker.Credential != "charts-reader-fixture" || worker.AuthType != deploy.SourceAuthGitHubApp {
		t.Fatal("worker did not receive the selected App's clone credential")
	}
	access, err := service.workflowRepositoryAccess(ctx, config, snapshot["gitops"])
	must(err)
	defer access.cleanup()
	if access.credentialID != "github-app:"+chartApp.ID ||
		!slices.Contains(access.environment, "GIT_CONFIG_VALUE_0=Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:charts-reader-fixture"))) {
		t.Fatal("controller checkout did not receive the selected App's clone credential")
	}
	documents, err := Parse(resource.Path, []byte(resource.Document))
	must(err)
	workflow := documents[0].Spec
	revision := core.WorkflowRevision{ID: "mixed-run", ResourceID: resource.ID, Sources: snapshot}
	prepared, err := service.prepareHelmDeployment(ctx, resource, config, revision, workflow.Stages[0], "release", workflow.Deployments["release"], core.Server{ID: "test"})
	must(err)
	if prepared.app.SourceCredentialID != chartApp.ID || prepared.app.SourceRepo != chartURL ||
		!strings.Contains(prepared.app.HelmValues, "replicaCount: 3") {
		t.Fatal("Helm did not use the pinned chart credential and values")
	}
	revision.PullRequests = []core.WorkflowPullRequest{{GitHubAppID: defaultApp.ID, Repository: "example/service", Number: 42, CommitSHA: serviceSHA}}
	reports, err := service.workflowCheckReports(ctx, resource, config, revision, false)
	must(err)
	if len(reports) == 0 {
		t.Fatal("PR reporting was lost")
	}
	for _, report := range reports {
		if report.GitHubAppID != defaultApp.ID || report.Repository == "example/gitops" {
			t.Fatalf("checkout override affected GitHub reporting: %+v", report)
		}
	}
	missing := resource
	missing.Document = strings.Replace(resource.Document, "connection: charts-reader", "connection: missing-connection", 1)
	if _, err := service.resolveResourceSources(ctx, config, missing); err == nil || !strings.Contains(err.Error(), "missing-connection") {
		t.Fatalf("missing override fell back to the default: %v", err)
	}

	resource.Temporary = true
	otherConfig := config
	otherConfig.GitHubAppID, otherConfig.RepositoryID = chartApp.ID, 15
	trigger := core.WorkflowPreviewTrigger{ID: "preview", ResourceID: resource.ID, GitHubAppID: defaultApp.ID, Repository: "example/service",
		PullRequestNumber: 42, Command: "/preview", TemplateSource: &core.WorkflowPreviewTemplateGitSource{Repository: "example/config"}, CreatedAt: time.Now().UTC()}
	for _, previewCtx := range []struct {
		name  string
		saved bool
	}{{name: "during creation"}, {name: "after saving", saved: true}} {
		t.Run(previewCtx.name, func(t *testing.T) {
			previewContext := store.WithWorkflowPreviewTrigger(ctx, trigger)
			if previewCtx.saved {
				if err := data.CreateWorkflowPreviewTrigger(ctx, trigger); err != nil {
					t.Fatal(err)
				}
				previewContext = ctx
			}
			preview, err := service.resolveResourceSources(previewContext, otherConfig, resource)
			if err != nil {
				t.Fatal(err)
			}
			if preview["service"].GitHubAppID != defaultApp.ID || preview["gitops"].GitHubAppID != chartApp.ID {
				t.Fatalf("preview did not inherit the template's clone connection: %+v", preview)
			}
		})
	}
}

func TestGitHubAppSourceURLRejectsOtherHostsAndCredentials(t *testing.T) {
	connection := core.GitHubAppConnection{Name: "chart-reader", WebURL: "https://github.example.com"}
	for _, repository := range []string{
		"https://other.example.com/team/chart.git",
		"https://user@github.example.com/team/chart.git",
		"https://github.example.com/team/chart.git?token=fixture",
		"git@github.example.com:team/chart.git",
	} {
		if _, err := githubAppSourceURL(connection, repository); err == nil {
			t.Errorf("accepted unsafe clone URL %q", repository)
		}
	}
}
