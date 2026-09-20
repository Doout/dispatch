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
