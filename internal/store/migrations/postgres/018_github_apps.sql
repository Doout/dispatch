CREATE TABLE github_apps (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    web_url TEXT NOT NULL,
    api_url TEXT NOT NULL,
    app_id BIGINT NOT NULL,
    client_id TEXT NOT NULL DEFAULT '',
    slug TEXT NOT NULL DEFAULT '',
    installation_id BIGINT NOT NULL DEFAULT 0,
    installation_account TEXT NOT NULL DEFAULT '',
    webhook_url TEXT NOT NULL,
    encrypted_private_key TEXT NOT NULL,
    encrypted_webhook_secret TEXT NOT NULL,
    state TEXT NOT NULL,
    last_verified_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX github_apps_host_app_id ON github_apps(web_url, app_id);
