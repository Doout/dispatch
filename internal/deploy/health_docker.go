package deploy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

// CandidateTarget is the isolated candidate endpoint, before route publication.
// Host and Port are allocated by the runtime, never copied from an HTTP response.
type CandidateTarget struct {
	Container string
	Host      string
	Port      int
	Service   string
}

func (e DockerExecutor) CheckCandidateHealth(ctx context.Context, d core.Deployment, app core.App, _ core.Server, targets []CandidateTarget, progress Progress) error {
	return evaluateDeploymentHealth(ctx, d, app, func(checkCtx context.Context, check core.HealthCheck) HealthObservation {
		selected := []CandidateTarget{}
		for _, target := range targets {
			if check.Service == "" || target.Service == check.Service {
				selected = append(selected, target)
			}
		}
		if len(selected) == 0 {
			return HealthObservation{Unavailable: true}
		}
		if check.Kind == "container" {
			for _, target := range selected {
				if target.Container == "" {
					return HealthObservation{Unavailable: true}
				}
				var output boundedStorageOutput
				if e.command(checkCtx, nil, &output, "docker", "inspect", "--format", `{"running":{{json .State.Running}},"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}"none"{{end}}}`, target.Container) != nil {
					return HealthObservation{Unavailable: true}
				}
				var state struct {
					Running bool
					Health  string
				}
				if json.Unmarshal([]byte(output.String()), &state) != nil {
					return HealthObservation{Unavailable: true}
				}
				if !state.Running || state.Health != "none" && state.Health != "healthy" {
					return HealthObservation{}
				}
			}
			return HealthObservation{Passed: true}
		}
		if check.Kind == "tls" {
			return probeCertificate(checkCtx, app.Domain, check.Port)
		}
		reachable := []CandidateTarget{}
		for _, target := range selected {
			if target.Host != "" && target.Port > 0 {
				reachable = append(reachable, target)
			}
		}
		if len(reachable) != 1 {
			return HealthObservation{Unavailable: true}
		}
		target := reachable[0]
		if check.Port > 0 && check.Port != app.ContainerPort && check.Port != target.Port {
			// A mapped host port only represents its one captured container port.
			return HealthObservation{Unavailable: true}
		}
		return probeEndpoint(checkCtx, check, target.Host, target.Port, app.Domain)
	}, progress)
}

func probeEndpoint(ctx context.Context, check core.HealthCheck, host string, port int, domain string) HealthObservation {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := &net.Dialer{}
	if check.Kind == "tcp" {
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return HealthObservation{}
		}
		_ = conn.Close()
		return HealthObservation{Passed: true}
	}
	if check.Kind != "http" {
		return HealthObservation{Unavailable: true}
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	endpoint := url.URL{Scheme: "http", Host: address, Path: check.Path}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return HealthObservation{Unavailable: true}
	}
	if check.Scope == "route" {
		name, ok := healthDomain(domain)
		if !ok {
			return HealthObservation{Unavailable: true}
		}
		req.Host = name
	}
	response, err := client.Do(req)
	if err != nil {
		return HealthObservation{}
	}
	// Neither bodies nor headers enter logs, stored evidence or error messages.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	return HealthObservation{Passed: response.StatusCode >= 200 && response.StatusCode < 300, HTTPStatus: response.StatusCode}
}

func healthDomain(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.User != nil {
			return "", false
		}
		value = parsed.Hostname()
	}
	if value == "" || strings.ContainsAny(value, "/?:#@\\ \r\n\x00") {
		return "", false
	}
	return value, true
}
func probeCertificate(ctx context.Context, domain string, port int) HealthObservation {
	host, ok := healthDomain(domain)
	if !ok {
		return HealthObservation{Unavailable: true}
	}
	if port == 0 {
		port = 443
	}
	dialer := tls.Dialer{Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return HealthObservation{}
	}
	_ = conn.Close()
	return HealthObservation{Passed: true}
}

// checkApplicationHealth covers private workloads without a managed route. It
// observes their actual containers; managed deployments supply staged targets
// directly to CheckCandidateHealth before publishing traffic.
func (e DockerExecutor) checkApplicationHealth(ctx context.Context, d core.Deployment, app core.App, server core.Server, progress Progress) error {
	ids := []string{dockerResourceName(app.ID)}
	if app.BuildType == core.BuildTypeCompose {
		var output boundedStorageOutput
		if e.command(ctx, nil, &output, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+dockerResourceName(app.ID)) != nil {
			return evaluateDeploymentHealth(ctx, d, app, nil, progress)
		}
		ids = strings.Fields(output.String())
	}
	targets := []CandidateTarget{}
	for _, id := range ids {
		target := CandidateTarget{Container: id, Host: "127.0.0.1", Port: app.ContainerPort}
		if app.BuildType == core.BuildTypeCompose {
			var output boundedStorageOutput
			if e.command(ctx, nil, &output, "docker", "inspect", "--format", `{"service":{{json (index .Config.Labels "com.docker.compose.service")}},"networks":{{json .NetworkSettings.Networks}}}`, id) != nil {
				return evaluateDeploymentHealth(ctx, d, app, nil, progress)
			}
			var details struct {
				Service  string
				Networks map[string]struct{ IPAddress string }
			}
			if json.Unmarshal([]byte(output.String()), &details) != nil {
				return evaluateDeploymentHealth(ctx, d, app, nil, progress)
			}
			target.Service = details.Service
			target.Host = ""
			networkNames := make([]string, 0, len(details.Networks))
			for name := range details.Networks {
				networkNames = append(networkNames, name)
			}
			sort.Strings(networkNames)
			for _, name := range networkNames {
				if address := details.Networks[name].IPAddress; address != "" {
					target.Host = address
					break
				}
			}
			policy := d.Health.Policy
			if policy.TimeoutSeconds == 0 {
				policy = app.HealthPolicy
			}
			for _, check := range policy.Checks {
				if check.Port > 0 && (check.Service == "" || check.Service == target.Service) {
					target.Port = check.Port
					break
				}
			}
		}
		targets = append(targets, target)
	}
	return e.CheckCandidateHealth(ctx, d, app, server, targets, progress)
}
