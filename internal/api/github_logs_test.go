package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runlog"
)

func TestGitHubLogEndpointScopesFullLogsToTheAcceptedWorkflow(t *testing.T) {
	f := newCheckFixture(t)
	ctx := context.Background()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_services/token/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/_services/token/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "key", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	issuer = provider.URL + "/_services/token"
	connection, err := f.a.store.GetGitHubApp(ctx, "check-app")
	if err != nil {
		t.Fatal(err)
	}
	connection.APIURL = provider.URL + "/api/v3"
	if err := f.a.store.UpdateGitHubApp(ctx, connection); err != nil {
		t.Fatal(err)
	}
	f.a.eventConfig.GitHubApps.Client = provider.Client()
	f.a.workflows.LogDirectory = filepath.Join(t.TempDir(), "logs")
	r := f.report
	r.ID = "full-report"
	r.RevisionID = "full-run"
	r.ExternalID = "check:full"
	r.APIURL = connection.APIURL
	r.GitHubLogs, r.LogRef = true, "main"
	r.LogRunID = 73
	build := r
	build.ID = "build-report"
	build.ExternalID = "check:build"
	build.Kind = "build"
	build.Job = "build"
	build.GitHubLogs = false
	build.LogRunID = 0
	foreign := build
	foreign.ID = "foreign-report"
	foreign.ExternalID = "check:foreign"
	foreign.Repository = "example/other"
	foreign.Job = "foreign"
	revision := core.WorkflowRevision{ID: r.RevisionID, ResourceID: r.ResourceID, State: "running", CreatedAt: time.Now().UTC(), Checks: []core.WorkflowCheckReport{r, build, foreign}}
	if err := f.a.store.CreateWorkflowRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	job := core.WorkflowJobResult{ID: "full-job", ResourceID: r.ResourceID, RevisionID: r.RevisionID, JobName: "build", State: "running", CreatedAt: time.Now().UTC()}
	if err := f.a.store.CreateWorkflowJobResult(ctx, job); err != nil {
		t.Fatal(err)
	}
	other := job
	other.ID = "other-job"
	other.JobName = "foreign"
	other.Log = "foreign private output"
	if err := f.a.store.CreateWorkflowJobResult(ctx, other); err != nil {
		t.Fatal(err)
	}
	archive, err := runlog.Open(f.a.workflows.LogDirectory, job.ID, "private-value")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1600; i++ {
		_, _ = archive.Write([]byte(strings.Repeat("building image ", 100) + "private-value\n"))
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "key"})
	claims, _ := json.Marshal(map[string]any{"iss": issuer, "aud": "doodship:check:" + r.ID, "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(), "repository": r.Repository, "workflow_ref": r.Repository + "/.github/workflows/doodship-logs.yml@refs/heads/main", "event_name": "workflow_dispatch", "run_id": "73"})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	token := unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
	get := func(query, credential string, want int) []byte {
		t.Helper()
		request := httptest.NewRequest("GET", "/api/v1/github-checks/"+r.ID+"/logs"+query, nil)
		if credential != "" {
			request.Header.Set("Authorization", "Bearer "+credential)
		}
		response := httptest.NewRecorder()
		f.a.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("log request: %d %s", response.Code, response.Body.String())
		}
		return response.Body.Bytes()
	}
	get("", "", 401)
	get("", "secret", 401)
	var index githubLogIndex
	if err := json.Unmarshal(get("", token, 200), &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Streams) != 1 || index.Streams[0].ID != "job-full-job" || index.Complete {
		t.Fatalf("foreign logs in index: %+v", index)
	}
	get("?stream=job-other-job&offset=0", token, 404)
	get("?stream=job-full-job&offset=-1", token, 400)
	var offset int64
	var full strings.Builder
	for {
		var page githubLogPage
		query := "?" + url.Values{"stream": {"job-full-job"}, "offset": {fmt.Sprint(offset)}}.Encode()
		if err := json.Unmarshal(get(query, token, 200), &page); err != nil {
			t.Fatal(err)
		}
		if page.Incomplete || page.Complete {
			t.Fatal("running build was marked incomplete or complete")
		}
		full.WriteString(page.Text)
		if page.Next == offset {
			break
		}
		offset = page.Next
	}
	if full.Len() < 2<<20 || strings.Contains(full.String(), "private-value") || strings.Contains(full.String(), "foreign private") {
		t.Fatal("full log missing or secret escaped")
	}
	now := time.Now().UTC()
	job.State = "succeeded"
	job.FinishedAt = &now
	revision.State = "succeeded"
	revision.FinishedAt = &now
	if err := f.a.store.UpdateWorkflowJobResult(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := f.a.store.UpdateWorkflowRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	var page githubLogPage
	_ = json.Unmarshal(get("?stream=job-full-job&offset="+fmt.Sprint(offset), token, 200), &page)
	if !page.Complete || page.Incomplete {
		t.Fatalf("final delivery not recognized: %+v", page)
	}
	past := now.Add(-25 * time.Hour)
	revision.FinishedAt = &past
	if err := f.a.store.UpdateWorkflowRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	get("", token, 410)
}
