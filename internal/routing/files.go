package routing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
)

const evidencePrefix = "# dispatch-route-v1: "

// FilePublisher controls a dedicated Traefik file-provider directory. It stores
// evidence in the same atomic file as the route, avoiding a second commit point.
// Use a directory mount with Traefik's watch option, not individual file mounts.
type FilePublisher struct{ Directory string }

func routeFile(appID string) string {
	sum := sha256.Sum256([]byte(appID))
	return "dispatch-" + hex.EncodeToString(sum[:16]) + ".yaml"
}

func (p *FilePublisher) locked(ctx context.Context, action func() error) error {
	if p == nil || p.Directory == "" || !filepath.IsAbs(p.Directory) {
		return errors.New("managed routing requires an operator-configured absolute provider directory")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(p.Directory, 0700); err != nil {
		return errors.New("cannot open the managed route provider directory")
	}
	lock, err := os.OpenFile(filepath.Join(p.Directory, ".dispatch.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return action()
}

func routeDocument(route core.ApplicationRoute) ([]byte, error) {
	record, err := json.Marshal(route)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSuffix(routeFile(route.AppID), ".yaml")
	servers := []map[string]string{}
	if route.Destination != "" {
		servers = append(servers, map[string]string{"url": route.Destination})
	}
	router := map[string]any{"middlewares": []string{name}, "rule": "Host(`" + route.Hostname + "`)", "entryPoints": []string{route.EntryPoint}, "service": name}
	if route.RequireTLS {
		router["tls"] = map[string]any{"certResolver": route.TLSResolver}
	}
	config := map[string]any{"http": map[string]any{"middlewares": map[string]any{name: map[string]any{"headers": map[string]any{"customResponseHeaders": map[string]string{"X-Dispatch-Deployment": route.DeploymentID}}}}, "routers": map[string]any{name: router}, "services": map[string]any{name: map[string]any{"loadBalancer": map[string]any{"passHostHeader": true, "servers": servers}}}}}
	body, err := yaml.Marshal(config)
	if err != nil {
		return nil, err
	}
	return append([]byte(evidencePrefix+base64.RawStdEncoding.EncodeToString(record)+"\n"), body...), nil
}

func (p *FilePublisher) read(appID string) (core.ApplicationRoute, error) {
	var route core.ApplicationRoute
	filename := filepath.Join(p.Directory, routeFile(appID))
	info, err := os.Lstat(filename)
	if err != nil {
		return route, err
	}
	if !info.Mode().IsRegular() || info.Size() > 256<<10 {
		return route, ErrConflict
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		return route, err
	}
	first, _, ok := bytes.Cut(raw, []byte("\n"))
	if !ok || !strings.HasPrefix(string(first), evidencePrefix) {
		return route, ErrConflict
	}
	record, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(string(first), evidencePrefix))
	if err != nil || json.Unmarshal(record, &route) != nil || route.AppID != appID {
		return route, ErrConflict
	}
	expected, err := routeDocument(route)
	if err != nil || !bytes.Equal(raw, expected) {
		return route, ErrConflict
	}
	return route, nil
}

func (p *FilePublisher) write(route core.ApplicationRoute) error {
	raw, err := routeDocument(route)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(p.Directory, ".route-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, filepath.Join(p.Directory, routeFile(route.AppID))); err != nil {
		return err
	}
	directory, err := os.Open(p.Directory)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func sameOwner(a, b core.ApplicationRoute) bool {
	return a.AppID == b.AppID && a.ProjectID == b.ProjectID && a.ServerID == b.ServerID && a.Hostname == b.Hostname
}

func (p *FilePublisher) Prepare(ctx context.Context, plan core.ApplicationRoute) (core.ApplicationRoute, error) {
	var result core.ApplicationRoute
	err := p.locked(ctx, func() error {
		entries, err := os.ReadDir(p.Directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if entry.IsDir() {
				return errors.New("the managed route directory must contain only Dispatch route files")
			}
			raw, err := os.ReadFile(filepath.Join(p.Directory, entry.Name()))
			if err != nil {
				return err
			}
			first, _, ok := bytes.Cut(raw, []byte("\n"))
			if !ok || !strings.HasPrefix(string(first), evidencePrefix) {
				return errors.New("the managed route directory contains configuration not owned by Dispatch")
			}
			record, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(string(first), evidencePrefix))
			var existing core.ApplicationRoute
			if err != nil || json.Unmarshal(record, &existing) != nil {
				return ErrConflict
			}
			if entry.Name() != routeFile(existing.AppID) {
				return ErrConflict
			}
			existing, err = p.read(existing.AppID)
			if err != nil {
				return err
			}
			if existing.Hostname == plan.Hostname && !sameOwner(existing, plan) {
				return ErrConflict
			}
		}
		current, err := p.read(plan.AppID)
		if err == nil {
			if !sameOwner(current, plan) {
				return fmt.Errorf("%w: remove the previous owned route before changing hostname or target", ErrConflict)
			}
			if current.DeploymentID != "" && (current.EntryPoint != plan.EntryPoint || current.RequireTLS != plan.RequireTLS || current.TLSResolver != plan.TLSResolver) {
				return fmt.Errorf("%w: clean up the owned route before changing its listener or TLS policy", ErrConflict)
			}
			if current.RequestedDeploymentID == plan.RequestedDeploymentID {
				result = current
				return nil
			}
			plan.DeploymentID, plan.Destination = current.DeploymentID, current.Destination
			plan.PreviousDeploymentID, plan.PreviousDestination = current.PreviousDeploymentID, current.PreviousDestination
			plan.PublishedAt = current.PublishedAt
			if current.Destination != "" {
				plan.State = "preparing"
				plan.Message = "Previous destination remains active while the candidate is checked."
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		plan.UpdatedAt = time.Now().UTC()
		result = plan
		return p.write(plan)
	})
	return result, err
}

func (p *FilePublisher) Promote(ctx context.Context, plan core.ApplicationRoute, destination string) (core.ApplicationRoute, error) {
	var result core.ApplicationRoute
	parsed, err := url.Parse(destination)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return result, errors.New("route destination must be an allocated loopback HTTP endpoint")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1024 || port > 65535 {
		return result, errors.New("route destination has an invalid allocated port")
	}
	err = p.locked(ctx, func() error {
		current, err := p.read(plan.AppID)
		if err != nil {
			return err
		}
		if !sameOwner(current, plan) || current.RequestedDeploymentID != plan.RequestedDeploymentID {
			return ErrConflict
		}
		if current.DeploymentID == plan.RequestedDeploymentID {
			if current.Destination != destination {
				return ErrConflict
			}
			result = current
			return nil
		}
		current.PreviousDeploymentID, current.PreviousDestination = current.DeploymentID, current.Destination
		now := time.Now().UTC()
		current.DeploymentID, current.Destination = plan.RequestedDeploymentID, destination
		current.PublishedAt = &now
		current.UpdatedAt = now
		current.State, current.Message = "published", "Candidate passed readiness; public endpoint verification is pending."
		current.DNS = "pending"
		if current.RequireTLS {
			current.Certificate = core.RouteCertificate{State: "pending", Message: "Waiting for public certificate verification."}
		}
		result = current
		return p.write(current)
	})
	return result, err
}

func (p *FilePublisher) Read(ctx context.Context, appID string) (core.ApplicationRoute, error) {
	var result core.ApplicationRoute
	err := p.locked(ctx, func() error { var err error; result, err = p.read(appID); return err })
	return result, err
}
func (p *FilePublisher) Remove(ctx context.Context, owner core.ApplicationRoute) error {
	return p.locked(ctx, func() error {
		current, err := p.read(owner.AppID)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !sameOwner(current, owner) {
			return ErrConflict
		}
		return os.Remove(filepath.Join(p.Directory, routeFile(owner.AppID)))
	})
}

func LoopbackDestination(port int) string {
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}
