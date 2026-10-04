package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
)

// Golden scopes are the persisted encryption contract, independent of the
// visitor's source definitions. Rollback copies use the original artifact scope.
var controllerCipherFixtures = []struct{ table, column, scope string }{
	{"secrets", "encrypted_value", "secret:saved"},
	{"secret_stores", "encrypted_credentials", "secret-store:saved"},
	{"servers", "relay_access_token", "relay-server:saved:access-token"},
	{"github_apps", "encrypted_private_key", "github-app:saved:private-key"},
	{"github_apps", "encrypted_webhook_secret", "github-app:saved:webhook-secret"},
	{"auth_providers", "encrypted_client_secret", "auth-provider:saved"},
	{"private_networks", "encrypted_credentials", "laneway-network:saved"},
	{"laneway_applications", "encrypted_client_secret", "laneway-application:saved"},
	{"laneway_authorization_transactions", "encrypted_code_verifier", "laneway-authorization:state-hash"},
	{"edge_jobs", "encrypted_request", "edge-job:saved:request"},
	{"edge_jobs", "encrypted_response", "edge-job:saved:response"},
	{"deployment_drift_baselines", "ciphertext", "deployment-drift:copy"},
	{"application_observation_configs", "webhook_ciphertext", "observation-webhook:app"},
	{"deployment_runtime_artifacts", "ciphertext", "deployment-runtime:original:app:server"},
	{"runtime_jobs", "encrypted_request", "runtime-job:saved:request"},
	{"runtime_jobs", "encrypted_result", "runtime-job:saved:result"},
	{"infrastructure_reviews", "encrypted_request", "infrastructure-review:saved"},
	{"infrastructure_snapshot_reviews", "encrypted_request", "snapshot-review:saved"},
	{"infrastructure_action_requests", "encrypted_request", "infrastructure-action:operation"},
	{"target_bootstraps", "encrypted_input", "target-bootstrap:saved"},
	{"service_resources", "request_cipher", "service-resource:run:request"},
	{"service_resources", "outputs_cipher", "service-resource:run:outputs"},
	{"workload_backups", "input_cipher", "workload-backup:saved:accepted"},
	{"workload_backup_operations", "input_cipher", "workload-backup:saved:operation"},
	{"workload_backup_policies", "input_cipher", "workload-backup:saved:policy"},
	{"webhook_deliveries", "ciphertext", "webhook-delivery:saved"},
}

