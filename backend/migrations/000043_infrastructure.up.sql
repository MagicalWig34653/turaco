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
-- Target of the composite foreign key that keeps a placement inside its rack.
-- The rack height is never updated, and the foreign key forbids it anyway while
-- placements exist.
ALTER TABLE infrastructure.racks ADD CONSTRAINT racks_id_height_unique UNIQUE (id, height_u);

-- A Rack Placement records where an Asset sits. Rows are kept as history:
-- removing or moving sets removed_at, so removed_at IS NULL means active.
CREATE TABLE IF NOT EXISTS infrastructure.rack_placements (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    rack_id uuid NOT NULL REFERENCES infrastructure.racks(id),
    asset_id uuid NOT NULL,
    u_position integer NOT NULL CHECK (u_position BETWEEN 1 AND 60),
    height_u integer NOT NULL CHECK (height_u BETWEEN 1 AND 60),
    face text NOT NULL CHECK (face IN ('front', 'rear')),
    -- Copy of the rack height: the composite foreign key below ties it to the
    -- rack, so the database itself enforces that a placement fits the rack.
    rack_height_u integer NOT NULL,
    CHECK (u_position + height_u - 1 <= rack_height_u),
    FOREIGN KEY (rack_id, rack_height_u) REFERENCES infrastructure.racks (id, height_u),
    -- Target of the occupancy foreign key.
    CONSTRAINT rack_placements_id_rack_face_unique UNIQUE (id, rack_id, face),
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
-- Rack elevation, active listings, the archive check and the placement
-- warnings read active placements; the history listing reads all of a rack.
CREATE INDEX IF NOT EXISTS rack_placements_rack_active ON infrastructure.rack_placements (rack_id, id) WHERE removed_at IS NULL;
CREATE INDEX IF NOT EXISTS rack_placements_rack_history ON infrastructure.rack_placements (rack_id, id);

-- One row per occupied unit of an active placement. btree_gist is not
-- installed, so the primary key (not application code alone) guarantees that
-- no unit of a rack face is occupied twice. Rows are deleted when the
-- placement is closed, in the same transaction.
CREATE TABLE IF NOT EXISTS infrastructure.rack_unit_occupancy (
    rack_id uuid NOT NULL REFERENCES infrastructure.racks(id),
    face text NOT NULL CHECK (face IN ('front', 'rear')),
    u integer NOT NULL CHECK (u BETWEEN 1 AND 60),
    placement_id uuid NOT NULL,
    PRIMARY KEY (rack_id, face, u),
    -- An occupied unit belongs to a placement of the same rack and face.
    FOREIGN KEY (placement_id, rack_id, face) REFERENCES infrastructure.rack_placements (id, rack_id, face)
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

-- Backstops for the parent-state rules the application enforces under row
-- locks: no active placement on an archived rack, no active rack in an
-- archived room, no active room in an archived building. FOR SHARE makes a
-- concurrent archive of the parent wait for the inserting transaction.
CREATE OR REPLACE FUNCTION infrastructure.check_active_parent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    parent_active boolean;
BEGIN
    IF TG_TABLE_NAME = 'rack_placements' THEN
        IF NEW.removed_at IS NOT NULL THEN RETURN NEW; END IF;
        SELECT active INTO parent_active FROM infrastructure.racks WHERE id = NEW.rack_id FOR SHARE;
    ELSIF TG_TABLE_NAME = 'racks' THEN
        IF NOT NEW.active THEN RETURN NEW; END IF;
        SELECT active INTO parent_active FROM infrastructure.rooms WHERE id = NEW.room_id FOR SHARE;
    ELSE
        IF NOT NEW.active THEN RETURN NEW; END IF;
        SELECT active INTO parent_active FROM infrastructure.buildings WHERE id = NEW.building_id FOR SHARE;
    END IF;
    IF parent_active IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'infrastructure: parent of % is archived', TG_TABLE_NAME USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION infrastructure.check_no_active_children() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    has_children boolean;
BEGIN
    IF NEW.active OR NOT OLD.active THEN RETURN NEW; END IF;
    IF TG_TABLE_NAME = 'racks' THEN
        SELECT EXISTS (SELECT 1 FROM infrastructure.rack_placements WHERE rack_id = NEW.id AND removed_at IS NULL) INTO has_children;
    ELSIF TG_TABLE_NAME = 'rooms' THEN
        SELECT EXISTS (SELECT 1 FROM infrastructure.racks WHERE room_id = NEW.id AND active) INTO has_children;
    ELSE
        SELECT EXISTS (SELECT 1 FROM infrastructure.rooms WHERE building_id = NEW.id AND active) INTO has_children;
    END IF;
    IF has_children THEN
        RAISE EXCEPTION 'infrastructure: % still has active children', TG_TABLE_NAME USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER rack_placements_active_parent BEFORE INSERT ON infrastructure.rack_placements
    FOR EACH ROW EXECUTE FUNCTION infrastructure.check_active_parent();
CREATE TRIGGER racks_active_parent BEFORE INSERT OR UPDATE OF active ON infrastructure.racks
    FOR EACH ROW EXECUTE FUNCTION infrastructure.check_active_parent();
CREATE TRIGGER rooms_active_parent BEFORE INSERT OR UPDATE OF active ON infrastructure.rooms
    FOR EACH ROW EXECUTE FUNCTION infrastructure.check_active_parent();
CREATE TRIGGER racks_no_active_children BEFORE UPDATE OF active ON infrastructure.racks
    FOR EACH ROW EXECUTE FUNCTION infrastructure.check_no_active_children();
CREATE TRIGGER rooms_no_active_children BEFORE UPDATE OF active ON infrastructure.rooms
    FOR EACH ROW EXECUTE FUNCTION infrastructure.check_no_active_children();
CREATE TRIGGER buildings_no_active_children BEFORE UPDATE OF active ON infrastructure.buildings
    FOR EACH ROW EXECUTE FUNCTION infrastructure.check_no_active_children();
