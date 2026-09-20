CREATE TABLE deployment_release_notes (
    deployment_id TEXT PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
    payload TEXT NOT NULL
);
CREATE TABLE deployment_release_actions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    app_id TEXT NOT NULL DEFAULT '',
    deployment_id TEXT NOT NULL DEFAULT '',
    source_deployment_id TEXT NOT NULL DEFAULT '',
    payload TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX release_actions_app_created ON deployment_release_actions(app_id, created_at);
CREATE INDEX release_actions_project_created ON deployment_release_actions(project_id, created_at);
