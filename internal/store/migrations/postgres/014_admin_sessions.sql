CREATE TABLE admin_sessions (
    token_hash TEXT PRIMARY KEY,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX admin_sessions_expiry ON admin_sessions(expires_at);
