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
