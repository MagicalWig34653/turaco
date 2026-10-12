ALTER TABLE platform.sessions
    ADD COLUMN entra_amr text[] NOT NULL DEFAULT '{}',
    ADD COLUMN entra_acrs text[] NOT NULL DEFAULT '{}',
    ADD COLUMN entra_auth_time timestamptz,
    ADD COLUMN assurance_at timestamptz;

ALTER TABLE platform.sessions
    ADD COLUMN entra_tenant_id text,
    ADD COLUMN entra_object_id text;

ALTER TABLE platform.oidc_login_transactions
    ADD COLUMN step_up_session_id uuid,
    ADD COLUMN step_up_token_hash bytea;
