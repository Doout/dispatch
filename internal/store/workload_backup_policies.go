package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type WorkloadBackupPolicyStore interface {
	CreateWorkloadBackupPolicy(context.Context, core.WorkloadBackupPolicy) error
	GetWorkloadBackupPolicy(context.Context, string) (core.WorkloadBackupPolicy, error)
	ListWorkloadBackupPolicies(context.Context, string) ([]core.WorkloadBackupPolicy, error)
	UpdateWorkloadBackupPolicy(context.Context, core.WorkloadBackupPolicy, int64) error
	AcceptWorkloadBackupCapture(context.Context, core.WorkloadBackupPolicy, core.WorkloadBackup, core.WorkloadBackupOperation) error
	AcceptWorkloadBackupRetention(context.Context, core.WorkloadBackupPolicy, core.WorkloadBackup, core.WorkloadBackupOperation) error
	AcceptWorkloadBackupExport(context.Context, core.WorkloadBackupPolicy, core.WorkloadBackup, core.WorkloadBackupOperation) error
	MarkWorkloadBackupExportMissed(context.Context, core.WorkloadBackupPolicy, core.WorkloadBackup) error
	UpdateWorkloadBackupPolicyEvent(context.Context, core.WorkloadBackupPolicy, int64, *core.ObservationEvent) error
}

