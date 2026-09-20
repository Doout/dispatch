ALTER TABLE role_assignments ADD COLUMN expires_at TEXT;
CREATE TABLE audit_events (
 id TEXT PRIMARY KEY, actor_id TEXT NOT NULL, actor_name TEXT NOT NULL,
 impersonator_id TEXT NOT NULL DEFAULT '', project_id TEXT NOT NULL DEFAULT '', app_id TEXT NOT NULL DEFAULT '',
 action TEXT NOT NULL, resource_id TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE INDEX audit_events_project_time ON audit_events(project_id, id DESC);
CREATE INDEX audit_events_app_time ON audit_events(app_id, id DESC);
CREATE TABLE application_owners (
 app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
 principal_type TEXT NOT NULL, principal_id TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE identity_team_mappings (
 id TEXT PRIMARY KEY, provider_id TEXT NOT NULL REFERENCES auth_providers(id) ON DELETE CASCADE,
 external_group TEXT NOT NULL, team_id TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
 UNIQUE(provider_id, external_group, team_id)
);
CREATE TABLE identity_team_members (
 mapping_id TEXT NOT NULL REFERENCES identity_team_mappings(id) ON DELETE CASCADE,
 provider_id TEXT NOT NULL REFERENCES auth_providers(id) ON DELETE CASCADE,
 team_id TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 PRIMARY KEY(mapping_id, user_id)
);
CREATE TABLE retention_policies (project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE, payload TEXT NOT NULL);
CREATE TABLE controller_backups (id TEXT PRIMARY KEY, payload TEXT NOT NULL);
