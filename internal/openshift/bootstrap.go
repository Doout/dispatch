package openshift

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/kubeconfig"
	"gopkg.in/yaml.v3"
)

const (
	ServiceAccount          = "dispatch-controller"
	ServiceAccountNamespace = "dispatch-system"
	TokenSecretPrefix       = "dispatch-controller-token-"
	ClusterRoleBinding      = "dispatch-controller-cluster-admin"
	ManagedContext          = "dispatch-openshift"
	maxResponseBytes        = 4 << 20
)

var ErrInvalidLoginCommand = errors.New("invalid oc login command")

type Result struct {
	Kubeconfig   []byte
	Context      string
	Server       string
	ConnectedAt  time.Time
	TokenSecret  string
	managedToken []byte
	managedCA    []byte
}

type Login struct {
	Server                string
	Token                 string
	Username              string
	Password              string
	InsecureSkipTLSVerify bool
}

type Bootstrapper struct {
	now            func() time.Time
	sleep          func(context.Context, time.Duration) error
	newTokenSecret func() (string, error)
}

func New() *Bootstrapper {
	return &Bootstrapper{now: time.Now, sleep: sleepContext, newTokenSecret: randomTokenSecret}
}

func (b *Bootstrapper) Bootstrap(ctx context.Context, loginCommand string) (Result, error) {
	return b.bootstrap(ctx, loginCommand)
}

func (b *Bootstrapper) Repair(ctx context.Context, loginCommand string) (Result, error) {
	return b.bootstrap(ctx, loginCommand)
}

func (b *Bootstrapper) bootstrap(ctx context.Context, loginCommand string) (Result, error) {
	login, err := ParseLoginCommand(loginCommand)
	if err != nil {
		return Result{}, err
	}
	client := newClient(login.InsecureSkipTLSVerify, nil)
	bootstrapToken := login.Token
	if bootstrapToken == "" {
		bootstrapToken, err = obtainOAuthToken(ctx, client, login)
		if err != nil {
			return Result{}, err
		}
	}
	if err := verifyBootstrapIdentity(ctx, client, login.Server, bootstrapToken); err != nil {
		return Result{}, err
	}
	tokenSecret, err := b.newTokenSecret()
	if err != nil {
		return Result{}, fmt.Errorf("name managed OpenShift token: %w", err)
	}
	keepToken := false
	defer func() {
		if !keepToken {
			_ = deleteManagedToken(context.Background(), client, login.Server, bootstrapToken, tokenSecret)
		}
	}()
	if err := ensureManagedResources(ctx, client, login.Server, bootstrapToken, tokenSecret); err != nil {
		return Result{}, err
	}
	secret, err := b.waitForToken(ctx, client, login.Server, bootstrapToken, tokenSecret)
	if err != nil {
		return Result{}, err
	}
	cluster := clusterConnection{Server: login.Server, InsecureSkipTLSVerify: login.InsecureSkipTLSVerify}
	managed, err := managedKubeconfig(cluster, secret)
	if err != nil {
		return Result{}, err
	}
	contextName, err := kubeconfig.ValidateStored(managed, nil, ManagedContext)
	if err != nil {
		return Result{}, fmt.Errorf("validate managed OpenShift kubeconfig: %w", err)
	}
	if err := verifyManagedIdentity(ctx, login.Server, secret); err != nil {
		return Result{}, err
	}
	keepToken = true
	return Result{
		Kubeconfig: managed, Context: contextName, Server: login.Server, ConnectedAt: b.now().UTC(), TokenSecret: tokenSecret,
		managedToken: secret.Token, managedCA: secret.CA,
	}, nil
}

// DeletePreviousToken revokes the credential replaced by a successful repair.
// Callers must persist result before invoking this method.
func (b *Bootstrapper) DeletePreviousToken(ctx context.Context, result Result, previousTokenSecret string) error {
	if previousTokenSecret == "" || previousTokenSecret == result.TokenSecret {
		return nil
	}
	client := newClient(false, result.managedCA)
	return deleteManagedToken(ctx, client, result.Server, string(result.managedToken), previousTokenSecret)
}

func deleteManagedToken(ctx context.Context, client *http.Client, server, token, tokenSecret string) error {
	endpoint := server + "/api/v1/namespaces/" + ServiceAccountNamespace + "/secrets/" + tokenSecret
	status, _, err := apiRequest(ctx, client, token, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("rotate managed OpenShift token: %w", err)
	}
	if status != http.StatusOK && status != http.StatusAccepted && status != http.StatusNotFound {
		return fmt.Errorf("rotate managed OpenShift token: HTTP %d", status)
	}
	return nil
}

