package core

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// HealthPolicy is captured with the accepted deployment. Checks contain no
// credentials or response bodies, so policy and evidence can be shown safely.
type HealthPolicy struct {
	TimeoutSeconds      int           `json:"timeoutSeconds"`
	CheckTimeoutSeconds int           `json:"checkTimeoutSeconds"`
	IntervalSeconds     int           `json:"intervalSeconds"`
	FailureThreshold    int           `json:"failureThreshold"`
	Checks              []HealthCheck `json:"checks"`
}
type HealthCheck struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Scope   string `json:"scope"`
	Service string `json:"service,omitempty"`
	Port    int    `json:"port,omitempty"`
	Path    string `json:"path,omitempty"`
}
type HealthCheckResult struct {
	Check      HealthCheck `json:"check"`
	State      string      `json:"state"`
	Attempts   int         `json:"attempts"`
	Failures   int         `json:"failures"`
	Message    string      `json:"message"`
	HTTPStatus int         `json:"httpStatus,omitempty"`
}
type DeploymentHealth struct {
	Simulated  bool                `json:"simulated,omitempty"`
	Policy     HealthPolicy        `json:"policy"`
	State      string              `json:"state"`
	Checks     []HealthCheckResult `json:"checks"`
	StartedAt  *time.Time          `json:"startedAt,omitempty"`
	FinishedAt *time.Time          `json:"finishedAt,omitempty"`
}

var healthName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

func NormalizeHealthPolicy(p HealthPolicy) (HealthPolicy, error) {
	if p.TimeoutSeconds == 0 {
		p.TimeoutSeconds = 120
	}
	if p.CheckTimeoutSeconds == 0 {
		p.CheckTimeoutSeconds = 5
	}
	if p.IntervalSeconds == 0 {
		p.IntervalSeconds = 2
	}
	if p.FailureThreshold == 0 {
		p.FailureThreshold = 3
	}
	if p.TimeoutSeconds < 1 || p.TimeoutSeconds > 600 || p.CheckTimeoutSeconds < 1 || p.CheckTimeoutSeconds > 60 || p.IntervalSeconds < 1 || p.IntervalSeconds > 60 || p.FailureThreshold < 1 || p.FailureThreshold > 20 || len(p.Checks) > 17 {
		return p, errors.New("health checks require timeout 1–600 seconds, check timeout and interval 1–60 seconds, failure threshold 1–20, and at most 16 checks")
	}
	checks := []HealthCheck{{ID: "workload-readiness", Kind: "container", Scope: "workload"}}
	ids := map[string]bool{"workload-readiness": true}
	for _, c := range p.Checks {
		if c == checks[0] {
			continue
		}
		if !healthName.MatchString(c.ID) || ids[c.ID] || c.Service != "" && !healthName.MatchString(c.Service) {
			return p, errors.New("health check IDs and service names must be unique safe names")
		}
		ids[c.ID] = true
		if c.Scope == "" {
			c.Scope = "workload"
		}
		if c.Scope != "workload" && c.Scope != "route" && c.Scope != "certificate" {
			return p, errors.New("health check scope must be workload, route or certificate")
		}
		if c.Kind != "container" && c.Kind != "http" && c.Kind != "tcp" && c.Kind != "tls" {
			return p, errors.New("health check kind must be container, http, tcp or tls")
		}
		if c.Kind == "container" && c.Scope != "workload" || c.Kind == "tls" && c.Scope != "certificate" || c.Scope == "certificate" && c.Kind != "tls" {
			return p, errors.New("container checks require workload scope; TLS checks require certificate scope")
		}
		if c.Port < 0 || c.Port > 65535 || c.Kind == "container" && (c.Port != 0 || c.Path != "") {
			return p, errors.New("health check port must be between 1 and 65535, or omitted")
		}
		if c.Kind == "http" {
			if c.Path == "" {
				c.Path = "/"
			}
			if !strings.HasPrefix(c.Path, "/") || strings.HasPrefix(c.Path, "//") || strings.ContainsAny(c.Path, "?#\r\n\x00") || len(c.Path) > 1024 {
				return p, errors.New("HTTP health paths must be absolute paths without query, fragment or credentials")
			}
		} else if c.Path != "" {
			return p, errors.New("only HTTP checks accept a path")
		}
		checks = append(checks, c)
	}
	if len(checks) > 17 {
		return p, errors.New("health policies permit at most 16 configured checks")
	}
	p.Checks = checks
	return p, nil
}
