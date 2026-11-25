package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/doout/dispatch/internal/core"
)

const repositoryConnectionSelect = `SELECT id, project_id, name, repository_url, COALESCE(github_app_id,''), COALESCE(credential_secret_id,''), updated_at FROM repository_connections`

func scanRepositoryConnection(row scanner) (core.RepositoryConnection, error) {
	var item core.RepositoryConnection
	var updated string
	err := row.Scan(&item.ID, &item.ProjectID, &item.Name, &item.RepositoryURL, &item.GitHubAppID, &item.CredentialSecretID, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	if err == nil {
		item.UpdatedAt = parseTime(updated)
	}
	return item, err
}

func (s *SQLStore) GetRepositoryConnection(ctx context.Context, id string) (core.RepositoryConnection, error) {
	return scanRepositoryConnection(s.db.QueryRowContext(ctx, s.q(repositoryConnectionSelect+` WHERE id=?`), id))
}

func (s *SQLStore) GetRepositoryConnectionByName(ctx context.Context, projectID, name string) (core.RepositoryConnection, error) {
	return scanRepositoryConnection(s.db.QueryRowContext(ctx, s.q(repositoryConnectionSelect+` WHERE project_id=? AND name=?`), projectID, name))
}

func (s *SQLStore) ListRepositoryConnections(ctx context.Context) ([]core.RepositoryConnection, error) {
	rows, err := s.db.QueryContext(ctx, repositoryConnectionSelect+` ORDER BY project_id,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.RepositoryConnection{}
	for rows.Next() {
		item, err := scanRepositoryConnection(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// GitHub Apps and named connections can both be referenced by a YAML connection name.
// Serialize claims for the same name across the two tables. SQLite serializes writers itself.
func (s *SQLStore) writeConnectionName(ctx context.Context, name, conflictQuery string, write func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.postgres {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(109, hashtext($1))`, name); err != nil {
			return err
		}
	}
	var conflict bool
	if err := tx.QueryRowContext(ctx, s.q(conflictQuery), name).Scan(&conflict); err != nil {
		return err
	}
	if conflict {
		return ErrAlreadyExists
	}
	if err := write(tx.Tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) CreateRepositoryConnection(ctx context.Context, item core.RepositoryConnection) error {
	return s.writeConnectionName(ctx, item.Name, `SELECT EXISTS(SELECT 1 FROM github_apps WHERE name=?)`, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.q(`INSERT INTO repository_connections(id,project_id,name,repository_url,github_app_id,credential_secret_id,updated_at)
			VALUES(?,?,?,?,?,?,?) ON CONFLICT(project_id,name) DO NOTHING`), item.ID, item.ProjectID, item.Name, item.RepositoryURL, nullString(item.GitHubAppID), nullString(item.CredentialSecretID), stamp(item.UpdatedAt))
		if err = changed(result, err); errors.Is(err, ErrNotFound) {
			return ErrAlreadyExists
		}
		return err
	})
}

func (s *SQLStore) UpdateRepositoryConnection(ctx context.Context, item core.RepositoryConnection) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE repository_connections SET github_app_id=?, credential_secret_id=?, updated_at=? WHERE id=? AND project_id=? AND name=? AND repository_url=?`),
		nullString(item.GitHubAppID), nullString(item.CredentialSecretID), stamp(item.UpdatedAt), item.ID, item.ProjectID, item.Name, item.RepositoryURL)
	return changed(result, err)
}
