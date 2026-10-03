-- Infrastructure topology (F7a): Buildings, Rooms, Racks, Rack Placements and
-- Virtual Machines (docs/product/f7-infrastructure-change-design.md). Sites are
-- Organization Locations and physical devices are Assets: both are referenced
-- by id without a foreign key because other modules own those records.
CREATE SCHEMA IF NOT EXISTS infrastructure;

CREATE TABLE IF NOT EXISTS infrastructure.buildings (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    -- Organization Location acting as the Site.
    site_location_id uuid NOT NULL,
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 100),
    address_note text CHECK (address_note IS NULL OR (address_note = btrim(address_note) AND length(address_note) BETWEEN 1 AND 200)),
    active boolean NOT NULL DEFAULT true,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS buildings_name_unique ON infrastructure.buildings (site_location_id, lower(name));

CREATE TABLE IF NOT EXISTS infrastructure.rooms (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    building_id uuid NOT NULL REFERENCES infrastructure.buildings(id),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 100),
    floor text CHECK (floor IS NULL OR (floor = btrim(floor) AND length(floor) BETWEEN 1 AND 20)),
    active boolean NOT NULL DEFAULT true,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS rooms_name_unique ON infrastructure.rooms (building_id, lower(name));

CREATE TABLE IF NOT EXISTS infrastructure.racks (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    room_id uuid NOT NULL REFERENCES infrastructure.rooms(id),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 100),
    height_u integer NOT NULL CHECK (height_u BETWEEN 1 AND 60),
    active boolean NOT NULL DEFAULT true,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS racks_name_unique ON infrastructure.racks (room_id, lower(name));

-- A Rack Placement records where an Asset sits. Rows are kept as history:
-- removing or moving sets removed_at, so removed_at IS NULL means active.
CREATE TABLE IF NOT EXISTS infrastructure.rack_placements (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    rack_id uuid NOT NULL REFERENCES infrastructure.racks(id),
    asset_id uuid NOT NULL,
    u_position integer NOT NULL CHECK (u_position BETWEEN 1 AND 60),
    height_u integer NOT NULL CHECK (height_u BETWEEN 1 AND 60),
    face text NOT NULL CHECK (face IN ('front', 'rear')),
    CHECK (u_position + height_u - 1 <= 60),
    placed_by uuid,
    placed_at timestamptz NOT NULL DEFAULT now(),
    removed_at timestamptz,
    removed_by uuid,
    removal_reason text CHECK (removal_reason IS NULL OR removal_reason IN ('moved', 'relocated', 'replaced', 'decommissioned', 'error_correction', 'other')),
    previous_placement_id uuid REFERENCES infrastructure.rack_placements(id),
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    CHECK ((removed_at IS NULL) = (removal_reason IS NULL))
);
-- An Asset has at most one active placement.
CREATE UNIQUE INDEX IF NOT EXISTS rack_placements_asset_active ON infrastructure.rack_placements (asset_id) WHERE removed_at IS NULL;
CREATE INDEX IF NOT EXISTS rack_placements_rack ON infrastructure.rack_placements (rack_id, id);
CREATE INDEX IF NOT EXISTS rack_placements_asset ON infrastructure.rack_placements (asset_id, id);

-- One row per occupied unit of an active placement. btree_gist is not
-- installed, so the primary key (not application code alone) guarantees that
-- no unit of a rack face is occupied twice. Rows are deleted when the
-- placement is closed, in the same transaction.
CREATE TABLE IF NOT EXISTS infrastructure.rack_unit_occupancy (
    rack_id uuid NOT NULL REFERENCES infrastructure.racks(id),
    face text NOT NULL CHECK (face IN ('front', 'rear')),
    u integer NOT NULL CHECK (u BETWEEN 1 AND 60),
    placement_id uuid NOT NULL REFERENCES infrastructure.rack_placements(id),
    PRIMARY KEY (rack_id, face, u)
);
CREATE INDEX IF NOT EXISTS rack_unit_occupancy_placement ON infrastructure.rack_unit_occupancy (placement_id);

CREATE TABLE IF NOT EXISTS infrastructure.virtual_machines (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 100),
    state text NOT NULL CHECK (state IN ('running', 'stopped', 'unknown', 'decommissioned')),
    -- Hypervisor host: an Asset, by id.
    hypervisor_asset_id uuid,
    vcpu integer NOT NULL CHECK (vcpu BETWEEN 1 AND 1024),
    memory_mb integer NOT NULL CHECK (memory_mb BETWEEN 1 AND 16777216),
    management_address text CHECK (management_address IS NULL OR length(management_address) BETWEEN 1 AND 253),
    network_note text CHECK (network_note IS NULL OR length(network_note) BETWEEN 1 AND 500),
    notes text CHECK (notes IS NULL OR length(notes) BETWEEN 1 AND 2000),
    decommission_reason text CHECK (decommission_reason IS NULL OR decommission_reason IN ('retired', 'migrated', 'deleted', 'other')),
    decommissioned_at timestamptz,
    CHECK ((state = 'decommissioned') = (decommission_reason IS NOT NULL)),
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- Names are unique among live VMs; a decommissioned VM keeps its tombstone.
CREATE UNIQUE INDEX IF NOT EXISTS virtual_machines_name_live ON infrastructure.virtual_machines (lower(name)) WHERE state <> 'decommissioned';
CREATE INDEX IF NOT EXISTS virtual_machines_hypervisor ON infrastructure.virtual_machines (hypervisor_asset_id, id) WHERE hypervisor_asset_id IS NOT NULL;
