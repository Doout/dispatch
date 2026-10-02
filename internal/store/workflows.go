package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func (s *SQLStore) CreateConfigSource(ctx context.Context, item core.ConfigSource) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO config_sources(
		id,project_id,github_app_id,credential_secret_id,name,repository,branch,path,sync_mode,poll_interval_seconds,active,state,last_seen_sha,last_synced_at,last_polled_at,last_error,created_at,updated_at,repository_id,repository_status)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), item.ID, item.ProjectID, nullString(item.GitHubAppID), nullString(item.CredentialSecretID), item.Name, item.Repository, item.Branch,
		item.Path, item.SyncMode, item.PollIntervalSeconds, item.Active, item.State, item.LastSeenSHA, nullTime(item.LastSyncedAt),
		nullTime(item.LastPolledAt), item.LastError, stamp(item.CreatedAt), stamp(item.UpdatedAt), item.RepositoryID, jsonText(item.RepositoryStatus))
	return err
}

func (s *SQLStore) UpdateConfigSource(ctx context.Context, item core.ConfigSource) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE config_sources SET project_id=?,github_app_id=?,credential_secret_id=?,name=?,repository=?,branch=?,path=?,sync_mode=?,poll_interval_seconds=?,active=?,state=?,last_seen_sha=?,last_synced_at=?,last_polled_at=?,last_error=?,updated_at=?,repository_id=?,repository_status=? WHERE id=?`),
		item.ProjectID, nullString(item.GitHubAppID), nullString(item.CredentialSecretID), item.Name, item.Repository, item.Branch, item.Path, item.SyncMode, item.PollIntervalSeconds,
		item.Active, item.State, item.LastSeenSHA, nullTime(item.LastSyncedAt), nullTime(item.LastPolledAt), item.LastError, stamp(item.UpdatedAt), item.RepositoryID, jsonText(item.RepositoryStatus), item.ID)
	return changed(result, err)
}

func (s *SQLStore) DeleteConfigSource(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, s.q(`DELETE FROM config_sources WHERE id=?`), id)
	return changed(result, err)
}

const configSourceSelect = `SELECT id,project_id,github_app_id,credential_secret_id,name,repository,branch,path,sync_mode,poll_interval_seconds,active,state,last_seen_sha,last_synced_at,last_polled_at,last_error,created_at,updated_at,repository_id,repository_status FROM config_sources`

func (s *SQLStore) GetConfigSource(ctx context.Context, id string) (core.ConfigSource, error) {
	item, err := scanConfigSource(s.db.QueryRowContext(ctx, s.q(configSourceSelect+` WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *SQLStore) ListConfigSources(ctx context.Context) ([]core.ConfigSource, error) {
	rows, err := s.db.QueryContext(ctx, configSourceSelect+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.ConfigSource{}
	for rows.Next() {
		item, err := scanConfigSource(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanConfigSource(row scanner) (core.ConfigSource, error) {
	var item core.ConfigSource
	var githubAppID, credentialSecretID, synced, polled sql.NullString
	var created, updated, repositoryStatus string
	err := row.Scan(&item.ID, &item.ProjectID, &githubAppID, &credentialSecretID, &item.Name, &item.Repository, &item.Branch, &item.Path,
		&item.SyncMode, &item.PollIntervalSeconds, &item.Active, &item.State, &item.LastSeenSHA, &synced, &polled, &item.LastError, &created, &updated, &item.RepositoryID, &repositoryStatus)
	if err == nil {
		err = json.Unmarshal([]byte(repositoryStatus), &item.RepositoryStatus)
	}
	item.GitHubAppID, item.CredentialSecretID = githubAppID.String, credentialSecretID.String
	item.LastSyncedAt, item.LastPolledAt = parseNullTime(synced), parseNullTime(polled)
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return item, err
}

func (s *SQLStore) CreateWorkflowResource(ctx context.Context, item core.WorkflowResource) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO workflow_resources(id,config_source_id,api_version,kind,name,path,document,spec_digest,config_sha,temporary,active,state,last_error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		item.ID, item.ConfigSourceID, item.APIVersion, item.Kind, item.Name, item.Path, item.Document, item.SpecDigest, item.ConfigSHA,
		item.Temporary, item.Active, item.State, item.LastError, stamp(item.CreatedAt), stamp(item.UpdatedAt))
	if err != nil {
		return err
	}
	for _, serviceID := range item.ServiceIDs {
		if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO workflow_service_references(resource_id,service_id) VALUES(?,?)`), item.ID, serviceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLStore) UpdateWorkflowResource(ctx context.Context, item core.WorkflowResource) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_resources SET api_version=?,kind=?,name=?,path=?,document=?,spec_digest=?,config_sha=?,temporary=?,active=?,state=?,last_error=?,updated_at=? WHERE id=?`),
		item.APIVersion, item.Kind, item.Name, item.Path, item.Document, item.SpecDigest, item.ConfigSHA, item.Temporary, item.Active, item.State, item.LastError, stamp(item.UpdatedAt), item.ID)
	return changed(result, err)
}

const workflowResourceSelect = `SELECT id,config_source_id,api_version,kind,name,path,document,spec_digest,config_sha,temporary,active,state,last_error,created_at,updated_at FROM workflow_resources`

func (s *SQLStore) GetWorkflowResource(ctx context.Context, id string) (core.WorkflowResource, error) {
	item, err := scanWorkflowResource(s.db.QueryRowContext(ctx, s.q(workflowResourceSelect+` WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *SQLStore) ListWorkflowResources(ctx context.Context, configSourceID string) ([]core.WorkflowResource, error) {
	query, args := workflowResourceSelect, []any{}
	if configSourceID != "" {
		query += ` WHERE config_source_id=?`
		args = append(args, configSourceID)
	}
	query += ` ORDER BY kind,name`
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WorkflowResource{}
	for rows.Next() {
		item, err := scanWorkflowResource(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ReplaceWorkflowResources commits one validated repository snapshot together
// with the configuration source cursor. Readers see either the prior valid set
// or the complete replacement.
func (s *SQLStore) ReplaceWorkflowResources(ctx context.Context, source core.ConfigSource, items []core.WorkflowResource) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_resources SET active=?,state=?,updated_at=? WHERE config_source_id=? AND temporary=?`), false, "removed", stamp(source.UpdatedAt), source.ID, false); err != nil {
		return err
	}
	for _, item := range items {
		result, updateErr := tx.ExecContext(ctx, s.q(`UPDATE workflow_resources SET api_version=?,kind=?,name=?,path=?,document=?,spec_digest=?,config_sha=?,active=?,state=?,last_error=?,updated_at=? WHERE id=? AND config_source_id=? AND temporary=?`),
			item.APIVersion, item.Kind, item.Name, item.Path, item.Document, item.SpecDigest, item.ConfigSHA, item.Active, item.State, item.LastError, stamp(item.UpdatedAt), item.ID, source.ID, false)
		if updateErr != nil {
			return updateErr
		}
		count, countErr := result.RowsAffected()
		if countErr != nil {
			return countErr
		}
		if count == 0 {
			if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO workflow_resources(id,config_source_id,api_version,kind,name,path,document,spec_digest,config_sha,temporary,active,state,last_error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
				item.ID, item.ConfigSourceID, item.APIVersion, item.Kind, item.Name, item.Path, item.Document, item.SpecDigest, item.ConfigSHA,
				false, item.Active, item.State, item.LastError, stamp(item.CreatedAt), stamp(item.UpdatedAt)); err != nil {
				return err
			}
		}
		// Invalid resources retain their last accepted service dependencies.
		if item.State == "invalid" {
			continue
		}
		if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM workflow_service_references WHERE resource_id=?`), item.ID); err != nil {
			return err
		}
		for _, serviceID := range item.ServiceIDs {
			var payload string
			if err = tx.QueryRowContext(ctx, s.q(s.serviceLockQuery()), serviceID).Scan(&payload); err != nil {
				return err
			}
			connection, decodeErr := decodeService(payload)
			if decodeErr != nil {
				return decodeErr
			}
			if connection.ProjectID != source.ProjectID {
				return errors.New("service is outside the configuration project")
			}
			if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO workflow_service_references(resource_id,service_id) VALUES(?,?)`), item.ID, serviceID); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM workflow_service_references WHERE resource_id IN (SELECT id FROM workflow_resources WHERE config_source_id=? AND state='removed')`), source.ID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE config_sources SET project_id=?,github_app_id=?,credential_secret_id=?,name=?,repository=?,branch=?,path=?,sync_mode=?,poll_interval_seconds=?,active=?,state=?,last_seen_sha=?,last_synced_at=?,last_polled_at=?,last_error=?,updated_at=?,repository_id=?,repository_status=? WHERE id=?`),
		source.ProjectID, nullString(source.GitHubAppID), nullString(source.CredentialSecretID), source.Name, source.Repository, source.Branch, source.Path, source.SyncMode, source.PollIntervalSeconds,
		source.Active, source.State, source.LastSeenSHA, nullTime(source.LastSyncedAt), nullTime(source.LastPolledAt), source.LastError, stamp(source.UpdatedAt), source.RepositoryID, jsonText(source.RepositoryStatus), source.ID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	err = tx.Commit()
	return err
}

func scanWorkflowResource(row scanner) (core.WorkflowResource, error) {
	var item core.WorkflowResource
	var created, updated string
	err := row.Scan(&item.ID, &item.ConfigSourceID, &item.APIVersion, &item.Kind, &item.Name, &item.Path, &item.Document,
		&item.SpecDigest, &item.ConfigSHA, &item.Temporary, &item.Active, &item.State, &item.LastError, &created, &updated)
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return item, err
}

func (s *SQLStore) CreateWorkflowEvent(ctx context.Context, item core.WorkflowEvent) (bool, error) {
	source, err := s.GetConfigSource(ctx, item.ConfigSourceID)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`INSERT INTO workflow_events(id,config_source_id,provider,delivery_id,kind,repository,branch,commit_sha,state,error,created_at,processed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(config_source_id,provider,delivery_id) DO NOTHING`), item.ID, item.ConfigSourceID, item.Provider, item.DeliveryID, item.Kind, item.Repository, item.Branch, item.CommitSHA, item.State, item.Error, stamp(item.CreatedAt), nullTime(item.ProcessedAt))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count > 0 && workflowEventHasActivity(item) {
		if err := s.saveEventActivity(ctx, tx, "workflow-event:"+item.ID, workflowEventActivity(item, source)); err != nil {
			return false, err
		}
	}
	return count > 0, tx.Commit()
}

