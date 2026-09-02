ALTER TABLE auth_providers
    DROP CONSTRAINT IF EXISTS auth_providers_provisioning_check;

UPDATE auth_providers
SET provisioning = 'approval'
WHERE provisioning = 'domain';

ALTER TABLE auth_providers
    ADD CONSTRAINT auth_providers_provisioning_check
    CHECK(provisioning IN ('existing', 'approval'));

ALTER TABLE auth_providers
    DROP COLUMN domains,
    DROP COLUMN routing_mode;
