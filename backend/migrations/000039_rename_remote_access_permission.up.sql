-- Rename the reserved permission remote_support.start to remote_access.start_attended
-- (ADR-0026: "Remote Access" is the canonical, provider-agnostic term).
-- Stored grants are plain text and are intersected with the code registry, so an
-- unmigrated grant would silently disappear. Existing grants carry over to the
-- attended start only; no other remote_access.* permission is granted.
-- The built-in platform-administrator role has no role_permissions rows.
INSERT INTO platform.role_permissions (role_id, permission)
SELECT role_id, 'remote_access.start_attended'
FROM platform.role_permissions
WHERE permission = 'remote_support.start'
ON CONFLICT (role_id, permission) DO NOTHING;

DELETE FROM platform.role_permissions WHERE permission = 'remote_support.start';
