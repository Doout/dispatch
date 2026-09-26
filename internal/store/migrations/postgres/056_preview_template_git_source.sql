ALTER TABLE workflow_preview_templates ADD COLUMN git_source TEXT NOT NULL DEFAULT 'null';
ALTER TABLE workflow_preview_triggers ADD COLUMN template_source TEXT NOT NULL DEFAULT 'null';
ALTER TABLE workflow_preview_templates ADD COLUMN watch_repositories TEXT NOT NULL DEFAULT '[]';
