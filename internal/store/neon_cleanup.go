package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type NeonLifecycleStore interface {
	SetNeonServicePolicy(context.Context, string, int64, string, string, time.Time) (core.ServiceResource, error)
	SaveNeonCleanupService(context.Context, core.WorkflowPreviewCleanup, core.WorkflowPreviewCleanupService) error
	ClaimNeonCleanupService(context.Context, core.WorkflowPreviewCleanup, core.WorkflowPreviewCleanupService) (core.ServiceResource, error)
}

func (s *SQLStore) SetNeonServicePolicy(ctx context.Context, id string, revision int64, policy, actor string, now time.Time) (core.ServiceResource, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ServiceResource{}, err
	}
	defer tx.Rollback()
	initial, err := scanServiceResource(tx.QueryRowContext(ctx, s.q(`SELECT `+serviceResourceColumns+` FROM service_resources WHERE run_id=?`), id))
	if err != nil {
		return initial, err
	}
	if err = s.lockPreviewResource(ctx, tx, initial.PreviewID, true); err != nil {
		return initial, err
	}
	q := `SELECT ` + serviceResourceColumns + ` FROM service_resources WHERE run_id=?`
	if s.postgres {
		q += ` FOR UPDATE`
	}
	r, err := scanServiceResource(tx.QueryRowContext(ctx, s.q(q), id))
	if err != nil {
		return r, err
	}
	if r.Target.Provider != "neon" || r.PreviewID == "" || r.Revision != revision || r.State != "ready" || r.LeaseUntil.After(now) || actor == "" {
		return r, ErrServiceResourceChanged
	}
	if err = s.lockPreviewResource(ctx, tx, r.PreviewID, true); err != nil {
		return r, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_preview_cleanups WHERE resource_id=? AND state<>'succeeded'`), r.PreviewID).Scan(&active); err != nil {
		return r, err
	}
	if active == 0 {
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM neon_replacements WHERE preview_id=? AND state IN ('accepted','unresolved')`), r.PreviewID).Scan(&active); err != nil {
			return r, err
		}
	}
	if active > 0 {
		return r, ErrPreviewClosing
	}
	var linked string
	if err = tx.QueryRowContext(ctx, s.q(`SELECT run_id FROM neon_preview_services WHERE preview_id=? AND alias=?`), r.PreviewID, r.PreviewAlias).Scan(&linked); err != nil || linked != r.RunID {
		return r, ErrServiceResourceChanged
	}
	r.Policy = policy
	if !validServicePolicy(r) {
		return r, ErrServiceResourceChanged
	}
	r.PolicyActorID, r.PolicyApprovedAt, r.UpdatedAt = actor, &now, now
	r.Revision++
	result, err := tx.ExecContext(ctx, s.q(`UPDATE service_resources SET revision=?,payload=? WHERE run_id=? AND revision=?`), r.Revision, jsonText(r), id, revision)
	if err = changed(result, err); err != nil {
		return r, err
	}
	return r, tx.Commit()
}
func (s *SQLStore) captureNeonCleanupServices(ctx context.Context, tx *changeTx, c core.WorkflowPreviewCleanup) error {
	rows, err := tx.QueryContext(ctx, s.q(`SELECT r.`+`payload FROM service_resources r JOIN neon_preview_services n ON n.run_id=r.run_id WHERE n.preview_id=? ORDER BY n.alias`), c.ResourceID)
	if err != nil {
		return err
	}
	records := []core.ServiceResource{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var r core.ServiceResource
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			rows.Close()
			return err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.Target.Provider != "neon" || r.PreviewID != c.ResourceID || !validServicePolicy(r) {
			return ErrServiceResourceChanged
		}
		if r.Policy != "retain" && (r.PolicyActorID == "" || r.PolicyApprovedAt == nil) {
			return errors.New("Neon cleanup policy has no recorded review")
		}
		state := "pending"
		if r.State == "deleted" {
			state = "succeeded"
		}
		_, err = tx.ExecContext(ctx, s.q(`INSERT INTO workflow_preview_cleanup_services(cleanup_id,run_id,alias,policy,resource_id,operation_id,state) VALUES(?,?,?,?,?,?,?)`), c.ID, r.RunID, r.PreviewAlias, r.Policy, r.ResourceID, "neon-cleanup-"+c.ID+"-"+r.RunID, state)
		if err != nil {
			return err
		}
	}
	return nil
}
func (s *SQLStore) neonCleanupServices(ctx context.Context, id string) ([]core.WorkflowPreviewCleanupService, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT run_id,alias,policy,resource_id,operation_id,state,action_started,error FROM workflow_preview_cleanup_services WHERE cleanup_id=? ORDER BY alias`), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []core.WorkflowPreviewCleanupService{}
	for rows.Next() {
		var entry core.WorkflowPreviewCleanupService
		if err = rows.Scan(&entry.RunID, &entry.Alias, &entry.Policy, &entry.ResourceID, &entry.OperationID, &entry.State, &entry.ActionStarted, &entry.Error); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}
func (s *SQLStore) SaveNeonCleanupService(ctx context.Context, c core.WorkflowPreviewCleanup, e core.WorkflowPreviewCleanupService) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_cleanup_services SET state=?,action_started=CASE WHEN action_started=TRUE THEN TRUE ELSE ? END,error=? WHERE cleanup_id=? AND run_id=? AND operation_id=? AND EXISTS(SELECT 1 FROM workflow_preview_cleanups WHERE id=? AND lease_token=? AND lease_until>?)`), e.State, e.ActionStarted, e.Error, c.ID, e.RunID, e.OperationID, c.ID, c.LeaseToken, stamp(time.Now().UTC()))
	return changed(result, err)
}

