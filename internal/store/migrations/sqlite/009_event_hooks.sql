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
