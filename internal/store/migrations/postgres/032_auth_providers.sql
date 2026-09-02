CREATE TABLE auth_providers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    provider_type TEXT NOT NULL CHECK(provider_type IN ('github')),
    base_url TEXT NOT NULL,
    api_url TEXT NOT NULL,
    client_id TEXT NOT NULL,
    encrypted_client_secret TEXT NOT NULL,
    domains TEXT NOT NULL DEFAULT '[]',
    routing_mode TEXT NOT NULL CHECK(routing_mode IN ('preferred','required')),
    provisioning TEXT NOT NULL CHECK(provisioning IN ('existing','domain')),
    state TEXT NOT NULL CHECK(state IN ('ready','disabled')),
    last_verified_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE external_identities (
    provider_id TEXT NOT NULL REFERENCES auth_providers(id) ON DELETE CASCADE,
    subject TEXT NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    login TEXT NOT NULL,
    email TEXT NOT NULL DEFAULT '',
    last_login_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY(provider_id, subject),
    UNIQUE(provider_id, user_id)
);

CREATE INDEX external_identities_login_idx ON external_identities(login);
CREATE INDEX external_identities_email_idx ON external_identities(email);
