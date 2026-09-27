-- dispatch:migration 000_schema_migrations
CREATE TABLE IF NOT EXISTS schema_migrations (
    version TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL
);

-- dispatch:migration 001_initial
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

-- dispatch:migration 002_admin_credentials
CREATE TABLE admin_credentials (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at TEXT NOT NULL
);

-- dispatch:migration 003_inline_compose
ALTER TABLE apps ADD COLUMN compose_content TEXT NOT NULL DEFAULT '';

-- dispatch:migration 004_kubernetes_servers
ALTER TABLE servers ADD COLUMN kubeconfig_path TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN kube_context TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN kube_namespace TEXT NOT NULL DEFAULT '';

-- dispatch:migration 005_helm_applications
ALTER TABLE apps ADD COLUMN helm_chart TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN helm_version TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN helm_repository TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN helm_values TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN helm_namespace TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN helm_release TEXT NOT NULL DEFAULT '';

-- dispatch:migration 006_preview_events
CREATE TABLE event_triggers (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    repository TEXT NOT NULL,
    command TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
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
    trusted_actor INTEGER NOT NULL DEFAULT 0,
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

-- dispatch:migration 007_deployment_hooks
ALTER TABLE apps ADD COLUMN pre_deploy_hook TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN post_deploy_hook TEXT NOT NULL DEFAULT '';

-- dispatch:migration 008_preview_groups
ALTER TABLE apps ADD COLUMN helm_group_values TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN hook_environment TEXT NOT NULL DEFAULT '{}';
ALTER TABLE apps ADD COLUMN generated INTEGER NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN outputs TEXT NOT NULL DEFAULT '{}';

CREATE TABLE preview_groups (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    command TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE preview_group_components (
    id TEXT PRIMARY KEY,
    group_id TEXT NOT NULL REFERENCES preview_groups(id) ON DELETE CASCADE,
    app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE RESTRICT,
    alias TEXT NOT NULL,
    repository TEXT NOT NULL,
    default_branch TEXT NOT NULL,
    entrypoint INTEGER NOT NULL DEFAULT 0,
    depends_on TEXT NOT NULL DEFAULT '[]',
    bindings TEXT NOT NULL DEFAULT '[]',
    UNIQUE(group_id, alias),
    UNIQUE(group_id, repository)
);

CREATE INDEX preview_group_component_repository ON preview_group_components(repository);

CREATE TABLE preview_group_runs (
    id TEXT PRIMARY KEY,
    group_id TEXT NOT NULL REFERENCES preview_groups(id) ON DELETE CASCADE,
    slug TEXT NOT NULL UNIQUE,
    namespace TEXT NOT NULL UNIQUE,
    state TEXT NOT NULL,
    message TEXT NOT NULL,
    entrypoint_url TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0,
    last_successful_sources TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    closed_at TEXT
);

CREATE TABLE preview_group_sources (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES preview_group_runs(id) ON DELETE CASCADE,
    group_id TEXT NOT NULL REFERENCES preview_groups(id) ON DELETE CASCADE,
    component_id TEXT NOT NULL,
    alias TEXT NOT NULL,
    repository TEXT NOT NULL,
    pull_request INTEGER NOT NULL DEFAULT 0,
    head_ref TEXT NOT NULL,
    sha TEXT NOT NULL,
    base_ref TEXT NOT NULL,
    default_branch INTEGER NOT NULL DEFAULT 0,
    status_comment_id TEXT NOT NULL,
    closed_at TEXT,
    UNIQUE(run_id, component_id)
);

CREATE UNIQUE INDEX preview_group_active_pr ON preview_group_sources(group_id, repository, pull_request)
WHERE pull_request > 0 AND closed_at IS NULL;

CREATE TABLE preview_group_run_components (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES preview_group_runs(id) ON DELETE CASCADE,
    component_id TEXT NOT NULL,
    alias TEXT NOT NULL,
    generated_app_id TEXT REFERENCES apps(id) ON DELETE SET NULL,
    deployment_id TEXT REFERENCES deployments(id) ON DELETE SET NULL,
    state TEXT NOT NULL,
    url TEXT NOT NULL,
    outputs TEXT NOT NULL DEFAULT '{}',
    message TEXT NOT NULL,
    UNIQUE(run_id, component_id)
);

CREATE TABLE preview_group_attempts (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES preview_group_runs(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    state TEXT NOT NULL,
    configuration TEXT NOT NULL,
    desired_sources TEXT NOT NULL,
    previous_sources TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TEXT NOT NULL,
    finished_at TEXT,
    UNIQUE(run_id, sequence)
);

CREATE TABLE preview_group_deliveries (
    provider TEXT NOT NULL,
    delivery_id TEXT NOT NULL,
    group_id TEXT NOT NULL REFERENCES preview_groups(id) ON DELETE CASCADE,
    received_at TEXT NOT NULL,
    PRIMARY KEY(provider, delivery_id, group_id)
);

-- dispatch:migration 009_event_hooks
ALTER TABLE event_triggers ADD COLUMN pre_deploy_hook TEXT NOT NULL DEFAULT '';
ALTER TABLE event_triggers ADD COLUMN post_deploy_hook TEXT NOT NULL DEFAULT '';
UPDATE event_triggers
SET pre_deploy_hook = COALESCE((SELECT pre_deploy_hook FROM apps WHERE apps.id = event_triggers.app_id), ''),
    post_deploy_hook = COALESCE((SELECT post_deploy_hook FROM apps WHERE apps.id = event_triggers.app_id), '');

ALTER TABLE preview_group_components ADD COLUMN pre_deploy_hook TEXT NOT NULL DEFAULT '';
ALTER TABLE preview_group_components ADD COLUMN post_deploy_hook TEXT NOT NULL DEFAULT '';
UPDATE preview_group_components
SET pre_deploy_hook = COALESCE((SELECT pre_deploy_hook FROM apps WHERE apps.id = preview_group_components.app_id), ''),
    post_deploy_hook = COALESCE((SELECT post_deploy_hook FROM apps WHERE apps.id = preview_group_components.app_id), '');

ALTER TABLE preview_environments ADD COLUMN pre_deploy_hook TEXT NOT NULL DEFAULT '';
ALTER TABLE preview_environments ADD COLUMN post_deploy_hook TEXT NOT NULL DEFAULT '';
ALTER TABLE preview_environments ADD COLUMN hook_environment TEXT NOT NULL DEFAULT '{}';

ALTER TABLE preview_group_runs ADD COLUMN hook_environment TEXT NOT NULL DEFAULT '{}';

-- dispatch:migration 010_stored_kubeconfigs
ALTER TABLE servers ADD COLUMN kubeconfig_data TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN kube_ca_data TEXT NOT NULL DEFAULT '';

-- dispatch:migration 011_application_templates
ALTER TABLE apps ADD COLUMN template BOOLEAN NOT NULL DEFAULT FALSE;

-- dispatch:migration 012_openshift_servers
ALTER TABLE servers ADD COLUMN openshift_service_account TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN openshift_service_account_namespace TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN openshift_token_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN openshift_connected_at TEXT;

-- dispatch:migration 013_secrets
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

-- dispatch:migration 014_admin_sessions
CREATE TABLE admin_sessions (
    token_hash TEXT PRIMARY KEY,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX admin_sessions_expiry ON admin_sessions(expires_at);

-- dispatch:migration 015_app_source_credentials
ALTER TABLE apps ADD COLUMN source_auth_type TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN source_credential_id TEXT NOT NULL DEFAULT '';

-- dispatch:migration 016_secret_types
ALTER TABLE secrets ADD COLUMN secret_type TEXT NOT NULL DEFAULT 'text';
ALTER TABLE secrets ADD COLUMN public_value TEXT NOT NULL DEFAULT '';

-- dispatch:migration 017_preview_group_component_secrets
CREATE TABLE preview_group_component_secrets (
    component_id TEXT NOT NULL REFERENCES preview_group_components(id) ON DELETE CASCADE,
    secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
    PRIMARY KEY(component_id, secret_id)
);

-- dispatch:migration 018_github_apps
CREATE TABLE github_apps (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    web_url TEXT NOT NULL,
    api_url TEXT NOT NULL,
    app_id INTEGER NOT NULL,
    client_id TEXT NOT NULL DEFAULT '',
    slug TEXT NOT NULL DEFAULT '',
    installation_id INTEGER NOT NULL DEFAULT 0,
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

-- dispatch:migration 019_github_app_registration_owner
ALTER TABLE github_apps ADD COLUMN registration_owner TEXT NOT NULL DEFAULT '';
ALTER TABLE github_apps ADD COLUMN registration_owner_type TEXT NOT NULL DEFAULT '';

-- dispatch:migration 020_preview_group_github_app
ALTER TABLE preview_groups ADD COLUMN github_app_id TEXT NOT NULL DEFAULT '';
UPDATE preview_groups
SET github_app_id = (SELECT id FROM github_apps ORDER BY created_at LIMIT 1)
WHERE (SELECT COUNT(*) FROM github_apps) = 1;
CREATE INDEX preview_groups_github_app_id ON preview_groups(github_app_id);

-- dispatch:migration 021_event_trigger_github_app
ALTER TABLE event_triggers ADD COLUMN github_app_id TEXT NOT NULL DEFAULT '';
UPDATE event_triggers
SET github_app_id = COALESCE((SELECT source_credential_id FROM apps WHERE apps.id = event_triggers.app_id AND source_auth_type = 'github_app'), '');
CREATE INDEX event_triggers_github_app_id ON event_triggers(github_app_id);

-- dispatch:migration 022_github_app_installation_url
ALTER TABLE github_apps ADD COLUMN installation_url TEXT NOT NULL DEFAULT '';

-- dispatch:migration 023_relay_servers
ALTER TABLE servers ADD COLUMN relay_access_token TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN relay_pending_events INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN relay_oldest_pending_at TEXT;
ALTER TABLE servers ADD COLUMN relay_last_connected_at TEXT;
ALTER TABLE servers ADD COLUMN relay_last_error TEXT NOT NULL DEFAULT '';

CREATE TABLE relay_webhooks (
    id TEXT PRIMARY KEY,
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    provider TEXT NOT NULL,
    provider_connection_id TEXT NOT NULL DEFAULT '',
    remote_id TEXT NOT NULL,
    url TEXT NOT NULL,
    state TEXT NOT NULL,
    last_delivery_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(server_id, remote_id)
);

ALTER TABLE github_apps ADD COLUMN relay_webhook_id TEXT NOT NULL DEFAULT '';

-- dispatch:migration 024_external_secrets
CREATE TABLE secret_stores (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    provider TEXT NOT NULL,
    config TEXT NOT NULL DEFAULT '{}',
    encrypted_credentials TEXT NOT NULL,
    state TEXT NOT NULL,
    last_verified_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

ALTER TABLE secrets ADD COLUMN secret_source TEXT NOT NULL DEFAULT 'local';
ALTER TABLE secrets ADD COLUMN external_store_id TEXT REFERENCES secret_stores(id) ON DELETE RESTRICT;
ALTER TABLE secrets ADD COLUMN external_secret_id TEXT NOT NULL DEFAULT '';
ALTER TABLE secrets ADD COLUMN external_field TEXT NOT NULL DEFAULT '';
CREATE INDEX secrets_external_store_id ON secrets(external_store_id);

-- dispatch:migration 025_private_networks
CREATE TABLE private_networks (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    driver TEXT NOT NULL,
    config TEXT NOT NULL DEFAULT '{}',
    details TEXT NOT NULL DEFAULT '{}',
    state TEXT NOT NULL,
    last_verified_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- dispatch:migration 026_edge_nodes
ALTER TABLE private_networks ADD COLUMN token_hash TEXT NOT NULL DEFAULT '';

CREATE TABLE edge_jobs (
    id TEXT PRIMARY KEY,
    private_network_id TEXT NOT NULL REFERENCES private_networks(id) ON DELETE CASCADE,
    state TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0,
    lease_token TEXT NOT NULL DEFAULT '',
    lease_until TEXT,
    encrypted_request TEXT NOT NULL,
    encrypted_response TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX edge_jobs_ready ON edge_jobs(private_network_id, state, expires_at, created_at);

-- dispatch:migration 027_github_app_routes
ALTER TABLE github_apps ADD COLUMN private_network_id TEXT NOT NULL DEFAULT '';

-- dispatch:migration 028_workflows
CREATE TABLE config_sources (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    github_app_id TEXT NOT NULL REFERENCES github_apps(id),
    name TEXT NOT NULL UNIQUE,
    repository TEXT NOT NULL,
    branch TEXT NOT NULL,
    path TEXT NOT NULL,
    sync_mode TEXT NOT NULL,
    poll_interval_seconds INTEGER NOT NULL,
    active INTEGER NOT NULL,
    state TEXT NOT NULL,
    last_seen_sha TEXT NOT NULL DEFAULT '',
    last_synced_at TEXT,
    last_polled_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(github_app_id, repository, branch, path)
);

CREATE TABLE workflow_resources (
    id TEXT PRIMARY KEY,
    config_source_id TEXT NOT NULL REFERENCES config_sources(id) ON DELETE CASCADE,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    document TEXT NOT NULL,
    spec_digest TEXT NOT NULL,
    config_sha TEXT NOT NULL,
    active INTEGER NOT NULL,
    state TEXT NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(config_source_id, path, kind, name)
);

CREATE INDEX workflow_resources_source ON workflow_resources(config_source_id, active, kind, name);

CREATE TABLE workflow_events (
    id TEXT PRIMARY KEY,
    config_source_id TEXT NOT NULL REFERENCES config_sources(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    delivery_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    repository TEXT NOT NULL,
    branch TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    state TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    processed_at TEXT,
    UNIQUE(config_source_id, provider, delivery_id)
);

CREATE TABLE workflow_revisions (
    id TEXT PRIMARY KEY,
    resource_id TEXT NOT NULL REFERENCES workflow_resources(id) ON DELETE CASCADE,
    config_sha TEXT NOT NULL,
    spec_digest TEXT NOT NULL,
    state TEXT NOT NULL,
    trigger_name TEXT NOT NULL,
    sources TEXT NOT NULL,
    outputs TEXT NOT NULL,
    log TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT
);

CREATE INDEX workflow_revisions_resource ON workflow_revisions(resource_id, created_at DESC);

CREATE TABLE workflow_job_results (
    id TEXT PRIMARY KEY,
    resource_id TEXT NOT NULL REFERENCES workflow_resources(id) ON DELETE CASCADE,
    revision_id TEXT NOT NULL REFERENCES workflow_revisions(id) ON DELETE CASCADE,
    job_name TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    reused_from_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    sources TEXT NOT NULL,
    outputs TEXT NOT NULL,
    log TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT
);

CREATE INDEX workflow_job_results_reuse ON workflow_job_results(resource_id, job_name, fingerprint, created_at DESC);

CREATE TABLE workflow_stage_runs (
    id TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL REFERENCES workflow_revisions(id) ON DELETE CASCADE,
    stage_name TEXT NOT NULL,
    target_ref TEXT NOT NULL,
    state TEXT NOT NULL,
    approval TEXT NOT NULL,
    deployment_ids TEXT NOT NULL,
    check_runs TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    UNIQUE(revision_id, stage_name)
);

CREATE INDEX workflow_stage_runs_revision ON workflow_stage_runs(revision_id, created_at);

-- dispatch:migration 029_config_source_credentials
-- dispatch:foreign-keys-off
PRAGMA legacy_alter_table = ON;

ALTER TABLE config_sources RENAME TO config_sources_old;

CREATE TABLE config_sources (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    github_app_id TEXT REFERENCES github_apps(id),
    credential_secret_id TEXT REFERENCES secrets(id),
    name TEXT NOT NULL UNIQUE,
    repository TEXT NOT NULL,
    branch TEXT NOT NULL,
    path TEXT NOT NULL,
    sync_mode TEXT NOT NULL,
    poll_interval_seconds INTEGER NOT NULL,
    active INTEGER NOT NULL,
    state TEXT NOT NULL,
    last_seen_sha TEXT NOT NULL DEFAULT '',
    last_synced_at TEXT,
    last_polled_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((github_app_id IS NOT NULL) <> (credential_secret_id IS NOT NULL)),
    UNIQUE(github_app_id, repository, branch, path),
    UNIQUE(credential_secret_id, repository, branch, path)
);

INSERT INTO config_sources(
    id, project_id, github_app_id, credential_secret_id, name, repository, branch, path, sync_mode,
    poll_interval_seconds, active, state, last_seen_sha, last_synced_at, last_polled_at, last_error, created_at, updated_at
)
SELECT id, project_id, github_app_id, NULL, name, repository, branch, path, sync_mode,
       poll_interval_seconds, active, state, last_seen_sha, last_synced_at, last_polled_at, last_error, created_at, updated_at
FROM config_sources_old;

DROP TABLE config_sources_old;

PRAGMA legacy_alter_table = OFF;

-- dispatch:migration 030_deployment_snapshot
ALTER TABLE deployments ADD COLUMN spec_snapshot TEXT NOT NULL DEFAULT '{}';

-- dispatch:migration 031_access_control
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

-- dispatch:migration 032_auth_providers
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

-- dispatch:migration 033_pending_users
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

-- dispatch:migration 034_auth_provider_enrollment
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

-- dispatch:migration 035_laneway_network_credentials
ALTER TABLE private_networks ADD COLUMN encrypted_credentials TEXT NOT NULL DEFAULT '';

-- dispatch:migration 036_laneway_applications
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

-- dispatch:migration 037_deployment_history_index
-- Fetch recent history without sorting the saved deployment snapshots.
CREATE INDEX deployments_created_at ON deployments(created_at DESC);

-- dispatch:migration 038_analytics_outbox
-- Durable, compact completion events. No logs, credentials or configuration snapshots.
CREATE TABLE analytics_outbox (
 event_id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, entity_id TEXT NOT NULL,
 project_id TEXT NOT NULL, name TEXT NOT NULL, state TEXT NOT NULL,
 started_at TEXT NOT NULL, finished_at TEXT NOT NULL, reused INTEGER NOT NULL
);
CREATE TRIGGER analytics_deployment_insert AFTER INSERT ON deployments WHEN NEW.state IN ('succeeded','failed','cancelled') BEGIN INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused) SELECT 'deployment',NEW.id,a.project_id,a.name,NEW.state,COALESCE(NEW.started_at,NEW.created_at),COALESCE(NEW.finished_at,NEW.created_at),0 FROM apps a WHERE a.id=NEW.app_id; END;
CREATE TRIGGER analytics_deployment_update AFTER UPDATE ON deployments WHEN NEW.state IN ('succeeded','failed','cancelled') AND (OLD.state<>NEW.state OR COALESCE(OLD.finished_at,'')<>COALESCE(NEW.finished_at,'')) BEGIN INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused) SELECT 'deployment',NEW.id,a.project_id,a.name,NEW.state,COALESCE(NEW.started_at,NEW.created_at),COALESCE(NEW.finished_at,NEW.created_at),0 FROM apps a WHERE a.id=NEW.app_id; END;
INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused)
 SELECT 'deployment',t.id,a.project_id,a.name,t.state,COALESCE(t.started_at,t.created_at),COALESCE(t.finished_at,t.created_at),0
 FROM deployments t JOIN apps a ON a.id=t.app_id WHERE t.state IN ('succeeded','failed','cancelled');
CREATE TRIGGER analytics_workflow_insert AFTER INSERT ON workflow_revisions WHEN NEW.state IN ('succeeded','failed','cancelled') BEGIN INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused) SELECT 'workflow',NEW.id,c.project_id,r.name,NEW.state,COALESCE(NEW.started_at,NEW.created_at),COALESCE(NEW.finished_at,NEW.created_at),0 FROM workflow_resources r JOIN config_sources c ON c.id=r.config_source_id WHERE r.id=NEW.resource_id; END;
CREATE TRIGGER analytics_workflow_update AFTER UPDATE ON workflow_revisions WHEN NEW.state IN ('succeeded','failed','cancelled') AND (OLD.state<>NEW.state OR COALESCE(OLD.finished_at,'')<>COALESCE(NEW.finished_at,'')) BEGIN INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused) SELECT 'workflow',NEW.id,c.project_id,r.name,NEW.state,COALESCE(NEW.started_at,NEW.created_at),COALESCE(NEW.finished_at,NEW.created_at),0 FROM workflow_resources r JOIN config_sources c ON c.id=r.config_source_id WHERE r.id=NEW.resource_id; END;
INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused)
 SELECT 'workflow',t.id,c.project_id,r.name,t.state,COALESCE(t.started_at,t.created_at),COALESCE(t.finished_at,t.created_at),0
 FROM workflow_revisions t JOIN workflow_resources r ON r.id=t.resource_id JOIN config_sources c ON c.id=r.config_source_id WHERE t.state IN ('succeeded','failed','cancelled');
CREATE TRIGGER analytics_job_insert AFTER INSERT ON workflow_job_results WHEN NEW.state IN ('succeeded','failed','cancelled') BEGIN INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused) SELECT 'job',NEW.id,c.project_id,r.name || ' / ' || NEW.job_name,NEW.state,COALESCE(NEW.started_at,NEW.created_at),COALESCE(NEW.finished_at,NEW.created_at),CASE WHEN COALESCE(NEW.reused_from_id,'')<>'' THEN 1 ELSE 0 END FROM workflow_resources r JOIN config_sources c ON c.id=r.config_source_id WHERE r.id=NEW.resource_id; END;
CREATE TRIGGER analytics_job_update AFTER UPDATE ON workflow_job_results WHEN NEW.state IN ('succeeded','failed','cancelled') AND (OLD.state<>NEW.state OR COALESCE(OLD.finished_at,'')<>COALESCE(NEW.finished_at,'')) BEGIN INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused) SELECT 'job',NEW.id,c.project_id,r.name || ' / ' || NEW.job_name,NEW.state,COALESCE(NEW.started_at,NEW.created_at),COALESCE(NEW.finished_at,NEW.created_at),CASE WHEN COALESCE(NEW.reused_from_id,'')<>'' THEN 1 ELSE 0 END FROM workflow_resources r JOIN config_sources c ON c.id=r.config_source_id WHERE r.id=NEW.resource_id; END;
INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused)
 SELECT 'job',t.id,c.project_id,r.name || ' / ' || t.job_name,t.state,COALESCE(t.started_at,t.created_at),COALESCE(t.finished_at,t.created_at),CASE WHEN COALESCE(t.reused_from_id,'')<>'' THEN 1 ELSE 0 END
 FROM workflow_job_results t JOIN workflow_resources r ON r.id=t.resource_id JOIN config_sources c ON c.id=r.config_source_id WHERE t.state IN ('succeeded','failed','cancelled');

