package installation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

func checkSetupPort(c Config) error {
	listener, err := net.Listen("tcp", net.JoinHostPort(c.Bind, fmt.Sprint(c.Port)))
	if err != nil {
		return fmt.Errorf("cannot use loopback port %d; choose an available --port: %w", c.Port, err)
	}
	return listener.Close()
}

// Retry only resources owned by this setup. A completed installation must keep
// its data volume; an unfinished one may still need its volume initialized.
func (m *Manager) resumeSetup(ctx context.Context, c Config, owner setupOwner) error {
	if err := m.noPendingTransfer(); err != nil {
		return err
	}
	if err := m.preflight(ctx, c.Socket); err != nil {
		return err
	}
	if err := m.checkCompose(c); err != nil {
		return err
	}
	if owner.InstallationID != "" {
		if err := m.verifyVolume(ctx, c); err != nil {
			return fmt.Errorf("installation data volume is unavailable; restore it before retrying setup: %w", err)
		}
	}
	ids, err := m.Run(ctx, "docker", "ps", "--all", "--filter", "label=com.docker.compose.project="+c.Name, "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(ids) {
		label, err := m.Run(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "`+managedLabel+`"}}`, id)
		if err != nil {
			return err
		}
		if label != c.ID {
			return errors.New("an unrelated container uses this Compose project; refusing to replace it")
		}
	}
	id, err := m.container(ctx, c)
	if err != nil && !errors.Is(err, errControllerMissing) {
		return err
	}
	if id != "" {
		image, err := m.Run(ctx, "docker", "inspect", "--format", "{{.Image}}", id)
		if err != nil {
			return err
		}
		if image != c.ImageID {
			return errors.New("controller image changed outside the installer")
		}
	}
	if owner.InstallationID == "" || id == "" {
		return m.Recover(ctx)
	}
	if err := m.waitHealthy(ctx, c); err != nil {
		return m.Recover(ctx)
	}
	return nil
}
