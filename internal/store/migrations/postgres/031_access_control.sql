CREATE TABLE users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    email TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,
    system_role TEXT NOT NULL DEFAULT 'member' CHECK(system_role IN ('owner', 'member')),
    state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active', 'disabled')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO users(id, username, display_name, password_hash, system_role, state, created_at, updated_at)
SELECT 'legacy-admin', username, username, password_hash, 'owner', 'active', created_at, created_at
FROM admin_credentials WHERE id = 1;

ALTER TABLE admin_sessions ADD COLUMN user_id TEXT REFERENCES users(id) ON DELETE CASCADE;
UPDATE admin_sessions SET user_id = 'legacy-admin'
WHERE EXISTS (SELECT 1 FROM users WHERE id = 'legacy-admin');
CREATE INDEX admin_sessions_user_id ON admin_sessions(user_id);

CREATE TABLE teams (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE team_members (
    team_id TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'member' CHECK(role IN ('member', 'manager')),
    created_at TEXT NOT NULL,
    PRIMARY KEY(team_id, user_id)
);
CREATE INDEX team_members_user_id ON team_members(user_id);

CREATE TABLE role_assignments (
    id TEXT PRIMARY KEY,
    principal_type TEXT NOT NULL CHECK(principal_type IN ('user', 'team')),
    principal_id TEXT NOT NULL,
    scope_type TEXT NOT NULL CHECK(scope_type = 'project'),
    scope_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK(role IN ('admin', 'operator', 'deployer', 'viewer')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(principal_type, principal_id, scope_type, scope_id)
);
CREATE INDEX role_assignments_scope ON role_assignments(scope_type, scope_id);
CREATE INDEX role_assignments_principal ON role_assignments(principal_type, principal_id);
