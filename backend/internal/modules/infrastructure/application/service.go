package application

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Infrastructure operations. Audit actions:
// infrastructure.building|room|rack.created|renamed|updated|archived|unarchived,
// infrastructure.placement.placed|moved|removed and
// infrastructure.vm.created|updated|state_changed|hypervisor_changed|decommissioned.
// Audit metadata holds ids, states and reason codes only: names, addresses and
// notes are never copied into the audit log.
type Service struct {
	store  Store
	dir    Directory
	assets Assets
}

func NewService(store Store, dir Directory, assets Assets) *Service {
	return &Service{store: store, dir: dir, assets: assets}
}

func publish(ctx context.Context, tx pgx.Tx, c Caller, typ string, payload map[string]any) error {
	var actor *string
	if c.Actor.UserID != "" {
		a := c.Actor.UserID
		actor = &a
	}
	return events.Publish(ctx, tx, events.Publication{Type: typ, ActorID: actor, CorrelationID: c.CorrelationID, Payload: payload})
}

func recordAudit(ctx context.Context, tx pgx.Tx, c Caller, action, targetType, targetID string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: targetType, TargetID: targetID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
}

// requireVersion refuses a state-changing request without the version the
// caller saw: every such operation is optimistic-lock protected.
func requireVersion(expected *int) (int, error) {
	if expected == nil {
		return 0, invalid("expectedVersion is required")
	}
	return *expected, nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// checkIDs refuses ids that are not UUIDs before they reach SQL casts.
func checkIDs(ids ...string) error {
	for _, id := range ids {
		if !uuidPattern.MatchString(id) {
			return invalid("ids must be UUIDs")
		}
	}
	return nil
}

func cleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxName || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return "", invalid("name must be 1-%d characters without control or invisible formatting characters", maxName)
	}
	return s, nil
}

// cleanText validates an optional bounded text; empty means "none".
func cleanText(field, s string, max int, multiline bool) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(s) > max || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, multiline) {
		return nil, invalid("%s must be at most %d characters without control or invisible formatting characters", field, max)
	}
	return &s, nil
}

func oneOf(s string, set []string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

func (p Principal) require(manage bool) error {
	if manage && !p.Manage || !manage && !p.canView() {
		return ErrForbidden
	}
	return nil
}

// ---- buildings ----

func buildingState(b *Building) any {
	if b == nil {
		return nil
	}
	return map[string]any{"siteLocationId": b.SiteLocationID, "active": b.Active, "version": b.Version}
}

// CreateBuilding creates a Building at a Site (an active Organization
// Location). Requires infrastructure.manage.
func (s *Service) CreateBuilding(ctx context.Context, c Caller, p Principal, siteLocationID, name, addressNote string) (Building, error) {
	if err := c.validate(); err != nil {
		return Building{}, err
	}
	if err := p.require(true); err != nil {
		return Building{}, err
	}
	if err := checkIDs(siteLocationID); err != nil {
		return Building{}, err
	}
	name, err := cleanName(name)
	if err != nil {
		return Building{}, err
	}
	note, err := cleanText("address note", addressNote, maxAddress, false)
	if err != nil {
		return Building{}, err
	}
	ok, err := s.dir.ActiveLocations(ctx, []string{siteLocationID})
	if err != nil {
		return Building{}, fmt.Errorf("check site location: %w", err)
	}
	if !ok[siteLocationID] {
		return Building{}, ErrReferenceInvalid
	}
	var out Building
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		b, err := s.store.InsertBuildingTx(ctx, tx, siteLocationID, name, note)
		if err != nil {
			return err
		}
		out = b
		return recordAudit(ctx, tx, c, "infrastructure.building.created", "building", b.ID, nil, buildingState(&b), nil)
	})
	return out, err
}

// BuildingUpdate renames a Building or changes its address note; nil fields
// stay unchanged, an empty address note clears it.
type BuildingUpdate struct {
	Name        *string
	AddressNote *string
}