func (s *SQLStore) UpdateWorkflowEvent(ctx context.Context, item core.WorkflowEvent) error {
	source, err := s.GetConfigSource(ctx, item.ConfigSourceID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workflow_events SET state=?,error=?,processed_at=? WHERE id=?`), item.State, item.Error, nullTime(item.ProcessedAt), item.ID)
	if err := changed(result, err); err != nil {
		return err
	}
	if workflowEventHasActivity(item) {
		if err := s.saveEventActivity(ctx, tx, "workflow-event:"+item.ID, workflowEventActivity(item, source)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func workflowEventHasActivity(item core.WorkflowEvent) bool {
	return item.Provider != "poll" || item.Kind != "branch_scan" || item.State == "failed" || len(item.RevisionIDs) > 0
}

func workflowEventActivity(item core.WorkflowEvent, source core.ConfigSource) core.EventActivity {
	transport := "webhook"
	if item.Provider == "poll" {
		transport = "poll"
	}
	return core.EventActivity{ID: item.ID, ProjectID: source.ProjectID, RuleID: "configuration:" + source.ID, Name: source.Name, Transport: transport,
		Kind: item.Kind, Repository: item.Repository, Branch: item.Branch, CommitSHA: item.CommitSHA, State: item.State, Message: item.Error,
		ResourceID: item.ResourceID, RevisionIDs: item.RevisionIDs, CreatedAt: item.CreatedAt}
}

func (s *SQLStore) CreateWorkflowRevision(ctx context.Context, item core.WorkflowRevision) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workflow_revisions(id,resource_id,config_sha,spec_digest,state,trigger_name,sources,outputs,error,created_at,started_at,finished_at,pull_requests,feedback,feedback_pending,source_trust) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		item.ID, item.ResourceID, item.ConfigSHA, item.SpecDigest, item.State, item.Trigger, jsonText(item.Sources), jsonText(item.Outputs), item.Error,
		stamp(item.CreatedAt), nullTime(item.StartedAt), nullTime(item.FinishedAt), jsonText(item.PullRequests), jsonText(item.Feedback), item.Feedback != nil && !item.Feedback.Complete, jsonText(item.SourceTrust))
	return err
}

// Source trust is updated independently so a runtime holding an older revision
// cannot overwrite a newer decision while saving job or stage progress.
func (s *SQLStore) UpdateWorkflowRevision(ctx context.Context, item core.WorkflowRevision) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_revisions SET state=?,sources=?,outputs=?,error=?,started_at=?,finished_at=? WHERE id=? AND state!='cancelled'`),
		item.State, jsonText(item.Sources), jsonText(item.Outputs), item.Error, nullTime(item.StartedAt), nullTime(item.FinishedAt), item.ID)
	return changed(result, err)
}

