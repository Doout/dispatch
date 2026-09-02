-- dispatch:foreign-keys-off
PRAGMA legacy_alter_table = ON;

ALTER TABLE users RENAME TO users_old;

CREATE TABLE users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    email TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,
    system_role TEXT NOT NULL DEFAULT 'member' CHECK(system_role IN ('owner', 'member')),
    state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active', 'pending', 'disabled')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO users(id, username, display_name, email, password_hash, system_role, state, created_at, updated_at)
SELECT id, username, display_name, email, password_hash, system_role, state, created_at, updated_at
FROM users_old;

DROP TABLE users_old;

PRAGMA legacy_alter_table = OFF;
