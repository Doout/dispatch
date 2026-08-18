ALTER TABLE preview_groups ADD COLUMN github_app_id TEXT NOT NULL DEFAULT '';
UPDATE preview_groups
SET github_app_id = (SELECT id FROM github_apps ORDER BY created_at LIMIT 1)
WHERE (SELECT COUNT(*) FROM github_apps) = 1;
CREATE INDEX preview_groups_github_app_id ON preview_groups(github_app_id);