// SupersedeWorkflowRevisions marks queued and running work terminal before a
// newer preview is scheduled. Workers may finish asynchronously, so their
// ordinary updates cannot write over the cancelled state.
func (s *SQLStore) SupersedeWorkflowRevisions(ctx context.Context, resourceID string) ([]string, error) {
	return s.supersedeWorkflowRevisions(ctx, resourceID, false)
}

func (s *SQLStore) SupersedeWorkflowTestRevisions(ctx context.Context, resourceID string) ([]string, error) {
	return s.supersedeWorkflowRevisions(ctx, resourceID, true)
}

func (s *SQLStore) supersedeWorkflowRevisions(ctx context.Context, resourceID string, testsOnly bool) ([]string, error) {
	filter := ""
	reason := "Superseded by a newer preview run"
	if testsOnly {
		filter = " AND trigger_name LIKE 'pull request test %'"
		reason = "Superseded by a newer preview test"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, s.q(`SELECT id FROM workflow_revisions WHERE resource_id=? AND state IN ('queued','running','awaiting_approval')`+filter), resourceID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return ids, tx.Commit()
	}
	now := stamp(time.Now().UTC())
	for _, table := range []string{"workflow_job_results", "workflow_stage_runs"} {
		if _, err := tx.ExecContext(ctx, s.q(`UPDATE `+table+` SET state='cancelled',error=?,finished_at=? WHERE revision_id IN (SELECT id FROM workflow_revisions WHERE resource_id=? AND state IN ('queued','running','awaiting_approval')`+filter+`) AND state IN ('queued','running','awaiting_approval')`), reason, now, resourceID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE workflow_revisions SET state='cancelled',error=?,finished_at=? WHERE resource_id=? AND state IN ('queued','running','awaiting_approval')`+filter), reason, now, resourceID); err != nil {
		return nil, err
	}
	return ids, tx.Commit()
}

const workflowRevisionSelect = `SELECT id,resource_id,config_sha,spec_digest,state,trigger_name,sources,outputs,error,created_at,started_at,finished_at,pull_requests,feedback,source_trust FROM workflow_revisions`

func (s *SQLStore) GetWorkflowRevision(ctx context.Context, id string) (core.WorkflowRevision, error) {
	item, err := scanWorkflowRevision(s.db.QueryRowContext(ctx, s.q(workflowRevisionSelect+` WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *SQLStore) ListWorkflowRevisions(ctx context.Context, resourceID string, limit int) ([]core.WorkflowRevision, error) {
	query, args := workflowRevisionSelect, []any{}
	if resourceID != "" {
		query += ` WHERE resource_id=?`
		args = append(args, resourceID)
	}
	query += ` ORDER BY created_at DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WorkflowRevision{}
	for rows.Next() {
		item, err := scanWorkflowRevision(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanWorkflowRevision(row scanner) (core.WorkflowRevision, error) {
	var item core.WorkflowRevision
	var sources, outputs, created, pullRequests, feedback, sourceTrust string
	var started, finished sql.NullString
	err := row.Scan(&item.ID, &item.ResourceID, &item.ConfigSHA, &item.SpecDigest, &item.State, &item.Trigger, &sources, &outputs,
		&item.Error, &created, &started, &finished, &pullRequests, &feedback, &sourceTrust)
	_ = json.Unmarshal([]byte(sources), &item.Sources)
	_ = json.Unmarshal([]byte(outputs), &item.Outputs)
	_ = json.Unmarshal([]byte(pullRequests), &item.PullRequests)
	_ = json.Unmarshal([]byte(feedback), &item.Feedback)
	_ = json.Unmarshal([]byte(sourceTrust), &item.SourceTrust)
	item.CreatedAt, item.StartedAt, item.FinishedAt = parseTime(created), parseNullTime(started), parseNullTime(finished)
	return item, err
}

func (s *SQLStore) CreateWorkflowJobResult(ctx context.Context, item core.WorkflowJobResult) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workflow_job_results(id,resource_id,revision_id,job_name,fingerprint,reused_from_id,state,sources,outputs,log,error,created_at,started_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		item.ID, item.ResourceID, item.RevisionID, item.JobName, item.Fingerprint, item.ReusedFromID, item.State, jsonText(item.Sources), jsonText(item.Outputs), item.Log, item.Error,
		stamp(item.CreatedAt), nullTime(item.StartedAt), nullTime(item.FinishedAt))
	return err
}

func (s *SQLStore) UpdateWorkflowJobResult(ctx context.Context, item core.WorkflowJobResult) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_job_results SET state=?,sources=?,outputs=?,log=?,error=?,started_at=?,finished_at=? WHERE id=? AND state!='cancelled'`),
		item.State, jsonText(item.Sources), jsonText(item.Outputs), item.Log, item.Error, nullTime(item.StartedAt), nullTime(item.FinishedAt), item.ID)
	return changed(result, err)
}

