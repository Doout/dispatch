package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

var (
	ErrPreviewGroupActive  = errors.New("preview group has active runs")
	ErrPreviewGroupOverlap = errors.New("an enabled preview group already uses this repository and command")
)

func jsonText(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (s *SQLStore) CreatePreviewGroup(ctx context.Context, group core.PreviewGroup) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO preview_groups(id,name,github_app_id,command,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`),
		group.ID, group.Name, group.GitHubAppID, group.Command, group.Enabled, stamp(group.CreatedAt), stamp(group.UpdatedAt)); err != nil {
		return err
	}
	if err := s.insertPreviewGroupComponents(ctx, tx, group); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) UpdatePreviewGroup(ctx context.Context, group core.PreviewGroup) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE preview_groups SET name=?,github_app_id=?,command=?,enabled=?,updated_at=? WHERE id=?`),
		group.Name, group.GitHubAppID, group.Command, group.Enabled, stamp(group.UpdatedAt), group.ID)
	if err := changed(result, err); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM preview_group_components WHERE group_id=?`), group.ID); err != nil {
		return err
	}
	if err := s.insertPreviewGroupComponents(ctx, tx, group); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) insertPreviewGroupComponents(ctx context.Context, tx *changeTx, group core.PreviewGroup) error {
	for _, component := range group.Components {
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO preview_group_components(
            id,group_id,app_id,alias,repository,default_branch,entrypoint,depends_on,bindings,pre_deploy_hook,post_deploy_hook)
            VALUES(?,?,?,?,?,?,?,?,?,?,?)`),
			component.ID, group.ID, component.AppID, component.Alias, component.Repository, component.DefaultBranch,
			component.Entrypoint, jsonText(component.DependsOn), jsonText(component.Bindings), component.PreDeployHook,
			component.PostDeployHook); err != nil {
			return err
		}
		for _, secretID := range component.SecretIDs {
			if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO preview_group_component_secrets(component_id,secret_id) VALUES(?,?)`), component.ID, secretID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SQLStore) DeletePreviewGroup(ctx context.Context, id string) error {
	var active int
	if err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM preview_group_runs WHERE group_id=? AND state<>'closed'`), id).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return ErrPreviewGroupActive
	}
	result, err := s.db.ExecContext(ctx, s.q(`DELETE FROM preview_groups WHERE id=?`), id)
	return changed(result, err)
}

