CREATE TABLE workflow_preview_reports (
    revision_id TEXT NOT NULL REFERENCES workflow_revisions(id) ON DELETE CASCADE,
    repository TEXT NOT NULL,
    pull_request_number INTEGER NOT NULL,
    comment_id TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (revision_id, repository, pull_request_number)
);