// UpdateBuilding renames a Building or changes its address note. Requires infrastructure.manage.
func (s *Service) UpdateBuilding(ctx context.Context, c Caller, p Principal, id string, expected int, in BuildingUpdate) (Building, error) {
	if err := c.validate(); err != nil {
		return Building{}, err
	}
	if err := p.require(true); err != nil {
		return Building{}, err
	}
	var name string
	if in.Name != nil {
		n, err := cleanName(*in.Name)
		if err != nil {
			return Building{}, err
		}
		name = n
	}
	var note *string
	if in.AddressNote != nil {
		n, err := cleanText("address note", *in.AddressNote, maxAddress, false)
		if err != nil {
			return Building{}, err
		}
		note = n
	}
	var out Building
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockBuildingTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return ErrVersionConflict
		}
		next := cur
		var changed []string
		if in.Name != nil && name != cur.Name {
			next.Name = name
			changed = append(changed, "name")
		}
		if in.AddressNote != nil && !samePtr(note, cur.AddressNote) {
			next.AddressNote = note
			changed = append(changed, "addressNote")
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		out, err = s.store.UpdateBuildingTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "infrastructure.building.updated", "building", id, buildingState(&cur), buildingState(&out), map[string]any{"changedFields": changed})
	})
	return out, err
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// SetBuildingArchived archives a Building (no active Rooms may remain) or
// unarchives it. Requires infrastructure.manage.
func (s *Service) SetBuildingArchived(ctx context.Context, c Caller, p Principal, id string, expected *int, archived bool) (Building, error) {
	if err := c.validate(); err != nil {
		return Building{}, err
	}
	if err := p.require(true); err != nil {
		return Building{}, err
	}
	if _, err := requireVersion(expected); err != nil {
		return Building{}, err
	}
	action := "infrastructure.building.unarchived"
	if archived {
		action = "infrastructure.building.archived"
	}
	var out Building
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockBuildingTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Active == !archived {
			out = cur
			return nil
		}
		if archived {
			n, err := s.store.CountActiveRoomsTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if n > 0 {
				return ErrNotEmpty
			}
		}
		next := cur
		next.Active = !archived
		out, err = s.store.UpdateBuildingTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, action, "building", id, buildingState(&cur), buildingState(&out), nil)
	})
	return out, err
}

// ListBuildings lists Buildings, optionally of one Site. Requires infrastructure.view.
func (s *Service) ListBuildings(ctx context.Context, p Principal, siteLocationID string, includeArchived bool, page Page) (Result[Building], error) {
	if err := p.require(false); err != nil {
		return Result[Building]{}, err
	}
	return s.store.ListBuildings(ctx, siteLocationID, includeArchived, page.Normalize())
}

// GetBuilding returns one Building. Requires infrastructure.view.
func (s *Service) GetBuilding(ctx context.Context, p Principal, id string) (Building, error) {
	if err := p.require(false); err != nil {
		return Building{}, err
	}
	return s.store.GetBuilding(ctx, id)
}

// ---- rooms ----

func roomState(r *Room) any {
	if r == nil {
		return nil
	}
	return map[string]any{"buildingId": r.BuildingID, "active": r.Active, "version": r.Version}
}

// CreateRoom creates a Room in an active Building. Requires infrastructure.manage.
func (s *Service) CreateRoom(ctx context.Context, c Caller, p Principal, buildingID, name, floor string) (Room, error) {
	if err := c.validate(); err != nil {
		return Room{}, err
	}
	if err := p.require(true); err != nil {
		return Room{}, err
	}
	name, err := cleanName(name)
	if err != nil {
		return Room{}, err
	}
	fl, err := cleanText("floor", floor, maxFloor, false)
	if err != nil {
		return Room{}, err
	}
	var out Room
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		b, err := s.store.LockBuildingTx(ctx, tx, buildingID)
		if err != nil {
			return err
		}
		if !b.Active {
			return ErrArchived
		}
		r, err := s.store.InsertRoomTx(ctx, tx, buildingID, name, fl)
		if err != nil {
			return err
		}
		out = r
		return recordAudit(ctx, tx, c, "infrastructure.room.created", "room", r.ID, nil, roomState(&r), nil)
	})
	return out, err
}

// RoomUpdate renames a Room or changes its floor; an empty floor clears it.
type RoomUpdate struct {
	Name  *string
	Floor *string
}

// UpdateRoom renames a Room or changes its floor. Requires infrastructure.manage.
func (s *Service) UpdateRoom(ctx context.Context, c Caller, p Principal, id string, expected int, in RoomUpdate) (Room, error) {
	if err := c.validate(); err != nil {
		return Room{}, err
	}
	if err := p.require(true); err != nil {
		return Room{}, err
	}
	var name string
	if in.Name != nil {
		n, err := cleanName(*in.Name)
		if err != nil {
			return Room{}, err
		}
		name = n
	}
	var floor *string
	if in.Floor != nil {
		f, err := cleanText("floor", *in.Floor, maxFloor, false)
		if err != nil {
			return Room{}, err
		}
		floor = f
	}
	var out Room
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockRoomTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return ErrVersionConflict
		}
		next := cur
		var changed []string
		if in.Name != nil && name != cur.Name {
			next.Name = name
			changed = append(changed, "name")
		}
		if in.Floor != nil && !samePtr(floor, cur.Floor) {
			next.Floor = floor
			changed = append(changed, "floor")
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		out, err = s.store.UpdateRoomTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "infrastructure.room.updated", "room", id, roomState(&cur), roomState(&out), map[string]any{"changedFields": changed})
	})
	return out, err
}