-- dispatch:migration 039_services
CREATE TABLE services (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 name TEXT NOT NULL,
 revision BIGINT NOT NULL,
 payload TEXT NOT NULL,
 UNIQUE(project_id,name)
);
CREATE TABLE app_service_bindings (
 app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
 alias TEXT NOT NULL,
 service_id TEXT NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
 payload TEXT NOT NULL,
 PRIMARY KEY(app_id,alias)
);
CREATE TABLE deployment_service_bindings (
 deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
 alias TEXT NOT NULL,
 service_id TEXT NOT NULL,
 payload TEXT NOT NULL,
 PRIMARY KEY(deployment_id,alias)
);
CREATE INDEX deployment_services_by_service ON deployment_service_bindings(service_id);

CREATE TABLE workflow_service_references (
 resource_id TEXT NOT NULL REFERENCES workflow_resources(id) ON DELETE CASCADE,
 service_id TEXT NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
 PRIMARY KEY(resource_id,service_id)
);

-- dispatch:migration 040_application_drift
CREATE TABLE deployment_drift_baselines (
 deployment_id TEXT PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
 app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
 server_id TEXT NOT NULL,
 namespace TEXT NOT NULL,
 release_name TEXT NOT NULL,
 ciphertext TEXT NOT NULL
);
CREATE TABLE application_drift_checks (
 app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
 payload TEXT NOT NULL
);
CREATE TABLE application_drift_actions (
 id TEXT PRIMARY KEY,
 app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
 payload TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE INDEX application_drift_actions_app ON application_drift_actions(app_id, created_at DESC);

-- dispatch:migration 042_release_tools
CREATE TABLE deployment_release_notes (
    deployment_id TEXT PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
    payload TEXT NOT NULL
);
CREATE TABLE deployment_release_actions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    app_id TEXT NOT NULL DEFAULT '',
    deployment_id TEXT NOT NULL DEFAULT '',
    source_deployment_id TEXT NOT NULL DEFAULT '',
    payload TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX release_actions_app_created ON deployment_release_actions(app_id, created_at);
CREATE INDEX release_actions_project_created ON deployment_release_actions(project_id, created_at);

-- dispatch:migration 043_observations
CREATE TABLE application_observation_configs (
 app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 revision BIGINT NOT NULL,
 payload TEXT NOT NULL,
 webhook_ciphertext TEXT NOT NULL DEFAULT ''
);
CREATE TABLE application_observations (
 app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
 payload TEXT NOT NULL
);
CREATE TABLE application_observation_events (
 id TEXT PRIMARY KEY,
 app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 created_at TEXT NOT NULL,
 delivery TEXT NOT NULL,
 next_attempt_at TEXT NOT NULL DEFAULT '',
 payload TEXT NOT NULL
);
CREATE INDEX application_observation_events_app ON application_observation_events(app_id, created_at DESC);
CREATE INDEX application_observation_events_delivery ON application_observation_events(delivery, next_attempt_at);

-- dispatch:migration 044_operations
ALTER TABLE role_assignments ADD COLUMN expires_at TEXT;
CREATE TABLE audit_events (
 id TEXT PRIMARY KEY, actor_id TEXT NOT NULL, actor_name TEXT NOT NULL,
 impersonator_id TEXT NOT NULL DEFAULT '', project_id TEXT NOT NULL DEFAULT '', app_id TEXT NOT NULL DEFAULT '',
 action TEXT NOT NULL, resource_id TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE INDEX audit_events_project_time ON audit_events(project_id, id DESC);
CREATE INDEX audit_events_app_time ON audit_events(app_id, id DESC);
CREATE TABLE application_owners (
 app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
 principal_type TEXT NOT NULL, principal_id TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE identity_team_mappings (
 id TEXT PRIMARY KEY, provider_id TEXT NOT NULL REFERENCES auth_providers(id) ON DELETE CASCADE,
 external_group TEXT NOT NULL, team_id TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
 UNIQUE(provider_id, external_group, team_id)
);
CREATE TABLE identity_team_members (
 mapping_id TEXT NOT NULL REFERENCES identity_team_mappings(id) ON DELETE CASCADE,
 provider_id TEXT NOT NULL REFERENCES auth_providers(id) ON DELETE CASCADE,
 team_id TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 PRIMARY KEY(mapping_id, user_id)
);
CREATE TABLE retention_policies (project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE, payload TEXT NOT NULL);
CREATE TABLE controller_backups (id TEXT PRIMARY KEY, payload TEXT NOT NULL);

-- dispatch:migration 045_edge_credentials
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

-- dispatch:migration 046_controller_settings
CREATE TABLE controller_settings (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 operations_enabled INTEGER NOT NULL DEFAULT 0 CHECK (operations_enabled IN (0, 1))
);
INSERT INTO controller_settings (id, operations_enabled) VALUES (1, 0);

-- dispatch:migration 047_helm_equivalence
CREATE TABLE application_helm_equivalence (
 app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 app_name TEXT NOT NULL,
 app_spec_digest TEXT NOT NULL,
 candidate_spec_digest TEXT NOT NULL,
 chart_commit TEXT NOT NULL,
 server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 target_digest TEXT NOT NULL,
 deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
 checked_at TEXT NOT NULL
);
ALTER TABLE workflow_stage_runs ADD COLUMN deployment_results TEXT NOT NULL DEFAULT '[]';
CREATE TABLE workflow_helm_equivalence (
 resource_id TEXT PRIMARY KEY REFERENCES workflow_resources(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 spec_digest TEXT NOT NULL,
 baseline_revision_id TEXT NOT NULL REFERENCES workflow_revisions(id) ON DELETE CASCADE,
 sources TEXT NOT NULL,
 results TEXT NOT NULL,
 app_proofs TEXT NOT NULL,
 checked_at TEXT NOT NULL
);

-- dispatch:migration 048_preview_poll_cursors
CREATE TABLE preview_poll_cursors (
    github_app_id TEXT NOT NULL,
    repository TEXT NOT NULL,
    checked_at TEXT NOT NULL,
    PRIMARY KEY (github_app_id, repository)
);

-- dispatch:migration 049_workflow_preview_reports
CREATE TABLE workflow_preview_reports (
    revision_id TEXT NOT NULL REFERENCES workflow_revisions(id) ON DELETE CASCADE,
    repository TEXT NOT NULL,
    pull_request_number INTEGER NOT NULL,
    comment_id TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (revision_id, repository, pull_request_number)
);

-- dispatch:migration 050_temporary_workflow_resources
ALTER TABLE workflow_resources ADD COLUMN temporary BOOLEAN NOT NULL DEFAULT 0;

-- dispatch:migration 051_workflow_preview_triggers
CREATE TABLE workflow_preview_triggers (
    id TEXT PRIMARY KEY,
    resource_id TEXT NOT NULL REFERENCES workflow_resources(id) ON DELETE CASCADE,
    github_app_id TEXT NOT NULL REFERENCES github_apps(id),
    repository TEXT NOT NULL,
    pull_request_number INTEGER NOT NULL,
    command TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(resource_id, repository, pull_request_number, command)
);

CREATE TABLE workflow_preview_comments (
    trigger_id TEXT NOT NULL REFERENCES workflow_preview_triggers(id) ON DELETE CASCADE,
    comment_id TEXT NOT NULL,
    revision_id TEXT,
    PRIMARY KEY(trigger_id, comment_id)
);

-- dispatch:migration 052_helm_provenance_preview_close
ALTER TABLE apps ADD COLUMN helm_provenance TEXT NOT NULL DEFAULT '{}';
ALTER TABLE workflow_preview_triggers ADD COLUMN closed_at TEXT;

-- dispatch:migration 053_workflow_preview_auto_report
ALTER TABLE workflow_preview_triggers ADD COLUMN preview_url TEXT NOT NULL DEFAULT '';
ALTER TABLE workflow_preview_triggers ADD COLUMN report_comment_id TEXT NOT NULL DEFAULT '';

-- dispatch:migration 054_workflow_preview_links
ALTER TABLE workflow_preview_triggers ADD COLUMN linked_pull_requests TEXT NOT NULL DEFAULT '{}';

-- dispatch:migration 055_workflow_preview_templates
CREATE TABLE workflow_preview_templates (
    id TEXT PRIMARY KEY,
    config_source_id TEXT NOT NULL REFERENCES config_sources(id) ON DELETE CASCADE,
    github_app_id TEXT NOT NULL REFERENCES github_apps(id),
    name TEXT NOT NULL,
    repository TEXT NOT NULL,
    command TEXT NOT NULL,
    preview_url TEXT NOT NULL,
    document TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX workflow_preview_templates_active_command
    ON workflow_preview_templates(github_app_id, repository, command) WHERE active;

ALTER TABLE workflow_preview_triggers ADD COLUMN template_id TEXT REFERENCES workflow_preview_templates(id) ON DELETE SET NULL;

-- dispatch:migration 056_preview_template_git_source
ALTER TABLE workflow_preview_templates ADD COLUMN git_source TEXT NOT NULL DEFAULT 'null';
ALTER TABLE workflow_preview_triggers ADD COLUMN template_source TEXT NOT NULL DEFAULT 'null';
ALTER TABLE workflow_preview_templates ADD COLUMN watch_repositories TEXT NOT NULL DEFAULT '[]';
