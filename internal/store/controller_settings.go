package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

const controllerSettingsSelect = `SELECT operations_enabled,machine_provisioning,machine_snapshots,workload_backups,automation_credentials,infrastructure_assignments,mutation_receipts FROM controller_settings WHERE id=1`

var uiFeatureColumns = map[string]string{
	"machineProvisioning":       "machine_provisioning",
	"machineSnapshots":          "machine_snapshots",
	"workloadBackups":           "workload_backups",
	"automationCredentials":     "automation_credentials",
	"infrastructureAssignments": "infrastructure_assignments",
	"mutationReceipts":          "mutation_receipts",
}

func scanControllerSettings(row interface{ Scan(...any) error }) (core.ControllerSettings, error) {
	var settings core.ControllerSettings
	var provisioning, snapshots, backups, credentials, assignments, receipts bool
	err := row.Scan(&settings.OperationsEnabled, &provisioning, &snapshots, &backups, &credentials, &assignments, &receipts)
	settings.UIFeatures = map[string]bool{
		"machineProvisioning":       provisioning,
		"machineSnapshots":          snapshots,
		"workloadBackups":           backups,
		"automationCredentials":     credentials,
		"infrastructureAssignments": assignments,
		"mutationReceipts":          receipts,
	}
	return settings, err
}

func (s *SQLStore) GetControllerSettings(ctx context.Context) (core.ControllerSettings, error) {
	return scanControllerSettings(s.db.QueryRowContext(ctx, controllerSettingsSelect))
}

// SaveControllerSettings preserves UI flags when called by an older caller
// that supplies only OperationsEnabled.
func (s *SQLStore) SaveControllerSettings(ctx context.Context, settings core.ControllerSettings) error {
	_, err := s.UpdateControllerSettings(ctx, core.ControllerSettingsPatch{OperationsEnabled: &settings.OperationsEnabled, UIFeatures: settings.UIFeatures})
	return err
}

func (s *SQLStore) UpdateControllerSettings(ctx context.Context, patch core.ControllerSettingsPatch) (core.ControllerSettings, error) {
	var sets, differences []string
	var values []any
	add := func(column string, value bool) {
		sets = append(sets, column+"=?")
		differences = append(differences, column+"<>?")
		values = append(values, value)
	}
	if patch.OperationsEnabled != nil {
		add("operations_enabled", *patch.OperationsEnabled)
	}
	for name := range patch.UIFeatures {
		if !core.IsUIFeature(name) {
			return core.ControllerSettings{}, fmt.Errorf("unknown UI feature %q", name)
		}
	}
	for _, name := range core.UIFeatureNames() {
		if enabled, present := patch.UIFeatures[name]; present {
			add(uiFeatureColumns[name], enabled)
		}
	}
	if len(sets) == 0 {
		return core.ControllerSettings{}, fmt.Errorf("settings patch is empty")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ControllerSettings{}, err
	}
	defer tx.Rollback()
	// Each update assigns only supplied columns. The database serializes writes
	// to the singleton row without replacing flags saved by another owner.
	query := `UPDATE controller_settings SET ` + strings.Join(sets, ",") + ` WHERE id=1 AND (` + strings.Join(differences, " OR ") + `)`
	args := append(append([]any(nil), values...), values...)
	result, err := tx.ExecContext(ctx, s.q(query), args...)
	if err != nil {
		return core.ControllerSettings{}, err
	}
	settings, err := scanControllerSettings(tx.QueryRowContext(ctx, controllerSettingsSelect))
	if err != nil {
		return core.ControllerSettings{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return core.ControllerSettings{}, err
	}
	if changed == 0 {
		// Avoid waking overview subscribers when no stored value changed.
		return settings, tx.Rollback()
	}
	if err := tx.Commit(); err != nil {
		return core.ControllerSettings{}, err
	}
	return settings, nil
}
