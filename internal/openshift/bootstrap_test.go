package openshift

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseLoginCommandAcceptsTokenAndRejectsInteractiveOrOverrides(t *testing.T) {
	login, err := ParseLoginCommand(`$ oc login --token='short-token' --server=https://api.example.test:6443 --insecure-skip-tls-verify=true`)
	if err != nil {
		t.Fatal(err)
	}
	if login.Token != "short-token" || login.Server != "https://api.example.test:6443" || !login.InsecureSkipTLSVerify {
		t.Fatalf("unexpected parsed login: %#v", login)
	}
	for _, command := range []string{
		"kubectl login --token=x --server=https://api.example.test",
		"oc get pods",
		"oc login https://api.example.test",
		"oc login --token=x --server=https://api.example.test --kubeconfig=/tmp/owned",
		"oc login --token=x --server=http://api.example.test",
	} {
		if _, err := ParseLoginCommand(command); !errors.Is(err, ErrInvalidLoginCommand) {
			t.Fatalf("expected %q to be rejected, got %v", command, err)
		}
	}
}

func TestBootstrapCreatesManagedClusterAdminKubeconfigWithoutOCBinary(t *testing.T) {
	var ca []byte
	serviceToken := "managed-service-account-token"
	bootstrapToken := "temporary-login-token"
	var mu sync.Mutex
	created := map[string]bool{}
	createdSecrets := map[string]bool{}
	secretNames := []string{TokenSecretPrefix + "one", TokenSecretPrefix + "two"}
	secretIndex := 0
	deletedTokens := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			if r.Header.Get("Authorization") != "Bearer "+serviceToken {
				t.Errorf("managed verification used the wrong identity")
			}
			writeTestJSON(w, http.StatusCreated, map[string]any{"status": map[string]any{"allowed": true}})
			return
		}
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/secrets/") {
			if r.Header.Get("Authorization") != "Bearer "+serviceToken {
				t.Errorf("token cleanup used the wrong identity")
			}
		} else if r.Header.Get("Authorization") != "Bearer "+bootstrapToken {
			t.Errorf("bootstrap request used the wrong identity for %s", r.URL.Path)
		}
		if r.URL.Path == "/apis/user.openshift.io/v1/users/~" {
			writeTestJSON(w, http.StatusOK, map[string]any{"metadata": map[string]any{"name": "admin"}})
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodGet {
			if strings.Contains(r.URL.Path, "/secrets/") {
				name := lastPathSegment(r.URL.Path)
				if createdSecrets[name] {
					writeTestJSON(w, http.StatusOK, map[string]any{"data": map[string]string{
						"token":  base64.StdEncoding.EncodeToString([]byte(serviceToken)),
						"ca.crt": base64.StdEncoding.EncodeToString(ca),
					}})
					return
				}
				writeTestJSON(w, http.StatusNotFound, map[string]any{"reason": "NotFound"})
				return
			}
			key := resourceKey(r.URL.Path)
			if created[key] {
				if key == "binding" {
					writeTestJSON(w, http.StatusOK, managedBinding())
					return
				}
				writeTestJSON(w, http.StatusOK, map[string]any{"metadata": map[string]any{"name": key}})
				return
			}
			writeTestJSON(w, http.StatusNotFound, map[string]any{"reason": "NotFound"})
			return
		}
		if r.Method == http.MethodPost {
			key := collectionKey(r.URL.Path)
			created[key] = true
			if key == "secret" {
				var body struct {
					Metadata struct {
						Name string `json:"name"`
					} `json:"metadata"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				createdSecrets[body.Metadata.Name] = true
			}
			writeTestJSON(w, http.StatusCreated, map[string]any{"metadata": map[string]any{"name": key}})
			return
		}
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/secrets/") {
			createdSecrets[lastPathSegment(r.URL.Path)] = false
			deletedTokens++
			writeTestJSON(w, http.StatusOK, map[string]any{"status": "Success"})
			return
		}
		writeTestJSON(w, http.StatusMethodNotAllowed, nil)
	}))
	defer server.Close()
	ca = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})

	bootstrap := &Bootstrapper{
		now:   func() time.Time { return time.Date(2026, 8, 6, 3, 0, 0, 0, time.UTC) },
		sleep: func(context.Context, time.Duration) error { return nil },
		newTokenSecret: func() (string, error) {
			name := secretNames[secretIndex]
			secretIndex++
			return name, nil
		},
	}
	result, err := bootstrap.Bootstrap(context.Background(), "oc login --token="+bootstrapToken+" --server="+server.URL+" --insecure-skip-tls-verify=true")
	if err != nil {
		t.Fatal(err)
	}
	if result.Context != ManagedContext || result.Server != server.URL || result.ConnectedAt.IsZero() {
		t.Fatalf("unexpected bootstrap result: %#v", result)
	}
	if result.TokenSecret != secretNames[0] {
		t.Fatalf("unexpected managed token Secret: %q", result.TokenSecret)
	}
	if strings.Contains(string(result.Kubeconfig), bootstrapToken) || !strings.Contains(string(result.Kubeconfig), serviceToken) {
		t.Fatal("managed kubeconfig did not replace the bootstrap identity")
	}
	for _, key := range []string{"namespace", "serviceaccount", "binding", "secret"} {
		if !created[key] {
			t.Fatalf("managed resource %q was not created", key)
		}
	}
	repair, err := bootstrap.Repair(context.Background(), "oc login --token="+bootstrapToken+" --server="+server.URL+" --insecure-skip-tls-verify=true")
	if err != nil {
		t.Fatal(err)
	}
	if repair.TokenSecret != secretNames[1] || !createdSecrets[secretNames[0]] || !createdSecrets[secretNames[1]] {
		t.Fatalf("repair did not create a verified replacement before cleanup: result=%q secrets=%v", repair.TokenSecret, createdSecrets)
	}
	if err := bootstrap.DeletePreviousToken(context.Background(), repair, result.TokenSecret); err != nil {
		t.Fatal(err)
	}
	if deletedTokens != 1 || createdSecrets[secretNames[0]] || !createdSecrets[secretNames[1]] {
		t.Fatalf("repair did not rotate the managed token: deleted=%d secrets=%v", deletedTokens, createdSecrets)
	}
}

func TestUsernamePasswordLoginUsesOpenShiftOAuthChallenge(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			writeTestJSON(w, http.StatusOK, map[string]string{"authorization_endpoint": server.URL + "/oauth/authorize"})
		case "/oauth/authorize":
			username, password, ok := r.BasicAuth()
			if !ok || username != "operator" || password != "password" || r.Header.Get("X-CSRF-Token") != "1" {
				t.Fatal("OAuth request did not carry the supplied challenge credentials")
			}
			w.Header().Set("Location", server.URL+"/#access_token=temporary-token&token_type=Bearer")
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	login, err := ParseLoginCommand("oc login -u operator -p password --server=" + server.URL + " --insecure-skip-tls-verify=true")
	if err != nil {
		t.Fatal(err)
	}
	token, err := obtainOAuthToken(context.Background(), newClient(true, nil), login)
	if err != nil || token != "temporary-token" {
		t.Fatalf("unexpected OAuth token result: token=%q err=%v", token, err)
	}
}

func resourceKey(path string) string {
	switch {
	case strings.Contains(path, "/clusterrolebindings/"):
		return "binding"
	case strings.Contains(path, "/serviceaccounts/"):
		return "serviceaccount"
	case strings.Contains(path, "/secrets/"):
		return "secret"
	default:
		return "namespace"
	}
}

func collectionKey(path string) string {
	switch {
	case strings.HasSuffix(path, "/clusterrolebindings"):
		return "binding"
	case strings.HasSuffix(path, "/serviceaccounts"):
		return "serviceaccount"
	case strings.HasSuffix(path, "/secrets"):
		return "secret"
	default:
		return "namespace"
	}
}

func lastPathSegment(value string) string {
	return value[strings.LastIndexByte(value, '/')+1:]
}

func writeTestJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
