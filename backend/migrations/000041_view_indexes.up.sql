-- F6 slice 3 review: partial "current" indexes for the read paths of the management views.

-- Group nesting walks towards the parents (child -> parent) and group memberships by User use current rows only.
CREATE INDEX IF NOT EXISTS directory_group_nesting_current_child_idx
    ON organization.directory_group_nesting (child_group_id, parent_group_id) WHERE observed_until IS NULL;
CREATE INDEX IF NOT EXISTS directory_group_memberships_current_user_idx
    ON organization.directory_group_memberships (user_id, group_id) WHERE observed_until IS NULL;

-- Artifacts reached by all_devices / all_users targets (keyset by artifact id).
CREATE INDEX IF NOT EXISTS management_assignments_current_kind_idx
    ON endpoints.management_assignments (target_kind, artifact_id) WHERE valid_until IS NULL;

-- "Has this Device ever had a synced group membership?" (current or closed): memberships are known only then.
CREATE INDEX IF NOT EXISTS device_group_memberships_device_idx
    ON endpoints.device_group_memberships (device_id, provider);