const workflowJobResultSelect = `SELECT id,resource_id,revision_id,job_name,fingerprint,reused_from_id,state,sources,outputs,log,error,created_at,started_at,finished_at FROM workflow_job_results`

func (s *SQLStore) FindWorkflowJobResult(ctx context.Context, resourceID, jobName, fingerprint string) (core.WorkflowJobResult, error) {
	item, err := scanWorkflowJobResult(s.db.QueryRowContext(ctx, s.q(workflowJobResultSelect+` WHERE resource_id IN (SELECT id FROM workflow_resources WHERE kind='Application' AND config_source_id=(SELECT config_source_id FROM workflow_resources WHERE id=?)) AND job_name=? AND fingerprint=? AND state='succeeded' ORDER BY created_at DESC LIMIT 1`), resourceID, jobName, fingerprint))
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *SQLStore) ListWorkflowJobResults(ctx context.Context, revisionID string) ([]core.WorkflowJobResult, error) {
	rows, err := s.db.QueryContext(ctx, s.q(workflowJobResultSelect+` WHERE revision_id=? ORDER BY created_at`), revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WorkflowJobResult{}
	for rows.Next() {
		item, err := scanWorkflowJobResult(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanWorkflowJobResult(row scanner) (core.WorkflowJobResult, error) {
	var item core.WorkflowJobResult
	var sources, outputs, created string
	var started, finished sql.NullString
	err := row.Scan(&item.ID, &item.ResourceID, &item.RevisionID, &item.JobName, &item.Fingerprint, &item.ReusedFromID, &item.State, &sources, &outputs,
		&item.Log, &item.Error, &created, &started, &finished)
	_ = json.Unmarshal([]byte(sources), &item.Sources)
	_ = json.Unmarshal([]byte(outputs), &item.Outputs)
	item.CreatedAt, item.StartedAt, item.FinishedAt = parseTime(created), parseNullTime(started), parseNullTime(finished)
	return item, err
}

func (s *SQLStore) CreateWorkflowStageRun(ctx context.Context, item core.WorkflowStageRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.checkRuntimeReferences(ctx, tx, item.DeploymentIDs); err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO workflow_stage_runs(id,revision_id,stage_name,target_ref,state,approval,deployment_ids,check_runs,error,created_at,started_at,finished_at,deployment_results) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		item.ID, item.RevisionID, item.StageName, item.TargetRef, item.State, item.Approval, jsonText(item.DeploymentIDs), jsonText(item.CheckRuns), item.Error,
		stamp(item.CreatedAt), nullTime(item.StartedAt), nullTime(item.FinishedAt), jsonText(item.DeploymentResults))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) UpdateWorkflowStageRun(ctx context.Context, item core.WorkflowStageRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.checkRuntimeReferences(ctx, tx, item.DeploymentIDs); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, s.q(`UPDATE workflow_stage_runs SET state=?,approval=?,deployment_ids=?,check_runs=?,error=?,started_at=?,finished_at=?,deployment_results=? WHERE id=? AND state!='cancelled'`),
		item.State, item.Approval, jsonText(item.DeploymentIDs), jsonText(item.CheckRuns), item.Error, nullTime(item.StartedAt), nullTime(item.FinishedAt), jsonText(item.DeploymentResults), item.ID)
	if err = changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

const workflowStageRunSelect = `SELECT id,revision_id,stage_name,target_ref,state,approval,deployment_ids,check_runs,error,created_at,started_at,finished_at,deployment_results FROM workflow_stage_runs`

func (s *SQLStore) GetWorkflowStageRun(ctx context.Context, id string) (core.WorkflowStageRun, error) {
	item, err := scanWorkflowStageRun(s.db.QueryRowContext(ctx, s.q(workflowStageRunSelect+` WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *SQLStore) ListWorkflowStageRuns(ctx context.Context, revisionID string) ([]core.WorkflowStageRun, error) {
	query, args := workflowStageRunSelect, []any{}
	if revisionID != "" {
		query += ` WHERE revision_id=?`
		args = append(args, revisionID)
	}
	query += ` ORDER BY created_at`
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WorkflowStageRun{}
	for rows.Next() {
		item, err := scanWorkflowStageRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanWorkflowStageRun(row scanner) (core.WorkflowStageRun, error) {
	var item core.WorkflowStageRun
	var deploymentIDs, checkRuns, created, deploymentResults string
	var started, finished sql.NullString
	err := row.Scan(&item.ID, &item.RevisionID, &item.StageName, &item.TargetRef, &item.State, &item.Approval, &deploymentIDs, &checkRuns,
		&item.Error, &created, &started, &finished, &deploymentResults)
	_ = json.Unmarshal([]byte(deploymentIDs), &item.DeploymentIDs)
	_ = json.Unmarshal([]byte(checkRuns), &item.CheckRuns)
	_ = json.Unmarshal([]byte(deploymentResults), &item.DeploymentResults)
	item.CreatedAt, item.StartedAt, item.FinishedAt = parseTime(created), parseNullTime(started), parseNullTime(finished)
	return item, err
}

// Bound legacy cache inspection to recent successes in this configuration.
func (s *SQLStore) ListReusableWorkflowJobResults(ctx context.Context, resourceID, jobName string) ([]core.WorkflowJobResult, error) {
	rows, err := s.db.QueryContext(ctx, s.q(workflowJobResultSelect+` WHERE resource_id IN (SELECT id FROM workflow_resources WHERE kind='Application' AND config_source_id=(SELECT config_source_id FROM workflow_resources WHERE id=?)) AND job_name=? AND state='succeeded' ORDER BY created_at DESC LIMIT 100`), resourceID, jobName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []core.WorkflowJobResult{}
	for rows.Next() {
		item, err := scanWorkflowJobResult(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	return results, rows.Err()
}

// Feedback updates are separate from worker updates so reporting progress cannot
// be overwritten by the worker's original revision snapshot.
func (s *SQLStore) UpdateWorkflowFeedback(ctx context.Context, id string, feedback *core.WorkflowFeedback) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_revisions SET feedback=?,feedback_pending=? WHERE id=?`), jsonText(feedback), feedback != nil && !feedback.Complete, id)
	return changed(result, err)
}

func (s *SQLStore) PendingWorkflowFeedback(ctx context.Context, resourceID string) ([]core.WorkflowRevision, error) {
	query := workflowRevisionSelect + ` WHERE feedback_pending=TRUE`
	args := []any{}
	if resourceID != "" {
		query += ` AND resource_id=?`
		args = append(args, resourceID)
	}
	rows, err := s.db.QueryContext(ctx, s.q(query+` ORDER BY id`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WorkflowRevision{}
	for rows.Next() {
		item, err := scanWorkflowRevision(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) AcquireWorkflowFeedbackLease(ctx context.Context, resourceID, holder string, now, until time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workflow_feedback_leases(resource_id,holder,lease_until) VALUES(?,?,?)
 ON CONFLICT(resource_id) DO UPDATE SET holder=excluded.holder,lease_until=excluded.lease_until WHERE workflow_feedback_leases.lease_until<?`), resourceID, holder, stamp(until), stamp(now))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}
func (s *SQLStore) ReleaseWorkflowFeedbackLease(ctx context.Context, resourceID, holder string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM workflow_feedback_leases WHERE resource_id=? AND holder=?`), resourceID, holder)
	return err
}
