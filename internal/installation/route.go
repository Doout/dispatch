package installation

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ConfigureRoute updates the installer's existing route fields so even an older
// updater can reproduce the same Compose project on the next upgrade.
func (m *Manager) ConfigureRoute(ctx context.Context, hostname, network, resolver string, clear bool) error {
	if clear && (hostname != "" || network != "" || resolver != "") {
		return errors.New("--clear cannot be combined with route settings")
	}
	if !clear && (hostname == "" || network == "") {
		return errors.New("provide --hostname and --proxy-network, or use --clear")
	}
	if err := m.noPendingTransfer(); err != nil {
		return err
	}
	previous, err := m.Load()
	if err != nil {
		return err
	}
	next := previous
	next.Hostname, next.ProxyNetwork, next.TLSResolver = hostname, network, resolver
	if err := validate(next); err != nil {
		return err
	}
	if err := m.preflight(ctx, previous.Socket); err != nil {
		return err
	}
	if err := m.checkManaged(ctx, previous); err != nil {
		return err
	}
	if previous.Hostname == hostname && previous.ProxyNetwork == network && previous.TLSResolver == resolver {
		fmt.Fprintln(m.Out, "Route already configured.")
		return nil
	}
	if network != "" {
		if _, err := m.Run(ctx, "docker", "network", "inspect", network); err != nil {
			return fmt.Errorf("check Traefik network %q: %w", network, err)
		}
	}
	// Keep the previous installation record until the replacement is healthy.
	// An interrupted change can therefore be undone by ordinary dispatch recover.
	if err = m.start(ctx, next); err == nil {
		next.UpdatedAt = time.Now().UTC()
		err = saveJSON(m.path("installation.json"), next)
	}
	if err != nil {
		if rollbackErr := m.start(ctx, previous); rollbackErr != nil {
			return fmt.Errorf("route change failed: %w; run dispatch recover to restore the previous configuration: %w", err, rollbackErr)
		}
		return fmt.Errorf("route change failed; restored the previous controller configuration: %w", err)
	}
	if clear {
		fmt.Fprintln(m.Out, "Traefik route removed.")
	} else {
		fmt.Fprintf(m.Out, "Traefik route configured for %s on network %s.\n", hostname, network)
	}
	return nil
}
