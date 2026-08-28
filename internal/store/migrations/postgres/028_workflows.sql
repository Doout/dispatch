CREATE TABLE config_sources (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    github_app_id TEXT NOT NULL REFERENCES github_apps(id),
    name TEXT NOT NULL UNIQUE,
    repository TEXT NOT NULL,
    branch TEXT NOT NULL,
    path TEXT NOT NULL,
    sync_mode TEXT NOT NULL,
    poll_interval_seconds INTEGER NOT NULL,
    active BOOLEAN NOT NULL,
    state TEXT NOT NULL,
    last_seen_sha TEXT NOT NULL DEFAULT '',
    last_synced_at TEXT,
    last_polled_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(github_app_id, repository, branch, path)
);

CREATE TABLE workflow_resources (
    id TEXT PRIMARY KEY,
    config_source_id TEXT NOT NULL REFERENCES config_sources(id) ON DELETE CASCADE,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    document TEXT NOT NULL,
    spec_digest TEXT NOT NULL,
    config_sha TEXT NOT NULL,
    active BOOLEAN NOT NULL,
    state TEXT NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(config_source_id, path, kind, name)
);

CREATE INDEX workflow_resources_source ON workflow_resources(config_source_id, active, kind, name);

CREATE TABLE workflow_events (
    id TEXT PRIMARY KEY,
    config_source_id TEXT NOT NULL REFERENCES config_sources(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    delivery_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    repository TEXT NOT NULL,
    branch TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    state TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    processed_at TEXT,
    UNIQUE(config_source_id, provider, delivery_id)
);

CREATE TABLE workflow_revisions (
    id TEXT PRIMARY KEY,
    resource_id TEXT NOT NULL REFERENCES workflow_resources(id) ON DELETE CASCADE,
    config_sha TEXT NOT NULL,
    spec_digest TEXT NOT NULL,
    state TEXT NOT NULL,
    trigger_name TEXT NOT NULL,
    sources TEXT NOT NULL,
    outputs TEXT NOT NULL,
    log TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT
);

CREATE INDEX workflow_revisions_resource ON workflow_revisions(resource_id, created_at DESC);

CREATE TABLE workflow_job_results (
    id TEXT PRIMARY KEY,
    resource_id TEXT NOT NULL REFERENCES workflow_resources(id) ON DELETE CASCADE,
    revision_id TEXT NOT NULL REFERENCES workflow_revisions(id) ON DELETE CASCADE,
    job_name TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    reused_from_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    sources TEXT NOT NULL,
    outputs TEXT NOT NULL,
    log TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT
);

CREATE INDEX workflow_job_results_reuse ON workflow_job_results(resource_id, job_name, fingerprint, created_at DESC);

CREATE TABLE workflow_stage_runs (
    id TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL REFERENCES workflow_revisions(id) ON DELETE CASCADE,
    stage_name TEXT NOT NULL,
    target_ref TEXT NOT NULL,
    state TEXT NOT NULL,
    approval TEXT NOT NULL,
    deployment_ids TEXT NOT NULL,
    check_runs TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    UNIQUE(revision_id, stage_name)
);

CREATE INDEX workflow_stage_runs_revision ON workflow_stage_runs(revision_id, created_at);
