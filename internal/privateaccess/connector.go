package privateaccess

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const DriverLanewayConnector = "laneway_connector"

var (
	bootstrapCommandPattern = regexp.MustCompile(`^\s*curl\s+--fail\s+--silent\s+--show-error\s+--proto\s+'=https'\s+--tlsv1\.3\s+'(https://[^']+)'\s*\|\s*sudo\s+bash\s+-s\s+--\s+'([A-Za-z0-9_-]{43})'\s*$`)
	bootstrapPathPattern    = regexp.MustCompile(`^/\.well-known/laneway/bootstrap/[A-Za-z0-9_-]{43}$`)
	connectorNamePattern    = regexp.MustCompile(`(?m)^container_name='(laneway-connector-[A-Za-z0-9._-]+)'$`)
	safeContainerName       = regexp.MustCompile(`^laneway-connector-[A-Za-z0-9._-]+$`)
)

type ConnectorBootstrap struct {
	URL *url.URL
	Key string
}

func ValidateLanewayAuthority(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Enter the HTTPS origin for the Laneway control plane.")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func ParseConnectorBootstrap(command, authority string) (ConnectorBootstrap, error) {
	if len(command) > 4096 {
		return ConnectorBootstrap{}, errors.New("The Laneway bootstrap command is too long.")
	}
	match := bootstrapCommandPattern.FindStringSubmatch(command)
	if len(match) != 3 {
		return ConnectorBootstrap{}, errors.New("Paste the one-time Connector bootstrap printed by Laneway.")
	}
	parsed, err := url.Parse(match[1])
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !bootstrapPathPattern.MatchString(parsed.EscapedPath()) {
		return ConnectorBootstrap{}, errors.New("The Laneway bootstrap URL is invalid.")
	}
	expected, err := ValidateLanewayAuthority(authority)
	if err != nil {
		return ConnectorBootstrap{}, err
	}
	if !strings.EqualFold(parsed.Scheme+"://"+parsed.Host, expected) {
		return ConnectorBootstrap{}, errors.New("The bootstrap command belongs to another Laneway control plane.")
	}
	return ConnectorBootstrap{URL: parsed, Key: match[2]}, nil
}

func InstallConnector(ctx context.Context, bootstrap ConnectorBootstrap) (string, error) {
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("Laneway bootstrap redirects are not allowed")
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, bootstrap.URL.String(), nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download Laneway bootstrap: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download Laneway bootstrap: HTTP %d", response.StatusCode)
	}
	script, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return "", fmt.Errorf("read Laneway bootstrap: %w", err)
	}
	if len(script) == 0 || len(script) > 1<<20 || !bytes.HasPrefix(script, []byte("#!/bin/bash\n")) {
		return "", errors.New("Laneway returned an invalid Connector bootstrap.")
	}
	nameMatch := connectorNamePattern.FindSubmatch(script)
	if len(nameMatch) != 2 {
		return "", errors.New("Laneway returned a bootstrap without a Connector name.")
	}
	containerName := string(nameMatch[1])
	installCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(installCtx, "bash", "-s", "--", bootstrap.Key)
	command.Stdin = bytes.NewReader(script)
	command.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/tmp", "TMPDIR=/tmp"}
	if output, err := command.CombinedOutput(); err != nil {
		_ = output
		return "", errors.New("Laneway Connector bootstrap failed. Create a new invite and try again.")
	}
	return containerName, nil
}

func ConnectorStatus(ctx context.Context, containerName string) (string, error) {
	if !safeContainerName.MatchString(containerName) {
		return "", errors.New("Laneway Connector name is invalid")
	}
	checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	output, err := exec.CommandContext(checkCtx, "docker", "inspect", "--format", `{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}`, containerName).Output()
	if err != nil {
		return "", errors.New("Laneway Connector is not running")
	}
	state := strings.TrimSpace(string(output))
	if state != "healthy" && state != "running" {
		return state, fmt.Errorf("Laneway Connector is %s", state)
	}
	return state, nil
}

func RemoveConnector(ctx context.Context, containerName string) error {
	if !safeContainerName.MatchString(containerName) {
		return errors.New("Laneway Connector name is invalid")
	}
	removeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(removeCtx, "docker", "rm", "--force", containerName).CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(output))
		if strings.Contains(detail, "No such container") {
			return nil
		}
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("remove Laneway Connector: %s", detail)
	}
	return nil
}
