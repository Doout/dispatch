CREATE TABLE workflow_preview_templates (
    id TEXT PRIMARY KEY,
    config_source_id TEXT NOT NULL REFERENCES config_sources(id) ON DELETE CASCADE,
    github_app_id TEXT NOT NULL REFERENCES github_apps(id),
    name TEXT NOT NULL,
    repository TEXT NOT NULL,
    command TEXT NOT NULL,
    preview_url TEXT NOT NULL,
    document TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX workflow_preview_templates_active_command
    ON workflow_preview_templates(github_app_id, repository, command) WHERE active;

ALTER TABLE workflow_preview_triggers ADD COLUMN template_id TEXT REFERENCES workflow_preview_templates(id) ON DELETE SET NULL;
