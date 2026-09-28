package workflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// A slot is held for the duration of the job, including image push. Waiting
// jobs can be cancelled without occupying builder capacity.
func (s *Service) acquireDockerBuilder(ctx context.Context, cacheKeys ...string) (core.Server, func(), error) {
	cacheKey := ""
	if len(cacheKeys) > 0 {
		cacheKey = cacheKeys[0]
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		servers, err := s.Store.ListServers(ctx)
		if err != nil {
			return core.Server{}, nil, err
		}
		builders := make([]core.Server, 0)
		for _, server := range servers {
			if server.Runtime == core.ServerRuntimeBuilder && server.State == "ready" && server.Builder != nil && server.Builder.MaxConcurrent > 0 {
				builders = append(builders, server)
			}
		}
		if len(builders) == 0 {
			return core.Server{}, nil, errors.New("no Docker builders are available; add a builder server")
		}
		s.mu.Lock()
		if s.builderSlots == nil {
			s.builderSlots = map[string]chan struct{}{}
		}
		if s.builderWake == nil {
			s.builderWake = make(chan struct{})
		}
		wake := s.builderWake
		var chosen core.Server
		var slot chan struct{}
		best := 2.0
		bestCache := ""
		for _, builder := range builders {
			current := s.builderSlots[builder.ID]
			if current == nil || cap(current) != builder.Builder.MaxConcurrent && len(current) == 0 {
				current = make(chan struct{}, builder.Builder.MaxConcurrent)
				s.builderSlots[builder.ID] = current
			}
			load := float64(len(current)) / float64(cap(current))
			score := builderCacheScore(cacheKey, builder)
			preferred := load < best
			if cacheKey != "" {
				preferred = score > bestCache
			}
			if len(current) < cap(current) && preferred {
				chosen, slot, best = builder, current, load
				bestCache = score
			}
		}
		if slot != nil {
			slot <- struct{}{}
		}
		s.mu.Unlock()
		if slot != nil {
			return chosen, func() {
				<-slot
				s.mu.Lock()
				close(s.builderWake)
				s.builderWake = make(chan struct{})
				s.mu.Unlock()
			}, nil
		}
		select {
		case <-ctx.Done():
			return core.Server{}, nil, ctx.Err()
		case <-wake:
		case <-ticker.C:
		}
	}
}

func (s *Service) dockerBuilderEnvironment(ctx context.Context, builder core.Server) ([]string, func(), error) {
	if s.Secrets == nil {
		return nil, nil, errors.New("secret resolution is not configured")
	}
	key, err := s.Secrets.Resolve(ctx, builder.Builder.SSHSecretID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve builder SSH key: %w", err)
	}
	defer clear(key)
	if _, err := ssh.ParsePrivateKey(key); err != nil {
		return nil, nil, errors.New("builder SSH key must be an unencrypted private key")
	}
	return dockerBuilderEnvironmentForKey(builder, key)
}

func dockerBuilderEnvironmentForKey(builder core.Server, key []byte) ([]string, func(), error) {
	parsed, err := url.Parse(builder.Address)
	if err != nil {
		return nil, nil, err
	}
	publicKey, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(builder.Builder.HostKey))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, nil, errors.New("invalid builder host key")
	}
	directory, err := os.MkdirTemp("", "dispatch-builder-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	keyPath := filepath.Join(directory, "identity")
	knownHostsPath := filepath.Join(directory, "known_hosts")
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	port := parsed.Port()
	if port == "" {
		port = "22"
	}
	host := knownhosts.Normalize(net.JoinHostPort(parsed.Hostname(), port))
	if err := os.WriteFile(knownHostsPath, []byte(host+" "+string(ssh.MarshalAuthorizedKey(publicKey))), 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	// Docker invokes ssh from PATH. Other SSH commands retain the controller's
	// normal identity, including repository operations in the job script.
	wrapper := "#!/bin/sh\ncase \" $* \" in\n  *\" docker system dial-stdio\"*) exec " + shellEscape(sshPath) + " -i " + shellEscape(keyPath) + " -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=" + shellEscape(knownHostsPath) + " \"$@\" ;;\n  *) exec " + shellEscape(sshPath) + " \"$@\" ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(directory, "ssh"), []byte(wrapper), 0o700); err != nil {
		cleanup()
		return nil, nil, err
	}
	return []string{"DOCKER_HOST=" + builder.Address, "DOCKER_CONTEXT=", "PATH=" + directory + string(os.PathListSeparator) + os.Getenv("PATH")}, cleanup, nil
}

func shellEscape(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
