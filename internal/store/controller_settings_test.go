package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestControllerSettingsSQLite(t *testing.T) {
	testControllerSettings(t, filepath.Join(t.TempDir(), "settings.db"))
}

func TestControllerSettingsPostgres(t *testing.T) {
	dsn := os.Getenv("DISPATCH_SETTINGS_POSTGRES_URL")
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
