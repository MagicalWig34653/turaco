package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func placementState(p *Placement) any {
	if p == nil {
		return nil
	}
	return map[string]any{"rackId": p.RackID, "assetId": p.AssetID, "uPosition": p.UPosition, "heightU": p.HeightU,
		"face": p.Face, "removalReason": p.RemovalReason, "version": p.Version}
}

func placementEvent(p Placement, operation string) map[string]any {
	return map[string]any{"placementId": p.ID, "rackId": p.RackID, "assetId": p.AssetID, "operation": operation,
		"uPosition": p.UPosition, "heightU": p.HeightU, "face": p.Face}
}

func checkPlacement(in PlacementInput, rackHeight int) error {
	if in.Face != FaceFront && in.Face != FaceRear {
		return invalid("face must be front or rear")
	}
	if in.HeightU < 1 || in.HeightU > MaxRackHeight {
		return invalid("height must be between 1 and %d units", MaxRackHeight)
	}
	if in.UPosition < 1 {
		return invalid("position must be at least 1")
	}
	if in.UPosition+in.HeightU-1 > rackHeight {
		return invalid("the placement does not fit into a rack of %d units", rackHeight)
	}
	return nil
}

// usableAsset checks through the Assets contract that the Asset exists and is
// not disposed, lost or retired. Placing an Asset never changes its status.
func (s *Service) usableAsset(ctx context.Context, id string) error {
	found, err := s.assets.Assets(ctx, []string{id})
	if err != nil {
		return fmt.Errorf("check asset: %w", err)
	}
	if a, ok := found[id]; !ok || !a.Usable() {
		return ErrAssetUnusable
	}
	return nil
}

// PlaceAsset puts an Asset into a Rack at the given unit and face. The Rack's
// row lock serializes concurrent placements; the occupancy table guarantees
// that no unit is taken twice and the partial unique index that an Asset has
// at most one active placement. Requires infrastructure.manage.
func (s *Service) PlaceAsset(ctx context.Context, c Caller, p Principal, in PlacementInput) (Placement, error) {
	if err := c.validate(); err != nil {
		return Placement{}, err
	}
	if err := p.require(true); err != nil {
		return Placement{}, err
	}
	if err := checkIDs(in.RackID, in.AssetID); err != nil {
		return Placement{}, err
	}
	in.RackID, in.AssetID = strings.ToLower(in.RackID), strings.ToLower(in.AssetID)
	if err := s.usableAsset(ctx, in.AssetID); err != nil {
		return Placement{}, err
	}
	var out Placement
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		rack, err := s.store.LockRackTx(ctx, tx, in.RackID)
		if err != nil {
			return err
		}
		if !rack.Active {
			return ErrArchived
		}
		if err := checkPlacement(in, rack.HeightU); err != nil {
			return err
		}
		out, err = s.store.InsertPlacementTx(ctx, tx, in, c.Actor.UserID, nil)
		if err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "infrastructure.placement.placed", "rack_placement", out.ID, nil, placementState(&out), nil); err != nil {
			return err
		}
		return publish(ctx, tx, c, "RackPlacementChanged", placementEvent(out, "placed"))
	})
	return out, err
}

// MoveAsset moves an active placement to another position, face or Rack. The
// old placement is closed with reason "moved" and kept as history; the new
// placement links to it. Requires infrastructure.manage.
func (s *Service) MoveAsset(ctx context.Context, c Caller, p Principal, placementID string, expected *int, to PlacementInput) (Placement, error) {
	if err := c.validate(); err != nil {
		return Placement{}, err
	}
	if err := p.require(true); err != nil {
		return Placement{}, err
	}
	if _, err := requireVersion(expected); err != nil {
		return Placement{}, err
	}
	if err := checkIDs(to.RackID); err != nil {
		return Placement{}, err
	}
	to.RackID = strings.ToLower(to.RackID)
	// The Assets lookup is a cross-module call and must not run while rack
	// rows are locked, so it happens first, on the unlocked placement. The
	// window between this check and the commit is accepted: an Asset disposed
	// in that window still moves, and a disposed-while-placed Asset keeps its
	// units until someone removes it (see PlacementWarnings).
	pre, err := s.store.GetPlacement(ctx, placementID)
	if err != nil {
		return Placement{}, err
	}
	if pre.RemovedAt != nil {
		return Placement{}, ErrPlacementClosed
	}
	if err := s.usableAsset(ctx, pre.AssetID); err != nil {
		return Placement{}, err
	}
	var out Placement
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockPlacementTx(ctx, tx, placementID)
		if err != nil {
			return err
		}
		if cur.RemovedAt != nil {
			return ErrPlacementClosed
		}
		if *expected != cur.Version {
			return ErrVersionConflict
		}
		// Lock both racks in id order so concurrent moves cannot deadlock.
		first, second := cur.RackID, to.RackID
		if second < first {
			first, second = second, first
		}
		racks := map[string]Rack{}
		for _, id := range []string{first, second} {
			if _, done := racks[id]; done {
				continue
			}
			r, err := s.store.LockRackTx(ctx, tx, id)
			if err != nil {
				return err
			}
			racks[id] = r
		}
		if !racks[to.RackID].Active {
			return ErrArchived
		}
		to.AssetID = cur.AssetID
		if err := checkPlacement(to, racks[to.RackID].HeightU); err != nil {
			return err
		}
		if to.RackID == cur.RackID && to.UPosition == cur.UPosition && to.HeightU == cur.HeightU && to.Face == cur.Face {
			out = cur
			return nil
		}
		closed, err := s.store.ClosePlacementTx(ctx, tx, cur.ID, ReasonMoved, c.Actor.UserID)
		if err != nil {
			return err
		}
		out, err = s.store.InsertPlacementTx(ctx, tx, to, c.Actor.UserID, &cur.ID)
		if err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "infrastructure.placement.moved", "rack_placement", out.ID, placementState(&closed), placementState(&out),
			map[string]any{"previousPlacementId": cur.ID}); err != nil {
			return err
		}
		return publish(ctx, tx, c, "RackPlacementChanged", placementEvent(out, "moved"))
	})
	return out, err
}