func scanWorkloadBackupPolicy(row scanner) (core.WorkloadBackupPolicy, error) {
	var p core.WorkloadBackupPolicy
	var raw string
	err := row.Scan(&raw, &p.EncryptedInput)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &p)
	}
	return p, err
}
func (s *SQLStore) GetWorkloadBackupPolicy(ctx context.Context, id string) (core.WorkloadBackupPolicy, error) {
	return scanWorkloadBackupPolicy(s.db.QueryRowContext(ctx, s.q(`SELECT payload,input_cipher FROM workload_backup_policies WHERE id=?`), id))
}
func (s *SQLStore) ListWorkloadBackupPolicies(ctx context.Context, project string) ([]core.WorkloadBackupPolicy, error) {
	query := `SELECT payload,input_cipher FROM workload_backup_policies`
	args := []any{}
	if project != "" {
		query += ` WHERE project_id=?`
		args = append(args, project)
	}
	rows, err := s.db.QueryContext(ctx, s.q(query+` ORDER BY created_at,id`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []core.WorkloadBackupPolicy{}
	for rows.Next() {
		p, err := scanWorkloadBackupPolicy(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
func (s *SQLStore) CreateWorkloadBackupPolicy(ctx context.Context, p core.WorkloadBackupPolicy) error {
	if p.ID == "" || p.ProjectID == "" || p.SourceRunID == "" || p.ServerID == "" || p.EncryptedInput == "" || p.Revision != 1 || p.IntervalHours < 1 || p.IntervalHours > 8760 || p.KeepLast < 1 || p.KeepLast > 1000 {
		return ErrWorkloadBackupChanged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := `SELECT ` + serviceResourceColumns + ` FROM service_resources WHERE run_id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	source, err := scanServiceResource(tx.QueryRowContext(ctx, s.q(query), p.SourceRunID))
	if err != nil || source.ProjectID != p.ProjectID || source.Target.ServerID != p.ServerID || source.State != "ready" || source.ResourceID != p.SourceResourceID {
		return ErrWorkloadBackupChanged
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO workload_backup_policies(id,project_id,source_run_id,server_id,enabled,revision,input_cipher,payload,created_at) VALUES(?,?,?,?,?,?,?,?,?)`), p.ID, p.ProjectID, p.SourceRunID, p.ServerID, p.Enabled, p.Revision, p.EncryptedInput, jsonText(p), stamp(p.CreatedAt))
	if err != nil {
		return err
	}
	if err = s.BindMutationAcceptance(ctx, tx.Tx, "workload_backup_policy", p.ID); err != nil {
		return err
	}
	if err = s.UpdateMutationOutcome(ctx, tx.Tx, "workload_backup_policy", p.ID, "succeeded"); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) UpdateWorkloadBackupPolicy(ctx context.Context, p core.WorkloadBackupPolicy, revision int64) error {
	return s.UpdateWorkloadBackupPolicyEvent(ctx, p, revision, nil)
}

// Policy state and its notification outbox entry commit together.
func (s *SQLStore) UpdateWorkloadBackupPolicyEvent(ctx context.Context, p core.WorkloadBackupPolicy, revision int64, event *core.ObservationEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	before, err := s.lockBackupPolicy(ctx, tx.Tx, p.ID)
	if err != nil {
		return err
	}
	if before.Revision != revision || !sameBackupPolicyAuthority(before, p) {
		return ErrWorkloadBackupChanged
	}
	p.Revision = revision + 1
	p.UpdatedAt = time.Now().UTC()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workload_backup_policies SET enabled=?,revision=?,payload=? WHERE id=? AND revision=?`), p.Enabled, p.Revision, jsonText(p), p.ID, revision)
	if err = changed(result, err); err != nil {
		return err
	}
	if event != nil {
		if p.NotificationAppID == "" || event.AppID != p.NotificationAppID || event.ProjectID != p.ProjectID || event.Kind != "backup_offsite" {
			return ErrWorkloadBackupChanged
		}
		var project string
		if err = tx.QueryRowContext(ctx, s.q(`SELECT project_id FROM apps WHERE id=?`), event.AppID).Scan(&project); err != nil || project != p.ProjectID {
			return ErrWorkloadBackupChanged
		}
		if err = s.insertObservationEvent(ctx, tx, *event); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func sameBackupPolicyAuthority(a, b core.WorkloadBackupPolicy) bool {
	return a.ID == b.ID && a.ProjectID == b.ProjectID && a.SourceRunID == b.SourceRunID && a.SourceResourceID == b.SourceResourceID && a.ServerID == b.ServerID && a.NodeID == b.NodeID && a.NodeGeneration == b.NodeGeneration && a.Name == b.Name && a.IntervalHours == b.IntervalHours && a.KeepLast == b.KeepLast && a.EncryptedInput == b.EncryptedInput && a.OffsiteStoreID == b.OffsiteStoreID && a.OffsiteStoreDigest == b.OffsiteStoreDigest && a.OffsiteStaleAfterHours == b.OffsiteStaleAfterHours && a.NotificationAppID == b.NotificationAppID && a.RetireLocalAfterOffsiteVerification == b.RetireLocalAfterOffsiteVerification && a.CreatedAt.Equal(b.CreatedAt) && reflect.DeepEqual(a.Actor, b.Actor)
}

func (s *SQLStore) lockBackupPolicy(ctx context.Context, tx *sql.Tx, id string) (core.WorkloadBackupPolicy, error) {
	query := `SELECT payload,input_cipher FROM workload_backup_policies WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	return scanWorkloadBackupPolicy(tx.QueryRowContext(ctx, s.q(query), id))
}
func (s *SQLStore) AcceptWorkloadBackupCapture(ctx context.Context, p core.WorkloadBackupPolicy, b core.WorkloadBackup, o core.WorkloadBackupOperation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	before, err := s.lockBackupPolicy(ctx, tx.Tx, p.ID)
	if err != nil {
		return err
	}
	if !before.Enabled || before.Revision != p.Revision || b.CapturePolicyID != p.ID || b.ScheduledAt == nil || b.ProjectID != p.ProjectID || b.SourceRunID != p.SourceRunID || b.SourceResourceID != p.SourceResourceID || b.ServerID != p.ServerID || b.NodeID != p.NodeID || o.Action != "backup" || b.State != "creating" || b.Revision != 1 || b.EncryptedInput == "" || b.Policy != "retain" || b.Location != "target-local" {
		return ErrWorkloadBackupChanged
	}
	slot, next, missed := before.DueCapture(o.CreatedAt)
	if slot.IsZero() || !slot.Equal(*b.ScheduledAt) {
		return ErrWorkloadBackupChanged
	}
	// An unresolved older capture or verification must not be hidden by a new archive.
	var busy int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workload_backup_operations o JOIN workload_backups b ON b.id=o.backup_id WHERE b.capture_policy_id=? AND o.state IN ('running','unknown','unresolved')`), p.ID).Scan(&busy); err != nil {
		return err
	}
	if busy > 0 {
		return ErrWorkloadBackupChanged
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO workload_backups(id,project_id,server_id,source_run_id,state,revision,input_cipher,payload,created_at,capture_policy_id,scheduled_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`), b.ID, b.ProjectID, b.ServerID, b.SourceRunID, b.State, b.Revision, b.EncryptedInput, jsonText(b), stamp(b.CreatedAt), p.ID, stamp(slot))
	if err != nil {
		return err
	}
	if err = s.insertWorkloadBackupOperation(ctx, tx.Tx, b, o); err != nil {
		return err
	}
	p.LastScheduledAt = &slot
	p.NextCaptureAt = next
	p.LastAttemptAt = &o.CreatedAt
	p.LastBackupID = b.ID
	p.MissedCaptures += missed
	p.State = "capturing"
	p.Message = "Fresh backup capture accepted."
	p.Revision++
	p.UpdatedAt = o.CreatedAt
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workload_backup_policies SET revision=?,payload=? WHERE id=? AND revision=? AND enabled=TRUE`), p.Revision, jsonText(p), p.ID, before.Revision)
	if err = changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) AcceptWorkloadBackupRetention(ctx context.Context, p core.WorkloadBackupPolicy, b core.WorkloadBackup, o core.WorkloadBackupOperation) error {
	if b.Offsite != nil && o.Action == "delete" {
		return ErrWorkloadBackupChanged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.lockBackupDestructiveProject(ctx, tx.Tx, b); err != nil {
		return err
	}
	current, err := s.lockBackupPolicy(ctx, tx.Tx, p.ID)
	if err != nil {
		return err
	}
	if !current.Enabled || current.Revision != p.Revision || b.CapturePolicyID != p.ID || current.OffsiteStoreID == "" && o.Action != "delete" || current.OffsiteStoreID != "" && (!current.RetireLocalAfterOffsiteVerification || o.Action != "retire-local" || current.LastOffsiteBackupID != current.LastBackupID) {
		return ErrWorkloadBackupChanged
	}
	var deleting int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workload_backup_operations o JOIN workload_backups b ON b.id=o.backup_id WHERE b.capture_policy_id=? AND o.state IN ('running','unknown','unresolved')`), p.ID).Scan(&deleting); err != nil {
		return err
	}
	if deleting > 0 {
		return ErrWorkloadBackupChanged
	}
	// Review current archive identities under the policy lock, not a stale cleanup list.
	rows, err := tx.QueryContext(ctx, s.q(`SELECT payload,input_cipher,revision FROM workload_backups WHERE capture_policy_id=?`), p.ID)
	if err != nil {
		return err
	}
	items := []core.WorkloadBackup{}
	for rows.Next() {
		item, e := scanWorkloadBackup(rows)
		if e != nil {
			rows.Close()
			return e
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	verified := []core.WorkloadBackup{}
	for _, item := range items {
		if item.ID == b.ID && item.Offsite != nil && o.Action == "delete" {
			rows.Close()
			return ErrWorkloadBackupChanged
		}
		if item.LocalState != "retired" && item.LocalState != "retiring" && item.State == "ready" && item.VerificationState == "verified" && item.CleanupState == "complete" {
			verified = append(verified, item)
		}
	}
	sort.Slice(verified, func(i, j int) bool {
		if verified[i].CreatedAt.Equal(verified[j].CreatedAt) {
			return verified[i].ID > verified[j].ID
		}
		return verified[i].CreatedAt.After(verified[j].CreatedAt)
	})
	if len(verified) <= current.KeepLast || current.LastBackupID != current.LastVerifiedBackupID {
		return ErrWorkloadBackupChanged
	}
	latestVerified := false
	for _, item := range verified {
		if item.ID == current.LastBackupID {
			latestVerified = true
		}
	}
	if !latestVerified {
		return ErrWorkloadBackupChanged
	}
	allowed := false
	for i, item := range verified {
		if item.ID == b.ID && item.Revision == b.Revision && i >= current.KeepLast {
			allowed = true
		}
	}
	if !allowed {
		return ErrWorkloadBackupChanged
	}
	if err = s.guardBackupDestructiveCohort(ctx, tx.Tx, b); err != nil {
		return err
	}
	if o.Action == "retire-local" {
		if b.LocalState == "retired" || b.LocalState == "retiring" || b.Offsite == nil || b.Offsite.DeletedAt != nil || b.Offsite.StoreID != current.OffsiteStoreID || b.Offsite.VerificationState != "verified" || b.Offsite.VerifiedAt == nil || b.Offsite.ConfirmedAt.IsZero() || b.Offsite.ManifestChecksum == "" || b.Checksum == "" || o.OffsiteStoreID != current.OffsiteStoreID {
			return ErrWorkloadBackupChanged
		}
		if err = s.guardWorkloadBackupRetirement(ctx, tx.Tx, b, o.Action); err != nil {
			return err
		}
	}
	if err = s.insertWorkloadBackupOperation(ctx, tx.Tx, b, o); err != nil {
		return err
	}
	return tx.Commit()
}

// An enabled policy's newest verified archives require pausing retention before manual deletion.
func (s *SQLStore) protectedPolicyBackup(ctx context.Context, tx *sql.Tx, p core.WorkloadBackupPolicy, id string) (bool, error) {
	rows, err := tx.QueryContext(ctx, s.q(`SELECT payload,input_cipher,revision FROM workload_backups WHERE capture_policy_id=?`), p.ID)
	if err != nil {
		return true, err
	}
	defer rows.Close()
	verified := []core.WorkloadBackup{}
	for rows.Next() {
		b, err := scanWorkloadBackup(rows)
		if err != nil {
			return true, err
		}
		if usableRecoveryBackup(b) {
			verified = append(verified, b)
		}
	}
	if err = rows.Err(); err != nil {
		return true, err
	}
	sort.Slice(verified, func(i, j int) bool {
		if verified[i].CreatedAt.Equal(verified[j].CreatedAt) {
			return verified[i].ID > verified[j].ID
		}
		return verified[i].CreatedAt.After(verified[j].CreatedAt)
	})
	for i, b := range verified {
		if b.ID == id {
			return i < p.KeepLast, nil
		}
	}
	return false, nil
}

// One scheduled export per capture, even after failure or an uncertain reply.
func (s *SQLStore) AcceptWorkloadBackupExport(ctx context.Context, p core.WorkloadBackupPolicy, b core.WorkloadBackup, o core.WorkloadBackupOperation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.lockBackupPolicy(ctx, tx.Tx, p.ID)
	if err != nil {
		return err
	}
	if !current.Enabled || current.Revision != p.Revision || !sameBackupPolicyAuthority(current, p) || p.OffsiteStoreID == "" || o.OffsiteStoreID != p.OffsiteStoreID || o.CapturePolicyID != p.ID || o.Action != "export" {
		return ErrWorkloadBackupChanged
	}
	query := `SELECT payload,input_cipher,revision FROM workload_backups WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	original, err := scanWorkloadBackup(tx.QueryRowContext(ctx, s.q(query), b.ID))
	if err != nil {
		return err
	}
	if original.Revision != b.Revision || original.CapturePolicyID != p.ID || original.ProjectID != p.ProjectID || original.ServerID != p.ServerID || original.NodeID != p.NodeID || original.SourceResourceID != p.SourceResourceID || original.State != "ready" || original.VerificationState != "verified" || original.CleanupState != "complete" || original.Offsite != nil || original.LocalState == "retired" || original.LocalState == "retiring" || original.ScheduledExportOperationID != "" {
		return ErrWorkloadBackupChanged
	}
	var active int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workload_backup_operations o JOIN workload_backups b ON b.id=o.backup_id WHERE b.capture_policy_id=? AND o.state IN ('running','unknown','unresolved')`), p.ID).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return ErrWorkloadBackupChanged
	}
	if err = s.insertWorkloadBackupOperation(ctx, tx.Tx, original, o); err != nil {
		return err
	}
	original.ScheduledExportOperationID = o.ID
	original.Revision++
	original.UpdatedAt = o.CreatedAt
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workload_backups SET revision=?,payload=? WHERE id=? AND revision=?`), original.Revision, jsonText(original), original.ID, b.Revision)
	if err = changed(result, err); err != nil {
		return err
	}
	p.LastOffsiteOperationID, p.OffsiteState = o.ID, "exporting"
	p.OffsiteMessage = "Encrypted capture export accepted for the approved destination."
	p.Revision++
	p.UpdatedAt = o.CreatedAt
	result, err = tx.ExecContext(ctx, s.q(`UPDATE workload_backup_policies SET revision=?,payload=? WHERE id=? AND revision=?`), p.Revision, jsonText(p), p.ID, current.Revision)
	if err = changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

// Count a missed export once per captured archive. It can later export if its authority recovers.
func (s *SQLStore) MarkWorkloadBackupExportMissed(ctx context.Context, p core.WorkloadBackupPolicy, b core.WorkloadBackup) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.lockBackupPolicy(ctx, tx.Tx, p.ID)
	if err != nil {
		return err
	}
	if current.Revision != p.Revision || !current.Enabled || current.OffsiteStoreID == "" || !sameBackupPolicyAuthority(current, p) {
		return ErrWorkloadBackupChanged
	}
	query := `SELECT payload,input_cipher,revision FROM workload_backups WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	original, err := scanWorkloadBackup(tx.QueryRowContext(ctx, s.q(query), b.ID))
	if err != nil {
		return err
	}
	if original.CapturePolicyID != p.ID || original.Revision != b.Revision || original.ScheduledExportMissed {
		return ErrWorkloadBackupChanged
	}
	original.ScheduledExportMissed = true
	original.UpdatedAt = time.Now().UTC()
	original.Revision++
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workload_backups SET revision=?,payload=? WHERE id=? AND revision=?`), original.Revision, jsonText(original), original.ID, b.Revision)
	if err = changed(result, err); err != nil {
		return err
	}
	current.MissedExports++
	current.Revision++
	current.UpdatedAt = time.Now().UTC()
	result, err = tx.ExecContext(ctx, s.q(`UPDATE workload_backup_policies SET revision=?,payload=? WHERE id=? AND revision=?`), current.Revision, jsonText(current), current.ID, p.Revision)
	if err = changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}
