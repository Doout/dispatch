package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type logTransport func(*http.Request) (*http.Response, error)

func (f logTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func logResponse(value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(data)))}
}

func signedLogToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test"})
	payload, _ := json.Marshal(claims)
	value := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(value))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return value + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func TestLogWorkflowIdentityBindsIssuerRepositoryBranchAndRun(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"https://api.github.com", "https://github.example.test/api/v3"} {
		t.Run(host, func(t *testing.T) {
			issuer := "https://token.actions.githubusercontent.com"
			if strings.Contains(host, "example.test") {
				issuer = "https://github.example.test/_services/token"
			}
			connection := core.GitHubAppConnection{ID: "app", AppID: 42, APIURL: host}
			manager := New(testStore{connection: connection}, nil)
			discoveries := 0
			manager.Client = &http.Client{Transport: logTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "" {
					t.Fatal("App credential sent to identity provider")
				}
				switch r.URL.String() {
				case issuer + "/.well-known/openid-configuration":
					discoveries++
					return logResponse(map[string]any{"issuer": issuer, "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}}), nil
				case issuer + "/keys":
					return logResponse(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "test", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}}), nil
				default:
					t.Fatalf("unexpected discovery URL %s", r.URL)
					return nil, errors.New("unexpected")
				}
			})}
			report := core.WorkflowCheckReport{ID: "report", GitHubAppID: "app", AppID: 42, APIURL: host, Repository: "example/repo", Logs: &core.GitHubLogWorkflow{Workflow: "doodship-logs.yml", Ref: "main"}, LogRunID: 73}
			claims := func() map[string]any {
				return map[string]any{"iss": issuer, "sub": "repo:Example/Repo:ref:refs/heads/main", "aud": "doodship:check:report", "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(), "repository": "Example/Repo", "workflow_ref": "Example/Repo/.github/workflows/doodship-logs.yml@refs/heads/main", "event_name": "workflow_dispatch", "run_id": "73"}
			}
			if err := manager.VerifyLogWorkflowToken(context.Background(), report, signedLogToken(t, key, claims())); err != nil {
				t.Fatal(err)
			}
			for _, change := range []struct {
				field string
				value any
			}{
				{"iss", "https://evil.test"}, {"aud", "doodship:check:other"}, {"repository", "other/repo"}, {"run_id", "74"}, {"event_name", "pull_request"},
				{"workflow_ref", "Example/Repo/.github/workflows/doodship-logs.yml@refs/heads/Main"}, {"workflow_ref", "Example/Repo/.github/workflows/untrusted.yml@refs/heads/main"},
				{"exp", time.Now().Add(-time.Minute).Unix()}, {"iat", time.Now().Add(2 * time.Minute).Unix()},
			} {
				c := claims()
				c[change.field] = change.value
				if err := manager.VerifyLogWorkflowToken(context.Background(), report, signedLogToken(t, key, c)); err == nil {
					t.Fatalf("accepted mismatched %s", change.field)
				}
			}
			bad := signedLogToken(t, key, claims())
			bad = strings.Replace(bad, ".", ".tampered", 1)
			if err := manager.VerifyLogWorkflowToken(context.Background(), report, bad); err == nil {
				t.Fatal("accepted tampered signature")
			}
			if discoveries != 1 {
				t.Fatalf("identity provider not cached: %d", discoveries)
			}
			report.AppID++
			if err := manager.VerifyLogWorkflowToken(context.Background(), report, signedLogToken(t, key, claims())); err == nil {
				t.Fatal("accepted changed App")
			}
		})
	}
}

func TestLogWorkflowDispatchAndRecoveryUseCapturedIdentity(t *testing.T) {
	connection := core.GitHubAppConnection{ID: "app", AppID: 42, APIURL: "https://api.github.com"}
	manager := New(testStore{connection: connection}, nil)
	manager.repositories = map[string]repositoryInstallation{"app:example/repo": {ID: 73, ExpiresAt: time.Now().Add(time.Hour)}}
	manager.tokens[tokenCacheKey("app", 73)] = cachedToken{value: "installation-secret", expiresAt: time.Now().Add(time.Hour)}
	report := core.WorkflowCheckReport{ID: "report", GitHubAppID: "app", AppID: 42, APIURL: connection.APIURL, Repository: "example/repo", CommitSHA: strings.Repeat("a", 40), ExternalID: "check:report", Name: "Dispatch/deployment/app", Logs: &core.GitHubLogWorkflow{Workflow: "doodship-logs.yml", Ref: "main"}}
	posts := 0
	failure := 0
	manager.Client = &http.Client{Transport: logTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer installation-secret" {
			t.Fatal("unscoped token")
		}
		if !strings.HasPrefix(r.URL.Path, "/repos/example/repo/actions/workflows/doodship-logs.yml/") {
			t.Fatalf("unexpected endpoint %s", r.URL)
		}
		if r.Method == http.MethodPost {
			posts++
			var input struct {
				Ref    string
				Inputs map[string]string
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Ref != "main" || len(input.Inputs) != 1 || input.Inputs["report_id"] != "report" {
				t.Fatalf("unbound dispatch: %+v", input)
			}
			if failure != 0 {
				return &http.Response{StatusCode: failure, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"message":"password=upstream-secret"}`))}, nil
			}
			return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return logResponse(map[string]any{"workflow_runs": []LogWorkflowRun{{ID: 99, Title: "Doodship report", Event: "workflow_dispatch", Path: ".github/workflows/doodship-logs.yml@refs/heads/main", Branch: "main", URL: "https://github.com/example/repo/actions/runs/99"}, {ID: 100, Title: "forged", Event: "workflow_dispatch", Branch: "main"}}}), nil
	})}
	if err := manager.DispatchLogWorkflow(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	run, err := manager.FindLogWorkflow(context.Background(), report)
	if err != nil || run.ID != 99 {
		t.Fatalf("lost dispatch result: %+v %v", run, err)
	}
	for _, status := range []int{403, 502} {
		manager.repositories = map[string]repositoryInstallation{"app:example/repo": {ID: 73, ExpiresAt: time.Now().Add(time.Hour)}}
		manager.tokens[tokenCacheKey("app", 73)] = cachedToken{value: "installation-secret", expiresAt: time.Now().Add(time.Hour)}
		failure = status
		err := manager.DispatchLogWorkflow(context.Background(), report)
		var typed *CheckRunError
		if !errors.As(err, &typed) || typed.CreateUncertain != (status == 502) || strings.Contains(fmt.Sprint(err), "upstream-secret") {
			t.Fatalf("unsafe dispatch failure: %v", err)
		}
	}
	if posts != 3 {
		t.Fatal(posts)
	}
	report.AppID++
	if err := manager.DispatchLogWorkflow(context.Background(), report); err == nil || posts != 3 {
		t.Fatal("published after identity changed")
	}
}