// SetRoomArchived archives a Room (no active Racks may remain) or unarchives
// it (its Building must be active). Requires infrastructure.manage.
func (s *Service) SetRoomArchived(ctx context.Context, c Caller, p Principal, id string, expected *int, archived bool) (Room, error) {
	if err := c.validate(); err != nil {
		return Room{}, err
	}
	if err := p.require(true); err != nil {
		return Room{}, err
	}
	if _, err := requireVersion(expected); err != nil {
		return Room{}, err
	}
	action := "infrastructure.room.unarchived"
	if archived {
		action = "infrastructure.room.archived"
	}
	var out Room
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockRoomTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Active == !archived {
			out = cur
			return nil
		}
		if archived {
			n, err := s.store.CountActiveRacksTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if n > 0 {
				return ErrNotEmpty
			}
		} else {
			b, err := s.store.LockBuildingTx(ctx, tx, cur.BuildingID)
			if err != nil {
				return err
			}
			if !b.Active {
				return ErrArchived
			}
		}
		next := cur
		next.Active = !archived
		out, err = s.store.UpdateRoomTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, action, "room", id, roomState(&cur), roomState(&out), nil)
	})
	return out, err
}

// ListRooms lists the Rooms of a Building. Requires infrastructure.view.
func (s *Service) ListRooms(ctx context.Context, p Principal, buildingID string, includeArchived bool, page Page) (Result[Room], error) {
	if err := p.require(false); err != nil {
		return Result[Room]{}, err
	}
	return s.store.ListRooms(ctx, buildingID, includeArchived, page.Normalize())
}

// GetRoom returns one Room. Requires infrastructure.view.
func (s *Service) GetRoom(ctx context.Context, p Principal, id string) (Room, error) {
	if err := p.require(false); err != nil {
		return Room{}, err
	}
	return s.store.GetRoom(ctx, id)
}

// ---- racks ----

func rackState(r *Rack) any {
	if r == nil {
		return nil
	}
	return map[string]any{"roomId": r.RoomID, "heightU": r.HeightU, "active": r.Active, "version": r.Version}
}

// CreateRack creates a Rack of 1-60 U in an active Room. Requires infrastructure.manage.
func (s *Service) CreateRack(ctx context.Context, c Caller, p Principal, roomID, name string, heightU int) (Rack, error) {
	if err := c.validate(); err != nil {
		return Rack{}, err
	}
	if err := p.require(true); err != nil {
		return Rack{}, err
	}
	name, err := cleanName(name)
	if err != nil {
		return Rack{}, err
	}
	if heightU < 1 || heightU > MaxRackHeight {
		return Rack{}, invalid("height must be between 1 and %d units", MaxRackHeight)
	}
	var out Rack
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		room, err := s.store.LockRoomTx(ctx, tx, roomID)
		if err != nil {
			return err
		}
		if !room.Active {
			return ErrArchived
		}
		r, err := s.store.InsertRackTx(ctx, tx, roomID, name, heightU)
		if err != nil {
			return err
		}
		out = r
		return recordAudit(ctx, tx, c, "infrastructure.rack.created", "rack", r.ID, nil, rackState(&r), nil)
	})
	return out, err
}

// RenameRack renames a Rack. Requires infrastructure.manage.
func (s *Service) RenameRack(ctx context.Context, c Caller, p Principal, id string, expected int, name string) (Rack, error) {
	if err := c.validate(); err != nil {
		return Rack{}, err
	}
	if err := p.require(true); err != nil {
		return Rack{}, err
	}
	name, err := cleanName(name)
	if err != nil {
		return Rack{}, err
	}
	var out Rack
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockRackTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return ErrVersionConflict
		}
		if cur.Name == name {
			out = cur
			return nil
		}
		next := cur
		next.Name = name
		out, err = s.store.UpdateRackTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "infrastructure.rack.renamed", "rack", id, rackState(&cur), rackState(&out), nil)
	})
	return out, err
}

