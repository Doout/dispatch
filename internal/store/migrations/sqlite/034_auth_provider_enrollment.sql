-- dispatch:foreign-keys-off
PRAGMA legacy_alter_table = ON;

ALTER TABLE auth_providers RENAME TO auth_providers_old;

CREATE TABLE auth_providers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    provider_type TEXT NOT NULL CHECK(provider_type IN ('github')),
    base_url TEXT NOT NULL,
    api_url TEXT NOT NULL,
    client_id TEXT NOT NULL,
    encrypted_client_secret TEXT NOT NULL,
    provisioning TEXT NOT NULL CHECK(provisioning IN ('existing','approval')),
    state TEXT NOT NULL CHECK(state IN ('ready','disabled')),
    last_verified_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO auth_providers(
    id, name, provider_type, base_url, api_url, client_id,
    encrypted_client_secret, provisioning, state, last_verified_at,
    created_at, updated_at
)
SELECT
    id, name, provider_type, base_url, api_url, client_id,
    encrypted_client_secret,
    CASE provisioning WHEN 'domain' THEN 'approval' ELSE provisioning END,
    state, last_verified_at, created_at, updated_at
FROM auth_providers_old;

DROP TABLE auth_providers_old;

PRAGMA legacy_alter_table = OFF;
