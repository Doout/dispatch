package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

func TestBackupVerifiesRecoveryMaterialWithoutLegacySecrets(t *testing.T) {
	testRecoveryBackup(t, filepath.Join(t.TempDir(), "controller.db"), "")
}

func TestBackupRejectsUnreadableRecoveryMaterialAndClearsVerification(t *testing.T) {
	for _, domain := range []string{"service-request", "service-outputs", "backup-accepted", "backup-operation", "backup-policy", "bootstrap", "frozen-hook"} {
		t.Run(domain, func(t *testing.T) {
			testRecoveryBackup(t, filepath.Join(t.TempDir(), "controller.db"), domain)
		})
	}
}

func TestBackupPostgresRecoveryMaterialIntegration(t *testing.T) {
	dsn := os.Getenv("DISPATCH_BACKUP_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_BACKUP_POSTGRES_URL to a disposable PostgreSQL database with CREATE DATABASE permission")
	}
	for _, domain := range []string{"", "backup-operation", "backup-policy", "frozen-hook"} {
		name := domain
		if name == "" {
			name = "valid"
		}
		t.Run(name, func(t *testing.T) { testRecoveryBackup(t, postgresRecoveryDatabase(t, dsn), domain) })
	}
}

func postgresRecoveryDatabase(t *testing.T, dsn string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid disposable PostgreSQL connection")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("cannot open disposable PostgreSQL connection")
	}
	name := "backup_test_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE "`+name+`"`); err != nil {
		admin.Close()
		t.Fatal("cannot create disposable PostgreSQL database")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE "`+name+`" WITH (FORCE)`); err != nil {
			t.Error("cannot remove disposable PostgreSQL database")
		}
		admin.Close()
	})
	parsed.Path = "/" + name
	query := parsed.Query()
	query.Del("dbname")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func testRecoveryBackup(t *testing.T, dsn, corruptDomain string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "master.key")
	if err := os.WriteFile(key, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(key)
	if err != nil {
		t.Fatal(err)
	}
	seal := func(domain, scope string, value any) string {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		defer clear(raw)
		if domain == corruptDomain {
			// An authenticated value for another record is still unreadable here.
			scope += ":other-record"
		}
		cipher, err := vault.Encrypt(scope, raw)
		if err != nil {
			t.Fatal(err)
		}
		return cipher
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	prefix := strings.ToLower(ulid.Make().String())
	project := core.Project{ID: prefix + "-project", Name: prefix, CreatedAt: now}
	server := core.Server{ID: prefix + "-server", Name: prefix, Address: "local", Runtime: core.ServerRuntimeDocker, State: "ready", CreatedAt: now}
	must(s.CreateProject(ctx, project))
	must(s.CreateServer(ctx, server))
	run := core.ServiceProvisionRun{ID: prefix + "-run", TemplateID: "postgres", ProjectID: project.ID, ServiceName: "database", State: "queued", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: server.ID, ResourceName: prefix}, CreatedAt: now}
	request := core.ServiceProvisionRequest{Run: run, ServiceType: "postgresql", Password: "saved-service-password", Inputs: map[string]string{"database": "app", "username": "app"}}
	resource := core.ServiceResource{RunID: run.ID, ProjectID: project.ID, ServiceID: prefix + "-service", Name: run.ServiceName, Target: *run.Target, State: "accepted", Revision: 1, OperationID: run.ID, Policy: "retain", EncryptedRequest: seal("service-request", "service-resource:"+run.ID+":request", request), CreatedAt: now, UpdatedAt: now}
	must(s.CreateServiceResource(ctx, run, resource))
	resource, err = s.ClaimServiceResource(ctx, run.ID, 1, run.ID, "provisioning", "initial", now, "")
	must(err)
	resource.State, resource.ResourceID = "ready", "owned-container"
	resource.EncryptedOutputs = seal("service-outputs", "service-resource:"+run.ID+":outputs", map[string]string{"password": request.Password})
	run.State, run.ServiceID = "succeeded", resource.ServiceID
	connection := core.Service{ID: resource.ServiceID, ProjectID: project.ID, Name: run.ServiceName, Type: "postgresql", ProvisionRunID: run.ID, ProvisionTarget: run.Target, Revision: 1, CreatedAt: now, UpdatedAt: now}
	must(s.SaveServiceResource(ctx, resource, run, &connection))
	input := core.WorkloadBackupRequest{Source: request, Key: strings.Repeat("a", 64)}
	backupID, operationID := prefix+"-backup", prefix+"-operation"
	b := core.WorkloadBackup{ID: backupID, ProjectID: project.ID, ServerID: server.ID, SourceRunID: run.ID, State: "creating", Revision: 1, Policy: "retain", Location: "target-local", EncryptedInput: seal("backup-accepted", "workload-backup:"+backupID+":accepted", input), CreatedAt: now, UpdatedAt: now}
	op := core.WorkloadBackupOperation{ID: operationID, BackupID: b.ID, ProjectID: project.ID, Action: "backup", State: "running", Revision: 1, LeaseToken: "original", LeaseUntil: now.Add(31 * time.Minute), EncryptedInput: seal("backup-operation", "workload-backup:"+operationID+":operation", input), CreatedAt: now, UpdatedAt: now}
	must(s.CreateWorkloadBackup(ctx, b, op))
	b.State, op.State = "ready", "succeeded"
	must(s.CompleteWorkloadBackupOperation(ctx, b, op))
	policyID := prefix + "-policy"
	must(s.CreateWorkloadBackupPolicy(ctx, core.WorkloadBackupPolicy{ID: policyID, Name: "protection", ProjectID: project.ID, SourceRunID: run.ID, SourceResourceID: resource.ResourceID, ServerID: server.ID, Revision: 1, Enabled: true, IntervalHours: 24, KeepLast: 3, EncryptedInput: seal("backup-policy", "workload-backup:"+policyID+":policy", input), CreatedAt: now, UpdatedAt: now}))
	bootstrapID := prefix + "-bootstrap"
	must(s.CreateTargetBootstrap(ctx, core.TargetBootstrap{ID: bootstrapID, ServerID: server.ID, EncryptedInput: seal("bootstrap", "target-bootstrap:"+bootstrapID, map[string]string{"claimToken": "saved-enrollment-token"})}))
	if corruptDomain == "frozen-hook" {
		secretID := prefix + "-hook-secret"
		must(s.CreateSecret(ctx, core.Secret{ID: secretID, Name: secretID, Type: core.SecretTypeEnvironment, EnvironmentVariable: "SAVED_TOKEN", EncryptedValue: seal("current-hook", "secret:"+secretID, "rotated-secret-value"), CreatedAt: now, UpdatedAt: now}))
		must(s.CreateApp(ctx, core.App{ID: prefix + "-app", ProjectID: project.ID, ServerID: server.ID, Name: "application", SourceRepo: "https://example.test/application", Branch: "main", BuildType: core.BuildTypeDockerfile, State: "ready", CreatedAt: now, HookEnvironment: map[string]string{core.SecretEnvironmentKey(secretID, "SAVED_TOKEN"): seal("frozen-hook", "secret:"+secretID, "original-secret-value"), "LOG_LEVEL": "info"}}))
	}
	manager := Manager{Store: s, Directory: filepath.Join(dir, "backups"), MasterKeyFile: key, DatabaseURL: dsn}
	record, err := manager.Create(ctx)
	must(err)
	// Simulate a verification marker written by an older verifier that did not
	// examine these records. A recheck must revoke that earlier success.
	if corruptDomain != "" {
		verified := now.Add(-time.Hour)
		record.State, record.VerifiedAt = "verified", &verified
		must(s.SaveBackupRecord(ctx, record))
	}
	checked, err := manager.Verify(ctx, record.ID)
	if corruptDomain == "" {
		must(err)
		if checked.State != "verified" || checked.VerifiedAt == nil {
			t.Fatal("valid recovery material was not verified")
		}
	} else {
		if err == nil || err.Error() != "restored vault cannot decrypt saved controller data" || checked.State != "verification_failed" || checked.VerifiedAt != nil {
			t.Fatal("unreadable recovery material retained successful verification")
		}
		records, err := s.ListBackupRecords(ctx)
		must(err)
		found := false
		for _, saved := range records {
			if saved.ID == record.ID {
				found = saved.State == "verification_failed" && saved.VerifiedAt == nil
			}
		}
		if !found {
			t.Fatal("failed recheck was not saved")
		}
		if _, err := os.Stat(filepath.Join(manager.Directory, record.ID, "manifest.json")); err != nil {
			t.Fatal("failed recovery verification removed its backup")
		}
	}
	// Verification must not rewrite the source ciphertext or invoke recovery work.
	saved, err := s.GetWorkloadBackupOperation(ctx, op.ID)
	must(err)
	if saved.EncryptedInput != op.EncryptedInput || saved.State != "succeeded" {
		t.Fatal("restore verification changed live recovery evidence")
	}
}