func ParseLoginCommand(command string) (Login, error) {
	command = strings.TrimSpace(command)
	if strings.HasPrefix(command, "$ ") {
		command = strings.TrimSpace(strings.TrimPrefix(command, "$ "))
	}
	fields := strings.Fields(command)
	if len(fields) < 2 || filepath.Base(trimQuotes(fields[0])) != "oc" || trimQuotes(fields[1]) != "login" {
		return Login{}, fmt.Errorf("%w: paste a command beginning with oc login", ErrInvalidLoginCommand)
	}
	login := Login{}
	for index := 2; index < len(fields); index++ {
		value := trimQuotes(fields[index])
		var optionErr error
		next := func(label string) (string, error) {
			if index+1 >= len(fields) {
				return "", fmt.Errorf("%w: %s requires a value", ErrInvalidLoginCommand, label)
			}
			index++
			return trimQuotes(fields[index]), nil
		}
		switch {
		case value == "--token":
			login.Token, optionErr = next("--token")
		case strings.HasPrefix(value, "--token="):
			login.Token = trimQuotes(strings.TrimPrefix(value, "--token="))
		case value == "--server":
			login.Server, optionErr = next("--server")
		case strings.HasPrefix(value, "--server="):
			login.Server = trimQuotes(strings.TrimPrefix(value, "--server="))
		case value == "--username" || value == "-u":
			login.Username, optionErr = next(value)
		case strings.HasPrefix(value, "--username="):
			login.Username = trimQuotes(strings.TrimPrefix(value, "--username="))
		case value == "--password" || value == "-p":
			login.Password, optionErr = next(value)
		case strings.HasPrefix(value, "--password="):
			login.Password = trimQuotes(strings.TrimPrefix(value, "--password="))
		case value == "--insecure-skip-tls-verify":
			login.InsecureSkipTLSVerify = true
		case strings.HasPrefix(value, "--insecure-skip-tls-verify="):
			login.InsecureSkipTLSVerify = strings.EqualFold(strings.TrimPrefix(value, "--insecure-skip-tls-verify="), "true")
		case value == "--kubeconfig" || strings.HasPrefix(value, "--kubeconfig="):
			return Login{}, fmt.Errorf("%w: kubeconfig overrides are managed by Dispatch", ErrInvalidLoginCommand)
		case strings.HasPrefix(value, "-"):
			return Login{}, fmt.Errorf("%w: unsupported oc login option %s", ErrInvalidLoginCommand, optionName(value))
		case login.Server == "":
			login.Server = value
		default:
			return Login{}, fmt.Errorf("%w: unexpected login argument", ErrInvalidLoginCommand)
		}
		if optionErr != nil {
			return Login{}, optionErr
		}
	}
	login.Server = strings.TrimRight(strings.TrimSpace(login.Server), "/")
	parsed, err := url.Parse(login.Server)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" {
		return Login{}, fmt.Errorf("%w: include the HTTPS OpenShift API server", ErrInvalidLoginCommand)
	}
	if login.Token == "" && (login.Username == "" || login.Password == "") {
		return Login{}, fmt.Errorf("%w: include a token or non-interactive username and password", ErrInvalidLoginCommand)
	}
	return login, nil
}

func optionName(value string) string {
	if index := strings.IndexByte(value, '='); index >= 0 {
		return value[:index]
	}
	return value
}

func trimQuotes(value string) string {
	if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
		return value[1 : len(value)-1]
	}
	return value
}

func obtainOAuthToken(ctx context.Context, client *http.Client, login Login) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, login.Server+"/.well-known/oauth-authorization-server", nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("discover OpenShift OAuth server: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("discover OpenShift OAuth server: HTTP %d", response.StatusCode)
	}
	var metadata struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&metadata); err != nil || metadata.AuthorizationEndpoint == "" {
		return "", errors.New("OpenShift OAuth discovery did not return an authorization endpoint")
	}
	authorizationURL, err := url.Parse(metadata.AuthorizationEndpoint)
	if err != nil || authorizationURL.Scheme != "https" {
		return "", errors.New("OpenShift OAuth discovery returned an invalid authorization endpoint")
	}
	query := authorizationURL.Query()
	query.Set("client_id", "openshift-challenging-client")
	query.Set("response_type", "token")
	authorizationURL.RawQuery = query.Encode()
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, authorizationURL.String(), nil)
	if err != nil {
		return "", err
	}
	request.SetBasicAuth(login.Username, login.Password)
	request.Header.Set("X-CSRF-Token", "1")
	response, err = client.Do(request)
	if err != nil {
		return "", fmt.Errorf("authenticate to OpenShift OAuth: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound && response.StatusCode != http.StatusSeeOther {
		return "", fmt.Errorf("authenticate to OpenShift OAuth: HTTP %d", response.StatusCode)
	}
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		return "", errors.New("OpenShift OAuth returned an invalid token redirect")
	}
	values, err := url.ParseQuery(location.Fragment)
	if err != nil || values.Get("access_token") == "" {
		return "", errors.New("OpenShift OAuth did not return an access token")
	}
	return values.Get("access_token"), nil
}

