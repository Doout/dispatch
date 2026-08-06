CREATE TABLE event_triggers (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    repository TEXT NOT NULL,
    command TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(app_id, provider, repository)
);

CREATE TABLE incoming_events (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    delivery_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    action TEXT NOT NULL,
    repository TEXT NOT NULL,
    pull_request_number INTEGER NOT NULL,
    head_ref TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    base_ref TEXT NOT NULL,
    actor TEXT NOT NULL,
    actor_association TEXT NOT NULL,
    trusted_actor BOOLEAN NOT NULL DEFAULT FALSE,
    command TEXT NOT NULL,
    arguments TEXT NOT NULL,
    source_comment_id TEXT NOT NULL,
    received_at TEXT NOT NULL,
    UNIQUE(provider, delivery_id)
);

CREATE TABLE preview_environments (
    id TEXT PRIMARY KEY,
    trigger_id TEXT NOT NULL REFERENCES event_triggers(id) ON DELETE CASCADE,
    template_app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    app_id TEXT REFERENCES apps(id) ON DELETE SET NULL,
    provider TEXT NOT NULL,
    repository TEXT NOT NULL,
    pull_request_number INTEGER NOT NULL,
    head_ref TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    base_ref TEXT NOT NULL,
    delivery_id TEXT NOT NULL,
    source_comment_id TEXT NOT NULL,
    status_comment_id TEXT NOT NULL,
    url TEXT NOT NULL,
    deployment_id TEXT NOT NULL,
    triggered_by TEXT NOT NULL,
    state TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    closed_at TEXT,
    UNIQUE(trigger_id, pull_request_number)
);

CREATE INDEX preview_lookup ON preview_environments(provider, repository, pull_request_number);
