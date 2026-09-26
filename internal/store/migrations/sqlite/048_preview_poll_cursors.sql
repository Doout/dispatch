CREATE TABLE preview_poll_cursors (
    github_app_id TEXT NOT NULL,
    repository TEXT NOT NULL,
    checked_at TEXT NOT NULL,
    PRIMARY KEY (github_app_id, repository)
);