func (s *SQLStore) GetPreviewGroup(ctx context.Context, id string) (core.PreviewGroup, error) {
	var group core.PreviewGroup
	var created, updated string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT id,name,github_app_id,command,enabled,created_at,updated_at FROM preview_groups WHERE id=?`), id).
		Scan(&group.ID, &group.Name, &group.GitHubAppID, &group.Command, &group.Enabled, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return group, ErrNotFound
	}
	if err != nil {
		return group, err
	}
	group.CreatedAt, group.UpdatedAt = parseTime(created), parseTime(updated)
	group.Components, err = s.listPreviewGroupComponents(ctx, id)
	return group, err
}

func (s *SQLStore) ListPreviewGroups(ctx context.Context) ([]core.PreviewGroup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,github_app_id,command,enabled,created_at,updated_at FROM preview_groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.PreviewGroup{}
	for rows.Next() {
		var item core.PreviewGroup
		var created, updated string
		if err := rows.Scan(&item.ID, &item.Name, &item.GitHubAppID, &item.Command, &item.Enabled, &created, &updated); err != nil {
			return nil, err
		}
		item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Components, err = s.listPreviewGroupComponents(ctx, items[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *SQLStore) listPreviewGroupComponents(ctx context.Context, groupID string) ([]core.PreviewGroupComponent, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id,group_id,app_id,alias,repository,default_branch,entrypoint,depends_on,bindings,
        pre_deploy_hook,post_deploy_hook
        FROM preview_group_components WHERE group_id=? ORDER BY alias`), groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.PreviewGroupComponent{}
	for rows.Next() {
		var item core.PreviewGroupComponent
		var dependencies, bindings string
		if err := rows.Scan(&item.ID, &item.GroupID, &item.AppID, &item.Alias, &item.Repository, &item.DefaultBranch,
			&item.Entrypoint, &dependencies, &bindings, &item.PreDeployHook, &item.PostDeployHook); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(dependencies), &item.DependsOn)
		_ = json.Unmarshal([]byte(bindings), &item.Bindings)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range items {
		items[index].SecretIDs, err = s.previewGroupComponentSecretIDs(ctx, items[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *SQLStore) previewGroupComponentSecretIDs(ctx context.Context, componentID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT secret_id FROM preview_group_component_secrets WHERE component_id=? ORDER BY secret_id`), componentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *SQLStore) MatchingPreviewGroups(ctx context.Context, _ core.EventProvider, repository, command, connectionID string) ([]core.PreviewGroup, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT DISTINCT g.id FROM preview_groups g JOIN preview_group_components c ON c.group_id=g.id
		WHERE g.enabled=? AND c.repository=? AND g.command=? AND g.github_app_id=? ORDER BY g.id`), true, repository, command, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	items := make([]core.PreviewGroup, 0, len(ids))
	for _, id := range ids {
		item, err := s.GetPreviewGroup(ctx, id)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) RecordPreviewGroupDelivery(ctx context.Context, provider core.EventProvider, deliveryID, groupID string, received time.Time) (bool, error) {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO preview_group_deliveries(provider,delivery_id,group_id,received_at) VALUES(?,?,?,?)`),
		string(provider), deliveryID, groupID, stamp(received))
	if err == nil {
		return true, nil
	}
	var count int
	lookupErr := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM preview_group_deliveries WHERE provider=? AND delivery_id=? AND group_id=?`),
		string(provider), deliveryID, groupID).Scan(&count)
	if lookupErr == nil && count > 0 {
		return false, nil
	}
	return false, err
}