// Claim the captured resource only after all owned workloads have stopped.
// Removing configuration bindings is limited to applications in this cleanup.
func (s *SQLStore) ClaimNeonCleanupService(ctx context.Context, c core.WorkflowPreviewCleanup, e core.WorkflowPreviewCleanupService) (core.ServiceResource, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ServiceResource{}, err
	}
	defer tx.Rollback()
	if err = s.lockPreviewResource(ctx, tx, c.ResourceID, true); err != nil {
		return core.ServiceResource{}, err
	}
	var pending int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_preview_cleanups WHERE id=? AND resource_id=? AND lease_token=? AND lease_until>? AND state<>'succeeded'`), c.ID, c.ResourceID, c.LeaseToken, stamp(time.Now().UTC())).Scan(&pending); err != nil || pending != 1 {
		return core.ServiceResource{}, ErrPreviewClosing
	}
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_preview_cleanup_apps e JOIN apps a ON a.id=e.app_id WHERE e.cleanup_id=? AND (e.state<>'succeeded' OR a.state<>'closed')`), c.ID).Scan(&pending); err != nil || pending != 0 {
		return core.ServiceResource{}, ErrServiceInUse
	}
	q := `SELECT ` + serviceResourceColumns + ` FROM service_resources WHERE run_id=?`
	if s.postgres {
		q += ` FOR UPDATE`
	}
	r, err := scanServiceResource(tx.QueryRowContext(ctx, s.q(q), e.RunID))
	if err != nil {
		return r, err
	}
	if r.Target.Provider != "neon" || r.PreviewID != c.ResourceID || r.PreviewAlias != e.Alias || r.ResourceID != e.ResourceID || r.Policy != e.Policy || r.LeaseUntil.After(time.Now()) {
		return r, ErrServiceResourceChanged
	}
	if r.State == "deleted" {
		return r, nil
	}
	// Lock registration so concurrent service binding acceptance cannot race this claim.
	var raw string
	err = tx.QueryRowContext(ctx, s.q(s.serviceLockQuery()), r.ServiceID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	if e.Policy == "delete" || e.Policy == "suspend" {
		if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM app_service_bindings WHERE service_id=? AND app_id IN(SELECT app_id FROM workflow_preview_cleanup_apps WHERE cleanup_id=? AND state='succeeded')`), r.ServiceID, c.ID); err != nil {
			return r, err
		}
		busy, err := s.serviceResourceConsumers(ctx, tx.Tx, r.ServiceID)
		if err != nil {
			return r, err
		}
		if busy {
			return r, ErrServiceInUse
		}
	}
	r.State = "deleting"
	if e.Policy == "suspend" {
		r.State = "recovering"
	}
	r.OperationID, r.LeaseToken = e.OperationID, c.LeaseToken
	if e.Policy == "suspend" {
		r.OperationID = r.RunID
	}
	r.LeaseUntil = time.Now().UTC().Add(31 * time.Minute)
	r.Revision++
	result, err := tx.ExecContext(ctx, s.q(`UPDATE service_resources SET state=?,revision=?,operation_id=?,lease_token=?,lease_until=?,payload=? WHERE run_id=? AND revision=?`), r.State, r.Revision, r.OperationID, r.LeaseToken, stamp(r.LeaseUntil), jsonText(r), r.RunID, r.Revision-1)
	if err = changed(result, err); err != nil {
		return r, err
	}
	// The action-start checkpoint and owned resource claim commit together.
	result, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_preview_cleanup_services SET action_started=TRUE,state='running',error='' WHERE cleanup_id=? AND run_id=? AND operation_id=? AND action_started=FALSE`), c.ID, e.RunID, e.OperationID)
	if err = changed(result, err); err != nil {
		return r, err
	}
	return r, tx.Commit()
}
