ALTER TABLE private_networks ADD COLUMN token_hash TEXT NOT NULL DEFAULT '';

CREATE TABLE edge_jobs (
    id TEXT PRIMARY KEY,
    private_network_id TEXT NOT NULL REFERENCES private_networks(id) ON DELETE CASCADE,
    state TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0,
    lease_token TEXT NOT NULL DEFAULT '',
    lease_until TEXT,
    encrypted_request TEXT NOT NULL,
    encrypted_response TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX edge_jobs_ready ON edge_jobs(private_network_id, state, expires_at, created_at);
