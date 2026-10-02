package routing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/doout/dispatch/internal/core"
	"net"
	"regexp"
	"strings"
	"time"
)

var ErrConflict = errors.New("application route ownership conflict")
var label = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var configName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func NormalizeHostname(raw string) (string, error) {
	hostname := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if len(hostname) > 253 || net.ParseIP(hostname) != nil || !strings.Contains(hostname, ".") {
		return "", errors.New("routing requires a DNS hostname")
	}
	for _, part := range strings.Split(hostname, ".") {
		if !label.MatchString(part) {
			return "", errors.New("routing hostname contains an invalid DNS label")
		}
	}
	return hostname, nil
}

func ValidateConfig(config *core.RoutingConfig) error {
	if config == nil {
		return nil
	}
	domain, err := NormalizeHostname(config.BaseDomain)
	if err != nil {
		return fmt.Errorf("routing base domain: %w", err)
	}
	config.BaseDomain = domain
	if config.EntryPoint == "" {
		if config.RequireTLS {
			config.EntryPoint = "websecure"
		} else {
			config.EntryPoint = "web"
		}
	}
	if !configName.MatchString(config.EntryPoint) || config.TLSResolver != "" && !configName.MatchString(config.TLSResolver) {
		return errors.New("routing entry point and certificate resolver must be simple names")
	}
	if config.RequireTLS && config.TLSResolver == "" {
		return errors.New("required HTTPS routing needs a configured certificate resolver")
	}
	if config.ComposeService != "" && !configName.MatchString(config.ComposeService) {
		return errors.New("Compose ingress service must be a simple service name")
	}
	return nil
}

func Plan(d core.Deployment, app core.App, server core.Server) (*core.ApplicationRoute, error) {
	if app.BuildType == core.BuildTypeHelm {
		return nil, nil
	}
	if server.Routing == nil {
		if app.Domain != "" {
			return nil, errors.New("this target has no managed routing zone; configure its Traefik provider before requesting a public hostname")
		}
		return nil, nil
	}
	config := *server.Routing
	if err := ValidateConfig(&config); err != nil {
		return nil, err
	}
	if app.ContainerPort < 1 || app.ContainerPort > 65535 {
		return nil, errors.New("managed routing requires a container port between 1 and 65535")
	}
	hostname := app.Domain
	if hostname == "" {
		sum := sha256.Sum256([]byte(app.ProjectID + ":" + app.ID))
		hostname = "app-" + hex.EncodeToString(sum[:8]) + "." + config.BaseDomain
	}
	hostname, err := NormalizeHostname(hostname)
	if err != nil {
		return nil, err
	}
	if hostname != config.BaseDomain && !strings.HasSuffix(hostname, "."+config.BaseDomain) {
		return nil, errors.New("hostname is outside this target's managed routing zone")
	}
	cert := core.RouteCertificate{State: "not_required", Message: "HTTPS is not required by this route."}
	if config.RequireTLS {
		cert = core.RouteCertificate{State: "pending", Message: "Certificate issuance has not been verified."}
	}
	return &core.ApplicationRoute{AppID: app.ID, ProjectID: app.ProjectID, ServerID: server.ID, Hostname: hostname, EntryPoint: config.EntryPoint, TLSResolver: config.TLSResolver, RequireTLS: config.RequireTLS, RequestedDeploymentID: d.ID, State: "reserved", DNS: "pending", Certificate: cert, Message: "Hostname reserved; candidate is not public.", UpdatedAt: time.Now().UTC()}, nil
}
