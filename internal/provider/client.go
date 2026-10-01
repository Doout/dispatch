package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient accepts an injected transport for a registered direct/private route.
// HTTP is supported for local sidecars; callers must enforce their TLS policy.
func NewClient(endpoint, token string, transport *http.Client) (*Client, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme != "http" && parsed.Scheme != "https" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("provider endpoint or credential is invalid")
	}
	client := http.Client{Timeout: 30 * time.Second}
	if transport != nil {
		client = *transport
		if client.Timeout == 0 {
			client.Timeout = 30 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: strings.TrimRight(endpoint, "/"), token: token, http: &client}, nil
}

func (c *Client) request(ctx context.Context, method, path, key string, input, output any, expected int) error {
	var payload []byte
	var err error
	if input != nil {
		payload, err = json.Marshal(input)
		if err != nil || len(payload) > MaxMessageBytes {
			return errors.New("provider request cannot be encoded within the size limit")
		}
		defer clear(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return errors.New("cannot prepare provider request")
	}
	request.Header.Set("Accept", "application/json, application/problem+json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("provider request failed; check connectivity and reconcile mutations before retrying")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxMessageBytes+1))
	if err != nil || len(raw) > MaxMessageBytes {
		return errors.New("provider response is unreadable or exceeds the size limit")
	}
	defer clear(raw)
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		return errors.New("provider response is missing a valid content type")
	}
	if response.StatusCode != expected {
		var problem Problem
		if response.StatusCode < 400 || contentType != "application/problem+json" || json.Unmarshal(raw, &problem) != nil || problem.Status != response.StatusCode || problem.Title == "" || problem.Type == "" {
			return errors.New("provider returned an invalid status or problem response")
		}
		if c.token != "" {
			problem.Title = strings.ReplaceAll(problem.Title, c.token, "[redacted]")
			problem.Detail = strings.ReplaceAll(problem.Detail, c.token, "[redacted]")
		}
		return &problem
	}
	if contentType != "application/json" || json.Unmarshal(raw, output) != nil {
		return errors.New("provider returned an invalid JSON response")
	}
	return nil
}

func (c *Client) Manifest(ctx context.Context) (Manifest, error) {
	var result Manifest
	err := c.request(ctx, "GET", "/v1/manifest", "", nil, &result, http.StatusOK)
	if err == nil {
		err = ValidateManifest(result)
	}
	return result, err
}
func (c *Client) Validate(ctx context.Context, config map[string]any) error {
	var result validationResponse
	err := c.request(ctx, "POST", "/v1/validate", "", validationRequest{Config: config}, &result, http.StatusOK)
	if err == nil && !result.Valid {
		return errors.New("provider did not confirm configuration validity")
	}
	return err
}
func (c *Client) Options(ctx context.Context, input OptionRequest) ([]Option, error) {
	var result optionsResponse
	err := c.request(ctx, "POST", "/v1/options", "", input, &result, http.StatusOK)
	if err == nil {
		if result.Items == nil {
			return nil, errors.New("provider returned no options array")
		}
		seen := map[string]bool{}
		for _, item := range result.Items {
			if item.ID == "" || item.Name == "" || seen[item.ID] {
				return nil, errors.New("provider returned invalid or duplicate options")
			}
			seen[item.ID] = true
		}
	}
	return result.Items, err
}
func (c *Client) CreateServer(ctx context.Context, key string, input CreateServerRequest) (Operation, error) {
	if !ValidID(key) {
		return Operation{}, errors.New("provider idempotency key is invalid")
	}
	return c.operationRequest(ctx, "POST", "/v1/servers", key, input, http.StatusAccepted)
}
func (c *Client) Operation(ctx context.Context, id string) (Operation, error) {
	if !ValidID(id) {
		return Operation{}, errors.New("provider operation identity is invalid")
	}
	result, err := c.operationRequest(ctx, "GET", "/v1/operations/"+url.PathEscape(id), "", nil, http.StatusOK)
	if err == nil && result.ID != id {
		return Operation{}, errors.New("provider returned a different operation identity")
	}
	return result, err
}
func (c *Client) DeleteServer(ctx context.Context, key, id string) (Operation, error) {
	if !ValidID(key) || !ValidID(id) {
		return Operation{}, errors.New("provider deletion identity is invalid")
	}
	return c.operationRequest(ctx, "DELETE", "/v1/servers/"+url.PathEscape(id), key, nil, http.StatusAccepted)
}
func (c *Client) operationRequest(ctx context.Context, method, path, key string, input any, expected int) (Operation, error) {
	var result Operation
	err := c.request(ctx, method, path, key, input, &result, expected)
	if err == nil {
		err = ValidateOperation(result)
	}
	return result, err
}
func (c *Client) Server(ctx context.Context, id string) (Server, error) {
	if !ValidID(id) {
		return Server{}, errors.New("provider server identity is invalid")
	}
	var result Server
	err := c.request(ctx, "GET", "/v1/servers/"+url.PathEscape(id), "", nil, &result, http.StatusOK)
	if err == nil && (result.ID != id || result.Name == "" || result.State == "") {
		err = errors.New("provider returned an invalid server identity or state")
	}
	return result, err
}
