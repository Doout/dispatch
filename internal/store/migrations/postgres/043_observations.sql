CREATE TABLE application_observation_configs (
 app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 revision BIGINT NOT NULL,
 payload TEXT NOT NULL,
 webhook_ciphertext TEXT NOT NULL DEFAULT ''
);
CREATE TABLE application_observations (
 app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
 payload TEXT NOT NULL
);
CREATE TABLE application_observation_events (
 id TEXT PRIMARY KEY,
 app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 created_at TEXT NOT NULL,
 delivery TEXT NOT NULL,
 next_attempt_at TEXT NOT NULL DEFAULT '',
 payload TEXT NOT NULL
);
CREATE INDEX application_observation_events_app ON application_observation_events(app_id, created_at DESC);
CREATE INDEX application_observation_events_delivery ON application_observation_events(delivery, next_attempt_at);
