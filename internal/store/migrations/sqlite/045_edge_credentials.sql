CREATE TABLE edge_node_credentials (
 network_id TEXT PRIMARY KEY REFERENCES private_networks(id) ON DELETE CASCADE,
 generation BIGINT NOT NULL,
 enrollment_hash TEXT NOT NULL DEFAULT '',
 enrollment_expires_at TEXT NOT NULL,
 public_key TEXT NOT NULL DEFAULT '',
 session_hash TEXT NOT NULL DEFAULT '',
 session_expires_at TEXT NOT NULL,
 revoked BOOLEAN NOT NULL DEFAULT FALSE,
 updated_at TEXT NOT NULL
);
CREATE TABLE edge_node_challenges (
 challenge_hash TEXT PRIMARY KEY,
 network_id TEXT NOT NULL REFERENCES edge_node_credentials(network_id) ON DELETE CASCADE,
 generation BIGINT NOT NULL,
 expires_at TEXT NOT NULL
);
CREATE INDEX edge_node_challenges_node ON edge_node_challenges(network_id, expires_at);
