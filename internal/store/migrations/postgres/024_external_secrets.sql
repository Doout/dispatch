CREATE TABLE secret_stores (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    provider TEXT NOT NULL,
    config TEXT NOT NULL DEFAULT '{}',
    encrypted_credentials TEXT NOT NULL,
    state TEXT NOT NULL,
    last_verified_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

ALTER TABLE secrets ADD COLUMN secret_source TEXT NOT NULL DEFAULT 'local';
ALTER TABLE secrets ADD COLUMN external_store_id TEXT REFERENCES secret_stores(id) ON DELETE RESTRICT;
ALTER TABLE secrets ADD COLUMN external_secret_id TEXT NOT NULL DEFAULT '';
ALTER TABLE secrets ADD COLUMN external_field TEXT NOT NULL DEFAULT '';
CREATE INDEX secrets_external_store_id ON secrets(external_store_id);
