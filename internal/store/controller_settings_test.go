package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestControllerSettingsSQLite(t *testing.T) {
	testControllerSettings(t, filepath.Join(t.TempDir(), "settings.db"))
}

func TestControllerSettingsPostgres(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_SETTINGS_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_SETTINGS_POSTGRES_URL to a disposable database")
	}
	testControllerSettings(t, dsn)
}

func testControllerSettings(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	data, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if data != nil {
			data.Close()
		}
	})
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err := data.GetControllerSettings(ctx)
	if err != nil || settings.OperationsEnabled {
		t.Fatal("Operations must default to disabled", settings, err)
	}
	changes := data.Changes()
	if err = data.SaveControllerSettings(ctx, core.ControllerSettings{OperationsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	default:
		t.Fatal("saving settings did not notify overview subscribers")
	}
	if err = data.Close(); err != nil {
		t.Fatal(err)
	}
	data, err = Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err = data.GetControllerSettings(ctx)
	if err != nil || !settings.OperationsEnabled {
		t.Fatal("saved setting did not survive reopen and migration", settings, err)
	}
	changes = data.Changes()
	if err = data.SaveControllerSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
		t.Fatal("unchanged settings emitted a write notification")
	default:
	}
	if _, err = data.db.ExecContext(ctx, data.q(`INSERT INTO controller_settings (id,operations_enabled) VALUES (2,?)`), false); err == nil {
		t.Fatal("singleton settings accepted a second row")
	}
	if err = data.SaveControllerSettings(ctx, core.ControllerSettings{}); err != nil {
		t.Fatal(err)
	}
	settings, err = data.GetControllerSettings(ctx)
	if err != nil || settings.OperationsEnabled {
		t.Fatal("disabling Operations did not persist", settings, err)
	}
}

func TestControllerUIFeaturesSQLite(t *testing.T) {
	testControllerUIFeatures(t, filepath.Join(t.TempDir(), "features.db"))
}

func TestControllerUIFeaturesPostgres(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_SETTINGS_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_SETTINGS_POSTGRES_URL to a disposable database")
	}
	testControllerUIFeatures(t, dsn)
}

func testControllerUIFeatures(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	data, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	dialect := "sqlite"
	if data.postgres {
		dialect = "postgres"
	}
	steps := migrationSteps(t, dialect)
	featureMigration := -1
	for index, step := range steps {
		if step.version == "101_controller_ui_features" {
			featureMigration = index
			break
		}
	}
	if featureMigration < 0 {
		t.Fatal("UI feature migration is missing")
	}
	if err := data.applyMigrations(ctx, steps[:featureMigration]); err != nil {
		t.Fatal(err)
	}
	if _, err := data.db.ExecContext(ctx, `UPDATE controller_settings SET operations_enabled=TRUE WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err := data.GetControllerSettings(ctx)
	if err != nil || !settings.OperationsEnabled || len(settings.UIFeatures) != len(core.UIFeatureNames()) {
		t.Fatal("migration changed Operations or omitted UI defaults", settings, err)
	}
	for name, enabled := range settings.UIFeatures {
		if enabled {
			t.Fatal("UI feature enabled after upgrade", name)
		}
	}

	changes := data.Changes()
	settings, err = data.UpdateControllerSettings(ctx, core.ControllerSettingsPatch{UIFeatures: map[string]bool{"workloadBackups": true}})
	if err != nil || !settings.UIFeatures["workloadBackups"] || !settings.OperationsEnabled {
		t.Fatal("partial update changed unrelated settings", settings, err)
	}
	select {
	case <-changes:
	default:
		t.Fatal("UI feature save did not notify overview subscribers")
	}
	changes = data.Changes()
	if _, err := data.UpdateControllerSettings(ctx, core.ControllerSettingsPatch{UIFeatures: map[string]bool{"workloadBackups": true}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
		t.Fatal("unchanged UI features emitted a write notification")
	default:
	}
	if err := data.SaveControllerSettings(ctx, core.ControllerSettings{OperationsEnabled: false}); err != nil {
		t.Fatal(err)
	}
	settings, err = data.GetControllerSettings(ctx)
	if err != nil || settings.OperationsEnabled || !settings.UIFeatures["workloadBackups"] {
		t.Fatal("legacy save changed UI features", settings, err)
	}

	// Separate connections model concurrent controller processes and owners.
	other, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	results := make(chan error, len(core.UIFeatureNames()))
	for index, name := range core.UIFeatureNames() {
		writer := data
		if index%2 == 1 {
			writer = other
		}
		go func() {
			<-start
			_, err := writer.UpdateControllerSettings(ctx, core.ControllerSettingsPatch{UIFeatures: map[string]bool{name: true}})
			results <- err
		}()
	}
	close(start)
	for range core.UIFeatureNames() {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := data.Close(); err != nil {
		t.Fatal(err)
	}
	data, err = Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err = data.GetControllerSettings(ctx)
	if err != nil || settings.OperationsEnabled {
		t.Fatal("feature updates changed Operations", settings, err)
	}
	for _, name := range core.UIFeatureNames() {
		if !settings.UIFeatures[name] {
			t.Fatal("concurrent feature change lost or did not survive reopen", name)
		}
	}
	for _, patch := range []core.ControllerSettingsPatch{{}, {UIFeatures: map[string]bool{"unknown": true}}} {
		if _, err := data.UpdateControllerSettings(ctx, patch); err == nil {
			t.Fatal("invalid patch accepted", patch)
		}
	}
	settings, err = data.UpdateControllerSettings(ctx, core.ControllerSettingsPatch{UIFeatures: map[string]bool{"workloadBackups": false}})
	if err != nil || settings.UIFeatures["workloadBackups"] || !settings.UIFeatures["machineSnapshots"] {
		t.Fatal("disabling a feature changed another feature", settings, err)
	}
}
