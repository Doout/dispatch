package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/doout/dispatch/internal/core"
)

var ErrApplicationConfigurationChanged = errors.New("application configuration changed")
var ErrApplicationConfigurationManaged = errors.New("application configuration is managed by its source")

type ApplicationConfigurationStore interface {
	UpdateAppConfiguration(context.Context, core.App, string) error
	ActiveRuntimeMutation(context.Context, string) (*core.RuntimeJob, error)
}

// UpdateAppConfiguration checks the saved specification under the application row
// lock and writes only editable inputs. Runtime identity and deployment history stay intact.
func (s *SQLStore) UpdateAppConfiguration(ctx context.Context, app core.App, expected string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = changed(tx.ExecContext(ctx, s.q(`UPDATE apps SET id=id WHERE id=?`), app.ID)); err != nil {
		return err
	}
	query := appSelect + ` WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	current, err := scanApp(tx.QueryRowContext(ctx, s.q(query), app.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current.Generated || current.HelmProvenance.WorkflowResourceID != "" || current.HelmProvenance.WorkflowRevisionID != "" {
		return ErrApplicationConfigurationManaged
	}
	var managed int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT
		(SELECT COUNT(*) FROM temporary_environments WHERE app_id=?) +
		(SELECT COUNT(*) FROM workflow_preview_apps WHERE app_id=?)`), app.ID, app.ID).Scan(&managed); err != nil {
		return err
	}
	if managed != 0 {
		return ErrApplicationConfigurationManaged
	}
	if expected == "" || current.SpecDigest() != expected || current.ProjectID != app.ProjectID || current.ServerID != app.ServerID ||
		current.Name != app.Name || current.Generated != app.Generated || current.Template != app.Template {
		return ErrApplicationConfigurationChanged
	}
	var active int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT
		(SELECT COUNT(*) FROM deployments WHERE app_id=? AND state NOT IN ('succeeded','failed','cancelled')) +
		(SELECT COUNT(*) FROM runtime_jobs WHERE app_id=? AND operation NOT IN
		('inspect','logs','storage_inspect','service_inspect','workload_backup_inspect','workload_backup_offsite_inspect','workload_backup_retire_inspect','retention_inspect')
		AND state IN ('pending','running','unknown'))`), app.ID, app.ID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return ErrAppActive
	}
	if err = changed(tx.ExecContext(ctx, s.q(`UPDATE apps SET source_repo=?,branch=?,source_auth_type=?,source_credential_id=?,build_type=?,
		context_path=?,dockerfile_path=?,compose_path=?,compose_content=?,domain=?,container_port=?,helm_chart=?,helm_version=?,helm_repository=? WHERE id=?`),
		app.SourceRepo, app.Branch, app.SourceAuthType, app.SourceCredentialID, string(app.BuildType), app.ContextPath, app.DockerfilePath,
		app.ComposePath, app.ComposeContent, app.Domain, app.ContainerPort, app.HelmChart, app.HelmVersion, app.HelmRepository, app.ID)); err != nil {
		return err
	}
	return tx.Commit()
}
