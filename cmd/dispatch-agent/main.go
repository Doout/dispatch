package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type status struct {
	APIVersion    string `json:"apiVersion"`
	AgentVersion  string `json:"agentVersion"`
	Architecture  string `json:"architecture"`
	DockerReady   bool   `json:"dockerReady"`
	DockerVersion string `json:"dockerVersion,omitempty"`
	CheckedAt     string `json:"checkedAt"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	controllerURL := strings.TrimRight(strings.TrimSpace(os.Getenv("DISPATCH_EDGE_CONTROLLER_URL")), "/")
	nodeID := strings.TrimSpace(os.Getenv("DISPATCH_EDGE_NODE_ID"))
	token := strings.TrimSpace(os.Getenv("DISPATCH_EDGE_TOKEN"))
	if controllerURL != "" || nodeID != "" || token != "" {
		if err := runEdge(logger, controllerURL, nodeID, token); err != nil {
			logger.Error("edge node stopped", "error", err)
			os.Exit(1)
		}
		return
	}
	addr := env("DISPATCH_AGENT_ADDR", "127.0.0.1:9090")
	token = os.Getenv("DISPATCH_AGENT_TOKEN")
	if token == "" && !strings.HasPrefix(addr, "127.0.0.1:") {
		logger.Error("DISPATCH_AGENT_TOKEN is required when binding beyond loopback")
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /v1/status", authorize(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		output, err := exec.CommandContext(r.Context(), "docker", "version", "--format", "{{.Server.Version}}").Output()
		item := status{APIVersion: "dispatch.agent/v1", AgentVersion: "dev", Architecture: runtime.GOOS + "/" + runtime.GOARCH, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
		if err == nil {
			item.DockerReady, item.DockerVersion = true, strings.TrimSpace(string(output))
		}
		write(w, http.StatusOK, item)
	})))
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	logger.Info("dispatch agent listening", "addr", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

type edgeRequest struct {
	Method  string              `json:"method"`
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers"`
	Body    []byte              `json:"body,omitempty"`
}

type edgeResponse struct {
	StatusCode int                 `json:"statusCode"`
	Headers    map[string][]string `json:"headers"`
	Body       []byte              `json:"body,omitempty"`
}

type edgeJob struct {
	Kind       string      `json:"kind"`
	ID         string      `json:"id"`
	LeaseToken string      `json:"leaseToken"`
	Request    edgeRequest `json:"request"`
}

type edgeCompletion struct {
	LeaseToken string        `json:"leaseToken"`
	Response   *edgeResponse `json:"response,omitempty"`
	Error      string        `json:"error,omitempty"`
}

