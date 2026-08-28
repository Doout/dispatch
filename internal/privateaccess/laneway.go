package privateaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DriverLaneway        = "laneway"
	DefaultLanewaySocket = "/run/laneway/lanewayd.sock"
	maxResponseBytes     = 1 << 20
)

type LanewayStatus struct {
	Running        bool     `json:"running"`
	ProductVersion string   `json:"product_version"`
	SelectedPath   string   `json:"selected_path"`
	NetworkID      string   `json:"network_id"`
	Name           string   `json:"name"`
	SelectedRoutes []string `json:"selected_routes"`
	Controller     struct {
		ConfigurationLeaseExpired bool `json:"configuration_lease_expired"`
	} `json:"controller"`
}

type LanewayRoute struct {
	Prefix  string `json:"prefix"`
	ViaNode string `json:"via_node"`
	Kind    string `json:"kind"`
}

type LanewaySnapshot struct {
	Status LanewayStatus
	Routes []LanewayRoute
}

func CheckLaneway(ctx context.Context, socketPath string) (LanewaySnapshot, error) {
	socketPath, err := ValidateSocketPath(socketPath)
	if err != nil {
		return LanewaySnapshot{}, err
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	var snapshot LanewaySnapshot
	if err := getJSON(ctx, client, "/v1/status", &snapshot.Status); err != nil {
		return snapshot, fmt.Errorf("read Laneway status: %w", err)
	}
	if !snapshot.Status.Running {
		return snapshot, errors.New("Laneway is not connected")
	}
	if snapshot.Status.NetworkID == "" {
		return snapshot, errors.New("Laneway has no active network")
	}
	if snapshot.Status.Controller.ConfigurationLeaseExpired {
		return snapshot, errors.New("Laneway configuration lease has expired")
	}
	if err := getJSON(ctx, client, "/v1/routes", &snapshot.Routes); err != nil {
		return snapshot, fmt.Errorf("read Laneway routes: %w", err)
	}
	return snapshot, nil
}

func ValidateSocketPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = DefaultLanewaySocket
	}
	cleaned := filepath.Clean(value)
	if !filepath.IsAbs(cleaned) {
		return "", errors.New("Laneway socket path must be absolute")
	}
	return cleaned, nil
}

func RouteCovers(routes []LanewayRoute, address netip.Addr) bool {
	for _, route := range routes {
		prefix, err := netip.ParsePrefix(route.Prefix)
		if err == nil && prefix.Contains(address) {
			return true
		}
	}
	return false
}

func LanewayDetails(snapshot LanewaySnapshot) map[string]string {
	return map[string]string{
		"networkId":  snapshot.Status.NetworkID,
		"nodeName":   snapshot.Status.Name,
		"path":       snapshot.Status.SelectedPath,
		"version":    snapshot.Status.ProductVersion,
		"routeCount": strconv.Itoa(len(snapshot.Routes)),
	}
}

func getJSON(ctx context.Context, client *http.Client, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://lanewayd"+path, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid JSON response")
	}
	return nil
}
