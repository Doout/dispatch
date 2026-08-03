CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE servers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    address TEXT NOT NULL,
    runtime TEXT NOT NULL,
    state TEXT NOT NULL,
    agent_mode TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE apps (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    server_id TEXT NOT NULL REFERENCES servers(id),
    name TEXT NOT NULL,
    source_repo TEXT NOT NULL,
    branch TEXT NOT NULL,
    build_type TEXT NOT NULL,
    context_path TEXT NOT NULL,
    dockerfile_path TEXT NOT NULL,
    compose_path TEXT NOT NULL,
    container_port INTEGER NOT NULL,
    domain TEXT NOT NULL,
    state TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(project_id, name)
);

CREATE TABLE deployments (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL REFERENCES apps(id),
    commit_sha TEXT NOT NULL,
    spec_digest TEXT NOT NULL,
    state TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    lease_until TEXT
);

CREATE INDEX deployments_app_created
    ON deployments(app_id, created_at DESC);

CREATE TABLE deployment_logs (
    id INTEGER PRIMARY KEY,
    deployment_id TEXT NOT NULL REFERENCES deployments(id),
    level TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX deployment_logs_deployment_id
    ON deployment_logs(deployment_id, id);