func runEdge(logger *slog.Logger, controllerURL, nodeID, token string) error {
	parsed, err := url.Parse(controllerURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("DISPATCH_EDGE_CONTROLLER_URL must be an HTTPS origin")
	}
	if nodeID == "" {
		return errors.New("DISPATCH_EDGE_NODE_ID is required")
	}
	legacy := os.Getenv("DISPATCH_EDGE_LEGACY_TOKEN") == "true"
	if legacy && token == "" {
		return errors.New("Legacy mode requires DISPATCH_EDGE_TOKEN")
	}
	var identity edgeIdentity
	if !legacy {
		identity, err = loadIdentity(env("DISPATCH_EDGE_IDENTITY_FILE", "/var/lib/dispatch-edge/identity.json"), controllerURL, nodeID)
		if err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	control := &http.Client{Timeout: 35 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	outbound := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	logger.Info("edge node connected", "controller", parsed.Host, "node", nodeID)
	backoff := time.Second
	var session edgeSession
	if legacy {
		logger.Warn("edge node uses a legacy long-lived token; rotate enrollment to upgrade")
	}
	for ctx.Err() == nil {
		runtimeToken := token
		if !legacy {
			if session.Token == "" || time.Until(session.ExpiresAt) < time.Minute {
				next, e := obtainSession(ctx, control, identity, token)
				if e != nil {
					logger.Warn("edge authentication unavailable")
					select {
					case <-ctx.Done():
						return nil
					case <-time.After(backoff):
					}
					if backoff < 15*time.Second {
						backoff *= 2
					}
					continue
				}
				session = next
			}
			runtimeToken = session.Token
		}
		job, err := lease(ctx, control, controllerURL, nodeID, runtimeToken)
		if err != nil {
			logger.Warn("edge poll failed", "error", err)
			if errors.Is(err, errEdgeUnauthorized) {
				session = edgeSession{}
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if job == nil {
			continue
		}
		completion := edgeCompletion{LeaseToken: job.LeaseToken}
		var response *edgeResponse
		if job.Kind != "https_request" && !(legacy && job.Kind == "") {
			err = errors.New("Unsupported edge operation")
		} else {
			response, err = executeEdgeRequest(ctx, outbound, job.Request)
		}
		if err != nil {
			completion.Error = err.Error()
		} else {
			completion.Response = response
		}
		if err := complete(ctx, control, controllerURL, nodeID, runtimeToken, job.ID, completion); err != nil {
			logger.Warn("edge completion failed", "job", job.ID, "error", err)
		}
	}
	return nil
}

func lease(ctx context.Context, client *http.Client, controllerURL, nodeID, token string) (*edgeJob, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, controllerURL+"/api/v1/edge/nodes/"+url.PathEscape(nodeID)+"/jobs/next", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Dispatch-Agent-Version", "dev")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, responseError("poll controller", response)
	}
	var job edgeJob
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&job); err != nil || job.ID == "" || job.LeaseToken == "" {
		return nil, errors.New("controller returned an invalid edge job")
	}
	return &job, nil
}

func executeEdgeRequest(ctx context.Context, client *http.Client, input edgeRequest) (*edgeResponse, error) {
	parsed, err := url.Parse(input.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("edge job URL must use HTTPS")
	}
	if !validEdgeMethod(input.Method) || len(input.Body) > 1<<20 {
		return nil, errors.New("Unsupported edge method or excessive request size")
	}
	headerBytes := 0
	for name, values := range input.Headers {
		headerBytes += len(name)
		for _, value := range values {
			headerBytes += len(value)
		}
	}
	if headerBytes > 32<<10 {
		return nil, errors.New("Edge request headers exceed 32 KiB")
	}
	request, err := http.NewRequestWithContext(ctx, input.Method, parsed.String(), bytes.NewReader(input.Body))
	if err != nil {
		return nil, err
	}
	request.Header = http.Header(input.Headers).Clone()
	stripHopHeaders(request.Header)
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("Private HTTPS request failed; check endpoint reachability and TLS")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 2<<20 {
		return nil, errors.New("upstream response exceeds 2 MiB")
	}
	headers := response.Header.Clone()
	stripHopHeaders(headers)
	return &edgeResponse{StatusCode: response.StatusCode, Headers: headers, Body: body}, nil
}

func complete(ctx context.Context, client *http.Client, controllerURL, nodeID, token, jobID string, completion edgeCompletion) error {
	payload, err := json.Marshal(completion)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, controllerURL+"/api/v1/edge/nodes/"+url.PathEscape(nodeID)+"/jobs/"+url.PathEscape(jobID)+"/complete", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Dispatch-Agent-Version", "dev")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return responseError("complete edge job", response)
	}
	return nil
}

var errEdgeUnauthorized = errors.New("edge session expired or was revoked")

func responseError(action string, response *http.Response) error {
	if response.StatusCode == http.StatusUnauthorized {
		return errEdgeUnauthorized
	}
	return fmt.Errorf("%s: HTTP %d", action, response.StatusCode)
}

func stripHopHeaders(header http.Header) {
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}

func authorize(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
				write(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func write(w http.ResponseWriter, statusCode int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(value)
}
func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