// SetRackArchived archives a Rack (no active placements may remain) or
// unarchives it (its Room must be active). Requires infrastructure.manage.
func (s *Service) SetRackArchived(ctx context.Context, c Caller, p Principal, id string, expected *int, archived bool) (Rack, error) {
	if err := c.validate(); err != nil {
		return Rack{}, err
	}
	if err := p.require(true); err != nil {
		return Rack{}, err
	}
	if _, err := requireVersion(expected); err != nil {
		return Rack{}, err
	}
	action := "infrastructure.rack.unarchived"
	if archived {
		action = "infrastructure.rack.archived"
	}
	var out Rack
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockRackTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Active == !archived {
			out = cur
			return nil
		}
		if archived {
			n, err := s.store.CountActivePlacementsTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if n > 0 {
				return ErrNotEmpty
			}
		} else {
			room, err := s.store.LockRoomTx(ctx, tx, cur.RoomID)
			if err != nil {
				return err
			}
			if !room.Active {
				return ErrArchived
			}
		}
		next := cur
		next.Active = !archived
		out, err = s.store.UpdateRackTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, action, "rack", id, rackState(&cur), rackState(&out), nil)
	})
	return out, err
}

// ListRacks lists the Racks of a Room. Requires infrastructure.view.
func (s *Service) ListRacks(ctx context.Context, p Principal, roomID string, includeArchived bool, page Page) (Result[Rack], error) {
	if err := p.require(false); err != nil {
		return Result[Rack]{}, err
	}
	return s.store.ListRacks(ctx, roomID, includeArchived, page.Normalize())
}

// GetRack returns a Rack with its active placements (the rack elevation data).
// Requires infrastructure.view.
func (s *Service) GetRack(ctx context.Context, p Principal, id string) (RackDetail, error) {
	if err := p.require(false); err != nil {
		return RackDetail{}, err
	}
	r, err := s.store.GetRack(ctx, id)
	if err != nil {
		return RackDetail{}, err
	}
	ps, err := s.store.ActivePlacements(ctx, id)
	if err != nil {
		return RackDetail{}, err
	}
	return RackDetail{Rack: r, Placements: ps}, nil
}

// ---- reads across the tree ----

// Tree summarizes Buildings, Rooms, Racks and placed Assets per Site, bounded
// to MaxTreeSites Sites (Truncated is set when more exist). Requires infrastructure.view.
func (s *Service) Tree(ctx context.Context, p Principal, includeArchived bool) (TreeResult, error) {
	if err := p.require(false); err != nil {
		return TreeResult{}, err
	}
	tree, err := s.store.Tree(ctx, includeArchived)
	if err != nil {
		return TreeResult{}, err
	}
	ids := make([]string, 0, len(tree.Sites))
	for _, st := range tree.Sites {
		ids = append(ids, st.LocationID)
	}
	names, err := s.dir.LocationNames(ctx, ids)
	if err != nil {
		return TreeResult{}, fmt.Errorf("load site names: %w", err)
	}
	for i := range tree.Sites {
		tree.Sites[i].Name = names[tree.Sites[i].LocationID]
	}
	return tree, nil
}

// AssetReferences resolves Asset references for ids a caller already received.
// It performs no permission check.
func (s *Service) AssetReferences(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	found, err := s.assets.Assets(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load asset references: %w", err)
	}
	for id, a := range found {
		out[id] = a.Reference
	}
	return out, nil
}

// WhereIs tells where an Asset physically is for the Asset detail. Requires
// infrastructure.view and assets.view. ErrNotFound means the Asset does not
// exist; a nil result means it has no active placement.
func (s *Service) WhereIs(ctx context.Context, p Principal, assetID string) (*AssetLocation, error) {
	if err := p.require(false); err != nil {
		return nil, err
	}
	if !p.AssetsView {
		return nil, ErrForbidden
	}
	if err := checkIDs(assetID); err != nil {
		return nil, ErrNotFound
	}
	found, err := s.assets.Assets(ctx, []string{assetID})
	if err != nil {
		return nil, fmt.Errorf("check asset: %w", err)
	}
	if _, ok := found[assetID]; !ok {
		return nil, ErrNotFound
	}
	loc, err := s.PlacementOf(ctx, assetID)
	if err != nil {
		return nil, err
	}
	return loc, nil
}

// PlacementOf returns the active placement location of an Asset, or nil. It
// performs no permission check (the public contract for other modules).
func (s *Service) PlacementOf(ctx context.Context, assetID string) (*AssetLocation, error) {
	loc, err := s.store.WhereIs(ctx, assetID)
	if err == ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &loc, nil
}

// SiteNames resolves Site (Organization Location) names for ids a caller
// already received. It performs no permission check.
func (s *Service) SiteNames(ctx context.Context, ids []string) (map[string]string, error) {
	return s.dir.LocationNames(ctx, ids)
}
