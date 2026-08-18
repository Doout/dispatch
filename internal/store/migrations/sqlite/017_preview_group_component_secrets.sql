CREATE TABLE preview_group_component_secrets (
    component_id TEXT NOT NULL REFERENCES preview_group_components(id) ON DELETE CASCADE,
    secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
    PRIMARY KEY(component_id, secret_id)
);
