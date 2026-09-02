CREATE TABLE laneway_applications (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    authority TEXT NOT NULL,
    remote_application_id TEXT NOT NULL,
    client_id TEXT NOT NULL,
    encrypted_client_secret TEXT NOT NULL,
    state TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(authority, remote_application_id),
    UNIQUE(authority, client_id)
);

CREATE TABLE laneway_authorization_transactions (
    state_hash TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK(kind IN ('registration', 'installation')),
    connection_name TEXT NOT NULL,
    authority TEXT NOT NULL,
    redirect_uri TEXT NOT NULL,
    application_id TEXT REFERENCES laneway_applications(id) ON DELETE CASCADE,
    encrypted_code_verifier TEXT NOT NULL,
    initiating_user_id TEXT NOT NULL DEFAULT '',
    expires_at TEXT NOT NULL,
    consumed_at TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX laneway_authorization_transactions_expiry_idx ON laneway_authorization_transactions(expires_at);

ALTER TABLE private_networks ADD COLUMN laneway_application_id TEXT REFERENCES laneway_applications(id) ON DELETE RESTRICT;
ALTER TABLE private_networks ADD COLUMN laneway_installation_id TEXT;
ALTER TABLE private_networks ADD COLUMN laneway_network_id TEXT;
CREATE UNIQUE INDEX private_networks_laneway_installation_idx ON private_networks(laneway_application_id, laneway_installation_id) WHERE laneway_application_id IS NOT NULL;
CREATE UNIQUE INDEX private_networks_laneway_network_idx ON private_networks(laneway_application_id, laneway_network_id) WHERE laneway_application_id IS NOT NULL;

CREATE TABLE laneway_refresh_leases (
    private_network_id TEXT PRIMARY KEY REFERENCES private_networks(id) ON DELETE CASCADE,
    lease_token TEXT NOT NULL,
    lease_until TEXT NOT NULL
);
CREATE INDEX laneway_refresh_leases_expiry_idx ON laneway_refresh_leases(lease_until);