func verifyBootstrapIdentity(ctx context.Context, client *http.Client, server, token string) error {
	status, _, err := apiRequest(ctx, client, token, http.MethodGet, server+"/apis/user.openshift.io/v1/users/~", nil)
	if err != nil {
		return fmt.Errorf("verify temporary OpenShift login: %w", err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("verify temporary OpenShift login: HTTP %d", status)
	}
	return nil
}

func ensureManagedResources(ctx context.Context, client *http.Client, server, token, tokenSecret string) error {
	resources := []struct {
		label, itemURL, collectionURL string
		body                          map[string]any
	}{
		{"namespace", server + "/api/v1/namespaces/" + ServiceAccountNamespace, server + "/api/v1/namespaces", map[string]any{
			"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ServiceAccountNamespace},
		}},
		{"service account", server + "/api/v1/namespaces/" + ServiceAccountNamespace + "/serviceaccounts/" + ServiceAccount,
			server + "/api/v1/namespaces/" + ServiceAccountNamespace + "/serviceaccounts", map[string]any{
				"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]any{"name": ServiceAccount, "namespace": ServiceAccountNamespace},
			}},
		{"cluster-admin binding", server + "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/" + ClusterRoleBinding,
			server + "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings", managedBinding()},
		{"service-account token", server + "/api/v1/namespaces/" + ServiceAccountNamespace + "/secrets/" + tokenSecret,
			server + "/api/v1/namespaces/" + ServiceAccountNamespace + "/secrets", map[string]any{
				"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/service-account-token",
				"metadata": map[string]any{"name": tokenSecret, "namespace": ServiceAccountNamespace,
					"annotations": map[string]any{"kubernetes.io/service-account.name": ServiceAccount}},
			}},
	}
	for _, resource := range resources {
		status, existing, err := apiRequest(ctx, client, token, http.MethodGet, resource.itemURL, nil)
		if err != nil {
			return fmt.Errorf("inspect managed OpenShift %s: %w", resource.label, err)
		}
		if status >= 200 && status < 300 {
			if resource.label == "cluster-admin binding" && !bindingIsManaged(existing) {
				return errors.New("the managed OpenShift cluster role binding exists with different permissions")
			}
			continue
		}
		if status != http.StatusNotFound {
			return fmt.Errorf("inspect managed OpenShift %s: HTTP %d", resource.label, status)
		}
		status, _, err = apiRequest(ctx, client, token, http.MethodPost, resource.collectionURL, resource.body)
		if err != nil {
			return fmt.Errorf("create managed OpenShift %s: %w", resource.label, err)
		}
		if status != http.StatusCreated && status != http.StatusOK && status != http.StatusConflict {
			return fmt.Errorf("create managed OpenShift %s: HTTP %d", resource.label, status)
		}
	}
	return nil
}

func managedBinding() map[string]any {
	return map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": ClusterRoleBinding},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "cluster-admin"},
		"subjects": []any{map[string]any{"kind": "ServiceAccount", "name": ServiceAccount, "namespace": ServiceAccountNamespace}}}
}

func bindingIsManaged(contents []byte) bool {
	var binding struct {
		RoleRef  struct{ Kind, Name string }              `json:"roleRef"`
		Subjects []struct{ Kind, Name, Namespace string } `json:"subjects"`
	}
	if json.Unmarshal(contents, &binding) != nil || binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != "cluster-admin" {
		return false
	}
	for _, subject := range binding.Subjects {
		if subject.Kind == "ServiceAccount" && subject.Name == ServiceAccount && subject.Namespace == ServiceAccountNamespace {
			return true
		}
	}
	return false
}

type clusterConnection struct {
	Server                string
	InsecureSkipTLSVerify bool
}

type tokenSecret struct {
	Token []byte
	CA    []byte
}

