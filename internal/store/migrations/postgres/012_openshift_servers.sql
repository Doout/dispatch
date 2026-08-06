ALTER TABLE servers ADD COLUMN openshift_service_account TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN openshift_service_account_namespace TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN openshift_token_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN openshift_connected_at TEXT;