func TestControllerCiphertextsCoverStoredColumnsAndFrozenCredentials(t *testing.T) {
	s, vault := controllerCipherFixture(t)
	ctx := context.Background()
	seen := map[string]int{}
	err := s.VisitControllerCiphertexts(ctx, func(scope, ciphertext string) error {
		plain, err := vault.Decrypt(scope, ciphertext)
		defer clear(plain)
		if err != nil {
			return errors.New("ciphertext did not authenticate against its saved identity")
		}
		seen[scope]++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{}
	for _, fixture := range controllerCipherFixtures {
		want[fixture.scope]++
	}
	want["service:service:password"] = 2
	want["secret:deleted-secret"] = 1
	want["secret:rotated-secret"] = 3
	if !reflect.DeepEqual(seen, want) {
		t.Fatal("controller ciphertext inventory omitted or misidentified saved data")
	}

	// New cipher columns must join the inventory when a migration adds them.
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	var columns []string
	for _, table := range tables {
		info, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
		if err != nil {
			t.Fatal(err)
		}
		for info.Next() {
			var index, required, primary int
			var column, kind string
			var defaultValue any
			if err := info.Scan(&index, &column, &kind, &required, &defaultValue, &primary); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(column, "cipher") && !strings.HasSuffix(column, "_digest") || strings.HasPrefix(column, "encrypted_") || column == "relay_access_token" {
				columns = append(columns, table+"."+column)
			}
		}
		if err := info.Err(); err != nil {
			t.Fatal(err)
		}
		info.Close()
	}
	var covered []string
	for _, source := range controllerCipherSources {
		covered = append(covered, source.table+"."+source.column)
	}
	sort.Strings(columns)
	sort.Strings(covered)
	if !reflect.DeepEqual(columns, covered) {
		t.Fatalf("persisted cipher columns differ from verification inventory: schema=%v inventory=%v", columns, covered)
	}
}

func TestControllerCiphertextsSkipErasedPayloadsAndStopOnFailure(t *testing.T) {
	s, _ := controllerCipherFixture(t)
	ctx := context.Background()
	failure := errors.New("verification rejected")
	calls := 0
	if err := s.VisitControllerCiphertexts(ctx, func(string, string) error {
		calls++
		return failure
	}); !errors.Is(err, failure) || calls != 1 {
		t.Fatal("visitor continued after verification failure")
	}
	for _, fixture := range controllerCipherFixtures {
		if _, err := s.db.ExecContext(ctx, `UPDATE `+fixture.table+` SET `+fixture.column+`=''`); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"services", "deployment_service_bindings"} {
		if _, err := s.db.ExecContext(ctx, `UPDATE `+table+` SET payload='{}'`); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"apps", "preview_environments", "preview_group_runs"} {
		if _, err := s.db.ExecContext(ctx, `UPDATE `+table+` SET hook_environment='{}'`); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.VisitControllerCiphertexts(ctx, func(string, string) error {
		return errors.New("erased ciphertext was visited")
	}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.VisitControllerCiphertexts(cancelled, func(string, string) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal("visitor ignored cancellation")
	}
}

func TestControllerCiphertextsRejectWrongRecordScopes(t *testing.T) {
	s, vault := controllerCipherFixture(t)
	ctx := context.Background()
	verify := func(scope, ciphertext string) error {
		plain, err := vault.Decrypt(scope, ciphertext)
		clear(plain)
		if err != nil {
			return errors.New("unreadable controller ciphertext")
		}
		return nil
	}
	for _, fixture := range controllerCipherFixtures {
		t.Run(fixture.table+"/"+fixture.column, func(t *testing.T) {
			wrong, err := vault.Encrypt(fixture.scope+":another-record", []byte("private data"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE `+fixture.table+` SET `+fixture.column+`=?`, wrong); err != nil {
				t.Fatal(err)
			}
			if err := s.VisitControllerCiphertexts(ctx, verify); err == nil {
				t.Fatal("record accepted ciphertext from another identity")
			}
			valid, err := vault.Encrypt(fixture.scope, []byte("private data"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE `+fixture.table+` SET `+fixture.column+`=?`, valid); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, payload := range []string{
		jsonText(storedCapture{Service: storedService{Credentials: map[string]string{"password": "unreadable-frozen-credential"}}}),
		jsonText(storedCapture{Service: storedService{CapturedSecrets: map[string]capturedSecret{"token": {ID: "deleted-secret", Cipher: "unreadable-frozen-secret"}}}}),
		"{invalid",
	} {
		if _, err := s.db.ExecContext(ctx, `UPDATE deployment_service_bindings SET payload=?`, payload); err != nil {
			t.Fatal(err)
		}
		if err := s.VisitControllerCiphertexts(ctx, verify); err == nil {
			t.Fatal("unreadable frozen deployment credentials were omitted")
		}
	}
}

func TestControllerCiphertextsVerifyFrozenHookSecretsAfterRotation(t *testing.T) {
	s, vault := controllerCipherFixture(t)
	ctx := context.Background()
	verify := func(scope, ciphertext string) error {
		plain, err := vault.Decrypt(scope, ciphertext)
		clear(plain)
		return err
	}
	// The current secret is readable after rotation. Every retained hook copy
	// must still authenticate independently under its original secret identity.
	current, err := vault.Encrypt("secret:rotated-secret", []byte("new-value"))
	if err != nil {
		t.Fatal(err)
	}
	insertControllerCipherRow(t, s, "secrets", map[string]any{"id": "rotated-secret", "encrypted_value": current, "name": "rotated-secret", "environment_variable": "ROTATED_TOKEN"})
	for _, table := range []string{"apps", "preview_environments", "preview_group_runs"} {
		t.Run(table, func(t *testing.T) {
			for _, payload := range []string{
				jsonText(map[string]string{core.SecretEnvironmentKey("rotated-secret", "TOKEN"): "unreadable-frozen-value"}),
				"{invalid",
			} {
				if _, err := s.db.ExecContext(ctx, `UPDATE `+table+` SET hook_environment=?`, payload); err != nil {
					t.Fatal(err)
				}
				if err := s.VisitControllerCiphertexts(ctx, verify); err == nil {
					t.Fatal("unreadable frozen hook data escaped verification")
				}
			}
			// Ordinary hook values are plaintext, including malformed secret keys.
			if _, err := s.db.ExecContext(ctx, `UPDATE `+table+` SET hook_environment=?`, jsonText(map[string]string{"LOG_LEVEL": "info", core.SecretEnvironmentPrefix + "invalid": "plaintext", core.SecretEnvironmentKey("rotated-secret", "TOKEN"): ""})); err != nil {
				t.Fatal(err)
			}
			if err := s.VisitControllerCiphertexts(ctx, verify); err != nil {
				t.Fatal("ordinary or erased hook values were treated as ciphertext")
			}
		})
	}
}

func controllerCipherFixture(t *testing.T) (*SQLStore, *secretcrypto.Vault) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, filepath.Join(dir, "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// The fixture isolates encryption inventory from lifecycle admission. Each
	// row still uses the migrated table and its constraints and column types.
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "master.key")
	if err := os.WriteFile(key, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(key)
	if err != nil {
		t.Fatal(err)
	}
	inserted := map[string]bool{}
	for _, fixture := range controllerCipherFixtures {
		if !inserted[fixture.table] {
			insertControllerCipherRow(t, s, fixture.table, nil)
			inserted[fixture.table] = true
		}
		cipher, err := vault.Encrypt(fixture.scope, []byte("saved private data"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE `+fixture.table+` SET `+fixture.column+`=?`, cipher); err != nil {
			t.Fatal(err)
		}
	}
	current, err := vault.Encrypt("service:service:password", []byte("current-password"))
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := vault.Encrypt("service:service:password", []byte("original-password"))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := vault.Encrypt("secret:deleted-secret", []byte("frozen-secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	item := storedService{Service: core.Service{ID: "service"}, Credentials: map[string]string{"password": current}}
	insertControllerCipherRow(t, s, "services", map[string]any{"id": "service", "payload": jsonText(item)})
	item.Credentials["password"] = frozen
	item.CapturedSecrets = map[string]capturedSecret{"token": {ID: "deleted-secret", Cipher: secret}}
	insertControllerCipherRow(t, s, "deployment_service_bindings", map[string]any{"service_id": "service", "payload": jsonText(storedCapture{Service: item})})
	hook, err := vault.Encrypt("secret:rotated-secret", []byte("frozen-hook-value"))
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"apps", "preview_environments", "preview_group_runs"} {
		insertControllerCipherRow(t, s, table, map[string]any{"hook_environment": jsonText(map[string]string{core.SecretEnvironmentKey("rotated-secret", "TOKEN"): hook, "LOG_LEVEL": "info"})})
	}
	return s, vault
}

func insertControllerCipherRow(t *testing.T, s *SQLStore, table string, overrides map[string]any) {
	t.Helper()
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	var values []any
	for rows.Next() {
		var index, required, primary int
		var column, kind string
		var defaultValue any
		if err := rows.Scan(&index, &column, &kind, &required, &defaultValue, &primary); err != nil {
			t.Fatal(err)
		}
		value, overridden := overrides[column]
		if !overridden && defaultValue != nil || !overridden && required == 0 && primary == 0 {
			continue
		}
		if !overridden {
			value = "saved"
			if strings.Contains(kind, "INT") || kind == "BOOLEAN" {
				value = 1
			}
			switch column {
			case "deployment_id":
				value = "copy"
			case "scope_id":
				value = "original"
			case "app_id", "server_id", "run_id", "operation_id", "state_hash":
				value = strings.TrimSuffix(column, "_id")
				if column == "state_hash" {
					value = "state-hash"
				}
			case "provider_type":
				value = "github"
			case "provisioning":
				value = "existing"
			case "kind":
				value = "registration"
			case "state":
				value = "ready"
			case "payload", "record", "config", "details", "metadata":
				value = "{}"
			}
		}
		columns, values = append(columns, column), append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(columns) == 0 {
		t.Fatal("missing ciphertext fixture table")
	}
	query := `INSERT INTO ` + table + `(` + strings.Join(columns, ",") + `) VALUES(` + strings.TrimSuffix(strings.Repeat("?,", len(values)), ",") + `)`
	if _, err := s.db.ExecContext(ctx, query, values...); err != nil {
		t.Fatalf("ciphertext fixture %s: %v", table, err)
	}
}
