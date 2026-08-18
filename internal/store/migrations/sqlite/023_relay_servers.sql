ALTER TABLE servers ADD COLUMN relay_access_token TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN relay_pending_events INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN relay_oldest_pending_at TEXT;
ALTER TABLE servers ADD COLUMN relay_last_connected_at TEXT;
ALTER TABLE servers ADD COLUMN relay_last_error TEXT NOT NULL DEFAULT '';

CREATE TABLE relay_webhooks (
    id TEXT PRIMARY KEY,
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    provider TEXT NOT NULL,
    provider_connection_id TEXT NOT NULL DEFAULT '',
    remote_id TEXT NOT NULL,
    url TEXT NOT NULL,
    state TEXT NOT NULL,
    last_delivery_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(server_id, remote_id)
);

ALTER TABLE github_apps ADD COLUMN relay_webhook_id TEXT NOT NULL DEFAULT '';
