CREATE TABLE workflow_preview_triggers (
    id TEXT PRIMARY KEY,
    resource_id TEXT NOT NULL REFERENCES workflow_resources(id) ON DELETE CASCADE,
    github_app_id TEXT NOT NULL REFERENCES github_apps(id),
    repository TEXT NOT NULL,
    pull_request_number INTEGER NOT NULL,
    command TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(resource_id, repository, pull_request_number, command)
);

CREATE TABLE workflow_preview_comments (
    trigger_id TEXT NOT NULL REFERENCES workflow_preview_triggers(id) ON DELETE CASCADE,
    comment_id TEXT NOT NULL,
    revision_id TEXT,
    PRIMARY KEY(trigger_id, comment_id)
);
