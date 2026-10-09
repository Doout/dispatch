package workflowrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/edgeclient"
)

type Client struct {
	HTTP       *http.Client
	Identity   edgeclient.Identity
	Enrollment string
	Mode       string
	Docker     bool
	session    edgeclient.Session
}

func (c *Client) Token(ctx context.Context) (string, error) {
	if c.session.Token == "" || time.Until(c.session.ExpiresAt) < time.Minute {
		next, err := edgeclient.ObtainSession(ctx, c.HTTP, c.Identity, c.Enrollment)
		if err != nil {
			return "", err
		}
		c.session = next
		c.Enrollment = ""
	}
	return c.session.Token, nil
}
func (c *Client) request(ctx context.Context, method, path string, input, output any) (bool, error) {
	token, err := c.Token(ctx)
	if err != nil {
		return false, err
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return false, err
		}
		body = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	base := strings.TrimRight(c.Identity.Controller, "/") + "/api/v1/edge/nodes/" + url.PathEscape(c.Identity.NodeID) + "/workflow/jobs/"
	request, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return false, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Dispatch-Worker-Version", Version)
	request.Header.Set("X-Dispatch-Worker-Mode", c.Mode)
	if c.Docker {
		request.Header.Set("X-Dispatch-Worker-Docker", "true")
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == 401 {
		c.session = edgeclient.Session{}
	}
	if response.StatusCode == 204 {
		return false, nil
	}
	if response.StatusCode != 200 {
		return false, fmt.Errorf("worker request returned HTTP %d", response.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, MaxPayload+1)).Decode(output); err != nil {
		return false, err
	}
	return true, nil
}
func (c *Client) Poll(ctx context.Context, w *Worker) (bool, error) {
	var job LeasedJob
	found, err := c.request(ctx, http.MethodGet, "next", nil, &job)
	if err != nil || !found {
		return false, err
	}
	if !identifier.MatchString(job.ID) || job.LeaseToken == "" || job.ExpiresAt.IsZero() || job.Request.Mode != c.Mode {
		return true, errors.New("controller returned an invalid worker lease")
	}
	execution, cancel := context.WithDeadline(ctx, job.ExpiresAt)
	defer cancel()
	var mu sync.Mutex
	log := ""
	result := make(chan Result, 1)
	go func() { result <- w.Run(execution, job, func(value string) { mu.Lock(); log = value; mu.Unlock() }) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var leaseErr error
	stopped := ctx.Done()
	for {
		select {
		case outcome := <-result:
			if leaseErr != nil {
				return true, leaseErr
			}
			var accepted struct {
				Accepted bool `json:"accepted"`
			}
			_, err = c.request(ctx, http.MethodPost, url.PathEscape(job.ID)+"/complete", Completion{LeaseToken: job.LeaseToken, Result: outcome}, &accepted)
			return true, err
		case <-ticker.C:
			if leaseErr != nil {
				continue
			}
			mu.Lock()
			current := log
			mu.Unlock()
			var reply struct {
				CancelRequested bool `json:"cancelRequested"`
			}
			_, err = c.request(ctx, http.MethodPost, url.PathEscape(job.ID)+"/heartbeat", Progress{LeaseToken: job.LeaseToken, Log: current}, &reply)
			if err != nil {
				leaseErr = err
				cancel()
			} else if reply.CancelRequested {
				cancel()
			}
		case <-stopped:
			stopped = nil
			cancel()
			leaseErr = ctx.Err()
		}
	}
}

func (c *Client) InvalidateSession() { c.session = edgeclient.Session{} }
