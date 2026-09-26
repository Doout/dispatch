ALTER TABLE apps ADD COLUMN helm_provenance TEXT NOT NULL DEFAULT '{}';
ALTER TABLE workflow_preview_triggers ADD COLUMN closed_at TEXT;
