ALTER TABLE config_sources
    ALTER COLUMN github_app_id DROP NOT NULL,
    ADD COLUMN credential_secret_id TEXT REFERENCES secrets(id),
    ADD CONSTRAINT config_sources_auth_check CHECK ((github_app_id IS NOT NULL) <> (credential_secret_id IS NOT NULL)),
    ADD CONSTRAINT config_sources_credential_repository_key UNIQUE (credential_secret_id, repository, branch, path);
