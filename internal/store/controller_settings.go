package store

import (
	"context"

	"github.com/doout/dispatch/internal/core"
)

func (s *SQLStore) GetControllerSettings(ctx context.Context) (core.ControllerSettings, error) {
	var settings core.ControllerSettings
	err := s.db.QueryRowContext(ctx, `SELECT operations_enabled FROM controller_settings WHERE id=1`).Scan(&settings.OperationsEnabled)
	return settings, err
}

func (s *SQLStore) SaveControllerSettings(ctx context.Context, settings core.ControllerSettings) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO controller_settings (id,operations_enabled) VALUES (1,?) ON CONFLICT (id) DO UPDATE SET operations_enabled=excluded.operations_enabled WHERE controller_settings.operations_enabled<>excluded.operations_enabled`), settings.OperationsEnabled)
	return err
}
