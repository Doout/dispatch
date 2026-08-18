ALTER TABLE event_triggers ADD COLUMN github_app_id TEXT NOT NULL DEFAULT '';
UPDATE event_triggers
SET github_app_id = COALESCE((SELECT source_credential_id FROM apps WHERE apps.id = event_triggers.app_id AND source_auth_type = 'github_app'), '');
CREATE INDEX event_triggers_github_app_id ON event_triggers(github_app_id);
