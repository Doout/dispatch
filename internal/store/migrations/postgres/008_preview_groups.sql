ALTER TABLE apps ADD COLUMN helm_group_values TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN hook_environment TEXT NOT NULL DEFAULT '{}';
ALTER TABLE apps ADD COLUMN generated BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE deployments ADD COLUMN outputs TEXT NOT NULL DEFAULT '{}';

CREATE TABLE preview_groups (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    command TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE preview_group_components (
    id TEXT PRIMARY KEY,
    group_id TEXT NOT NULL REFERENCES preview_groups(id) ON DELETE CASCADE,
    app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE RESTRICT,
    alias TEXT NOT NULL,
    repository TEXT NOT NULL,
    default_branch TEXT NOT NULL,
    entrypoint BOOLEAN NOT NULL DEFAULT FALSE,
    depends_on TEXT NOT NULL DEFAULT '[]',
    bindings TEXT NOT NULL DEFAULT '[]',
    UNIQUE(group_id, alias),
    UNIQUE(group_id, repository)
);

CREATE INDEX preview_group_component_repository ON preview_group_components(repository);

CREATE TABLE preview_group_runs (
    id TEXT PRIMARY KEY,
    group_id TEXT NOT NULL REFERENCES preview_groups(id) ON DELETE CASCADE,
    slug TEXT NOT NULL UNIQUE,
    namespace TEXT NOT NULL UNIQUE,
    state TEXT NOT NULL,
    message TEXT NOT NULL,
    entrypoint_url TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0,
    last_successful_sources TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    closed_at TEXT
);

CREATE TABLE preview_group_sources (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES preview_group_runs(id) ON DELETE CASCADE,
    group_id TEXT NOT NULL REFERENCES preview_groups(id) ON DELETE CASCADE,
    component_id TEXT NOT NULL,
    alias TEXT NOT NULL,
    repository TEXT NOT NULL,
    pull_request INTEGER NOT NULL DEFAULT 0,
    head_ref TEXT NOT NULL,
    sha TEXT NOT NULL,
    base_ref TEXT NOT NULL,
    default_branch BOOLEAN NOT NULL DEFAULT FALSE,
    status_comment_id TEXT NOT NULL,
    closed_at TEXT,
    UNIQUE(run_id, component_id)
);

CREATE UNIQUE INDEX preview_group_active_pr ON preview_group_sources(group_id, repository, pull_request)
WHERE pull_request > 0 AND closed_at IS NULL;

CREATE TABLE preview_group_run_components (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES preview_group_runs(id) ON DELETE CASCADE,
    component_id TEXT NOT NULL,
    alias TEXT NOT NULL,
    generated_app_id TEXT REFERENCES apps(id) ON DELETE SET NULL,
    deployment_id TEXT REFERENCES deployments(id) ON DELETE SET NULL,
    state TEXT NOT NULL,
    url TEXT NOT NULL,
    outputs TEXT NOT NULL DEFAULT '{}',
    message TEXT NOT NULL,
    UNIQUE(run_id, component_id)
);

CREATE TABLE preview_group_attempts (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES preview_group_runs(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    state TEXT NOT NULL,
    configuration TEXT NOT NULL,
    desired_sources TEXT NOT NULL,
    previous_sources TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TEXT NOT NULL,
    finished_at TEXT,
    UNIQUE(run_id, sequence)
);

CREATE TABLE preview_group_deliveries (
    provider TEXT NOT NULL,
    delivery_id TEXT NOT NULL,
    group_id TEXT NOT NULL REFERENCES preview_groups(id) ON DELETE CASCADE,
    received_at TEXT NOT NULL,
    PRIMARY KEY(provider, delivery_id, group_id)
);