func (s *SQLStore) CreatePreviewGroupRun(ctx context.Context, run core.PreviewGroupRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO preview_group_runs(
        id,group_id,slug,namespace,state,message,entrypoint_url,attempt,last_successful_sources,hook_environment,created_at,updated_at,closed_at)
        VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`), run.ID, run.GroupID, run.Slug, run.Namespace, string(run.State), run.Message,
		run.EntrypointURL, run.Attempt, jsonText(run.LastSuccessfulSources), jsonText(run.HookEnvironment), stamp(run.CreatedAt),
		stamp(run.UpdatedAt), nullTime(run.ClosedAt))
	if err != nil {
		return err
	}
	for _, source := range run.Sources {
		if source.ID == "" {
			source.ID = newID()
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO preview_group_sources(id,run_id,group_id,component_id,alias,repository,
            pull_request,head_ref,sha,base_ref,default_branch,status_comment_id,closed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`),
			source.ID, run.ID, source.GroupID, source.ComponentID, source.Alias, source.Repository, source.PullRequest,
			source.HeadRef, source.SHA, source.BaseRef, source.DefaultBranch, source.StatusCommentID, nullTime(source.ClosedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLStore) UpdatePreviewGroupRun(ctx context.Context, run core.PreviewGroupRun) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE preview_group_runs SET state=?,message=?,entrypoint_url=?,attempt=?,
		last_successful_sources=?,hook_environment=?,updated_at=?,closed_at=? WHERE id=?`), string(run.State), run.Message, run.EntrypointURL,
		run.Attempt, jsonText(run.LastSuccessfulSources), jsonText(run.HookEnvironment), stamp(run.UpdatedAt), nullTime(run.ClosedAt), run.ID)
	return changed(result, err)
}

func (s *SQLStore) GetPreviewGroupRun(ctx context.Context, id string) (core.PreviewGroupRun, error) {
	var run core.PreviewGroupRun
	var state, successful, environment, created, updated string
	var closed sql.NullString
	err := s.db.QueryRowContext(ctx, s.q(`SELECT id,group_id,slug,namespace,state,message,entrypoint_url,attempt,
		last_successful_sources,hook_environment,created_at,updated_at,closed_at FROM preview_group_runs WHERE id=?`), id).
		Scan(&run.ID, &run.GroupID, &run.Slug, &run.Namespace, &state, &run.Message, &run.EntrypointURL, &run.Attempt,
			&successful, &environment, &created, &updated, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return run, ErrNotFound
	}
	if err != nil {
		return run, err
	}
	run.State, run.CreatedAt, run.UpdatedAt, run.ClosedAt = core.PreviewGroupState(state), parseTime(created), parseTime(updated), parseNullTime(closed)
	_ = json.Unmarshal([]byte(successful), &run.LastSuccessfulSources)
	_ = json.Unmarshal([]byte(environment), &run.HookEnvironment)
	group, groupErr := s.GetPreviewGroup(ctx, run.GroupID)
	if groupErr != nil && !errors.Is(groupErr, ErrNotFound) {
		return run, groupErr
	}
	if groupErr == nil {
		run.Group = &group
	}
	run.Sources, err = s.listPreviewGroupSources(ctx, id)
	if err != nil {
		return run, err
	}
	run.Components, err = s.listPreviewGroupRunComponents(ctx, id)
	if err != nil {
		return run, err
	}
	run.Attempts, err = s.listPreviewGroupAttempts(ctx, id)
	return run, err
}

func (s *SQLStore) ListPreviewGroupRuns(ctx context.Context, groupID string) ([]core.PreviewGroupRun, error) {
	query, args := `SELECT id FROM preview_group_runs ORDER BY created_at DESC`, []any{}
	if groupID != "" {
		query, args = `SELECT id FROM preview_group_runs WHERE group_id=? ORDER BY created_at DESC`, []any{groupID}
	}
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	items := make([]core.PreviewGroupRun, 0, len(ids))
	for _, id := range ids {
		item, err := s.GetPreviewGroupRun(ctx, id)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) FindPreviewGroupRunForPR(ctx context.Context, groupID, repository string, number int) (*core.PreviewGroupRun, error) {
	var id string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT run_id FROM preview_group_sources WHERE group_id=? AND repository=?
        AND pull_request=? AND closed_at IS NULL LIMIT 1`), groupID, repository, number).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	run, err := s.GetPreviewGroupRun(ctx, id)
	return &run, err
}

func (s *SQLStore) ReplacePreviewGroupSources(ctx context.Context, runID string, sources []core.PreviewGroupSource) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM preview_group_sources WHERE run_id=?`), runID); err != nil {
		return err
	}
	for _, source := range sources {
		if source.ID == "" {
			source.ID = newID()
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO preview_group_sources(id,run_id,group_id,component_id,alias,repository,
            pull_request,head_ref,sha,base_ref,default_branch,status_comment_id,closed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`),
			source.ID, runID, source.GroupID, source.ComponentID, source.Alias, source.Repository, source.PullRequest,
			source.HeadRef, source.SHA, source.BaseRef, source.DefaultBranch, source.StatusCommentID, nullTime(source.ClosedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLStore) listPreviewGroupSources(ctx context.Context, runID string) ([]core.PreviewGroupSource, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id,run_id,group_id,component_id,alias,repository,pull_request,head_ref,sha,
        base_ref,default_branch,status_comment_id,closed_at FROM preview_group_sources WHERE run_id=? ORDER BY alias`), runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.PreviewGroupSource{}
	for rows.Next() {
		var item core.PreviewGroupSource
		var closed sql.NullString
		if err := rows.Scan(&item.ID, &item.RunID, &item.GroupID, &item.ComponentID, &item.Alias, &item.Repository,
			&item.PullRequest, &item.HeadRef, &item.SHA, &item.BaseRef, &item.DefaultBranch, &item.StatusCommentID, &closed); err != nil {
			return nil, err
		}
		item.ClosedAt = parseNullTime(closed)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) UpsertPreviewGroupRunComponent(ctx context.Context, item core.PreviewGroupRunComponent) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE preview_group_run_components SET generated_app_id=?,deployment_id=?,state=?,url=?,outputs=?,message=? WHERE run_id=? AND component_id=?`),
		nullString(item.GeneratedAppID), nullString(item.DeploymentID), item.State, item.URL, jsonText(item.Outputs), item.Message, item.RunID, item.ComponentID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows > 0 {
		return nil
	}
	if item.ID == "" {
		item.ID = newID()
	}
	_, err = s.db.ExecContext(ctx, s.q(`INSERT INTO preview_group_run_components(id,run_id,component_id,alias,generated_app_id,
        deployment_id,state,url,outputs,message) VALUES(?,?,?,?,?,?,?,?,?,?)`), item.ID, item.RunID, item.ComponentID,
		item.Alias, nullString(item.GeneratedAppID), nullString(item.DeploymentID), item.State, item.URL, jsonText(item.Outputs), item.Message)
	return err
}

