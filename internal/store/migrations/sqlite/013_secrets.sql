CREATE TABLE secrets (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    environment_variable TEXT NOT NULL UNIQUE,
    encrypted_value TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE event_trigger_secrets (
    trigger_id TEXT NOT NULL REFERENCES event_triggers(id) ON DELETE CASCADE,
    secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE CASCADE,
    PRIMARY KEY(trigger_id, secret_id)
);
