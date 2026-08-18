ALTER TABLE github_apps ADD COLUMN registration_owner TEXT NOT NULL DEFAULT '';
ALTER TABLE github_apps ADD COLUMN registration_owner_type TEXT NOT NULL DEFAULT '';
