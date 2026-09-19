CREATE TABLE deployment_drift_baselines (
 deployment_id TEXT PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
 app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
 server_id TEXT NOT NULL,
 namespace TEXT NOT NULL,
 release_name TEXT NOT NULL,
 ciphertext TEXT NOT NULL
);
CREATE TABLE application_drift_checks (
 app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
 payload TEXT NOT NULL
);
CREATE TABLE application_drift_actions (
 id TEXT PRIMARY KEY,
 app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
 payload TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE INDEX application_drift_actions_app ON application_drift_actions(app_id, created_at DESC);
