-- Module switches (ADR-0032, docs/product/module-switches-design.md). Owned by platform/modules.
-- One row per optional module whose switch was ever set. A module without a row has the default of the code catalog
-- (enabled, except Workforce Presence and Turaco AI) and the implicit version 0, so adding a module needs no migration.
-- Disabling a module never deletes its data: this table is the only thing a switch changes.
CREATE TABLE IF NOT EXISTS platform.module_switches (
    module_key text PRIMARY KEY CHECK (module_key ~ '^[a-z][a-z0-9_]{1,39}$'),
    enabled boolean NOT NULL,
    version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
    reason_code text NOT NULL CHECK (reason_code IN ('upgrade_default', 'initial_setup', 'business_need', 'not_needed', 'maintenance', 'compliance_review', 'evaluation')),
    updated_by uuid,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Upgrade: an installation that already runs Workforce Presence or Turaco AI keeps them on; everything else stays
-- on through the catalog default, so the upgrade changes nothing observable.
INSERT INTO platform.module_switches (module_key, enabled, reason_code)
SELECT 'presence', enabled, 'upgrade_default' FROM presence.settings WHERE singleton
ON CONFLICT (module_key) DO NOTHING;
INSERT INTO platform.module_switches (module_key, enabled, reason_code)
SELECT 'ai', enabled, 'upgrade_default' FROM ai.settings WHERE singleton
ON CONFLICT (module_key) DO NOTHING;
