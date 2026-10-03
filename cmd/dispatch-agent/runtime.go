package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/remoteruntime"
)

func runtimeRequest(ctx context.Context, client *http.Client, controller, node, token, method, path string, input, output any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, controller+"/api/v1/edge/nodes/"+url.PathEscape(node)+"/runtime/jobs/"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dispatch-Runtime-Version", remoteruntime.APIVersion)
	req.Header.Set("X-Dispatch-Runtime-Capabilities", remoteruntime.CapabilityHeader())
	req.Header.Set("X-Dispatch-Agent-Version", "dev")
	req.Header.Set("X-Dispatch-Agent-Artifact", runningArtifact())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return responseError("runtime operation", resp)
	}
	if output == nil {
		return errors.New("unexpected runtime response")
	}
	return json.NewDecoder(io.LimitReader(resp.Body, remoteruntime.MaxPayload+16384)).Decode(output)
}

func leaseRuntime(ctx context.Context, client *http.Client, controller, node, token string) (*remoteruntime.LeasedJob, error) {
	var job *remoteruntime.LeasedJob
	err := runtimeRequest(ctx, client, controller, node, token, http.MethodGet, "next", nil, &job)
	if err != nil {
		return nil, err
	}
	if job != nil && (job.ID == "" || job.LeaseToken == "") {
		return nil, errors.New("invalid leased runtime job")
	}
	return job, nil
}

var runtimeHeartbeatInterval = 10 * time.Second

type runtimeWorker interface {
	Run(context.Context, remoteruntime.LeasedJob, deploy.Progress) remoteruntime.Result
}

func executeRuntime(ctx context.Context, client *http.Client, controller, node string, token func(context.Context) (string, error), worker runtimeWorker, job remoteruntime.LeasedJob) error {
	execution, cancel := context.WithDeadline(ctx, job.ExpiresAt)
	defer cancel()
	if job.CancelRequested {
		cancel()
	}
	var mu sync.Mutex
	heartbeat := remoteruntime.Heartbeat{LeaseToken: job.LeaseToken}
	result := make(chan remoteruntime.Result, 1)
	go func() {
		result <- worker.Run(execution, job, func(phase core.DeploymentState, message string) error {
			if phase == core.DeploymentSucceeded {
				return execution.Err()
			}
			mu.Lock()
			heartbeat.Phase, heartbeat.Message = string(phase), message
			mu.Unlock()
			return execution.Err()
		})
	}()
	ticker := time.NewTicker(runtimeHeartbeatInterval)
	defer ticker.Stop()
	var connectionErr error
	stopped := execution.Done()
	for {
		select {
		case value := <-result:
			if connectionErr != nil {
				return connectionErr
			}
			current, err := token(ctx)
			if err != nil {
				return err
			}
			return runtimeRequest(ctx, client, controller, node, current, http.MethodPost, url.PathEscape(job.ID)+"/complete", remoteruntime.Completion{LeaseToken: job.LeaseToken, Result: value}, nil)
		case <-stopped:
			// Wait for the command process to stop before accepting another mutation.
			// The receipt stays unfinished if the process itself is terminated.
			stopped = nil
		case <-ticker.C:
			if connectionErr != nil {
				continue
			}
			current, err := token(ctx)
			if err == nil {
				mu.Lock()
				payload := heartbeat
				mu.Unlock()
				var state struct {
					CancelRequested bool `json:"cancelRequested"`
				}
				err = runtimeRequest(ctx, client, controller, node, current, http.MethodPost, url.PathEscape(job.ID)+"/heartbeat", payload, &state)
				if state.CancelRequested {
					cancel()
				}
			}
			if err != nil {
				connectionErr = err
				cancel()
			}
		}
	}
}
