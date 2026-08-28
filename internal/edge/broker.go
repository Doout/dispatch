package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

const (
	DriverAgent       = "dispatch_agent"
	maxRequestBytes   = 1 << 20
	maxResponseBytes  = 2 << 20
	leaseDuration     = 30 * time.Second
	defaultJobTimeout = 45 * time.Second
)

type Store interface {
	CreateEdgeJob(context.Context, core.EdgeJob) error
	LeaseEdgeJob(context.Context, string, time.Time, time.Duration) (*core.EdgeJob, error)
	CompleteEdgeJob(context.Context, string, string, string, string, string, time.Time) error
	GetEdgeJob(context.Context, string) (core.EdgeJob, error)
	DeleteEdgeJob(context.Context, string) error
}

type HTTPRequest struct {
	Method  string              `json:"method"`
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers"`
	Body    []byte              `json:"body,omitempty"`
}

type HTTPResponse struct {
	StatusCode int                 `json:"statusCode"`
	Headers    map[string][]string `json:"headers"`
	Body       []byte              `json:"body,omitempty"`
}

type LeasedJob struct {
	ID         string      `json:"id"`
	LeaseToken string      `json:"leaseToken"`
	Request    HTTPRequest `json:"request"`
}

type Completion struct {
	LeaseToken string        `json:"leaseToken"`
	Response   *HTTPResponse `json:"response,omitempty"`
	Error      string        `json:"error,omitempty"`
}

type Broker struct {
	Store Store
	Vault *secretcrypto.Vault
}

func New(data Store, vault *secretcrypto.Vault) *Broker { return &Broker{Store: data, Vault: vault} }

// Do sends one bounded HTTP request through an edge node. The queued payload
// and result are encrypted with the controller master key and deleted after
// the caller receives the result.
func (b *Broker) Do(ctx context.Context, network core.PrivateNetwork, request *http.Request) (*http.Response, error) {
	if b == nil || b.Store == nil || b.Vault == nil {
		return nil, errors.New("edge routing is not configured")
	}
	if network.Driver != DriverAgent {
		return nil, fmt.Errorf("network %q is not an edge node", network.Name)
	}
	if request.URL == nil || request.URL.Scheme != "https" {
		return nil, errors.New("edge requests must use HTTPS")
	}
	var body []byte
	var err error
	if request.Body != nil {
		body, err = io.ReadAll(io.LimitReader(request.Body, maxRequestBytes+1))
		if err != nil {
			return nil, err
		}
	}
	if len(body) > maxRequestBytes {
		return nil, errors.New("edge request exceeds 1 MiB")
	}
	request.Body = io.NopCloser(strings.NewReader(string(body)))
	payload, err := json.Marshal(HTTPRequest{Method: request.Method, URL: request.URL.String(), Headers: request.Header.Clone(), Body: body})
	if err != nil {
		return nil, err
	}
	id := ulid.Make().String()
	encrypted, err := b.Vault.Encrypt("edge-job:"+id+":request", payload)
	clear(payload)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	deadline := now.Add(defaultJobTimeout)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	job := core.EdgeJob{ID: id, PrivateNetworkID: network.ID, State: "pending", EncryptedRequest: encrypted, ExpiresAt: deadline, CreatedAt: now, UpdatedAt: now}
	if err := b.Store.CreateEdgeJob(ctx, job); err != nil {
		return nil, err
	}
	defer b.Store.DeleteEdgeJob(context.Background(), id) // encrypted, short-lived, and single consumer

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := b.Store.GetEdgeJob(ctx, id)
		if err != nil {
			return nil, err
		}
		switch current.State {
		case "completed":
			plaintext, err := b.Vault.Decrypt("edge-job:"+id+":response", current.EncryptedResponse)
			if err != nil {
				return nil, err
			}
			defer clear(plaintext)
			var result HTTPResponse
			if err := json.Unmarshal(plaintext, &result); err != nil {
				return nil, errors.New("edge node returned an invalid response")
			}
			if result.StatusCode < 100 || result.StatusCode > 599 || len(result.Body) > maxResponseBytes {
				return nil, errors.New("edge node returned an invalid HTTP response")
			}
			return &http.Response{StatusCode: result.StatusCode, Status: fmt.Sprintf("%d %s", result.StatusCode, http.StatusText(result.StatusCode)), Header: http.Header(result.Headers), Body: io.NopCloser(strings.NewReader(string(result.Body))), Request: request}, nil
		case "failed":
			if current.Error == "" {
				current.Error = "edge request failed"
			}
			return nil, errors.New(current.Error)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (b *Broker) Lease(ctx context.Context, networkID string) (*LeasedJob, error) {
	job, err := b.Store.LeaseEdgeJob(ctx, networkID, time.Now().UTC(), leaseDuration)
	if err != nil || job == nil {
		return nil, err
	}
	plaintext, err := b.Vault.Decrypt("edge-job:"+job.ID+":request", job.EncryptedRequest)
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	var request HTTPRequest
	if err := json.Unmarshal(plaintext, &request); err != nil {
		return nil, errors.New("edge job request is invalid")
	}
	return &LeasedJob{ID: job.ID, LeaseToken: job.LeaseToken, Request: request}, nil
}

func (b *Broker) Complete(ctx context.Context, networkID, jobID string, completion Completion) error {
	job, err := b.Store.GetEdgeJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job.PrivateNetworkID != networkID {
		return store.ErrNotFound
	}
	state, encrypted, detail := "completed", "", strings.TrimSpace(completion.Error)
	if detail != "" || completion.Response == nil {
		state = "failed"
		if detail == "" {
			detail = "edge node did not return a response"
		}
		if len(detail) > 2048 {
			detail = detail[:2048]
		}
	} else {
		if len(completion.Response.Body) > maxResponseBytes {
			return errors.New("edge response exceeds 2 MiB")
		}
		payload, err := json.Marshal(completion.Response)
		if err != nil {
			return err
		}
		encrypted, err = b.Vault.Encrypt("edge-job:"+jobID+":response", payload)
		clear(payload)
		if err != nil {
			return err
		}
	}
	return b.Store.CompleteEdgeJob(ctx, jobID, completion.LeaseToken, state, encrypted, detail, time.Now().UTC())
}