func (s *SQLStore) ClearPreviewGroupRunComponents(ctx context.Context, runID string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM preview_group_run_components WHERE run_id=?`), runID)
	return err
}

func (s *SQLStore) listPreviewGroupRunComponents(ctx context.Context, runID string) ([]core.PreviewGroupRunComponent, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id,run_id,component_id,alias,generated_app_id,deployment_id,state,url,outputs,message
        FROM preview_group_run_components WHERE run_id=? ORDER BY alias`), runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.PreviewGroupRunComponent{}
	for rows.Next() {
		var item core.PreviewGroupRunComponent
		var appID, deploymentID sql.NullString
		var outputs string
		if err := rows.Scan(&item.ID, &item.RunID, &item.ComponentID, &item.Alias, &appID, &deploymentID,
			&item.State, &item.URL, &outputs, &item.Message); err != nil {
			return nil, err
		}
		item.GeneratedAppID, item.DeploymentID = appID.String, deploymentID.String
		_ = json.Unmarshal([]byte(outputs), &item.Outputs)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) CreatePreviewGroupAttempt(ctx context.Context, item core.PreviewGroupAttempt) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO preview_group_attempts(id,run_id,sequence,state,configuration,desired_sources,
        previous_sources,message,created_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?,?)`), item.ID, item.RunID, item.Sequence,
		string(item.State), jsonText(item.Configuration), jsonText(item.DesiredSources), jsonText(item.PreviousSources), item.Message,
		stamp(item.CreatedAt), nullTime(item.FinishedAt))
	return err
}

func (s *SQLStore) UpdatePreviewGroupAttempt(ctx context.Context, item core.PreviewGroupAttempt) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE preview_group_attempts SET state=?,message=?,finished_at=? WHERE id=?`),
		string(item.State), item.Message, nullTime(item.FinishedAt), item.ID)
	return changed(result, err)
}

func (s *SQLStore) listPreviewGroupAttempts(ctx context.Context, runID string) ([]core.PreviewGroupAttempt, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id,run_id,sequence,state,configuration,desired_sources,previous_sources,message,created_at,finished_at
        FROM preview_group_attempts WHERE run_id=? ORDER BY sequence DESC`), runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.PreviewGroupAttempt{}
	for rows.Next() {
		var item core.PreviewGroupAttempt
		var state, configuration, desired, previous, created string
		var finished sql.NullString
		if err := rows.Scan(&item.ID, &item.RunID, &item.Sequence, &state, &configuration, &desired, &previous,
			&item.Message, &created, &finished); err != nil {
			return nil, err
		}
		item.State, item.CreatedAt, item.FinishedAt = core.PreviewGroupState(state), parseTime(created), parseNullTime(finished)
		_ = json.Unmarshal([]byte(configuration), &item.Configuration)
		_ = json.Unmarshal([]byte(desired), &item.DesiredSources)
		_ = json.Unmarshal([]byte(previous), &item.PreviousSources)
		items = append(items, item)
	}
	return items, rows.Err()
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
