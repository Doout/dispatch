-- dispatch:foreign-keys-off
PRAGMA legacy_alter_table = ON;

ALTER TABLE config_sources RENAME TO config_sources_old;

CREATE TABLE config_sources (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    github_app_id TEXT REFERENCES github_apps(id),
    credential_secret_id TEXT REFERENCES secrets(id),
    name TEXT NOT NULL UNIQUE,
    repository TEXT NOT NULL,
    branch TEXT NOT NULL,
    path TEXT NOT NULL,
    sync_mode TEXT NOT NULL,
    poll_interval_seconds INTEGER NOT NULL,
    active INTEGER NOT NULL,
    state TEXT NOT NULL,
    last_seen_sha TEXT NOT NULL DEFAULT '',
    last_synced_at TEXT,
    last_polled_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((github_app_id IS NOT NULL) <> (credential_secret_id IS NOT NULL)),
    UNIQUE(github_app_id, repository, branch, path),
    UNIQUE(credential_secret_id, repository, branch, path)
);

INSERT INTO config_sources(
    id, project_id, github_app_id, credential_secret_id, name, repository, branch, path, sync_mode,
    poll_interval_seconds, active, state, last_seen_sha, last_synced_at, last_polled_at, last_error, created_at, updated_at
)
SELECT id, project_id, github_app_id, NULL, name, repository, branch, path, sync_mode,
       poll_interval_seconds, active, state, last_seen_sha, last_synced_at, last_polled_at, last_error, created_at, updated_at
FROM config_sources_old;

DROP TABLE config_sources_old;

PRAGMA legacy_alter_table = OFF;
