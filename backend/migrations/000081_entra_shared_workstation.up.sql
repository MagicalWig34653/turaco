-- Shared-workstation marker of an Entra sign-in (ADR-0035, administration setting auth.entra_signout_mode):
-- the sign-in page offers "this is a shared computer"; logging out of such a session also ends the Entra session.
ALTER TABLE platform.oidc_login_transactions ADD COLUMN IF NOT EXISTS shared boolean NOT NULL DEFAULT false;
ALTER TABLE platform.sessions ADD COLUMN IF NOT EXISTS shared_workstation boolean NOT NULL DEFAULT false;
