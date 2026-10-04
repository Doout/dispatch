// Package automationclient is the shared, bounded API client used by dispatchctl
// and its MCP transport. The controller remains the authorization authority.
package automationclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const MaxResponseBytes = 4 << 20
const MaxInputBytes = 1 << 20

// Result is the versioned JSON envelope shared by CLI output and MCP results.
type Result struct {
	Version      string          `json:"version"`
	OK           bool            `json:"ok"`
	Status       int             `json:"status,omitempty"`
	Data         json.RawMessage `json:"data,omitempty"`
	Error        *Problem        `json:"error,omitempty"`
	Location     string          `json:"location,omitempty"`
	Replayed     bool            `json:"replayed,omitempty"`
	RetryUntil   string          `json:"retryUntil,omitempty"`
	Continuation *Continuation   `json:"continuation,omitempty"`
}
type Problem struct {
	Code     string          `json:"code"`
	Title    string          `json:"title"`
	Detail   string          `json:"detail,omitempty"`
	Evidence json.RawMessage `json:"evidence,omitempty"`
}
type Continuation struct {
	ProjectID   string `json:"projectId,omitempty"`
	ResourceID  string `json:"resourceId,omitempty"`
	OperationID string `json:"operationId,omitempty"`
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	Key         string `json:"idempotencyKey,omitempty"`
}

func Failure(code, title string) Result {
	return Result{Version: "dispatch.client/v1", Error: &Problem{Code: code, Title: title}}
}
func (r Result) ExitCode() int {
	if r.OK {
		return 0
	}
	if r.Error == nil {
		return 1
	}
	switch r.Error.Code {
	case "invalid_input", "configuration":
		return 2
	case "authentication", "forbidden":
		return 3
	case "timeout", "cancelled":
		return 4
	case "operation_failed", "operation_cancelled", "operation_paused", "operation_stopped", "outcome_unknown":
		return 5
	default:
		return 1
	}
}

type Client struct {
	base  *url.URL
	token string
	HTTP  *http.Client
}

func New(endpoint, token string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.Trim(u.Path, "/") != "" {
		return nil, errors.New("DISPATCH_URL must be a controller origin without credentials, path, query or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, errors.New("HTTPS is required except for loopback controller addresses")
	}
	if timeout <= 0 || timeout > 10*time.Minute {
		return nil, errors.New("request timeout must be between zero and ten minutes")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("a scoped automation credential is required")
	}
	u.Path = ""
	return &Client{base: u, token: strings.TrimSpace(token), HTTP: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func ReadToken(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot open credential file")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return "", errors.New("credential file must be a regular file accessible only by its owner, use mode 0600")
	}
	b, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(b) > 8192 {
		return "", errors.New("cannot read bounded credential file")
	}
	defer clear(b)
	return strings.TrimSpace(string(b)), nil
}
func (c *Client) request(ctx context.Context, method, path, key string, body any) Result {
	out := Result{Version: "dispatch.client/v1"}
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil || len(payload) > MaxInputBytes {
			return Failure("invalid_input", "Request exceeds the input limit or is not JSON")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+"/api/v1"+path, bytes.NewReader(payload))
	if err != nil {
		return Failure("invalid_input", "Cannot construct the request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dispatchctl/1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return Failure("cancelled", "Client stopped waiting. Accepted server work was not cancelled.")
		}
		var timed net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timed) && timed.Timeout() {
			return Failure("timeout", "Client deadline reached. Inspect the original receipt or retry the identical request and key.")
		}
		return Failure("unavailable", "Controller request failed. Inspect the original receipt or retry the identical request and key.")
	}
	defer res.Body.Close()
	out.Status = res.StatusCode
	// Response locations are references, never followed as arbitrary URLs.
	location := res.Header.Get("Location")
	if strings.HasPrefix(location, "/api/v1/") && !strings.ContainsAny(location, "\r\n") {
		out.Location = location
	}
	out.Replayed = res.Header.Get("Idempotency-Replayed") == "true"
	out.RetryUntil = res.Header.Get("Idempotency-Retry-Until")
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxResponseBytes+1))
	if err != nil || len(raw) > MaxResponseBytes {
		return Failure("response_limit", "Controller response could not be read within the response limit")
	}
	if len(raw) == 0 && res.StatusCode >= 200 && res.StatusCode < 300 {
		raw = []byte("null")
	}
	if !json.Valid(raw) {
		return Failure("invalid_response", "Controller returned a non-JSON response")
	}
	// Redact the exact authentication credential even if an upstream diagnostic
	// accidentally echoes it. No request header or transport error is serialized.
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return Failure("invalid_response", "Controller returned invalid JSON")
	}
	raw, _ = json.Marshal(redact(value, c.token))
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		out.OK = true
		out.Data = raw
		return out
	}
	code := "request_failed"
	switch res.StatusCode {
	case 401:
		code = "authentication"
	case 403:
		code = "forbidden"
	case 409:
		code = "conflict"
	case 422:
		code = "unsupported_or_invalid"
	case 429:
		code = "rate_limited"
	}
	var p struct{ Title, Detail string }
	_ = json.Unmarshal(raw, &p)
	if p.Title == "" {
		p.Title = fmt.Sprintf("Controller rejected request with HTTP %d", res.StatusCode)
	}
	out.Error = &Problem{Code: code, Title: p.Title, Detail: p.Detail, Evidence: raw}
	return out
}
func redact(value any, token string) any {
	switch v := value.(type) {
	case string:
		return strings.ReplaceAll(v, token, "[REDACTED]")
	case []any:
		for i := range v {
			v[i] = redact(v[i], token)
		}
	case map[string]any:
		for k, x := range v {
			delete(v, k)
			v[strings.ReplaceAll(k, token, "[REDACTED]")] = redact(x, token)
		}
	}
	return value
}