// RemoveAsset takes an Asset out of its Rack with a reason code. The
// placement stays as history. Repeating a successful removal with the same
// reason returns the current record without a second audit entry. Requires infrastructure.manage.
func (s *Service) RemoveAsset(ctx context.Context, c Caller, p Principal, placementID string, expected *int, reason string) (Placement, error) {
	if err := c.validate(); err != nil {
		return Placement{}, err
	}
	if err := p.require(true); err != nil {
		return Placement{}, err
	}
	if _, err := requireVersion(expected); err != nil {
		return Placement{}, err
	}
	if !oneOf(reason, RemovalReasons) {
		return Placement{}, invalid("reason must be one of %s", strings.Join(RemovalReasons, ", "))
	}
	var out Placement
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockPlacementTx(ctx, tx, placementID)
		if err != nil {
			return err
		}
		if cur.RemovedAt != nil {
			// A retry of a remove that already succeeded (same reason, version
			// from before the removal) returns the current record.
			if cur.RemovalReason != nil && *cur.RemovalReason == reason && (*expected == cur.Version-1 || *expected == cur.Version) {
				out = cur
				return nil
			}
			return ErrPlacementClosed
		}
		if *expected != cur.Version {
			return ErrVersionConflict
		}
		if _, err := s.store.LockRackTx(ctx, tx, cur.RackID); err != nil {
			return err
		}
		out, err = s.store.ClosePlacementTx(ctx, tx, cur.ID, reason, c.Actor.UserID)
		if err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "infrastructure.placement.removed", "rack_placement", out.ID, placementState(&cur), placementState(&out),
			map[string]any{"reason": reason}); err != nil {
			return err
		}
		ev := placementEvent(out, "removed")
		ev["reason"] = reason
		return publish(ctx, tx, c, "RackPlacementChanged", ev)
	})
	return out, err
}

// GetPlacement returns one placement, active or removed. Requires infrastructure.view.
func (s *Service) GetPlacement(ctx context.Context, p Principal, id string) (Placement, error) {
	if err := p.require(false); err != nil {
		return Placement{}, err
	}
	return s.store.GetPlacement(ctx, id)
}

// ListPlacements lists the placements of a Rack, newest first is not needed:
// ascending by id (oldest first). Removed placements are history and only
// included on request. Requires infrastructure.view.
func (s *Service) ListPlacements(ctx context.Context, p Principal, rackID string, includeRemoved bool, page Page) (Result[Placement], error) {
	if err := p.require(false); err != nil {
		return Result[Placement]{}, err
	}
	if _, err := s.store.GetRack(ctx, rackID); err != nil {
		return Result[Placement]{}, err
	}
	return s.store.ListPlacements(ctx, rackID, includeRemoved, page.Normalize())
}

// PlacementWarnings lists active placements whose Asset is disposed, lost or
// retired (or missing). Placing never changes an Asset's status and Assets
// cannot know about racks, so a disposed Asset keeps its units until someone
// removes the placement; this list is how it gets noticed. It checks at most
// MaxWarningScan active placements per call, in id order, through the Assets
// contract in batches, and returns at most MaxWarnings. Requires
// infrastructure.view and assets.view.
func (s *Service) PlacementWarnings(ctx context.Context, p Principal) (WarningsResult, error) {
	if err := p.require(false); err != nil {
		return WarningsResult{}, err
	}
	if !p.AssetsView {
		return WarningsResult{}, ErrForbidden
	}
	res := WarningsResult{Items: []PlacementWarning{}}
	after, scanned := "", 0
	for scanned < MaxWarningScan {
		batch, err := s.store.ActivePlacementsAfter(ctx, after, warningBatch)
		if err != nil {
			return WarningsResult{}, err
		}
		if len(batch) == 0 {
			return res, nil
		}
		ids := make([]string, 0, len(batch))
		for _, b := range batch {
			ids = append(ids, b.Placement.AssetID)
		}
		found, err := s.assets.Assets(ctx, ids)
		if err != nil {
			return WarningsResult{}, fmt.Errorf("check assets: %w", err)
		}
		for _, b := range batch {
			a, ok := found[b.Placement.AssetID]
			if ok && a.Usable() {
				continue
			}
			if len(res.Items) >= MaxWarnings {
				res.Truncated = true
				return res, nil
			}
			w := PlacementWarning{Placement: b.Placement, RackName: b.RackName, AssetStatus: "missing"}
			if ok {
				w.AssetRef, w.AssetStatus = a.Reference, a.Status
			}
			res.Items = append(res.Items, w)
		}
		scanned += len(batch)
		after = batch[len(batch)-1].Placement.ID
		if len(batch) < warningBatch {
			return res, nil
		}
	}
	// The scan limit was reached; more active placements may exist.
	res.Truncated = true
	return res, nil
}
