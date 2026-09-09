package installation

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const updaterRoleLabel = "io.dispatch.role"

type automaticSchedule struct {
	NextCheck   time.Time `json:"nextCheck"`
	LastAttempt time.Time `json:"lastAttempt,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
}

func (m *Manager) updater(ctx context.Context, c Config) (string, error) {
	name := c.Name + "-updater"
	id, err := m.Run(ctx, "docker", "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
	if err != nil || id == "" {
		return id, err
	}
	label, err := m.Run(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "`+managedLabel+`"}} {{index .Config.Labels "`+updaterRoleLabel+`"}}`, id)
	if err != nil {
		return "", err
	}
	if label != c.ID+" updater" {
		return "", errors.New("an unmanaged container uses the updater name; refusing to replace it")
	}
	return id, nil
}

// AutoUpgrade manages a separate Docker worker. No host service or timer is used.
func (m *Manager) AutoUpgrade(ctx context.Context, action string, interval time.Duration, pull bool, registryConfig string) error {
	c, err := m.Load()
	if err != nil {
		return err
	}
	if err = m.preflight(ctx, c.Socket); err != nil {
		return err
	}
	id, err := m.updater(ctx, c)
	if err != nil {
		return err
	}
	switch action {
	case "status":
		if id == "" {
			fmt.Fprintln(m.Out, "Automatic upgrades: disabled")
			return nil
		}
		state, err := m.Run(ctx, "docker", "inspect", "--format", "{{.State.Status}}", id)
		if err != nil {
			return err
		}
		fmt.Fprintln(m.Out, "Automatic upgrades:", state)
		if data, err := os.ReadFile(m.path("automatic.json")); err == nil {
			var schedule automaticSchedule
			if json.Unmarshal(data, &schedule) == nil {
				fmt.Fprintln(m.Out, "Next check:", schedule.NextCheck.Format(time.RFC3339))
				if schedule.LastError != "" {
					fmt.Fprintln(m.Out, "Last check failed:", schedule.LastError)
				}
			}
		}
		return nil
	case "disable":
		if id != "" {
			if _, err = m.Run(ctx, "docker", "stop", "--time", "180", id); err != nil {
				return err
			}
			if _, err = m.Run(ctx, "docker", "rm", id); err != nil {
				return err
			}
		}
		fmt.Fprintln(m.Out, "Automatic upgrades disabled.")
		return nil
	case "enable":
		if strings.Contains(c.Image, "@sha256:") {
			return errors.New("automatic upgrades need a moving image tag; select an update channel with dispatch upgrade --image")
		}
		args := []string{"run", "--detach", "--name", c.Name + "-updater", "--restart", "unless-stopped", "--network", "host", "--user", "0", "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev", "--label", managedLabel + "=" + c.ID, "--label", updaterRoleLabel + "=updater", "--env", "DOCKER_HOST=unix://" + c.Socket, "--volume", m.Dir + ":" + m.Dir, "--volume", c.Socket + ":" + c.Socket}
		if registryConfig != "" {
			registryConfig, err = filepath.Abs(registryConfig)
			if err != nil {
				return err
			}
			if strings.ContainsAny(registryConfig, ":\n\r") {
				return errors.New("invalid Docker registry configuration directory")
			}
			data, err := os.ReadFile(filepath.Join(registryConfig, "config.json"))
			if err != nil {
				return err
			}
			var config struct {
				CredsStore  string            `json:"credsStore"`
				CredHelpers map[string]string `json:"credHelpers"`
			}
			if err = json.Unmarshal(data, &config); err != nil {
				return err
			}
			if config.CredsStore != "" || len(config.CredHelpers) > 0 {
				return errors.New("updater cannot use host credential helpers; provide a Docker config directory containing registry credentials")
			}
			args = append(args, "--volume", registryConfig+":/registry:ro", "--env", "DOCKER_CONFIG=/registry")
		}
		args = append(args, "--entrypoint", "/usr/local/bin/dispatch", c.ImageID, "auto-upgrade", "run", "--dir", m.Dir, "--interval", interval.String(), fmt.Sprintf("--pull=%t", pull), "--timeout", m.Timeout.String())
		if id != "" {
			if _, err = m.Run(ctx, "docker", "stop", "--time", "180", id); err != nil {
				return err
			}
			if _, err = m.Run(ctx, "docker", "rm", id); err != nil {
				return err
			}
		}
		if _, err = m.Run(ctx, "docker", args...); err != nil {
			return err
		}
		// Check startup without waiting for the first upgrade while holding the lock.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		id, err = m.updater(ctx, c)
		if err != nil {
			return err
		}
		if id == "" {
			return errors.New("updater container was not created")
		}
		state, err := m.Run(ctx, "docker", "inspect", "--format", "{{.State.Status}}", id)
		if err != nil {
			return err
		}
		if state != "running" {
			return fmt.Errorf("updater is %s; inspect docker logs %s-updater", state, c.Name)
		}
		fmt.Fprintf(m.Out, "Automatic upgrades enabled for %s. Checks every %s, with a small random delay.\nLogs: docker logs %s-updater\n", c.Image, interval, c.Name)
		return nil
	default:
		return errors.New("use auto-upgrade enable, disable, or status")
	}
}

// Each check acquires the same installation lock as the manual CLI. The lock is
// released between checks so the sleeping worker never blocks operator commands.
func (m *Manager) automaticCheck(ctx context.Context, interval time.Duration, pull bool) (time.Duration, error) {
	wait := time.Minute
	err := m.Locked(func() error {
		if _, err := os.Stat(m.path("upgrade.json")); err == nil {
			if err = m.Recover(ctx); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var schedule automaticSchedule
		if data, err := os.ReadFile(m.path("automatic.json")); err == nil {
			if err = json.Unmarshal(data, &schedule); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		now := time.Now().UTC()
		if schedule.NextCheck.After(now) {
			wait = time.Until(schedule.NextCheck)
			return nil
		}
		jitterLimit := min(interval/20, 30*time.Minute)
		jitter, err := rand.Int(rand.Reader, big.NewInt(int64(jitterLimit)+1))
		if err != nil {
			return err
		}
		wait = interval + time.Duration(jitter.Int64())
		schedule.LastAttempt = now
		schedule.NextCheck = now.Add(wait)
		schedule.LastError = ""
		// Persist before starting, so a restart cannot repeatedly retry a bad release.
		if err = saveJSON(m.path("automatic.json"), schedule); err != nil {
			return err
		}
		upgradeErr := m.Upgrade(ctx, "", pull, false)
		if upgradeErr != nil {
			schedule.LastError = upgradeErr.Error()
		}
		if err = saveJSON(m.path("automatic.json"), schedule); err != nil {
			return errors.Join(upgradeErr, err)
		}
		return upgradeErr
	})
	return wait, err
}
func (m *Manager) RunAutomatic(ctx context.Context, interval time.Duration, pull bool) error {
	// Let the enable command release its lock before the first check.
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		wait, err := m.automaticCheck(ctx, interval, pull)
		if err != nil {
			fmt.Fprintln(m.Out, "Automatic upgrade check:", err)
		}
		if ctx.Err() != nil {
			return nil
		}
		timer.Reset(wait)
	}
}
