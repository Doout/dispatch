package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type ServiceResourceStore interface {
	CreateServiceResource(context.Context, core.ServiceProvisionRun, core.ServiceResource) error
	GetServiceResource(context.Context, string) (core.ServiceResource, error)
	ClaimServiceResource(context.Context, string, int64, string, string, string, time.Time, string) (core.ServiceResource, error)
	SaveServiceResource(context.Context, core.ServiceResource, core.ServiceProvisionRun, *core.Service) error
	ServiceResourceConsumers(context.Context, string) (bool, error)
	ReconcileServiceRuntimeJobs(context.Context, string, string, time.Time) error
}

var ErrServiceResourceChanged = errors.New("service resource changed or has an active operation; inspect its original run")

const serviceResourceColumns = `payload,request_cipher,outputs_cipher,lease_token,lease_until`

func scanServiceResource(row scanner) (core.ServiceResource, error) {
	var r core.ServiceResource
	var raw, lease string
	err := row.Scan(&raw, &r.EncryptedRequest, &r.EncryptedOutputs, &r.LeaseToken, &lease)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(raw), &r)
	r.LeaseUntil = parseTime(lease)
	return r, err
}
func (s *SQLStore) GetServiceResource(ctx context.Context, id string) (core.ServiceResource, error) {
	return scanServiceResource(s.db.QueryRowContext(ctx, s.q(`SELECT `+serviceResourceColumns+` FROM service_resources WHERE run_id=?`), id))
}
func (s *SQLStore) CreateServiceResource(ctx context.Context, run core.ServiceProvisionRun, r core.ServiceResource) error {
	if run.ID != r.RunID || run.ProjectID != r.ProjectID || run.Target == nil || r.Target != *run.Target || !validServicePolicy(r) || r.EncryptedRequest == "" || r.Revision != 1 || r.ServiceID == "" {
		return ErrServiceResourceChanged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if admission, scoped := core.ScopedServiceAdmissionFromContext(ctx); scoped {
		if admission.TemplateID != run.TemplateID || admission.Digest == "" || r.Target.ServerID == "" || r.Target.Provider != "docker" && r.Target.Provider != "helm" {
			return ErrServiceResourceChanged
		}
		// The policy row serializes admissions across controllers, including policy updates.
		if _, err = tx.ExecContext(ctx, s.q(`UPDATE project_infrastructure_policies SET revision=revision WHERE project_id=?`), r.ProjectID); err != nil {
			return err
		}
		p, err := scanQuotaPolicy(tx.QueryRowContext(ctx, s.q(`SELECT `+quotaPolicyColumns+` FROM project_infrastructure_policies WHERE project_id=?`), r.ProjectID))
		if errors.Is(err, ErrNotFound) {
			return &core.InfrastructureQuotaViolation{Code: "policy_required", Limit: "maxServices", Maximum: 0, Requested: 1}
		}
		if err != nil {
			return err
		}
		// Approval authorizes a particular definition, not an older document that
		// survived an edit between API preflight and this acceptance transaction.
		if _, err = tx.ExecContext(ctx, s.q(`UPDATE saved_service_templates SET revision=revision WHERE id=?`), admission.TemplateID); err != nil {
			return err
		}
		var templateProject, currentDigest string
		err = tx.QueryRowContext(ctx, s.q(`SELECT project_id,digest FROM saved_service_templates WHERE id=?`), admission.TemplateID).Scan(&templateProject, &currentDigest)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_resources SET config_sha=config_sha WHERE id=?`), admission.TemplateID); err != nil {
				return err
			}
			query := `SELECT c.project_id,w.config_sha FROM workflow_resources w JOIN config_sources c ON c.id=w.config_source_id WHERE w.id=? AND w.active=TRUE AND c.active=TRUE AND w.kind='ServiceTemplate'`
			if s.postgres {
				query += ` FOR UPDATE`
			}
			err = tx.QueryRowContext(ctx, s.q(query), admission.TemplateID).Scan(&templateProject, &currentDigest)
		}
		if err != nil || templateProject != r.ProjectID || currentDigest != admission.Digest {
			return ErrServiceResourceChanged
		}
		if _, err = tx.ExecContext(ctx, s.q(`UPDATE servers SET state=state WHERE id=?`), r.Target.ServerID); err != nil {
			return err
		}
		var targetProject string
		if err = tx.QueryRowContext(ctx, s.q(`SELECT project_id FROM servers WHERE id=?`), r.Target.ServerID).Scan(&targetProject); err != nil || targetProject != "" && targetProject != r.ProjectID {
			return ErrServiceResourceChanged
		}
		if targetProject != r.ProjectID {
			if err = s.lockServiceAssignment(ctx, tx.Tx, r.ProjectID, "target", r.Target.ServerID); err != nil {
				return err
			}
		}
		if err = s.lockServiceAssignment(ctx, tx.Tx, r.ProjectID, "service_template", admission.TemplateID+"@"+admission.Digest); err != nil {
			return err
		}

		var used int64
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM service_resources WHERE project_id=? AND state<>'deleted'`), r.ProjectID).Scan(&used); err != nil {
			return err
		}
		if p.MaxServices >= 0 && used+1 > p.MaxServices {
			return &core.InfrastructureQuotaViolation{Code: "quota_exceeded", Limit: "maxServices", Maximum: p.MaxServices, Usage: used, Requested: 1}
		}
	}
	if r.Target.Provider == "neon" {
		q := `SELECT project_id FROM neon_providers WHERE id=?`
		if s.postgres {
			q += ` FOR UPDATE`
		}
		var project string
		if err = tx.QueryRowContext(ctx, s.q(q), r.Target.ProviderRef).Scan(&project); err != nil || project != r.ProjectID {
			return ErrServiceResourceChanged
		}
	}
	if r.ReplacesRunID != "" {
		if err = s.checkNeonReplacement(ctx, tx, r, true); err != nil {
			return err
		}
	} else if r.PreviewID != "" {
		if err = s.lockPreviewResource(ctx, tx, r.PreviewID, false); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO service_provision_runs(id,template_id,project_id,service_name,state,payload) VALUES(?,?,?,?,?,?)`), run.ID, run.TemplateID, run.ProjectID, run.ServiceName, run.State, jsonText(run))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO service_resources(run_id,project_id,service_id,server_id,name,state,revision,operation_id,lease_until,lease_token,request_cipher,outputs_cipher,payload) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`), r.RunID, r.ProjectID, r.ServiceID, r.Target.ServerID, r.Name, r.State, r.Revision, r.OperationID, stamp(r.LeaseUntil), r.LeaseToken, r.EncryptedRequest, r.EncryptedOutputs, jsonText(r))
	if err != nil {
		return err
	}
	if r.ReplacesRunID != "" {
		_, err = tx.ExecContext(ctx, s.q(`INSERT INTO neon_replacements(source_run_id,replacement_run_id,preview_id,alias,state,created_at) VALUES(?,?,?,?,?,?)`), r.ReplacesRunID, r.RunID, r.PreviewID, r.PreviewAlias, "accepted", stamp(r.CreatedAt))
		if err != nil {
			return err
		}
	} else if r.PreviewID != "" {
		if r.Target.Provider != "neon" || r.PreviewAlias == "" {
			return ErrServiceResourceChanged
		}
		var project string
		var active bool
		if err = tx.QueryRowContext(ctx, s.q(`SELECT c.project_id,w.active FROM workflow_resources w JOIN config_sources c ON c.id=w.config_source_id WHERE w.id=? AND w.temporary=TRUE`), r.PreviewID).Scan(&project, &active); err != nil || project != r.ProjectID || !active {
			return ErrServiceResourceChanged
		}
		if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO neon_preview_services(preview_id,alias,run_id) VALUES(?,?,?)`), r.PreviewID, r.PreviewAlias, r.RunID); err != nil {
			return err
		}
	}
	for _, id := range r.Dependencies {
		var project string
		dependencyQuery := `SELECT project_id FROM services WHERE id=?`
		if s.postgres {
			dependencyQuery += ` FOR UPDATE`
		}
		if err = tx.QueryRowContext(ctx, s.q(dependencyQuery), id).Scan(&project); err != nil || project != r.ProjectID {
			return ErrServiceResourceChanged
		}
		var busy int
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM service_resources WHERE service_id=? AND state IN ('deleting','deleted')`), id).Scan(&busy); err != nil {
			return err
		}
		if busy > 0 {
			return ErrServiceInUse
		}
		if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO service_resource_dependencies(run_id,service_id) VALUES(?,?)`), r.RunID, id); err != nil {
			return err
		}
	}
	if err = s.BindMutationAcceptance(ctx, tx.Tx, "service_provision", run.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// Lock exact approvals until acceptance commits so concurrent revocation is ordered.
func (s *SQLStore) lockServiceAssignment(ctx context.Context, tx *sql.Tx, project, kind, id string) error {
	query := `SELECT resource_id FROM infrastructure_assignments WHERE project_id=? AND kind=? AND resource_id=?`
	if s.postgres {
		query += ` FOR SHARE`
	}
	var assigned string
	err := tx.QueryRowContext(ctx, s.q(query), project, kind, id).Scan(&assigned)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrServiceResourceChanged
	}
	return err
}

// Owned resources remain counted through failed or uncertain provisioning until verified deletion.
func (s *SQLStore) CountServiceResources(ctx context.Context, project string) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM service_resources WHERE project_id=? AND state<>'deleted'`), project).Scan(&count)
	return count, err
}
func (s *SQLStore) serviceResourceConsumers(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var n int
	for _, q := range []string{
		`SELECT COUNT(*) FROM app_service_bindings WHERE service_id=?`,
		`SELECT COUNT(*) FROM neon_replacements n JOIN service_resources r ON r.run_id=n.source_run_id WHERE r.service_id=? AND n.state IN ('accepted','unresolved')`,
		`SELECT COUNT(*) FROM neon_preview_services n JOIN service_resources r ON r.run_id=n.run_id JOIN workflow_resources w ON w.id=n.preview_id WHERE r.service_id=? AND w.active=TRUE`,
		`SELECT COUNT(*) FROM workflow_service_references WHERE service_id=?`,
		`SELECT COUNT(*) FROM deployment_service_bindings b JOIN deployments d ON d.id=b.deployment_id WHERE b.service_id=? AND d.state NOT IN ('succeeded','failed','cancelled')`,
		`SELECT COUNT(*) FROM deployment_service_bindings b JOIN deployments d ON d.id=b.deployment_id JOIN apps a ON a.id=d.app_id WHERE b.service_id=? AND a.state<>'closed' AND d.state='succeeded' AND NOT EXISTS (SELECT 1 FROM deployments newer WHERE newer.app_id=d.app_id AND newer.state='succeeded' AND (newer.created_at>d.created_at OR (newer.created_at=d.created_at AND newer.id>d.id)))`,
		`SELECT COUNT(*) FROM service_resource_dependencies d JOIN service_resources r ON r.run_id=d.run_id WHERE d.service_id=? AND r.state<>'deleted'`,
	} {
		if err := tx.QueryRowContext(ctx, s.q(q), id).Scan(&n); err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}
func (s *SQLStore) ServiceResourceConsumers(ctx context.Context, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	return s.serviceResourceConsumers(ctx, tx.Tx, id)
}
func (s *SQLStore) ClaimServiceResource(ctx context.Context, id string, revision int64, operation, state, token string, now time.Time, resourceID string) (core.ServiceResource, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ServiceResource{}, err
	}
	defer tx.Rollback()
	q := `SELECT ` + serviceResourceColumns + ` FROM service_resources WHERE run_id=?`
	if s.postgres {
		q += ` FOR UPDATE`
	}
	r, err := scanServiceResource(tx.QueryRowContext(ctx, s.q(q), id))
	if err != nil {
		return r, err
	}
	if r.Revision != revision || r.LeaseUntil.After(now) || r.State == "deleted" || operation == "" || token == "" || state != "provisioning" && state != "recovering" && state != "deleting" {
		return r, ErrServiceResourceChanged
	}
	if r.Target.Provider == "neon" && state == "recovering" {
		var pending int
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_preview_cleanup_services e JOIN workflow_preview_cleanups c ON c.id=e.cleanup_id WHERE e.run_id=? AND e.policy='suspend' AND e.action_started=TRUE AND e.state<>'succeeded' AND c.lease_token<>?`), r.RunID, token).Scan(&pending); err != nil {
			return r, err
		}
		if pending > 0 {
			return r, ErrServiceResourceChanged
		}
	}
	if state == "deleting" {
		if resourceID != "" {
			r.ResourceID = resourceID
		}
		// Lock the registration as well: binding acceptance locks this same row.
		var ignored string
		err = tx.QueryRowContext(ctx, s.q(s.serviceLockQuery()), r.ServiceID).Scan(&ignored)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
		busy, e := s.serviceResourceConsumers(ctx, tx.Tx, r.ServiceID)
		if e != nil {
			return r, e
		}
		if busy {
			return r, ErrServiceInUse
		}
	}
	r.State, r.OperationID, r.LeaseToken = state, operation, token
	r.LeaseUntil, r.UpdatedAt = now.Add(31*time.Minute), now
	r.Revision++
	result, err := tx.ExecContext(ctx, s.q(`UPDATE service_resources SET state=?,revision=?,operation_id=?,lease_until=?,lease_token=?,payload=? WHERE run_id=? AND revision=?`), r.State, r.Revision, r.OperationID, stamp(r.LeaseUntil), token, jsonText(r), id, revision)
	if err = changed(result, err); err != nil {
		return r, err
	}
	if state == "deleting" {
		if err = s.BindMutationAcceptance(ctx, tx.Tx, "service_resource", operation); err != nil {
			return r, err
		}
	}
	return r, tx.Commit()
}
func (s *SQLStore) SaveServiceResource(ctx context.Context, r core.ServiceResource, run core.ServiceProvisionRun, item *core.Service) error {
	if run.ID != r.RunID || run.ProjectID != r.ProjectID || r.LeaseToken == "" || !validServicePolicy(r) || r.State != "ready" && r.State != "unresolved" && r.State != "deleted" && !(r.Target.Provider == "neon" && r.State == "deleting" && r.OperationID != r.RunID) {
		return ErrServiceResourceChanged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if r.ReplacesRunID != "" {
		if err = s.lockPreviewResource(ctx, tx, r.PreviewID, true); err != nil {
			return err
		}
	}
	before := r.Revision
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE service_resources SET state=?,revision=?,lease_until=?,lease_token=?,outputs_cipher=?,payload=? WHERE run_id=? AND project_id=? AND server_id=? AND revision=? AND lease_token=?`), r.State, r.Revision, stamp(time.Time{}), "", r.EncryptedOutputs, jsonText(r), r.RunID, r.ProjectID, r.Target.ServerID, before, r.LeaseToken)
	if err = changed(result, err); err != nil {
		return err
	}
	if item != nil {
		if item.ID != r.ServiceID || item.ProvisionRunID != r.RunID || item.ProjectID != r.ProjectID {
			return ErrServiceResourceChanged
		}
		if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO services(id,project_id,name,revision,payload) VALUES(?,?,?,?,?) ON CONFLICT(id) DO NOTHING`), item.ID, item.ProjectID, item.Name, item.Revision, jsonText(encodeService(*item))); err != nil {
			return err
		}
		var raw string
		if err = tx.QueryRowContext(ctx, s.q(`SELECT payload FROM services WHERE id=?`), item.ID).Scan(&raw); err != nil {
			return err
		}
		existing, err := decodeService(raw)
		if err != nil || existing.ProjectID != r.ProjectID || existing.ProvisionRunID != r.RunID {
			return ErrServiceResourceChanged
		}
	}
	if r.ReplacesRunID != "" {
		if err = s.finishNeonReplacement(ctx, tx, r, item); err != nil {
			return err
		}
	}
	if r.State == "deleted" {
		busy, e := s.serviceResourceConsumers(ctx, tx.Tx, r.ServiceID)
		if e != nil {
			return e
		}
		if busy {
			return ErrServiceInUse
		}
		if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM services WHERE id=? AND project_id=?`), r.ServiceID, r.ProjectID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE service_provision_runs SET state=?,payload=? WHERE id=? AND project_id=?`), run.State, jsonText(run), run.ID, r.ProjectID); err != nil {
		return err
	}
	state := "unresolved"
	if r.State == "ready" || r.State == "deleted" {
		state = "succeeded"
	}
	if r.OperationID == r.RunID {
		if err = s.UpdateMutationOutcome(ctx, tx.Tx, "service_provision", r.RunID, state); err != nil {
			return err
		}
	}
	if r.ReplacesRunID != "" && r.State == "deleted" && run.State == "failed" {
		if err = s.UpdateMutationOutcome(ctx, tx.Tx, "service_provision", r.RunID, "failed"); err != nil {
			return err
		}
	}
	if r.OperationID != r.RunID {
		if err = s.UpdateMutationOutcome(ctx, tx.Tx, "service_resource", r.OperationID, state); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// An uncertain service mutation is unlocked only by a later inspection on the
// same current agent, after the old execution lease can no longer be active.
func (s *SQLStore) ReconcileServiceRuntimeJobs(ctx context.Context, run, inspection string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE runtime_jobs SET state='acknowledged',updated_at=? WHERE service_run_id=? AND operation IN ('provision_service','service_delete') AND state='unknown' AND lease_until<=? AND EXISTS (SELECT 1 FROM runtime_jobs i WHERE i.id=? AND i.service_run_id=runtime_jobs.service_run_id AND i.server_id=runtime_jobs.server_id AND i.node_id=runtime_jobs.node_id AND i.node_generation>=runtime_jobs.node_generation AND i.operation='service_inspect' AND i.state='succeeded' AND i.created_at>runtime_jobs.updated_at AND i.created_at>runtime_jobs.lease_until AND EXISTS (SELECT 1 FROM edge_node_credentials c WHERE c.network_id=i.node_id AND c.generation=i.node_generation AND c.revoked=FALSE AND c.public_key<>'')) AND EXISTS (SELECT 1 FROM servers s WHERE s.id=runtime_jobs.server_id AND s.agent_node_id=runtime_jobs.node_id)`), stamp(now), run, stamp(now), inspection)
	if err != nil {
		return err
	}
	var remaining int
	if err = s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM runtime_jobs WHERE service_run_id=? AND operation IN ('provision_service','service_delete') AND state IN ('pending','running','unknown')`), run).Scan(&remaining); err != nil {
		return err
	}
	if remaining > 0 {
		return ErrRuntimeJobConflict
	}
	return nil
}
