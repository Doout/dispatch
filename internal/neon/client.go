// Package neon manages explicitly owned branches on an existing Neon API.
// It never creates a Neon project or performs an unreviewed data restore.
package neon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const DefaultEndpoint = "https://console.neon.tech/api/v2"

var providerID = regexp.MustCompile(`^[a-z0-9-]{1,60}$`)
var databaseName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$-]{0,62}$`)

// Spec is immutable after acceptance. Token values never belong in this object.
type Spec struct {
	Endpoint            string `json:"endpoint" yaml:"endpoint"`
	ProjectID           string `json:"projectId" yaml:"projectId"`
	ParentBranchID      string `json:"parentBranchId" yaml:"parentBranchId"`
	CredentialRef       string `json:"credentialRef" yaml:"credentialRef"`
	Database            string `json:"database" yaml:"database"`
	DataMode            string `json:"dataMode" yaml:"dataMode"`
	SuspendAfterSeconds int    `json:"suspendAfterSeconds,omitempty" yaml:"suspendAfterSeconds,omitempty"`
}

type Scope struct {
	ProjectID        string `json:"projectId"`
	RunID            string `json:"runId"`
	PreviewID        string `json:"previewId,omitempty"`
	Generation       int64  `json:"generation"`
	ConfigDigest     string `json:"configDigest"`
	ApprovedDataCopy bool   `json:"approvedDataCopy"`
}

type Branch struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	ParentID   string `json:"parent_id"`
	Name       string `json:"name"`
	State      string `json:"current_state"`
	InitSource string `json:"init_source"`
	Default    bool   `json:"default"`
	Protected  bool   `json:"protected"`
}
type Endpoint struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	BranchID  string `json:"branch_id"`
	Type      string `json:"type"`
	Host      string `json:"host"`
	State     string `json:"current_state"`
	Disabled  bool   `json:"disabled"`
}
type Resource struct {
	Branch   Branch   `json:"branch"`
	Endpoint Endpoint `json:"endpoint"`
	Role     string   `json:"role"`
	State    string   `json:"state"`
}
type annotation struct {
	Value map[string]string `json:"value"`
}
type branchResponse struct {
	Branch     Branch     `json:"branch"`
	Annotation annotation `json:"annotation"`
}
type branchList struct {
	Branches    []Branch              `json:"branches"`
	Annotations map[string]annotation `json:"annotations"`
	Pagination  struct {
		Next string `json:"next"`
	} `json:"pagination"`
}
type role struct {
	BranchID  string `json:"branch_id"`
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
}

// APIError intentionally excludes response bodies, credentials and connection URLs.
type APIError struct {
	Status    int
	Uncertain bool
	Action    string
}

func (e *APIError) Error() string {
	if e.Uncertain {
		return "Neon " + e.Action + " outcome is uncertain; inspect the original owned resource before retrying"
	}
	if e.Status == 0 {
		return "Neon API request failed"
	}
	return fmt.Sprintf("Neon API %s returned HTTP %d", e.Action, e.Status)
}
func IsNotFound(err error) bool { var e *APIError; return errors.As(err, &e) && e.Status == 404 }

type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

func New(spec Spec, token string, transport *http.Client) (*Client, error) {
	if err := ValidateSpec(spec); err != nil {
		return nil, err
	}
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("Neon credential is unavailable")
	}
	if spec.Endpoint == "" {
		spec.Endpoint = DefaultEndpoint
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if transport != nil {
		copy := *transport
		client = &copy
		if client.Timeout == 0 {
			client.Timeout = 30 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("Neon API redirects are forbidden") }
	return &Client{endpoint: strings.TrimRight(spec.Endpoint, "/"), token: token, http: client}, nil
}
func ValidateSpec(s Spec) error {
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(endpoint, "\r\n\x00") {
		return errors.New("Neon endpoint must be an HTTPS API URL without credentials, query or fragment")
	}
	if !providerID.MatchString(s.ProjectID) || !providerID.MatchString(s.ParentBranchID) || s.CredentialRef == "" {
		return errors.New("Neon project, parent branch and scoped credential reference are required")
	}
	if !databaseName.MatchString(s.Database) {
		return errors.New("Neon database must be a simple database identifier")
	}
	if s.DataMode != "" && s.DataMode != "schema-only" && s.DataMode != "parent-data" {
		return errors.New("Neon data mode must be schema-only or explicitly approved parent-data")
	}
	if s.SuspendAfterSeconds < 0 || s.SuspendAfterSeconds > 604800 {
		return errors.New("Neon suspension interval must be zero for the provider default or at most one week")
	}
	return nil
}
func validateScope(s Spec, scope Scope) error {
	if err := ValidateSpec(s); err != nil {
		return err
	}
	if scope.ProjectID == "" || scope.RunID == "" || scope.ConfigDigest == "" || scope.Generation < 1 {
		return errors.New("Neon accepted ownership scope is incomplete")
	}
	if s.DataMode == "parent-data" && !scope.ApprovedDataCopy {
		return errors.New("copying parent database rows requires an explicit owner approval")
	}
	return nil
}
func Name(scope Scope) string {
	sum := sha256.Sum256([]byte(scope.ProjectID + "\x00" + scope.RunID + "\x00" + scope.PreviewID))
	return fmt.Sprintf("dispatch-%s-g%d", hex.EncodeToString(sum[:16]), scope.Generation)
}
func Role(scope Scope) string {
	sum := sha256.Sum256([]byte(Name(scope)))
	return "dispatch_" + hex.EncodeToString(sum[:12])
}
func annotations(scope Scope) map[string]string {
	return map[string]string{"dispatch.managed-by": "dispatch", "dispatch.project": scope.ProjectID, "dispatch.run": scope.RunID, "dispatch.preview": scope.PreviewID, "dispatch.generation": fmt.Sprint(scope.Generation), "dispatch.config": scope.ConfigDigest}
}
func validateBranch(s Spec, scope Scope, b Branch, a annotation) error {
	if !providerID.MatchString(b.ID) || b.ProjectID != s.ProjectID || b.Name != Name(scope) || b.Default || b.Protected {
		return errors.New("Neon branch identity or protection changed")
	}
	for k, v := range annotations(scope) {
		if a.Value[k] != v {
			return errors.New("Neon branch ownership annotation changed")
		}
	}
	mode := s.DataMode
	if mode == "" {
		mode = "schema-only"
	}
	if b.InitSource != mode || mode == "parent-data" && b.ParentID != s.ParentBranchID {
		return errors.New("Neon branch initialization policy changed")
	}
	return nil
}
func (c *Client) request(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var in io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		defer clear(raw)
		in = bytes.NewReader(raw)
	}
	target := c.endpoint + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, in)
	if err != nil {
		return errors.New("invalid Neon request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	mutation := method != "GET"
	if err != nil {
		return &APIError{Action: method, Uncertain: mutation}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return &APIError{Action: method, Status: response.StatusCode, Uncertain: mutation && response.StatusCode >= 500 && response.StatusCode != 503}
	}
	if out == nil || response.StatusCode == 204 {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
	if err != nil || len(raw) > 4<<20 {
		return &APIError{Action: method, Uncertain: mutation}
	}
	defer clear(raw)
	if json.Unmarshal(raw, out) != nil {
		return &APIError{Action: method, Uncertain: mutation}
	}
	return nil
}
func projectPath(s Spec) string { return "/projects/" + url.PathEscape(s.ProjectID) }
func (c *Client) branch(ctx context.Context, s Spec, scope Scope, id string) (Branch, error) {
	if !providerID.MatchString(id) {
		return Branch{}, errors.New("invalid Neon branch identity")
	}
	var response branchResponse
	if err := c.request(ctx, "GET", projectPath(s)+"/branches/"+id, nil, nil, &response); err != nil {
		return Branch{}, err
	}
	return response.Branch, validateBranch(s, scope, response.Branch, response.Annotation)
}
func (c *Client) Find(ctx context.Context, s Spec, scope Scope) (*Branch, error) {
	if err := validateScope(s, scope); err != nil {
		return nil, err
	}
	var found *Branch
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		query := url.Values{"search": {Name(scope)}, "limit": {"100"}, "sort_by": {"name"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var response branchList
		if err := c.request(ctx, "GET", projectPath(s)+"/branches", query, nil, &response); err != nil {
			return nil, err
		}
		for _, b := range response.Branches {
			if b.Name == Name(scope) {
				if found != nil {
					return nil, errors.New("multiple Neon branches match the accepted identity")
				}
				if err := validateBranch(s, scope, b, response.Annotations[b.ID]); err != nil {
					return nil, err
				}
				copy := b
				found = &copy
			}
		}
		cursor = response.Pagination.Next
		if cursor == "" {
			return found, nil
		}
		if seen[cursor] {
			return nil, errors.New("Neon branch pagination did not advance")
		}
		seen[cursor] = true
	}
	return nil, errors.New("Neon branch inventory exceeds the bounded inspection limit")
}
func (c *Client) Inspect(ctx context.Context, s Spec, scope Scope) (Resource, error) {
	b, err := c.Find(ctx, s, scope)
	if err != nil {
		return Resource{}, err
	}
	if b == nil {
		return Resource{State: "absent"}, nil
	}
	result := Resource{Branch: *b, Role: Role(scope), State: "unready"}
	var endpoints struct {
		Endpoints []Endpoint `json:"endpoints"`
	}
	if err = c.request(ctx, "GET", projectPath(s)+"/branches/"+b.ID+"/endpoints", nil, nil, &endpoints); err != nil {
		return result, err
	}
	for _, ep := range endpoints.Endpoints {
		if ep.Type == "read_write" {
			if result.Endpoint.ID != "" || !providerID.MatchString(ep.ID) || ep.BranchID != b.ID || ep.ProjectID != s.ProjectID || ep.Host == "" {
				return result, errors.New("Neon compute ownership is ambiguous")
			}
			result.Endpoint = ep
		}
	}
	if result.Endpoint.ID == "" || result.Endpoint.Disabled || b.State != "ready" || result.Endpoint.State != "active" && result.Endpoint.State != "idle" {
		return result, nil
	}
	var roleResponse struct {
		Role role `json:"role"`
	}
	err = c.request(ctx, "GET", projectPath(s)+"/branches/"+b.ID+"/roles/"+url.PathEscape(result.Role), nil, nil, &roleResponse)
	if IsNotFound(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if roleResponse.Role.BranchID != b.ID || roleResponse.Role.Name != result.Role || roleResponse.Role.Protected {
		return result, errors.New("Neon connection role ownership changed")
	}
	result.State = "ready"
	return result, nil
}