func (b *Bootstrapper) waitForToken(ctx context.Context, client *http.Client, server, bootstrapToken, tokenSecretName string) (tokenSecret, error) {
	secretURL := server + "/api/v1/namespaces/" + ServiceAccountNamespace + "/secrets/" + tokenSecretName
	for attempt := 0; attempt < 40; attempt++ {
		status, contents, err := apiRequest(ctx, client, bootstrapToken, http.MethodGet, secretURL, nil)
		if err == nil && status == http.StatusOK {
			var secret struct {
				Data map[string]string `json:"data"`
			}
			if json.Unmarshal(contents, &secret) == nil {
				token, tokenErr := base64.StdEncoding.DecodeString(secret.Data["token"])
				ca, caErr := base64.StdEncoding.DecodeString(secret.Data["ca.crt"])
				if tokenErr == nil && caErr == nil && len(token) > 0 {
					return tokenSecret{Token: token, CA: ca}, nil
				}
			}
		}
		if err := b.sleep(ctx, 250*time.Millisecond); err != nil {
			return tokenSecret{}, err
		}
	}
	return tokenSecret{}, errors.New("OpenShift did not issue the managed service-account token before the timeout")
}

func managedKubeconfig(cluster clusterConnection, secret tokenSecret) ([]byte, error) {
	caData := ""
	if len(secret.CA) > 0 {
		caData = base64.StdEncoding.EncodeToString(secret.CA)
	}
	clusterData := map[string]any{"server": cluster.Server}
	if caData != "" {
		clusterData["certificate-authority-data"] = caData
	} else if cluster.InsecureSkipTLSVerify {
		clusterData["insecure-skip-tls-verify"] = true
	}
	document := struct {
		APIVersion     string           `yaml:"apiVersion"`
		Kind           string           `yaml:"kind"`
		CurrentContext string           `yaml:"current-context"`
		Clusters       []map[string]any `yaml:"clusters"`
		Contexts       []map[string]any `yaml:"contexts"`
		Users          []map[string]any `yaml:"users"`
	}{APIVersion: "v1", Kind: "Config", CurrentContext: ManagedContext,
		Clusters: []map[string]any{{"name": ManagedContext, "cluster": clusterData}},
		Contexts: []map[string]any{{"name": ManagedContext, "context": map[string]any{"cluster": ManagedContext, "user": ServiceAccount}}},
		Users:    []map[string]any{{"name": ServiceAccount, "user": map[string]any{"token": string(secret.Token)}}}}
	return yaml.Marshal(document)
}

func verifyManagedIdentity(ctx context.Context, server string, secret tokenSecret) error {
	client := newClient(false, secret.CA)
	body := map[string]any{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview",
		"spec": map[string]any{"resourceAttributes": map[string]any{"verb": "*", "group": "*", "resource": "*"}}}
	status, contents, err := apiRequest(ctx, client, string(secret.Token), http.MethodPost,
		server+"/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", body)
	if err != nil {
		return fmt.Errorf("verify managed OpenShift service account: %w", err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("verify managed OpenShift service account: HTTP %d", status)
	}
	var review struct {
		Status struct {
			Allowed bool `json:"allowed"`
		} `json:"status"`
	}
	if json.Unmarshal(contents, &review) != nil || !review.Status.Allowed {
		return errors.New("managed OpenShift service account does not have cluster-admin access")
	}
	return nil
}

func newClient(insecure bool, certificateAuthority []byte) *http.Client {
	pool, _ := x509.SystemCertPool()
	if pool == nil {
		pool = x509.NewCertPool()
	}
	if len(certificateAuthority) > 0 {
		pool.AppendCertsFromPEM(certificateAuthority)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, InsecureSkipVerify: insecure}} // #nosec G402 -- explicit oc login compatibility option.
	return &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func apiRequest(ctx context.Context, client *http.Client, token, method, endpoint string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, err
	}
	if len(contents) > maxResponseBytes {
		return 0, nil, errors.New("OpenShift API response is too large")
	}
	return response.StatusCode, contents, nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func randomTokenSecret() (string, error) {
	value := make([]byte, 6)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return TokenSecretPrefix + hex.EncodeToString(value), nil
}

func Config(result Result, namespace string) core.KubernetesServerConfig {
	connectedAt := result.ConnectedAt
	return core.KubernetesServerConfig{KubeconfigData: string(result.Kubeconfig), KubeconfigStored: true,
		Context: result.Context, Namespace: namespace, OpenShift: &core.OpenShiftServerConfig{Managed: true,
			ServiceAccount: ServiceAccount, ServiceAccountNamespace: ServiceAccountNamespace,
			TokenSecret: result.TokenSecret, ConnectedAt: &connectedAt}}
}
