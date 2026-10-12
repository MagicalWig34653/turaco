-- Credential and account-status changes end MCP consent in the same transaction.
ALTER TABLE ai.mcp_grants DROP CONSTRAINT mcp_grants_revoked_reason_check;
ALTER TABLE ai.mcp_grants ADD CONSTRAINT mcp_grants_revoked_reason_check
    CHECK (revoked_reason IS NULL OR revoked_reason IN
        ('user', 'client_revocation', 'refresh_replay', 'code_replay', 'client_disabled',
         'credential_changed', 'user_deactivated', 'administrator'));
