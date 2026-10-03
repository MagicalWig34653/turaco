package application

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Asset operations. Audit actions: assets.asset.created,
// .updated, .provisioning_changed and one per lifecycle operation
// (.make_available, .assign, .return, ...). Notes and free text are never
// copied into audit; ids, statuses and (for lifecycle operations) the given
// reason are.
type Service struct {
	store    Store
	dir      Directory
	products Products
	now      func() time.Time
}

func NewService(store Store, dir Directory, products Products, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, dir: dir, products: products, now: now}
}

func publish(ctx context.Context, tx pgx.Tx, c Caller, typ string, payload map[string]any) error {
	var actor *string
	if c.Actor.UserID != "" {
		a := c.Actor.UserID
		actor = &a
	}
	return events.Publish(ctx, tx, events.Publication{Type: typ, ActorID: actor, CorrelationID: c.CorrelationID, Payload: payload})
}

func auditState(a *Asset) any {
	if a == nil {
		return nil
	}
	return map[string]any{
		"status": a.Status, "provisioning": a.ProvisioningStatus, "ownership": a.OwnershipType,
		"productId": a.ProductID, "locationId": a.LocationID, "version": a.Version,
		"serialNumber": a.SerialNumber, "assetTag": a.AssetTag,
	}
}

func (s *Service) record(ctx context.Context, tx pgx.Tx, c Caller, action string, before, after *Asset, meta map[string]any) error {
	id := ""
	if after != nil {
		id = after.ID
	} else {
		id = before.ID
	}
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: "asset", TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: auditState(before), After: auditState(after), Metadata: meta,
	})
}

func cleanCode(s string, limit int, what string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > limit || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return "", invalid("%s must be 1-%d characters without control or invisible formatting characters", what, limit)
	}
	return s, nil
}

func cleanNote(s string, limit int, what string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > limit || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, true) {
		return "", invalid("%s must be at most %d characters without control or invisible formatting characters", what, limit)
	}
	return s, nil
}

func cleanReason(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxNote || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return "", invalid("a reason of 1-%d characters without control or invisible formatting characters is required", maxNote)
	}
	return s, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---- create ----

// CreateInput registers an existing asset (manual registration).
type CreateInput struct {
	ProductID     string
	SerialNumber  string
	AssetTag      string
	OwnershipType string
	// Status is "available" (default) or "received" (still to be checked).
	Status        string
	SupplierID    *string
	PurchasedAt   *time.Time
	WarrantyUntil *time.Time
	LocationID    *string
	Notes         string
}

// Create registers an asset. Requires assets.manage.
func (s *Service) Create(ctx context.Context, c Caller, p Principal, in CreateInput) (Asset, error) {
	if err := c.validate(); err != nil {
		return Asset{}, err
	}
	if !p.Manage {
		return Asset{}, ErrForbidden
	}
	if in.Status == "" {
		in.Status = StatusAvailable
	}
	if in.Status != StatusAvailable && in.Status != StatusReceived {
		return Asset{}, invalid("status must be available or received")
	}
	var out Asset
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		a, err := s.createInTx(ctx, tx, c, in, nil, nil)
		out = a
		return err
	})
	return out, err
}

// CreateInTx registers an asset in the caller's transaction (the contract
// Inventory uses for goods receipts). source says where it came from.
func (s *Service) CreateInTx(ctx context.Context, tx pgx.Tx, c Caller, in CreateInput, sourceType, sourceID *string) (Asset, error) {
	if err := c.validate(); err != nil {
		return Asset{}, err
	}
	return s.createInTx(ctx, tx, c, in, sourceType, sourceID)
}

func (s *Service) createInTx(ctx context.Context, tx pgx.Tx, c Caller, in CreateInput, sourceType, sourceID *string) (Asset, error) {
	if in.OwnershipType == "" {
		in.OwnershipType = "owned"
	}
	if !slices.Contains(OwnershipTypes, in.OwnershipType) {
		return Asset{}, invalid("ownership type must be one of %s", strings.Join(OwnershipTypes, ", "))
	}
	if in.Status != StatusAvailable && in.Status != StatusReceived {
		return Asset{}, invalid("status must be available or received")
	}
	prod, err := s.products.Products(ctx, []string{in.ProductID})
	if err != nil {
		return Asset{}, fmt.Errorf("check product: %w", err)
	}
	pr, ok := prod[in.ProductID]
	if !ok || !pr.Active || !pr.AssetManaged {
		return Asset{}, ErrProductInvalid
	}
	n := NewAsset{
		ProductID: in.ProductID, Status: in.Status, OwnershipType: in.OwnershipType, SupplierID: in.SupplierID,
		SourceType: sourceType, SourceID: sourceID, PurchasedAt: in.PurchasedAt, WarrantyUntil: in.WarrantyUntil,
	}
	if serial := strings.TrimSpace(in.SerialNumber); serial != "" {
		v, err := cleanCode(serial, maxSerial, "serial number")
		if err != nil {
			return Asset{}, err
		}
		n.SerialNumber = &v
	} else if pr.Serialized {
		return Asset{}, invalid("a serial number is required for a serialized product")
	}
	if tag := strings.TrimSpace(in.AssetTag); tag != "" {
		v, err := cleanCode(tag, maxTag, "asset tag")
		if err != nil {
			return Asset{}, err
		}
		n.AssetTag = &v
	}
	if notes, err := cleanNote(in.Notes, maxText, "notes"); err != nil {
		return Asset{}, err
	} else if notes != "" {
		n.Notes = &notes
	}
	if in.LocationID != nil {
		ok, err := s.dir.ActiveLocations(ctx, []string{*in.LocationID})
		if err != nil {
			return Asset{}, fmt.Errorf("check location: %w", err)
		}
		if !ok[*in.LocationID] {
			return Asset{}, ErrReferenceInvalid
		}
		n.LocationID = in.LocationID
	}
	a, err := s.store.InsertTx(ctx, tx, n)
	if err != nil {
		return Asset{}, err
	}
	if err := s.record(ctx, tx, c, "assets.asset.created", nil, &a, nil); err != nil {
		return Asset{}, err
	}
	if err := publish(ctx, tx, c, "AssetCreated", map[string]any{"assetId": a.ID, "productId": a.ProductID}); err != nil {
		return Asset{}, err
	}
	return a, nil
}

// ---- update ----

// UpdateInput changes descriptive fields; nil leaves a field unchanged and an
// empty string clears an optional text field.
type UpdateInput struct {
	SerialNumber  *string
	AssetTag      *string
	OwnershipType *string
	PurchasedAt   *time.Time
	WarrantyUntil *time.Time
	// ClearPurchasedAt and ClearWarrantyUntil remove the dates.
	ClearPurchasedAt   bool
	ClearWarrantyUntil bool
	LocationID         *string
	ClearLocation      bool
	Notes              *string
}

// Update changes an asset's descriptive fields. Requires assets.manage; the
// asset must not be disposed and expected must match the current version.
func (s *Service) Update(ctx context.Context, c Caller, p Principal, id string, expected int, in UpdateInput) (Asset, error) {
	if err := c.validate(); err != nil {
		return Asset{}, err
	}
	if !p.Manage {
		return Asset{}, ErrForbidden
	}
	if in.LocationID != nil {
		ok, err := s.dir.ActiveLocations(ctx, []string{*in.LocationID})
		if err != nil {
			return Asset{}, fmt.Errorf("check location: %w", err)
		}
		if !ok[*in.LocationID] {
			return Asset{}, ErrReferenceInvalid
		}
	}
	var out Asset
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return ErrVersionConflict
		}
		if cur.Status == StatusDisposed {
			return &InvalidTransitionError{Operation: "update", From: cur.Status}
		}
		next := cur
		var changed []string
		if in.SerialNumber != nil {
			next.SerialNumber = nil
			if v := strings.TrimSpace(*in.SerialNumber); v != "" {
				cleaned, err := cleanCode(v, maxSerial, "serial number")
				if err != nil {
					return err
				}
				next.SerialNumber = &cleaned
			} else {
				pr, perr := s.products.Products(ctx, []string{cur.ProductID})
				if perr != nil {
					return fmt.Errorf("check product: %w", perr)
				}
				if pr[cur.ProductID].Serialized {
					return invalid("a serial number is required for a serialized product")
				}
			}
			changed = append(changed, "serialNumber")
		}
		if in.AssetTag != nil {
			next.AssetTag = nil
			if v := strings.TrimSpace(*in.AssetTag); v != "" {
				cleaned, err := cleanCode(v, maxTag, "asset tag")
				if err != nil {
					return err
				}
				next.AssetTag = &cleaned
			}
			changed = append(changed, "assetTag")
		}
		if in.OwnershipType != nil {
			if !slices.Contains(OwnershipTypes, *in.OwnershipType) {
				return invalid("ownership type must be one of %s", strings.Join(OwnershipTypes, ", "))
			}
			next.OwnershipType = *in.OwnershipType
			changed = append(changed, "ownershipType")
		}
		switch {
		case in.ClearPurchasedAt:
			next.PurchasedAt = nil
			changed = append(changed, "purchasedAt")
		case in.PurchasedAt != nil:
			next.PurchasedAt = in.PurchasedAt
			changed = append(changed, "purchasedAt")
		}
		switch {
		case in.ClearWarrantyUntil:
			next.WarrantyUntil = nil
			changed = append(changed, "warrantyUntil")
		case in.WarrantyUntil != nil:
			next.WarrantyUntil = in.WarrantyUntil
			changed = append(changed, "warrantyUntil")
		}
		switch {
		case in.ClearLocation:
			next.LocationID = nil
			changed = append(changed, "location")
		case in.LocationID != nil:
			next.LocationID = in.LocationID
			changed = append(changed, "location")
		}
		if in.Notes != nil {
			cleaned, err := cleanNote(*in.Notes, maxText, "notes")
			if err != nil {
				return err
			}
			next.Notes = strPtr(cleaned)
			changed = append(changed, "notes")
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		out, err = s.store.UpdateTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, c, "assets.asset.updated", &cur, &out, map[string]any{"changedFields": changed})
	})
	return out, err
}

// SetProvisioning changes the provisioning status (separate from the
// lifecycle). Requires assets.manage; not for disposed assets.
func (s *Service) SetProvisioning(ctx context.Context, c Caller, p Principal, id string, expected *int, status string) (Asset, error) {
	if err := c.validate(); err != nil {
		return Asset{}, err
	}
	if !p.Manage {
		return Asset{}, ErrForbidden
	}
	if !slices.Contains(ProvisioningStatuses, status) {
		return Asset{}, invalid("provisioning status must be one of %s", strings.Join(ProvisioningStatuses, ", "))
	}
	var out Asset
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Status == StatusDisposed {
			return &InvalidTransitionError{Operation: "set_provisioning", From: cur.Status}
		}
		if cur.ProvisioningStatus == status {
			out = cur
			return nil
		}
		next := cur
		next.ProvisioningStatus = status
		out, err = s.store.UpdateTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, c, "assets.asset.provisioning_changed", &cur, &out, map[string]any{"from": cur.ProvisioningStatus, "to": status})
	})
	return out, err
}

// ---- lifecycle ----

// Assignee names who receives an asset.
type Assignee struct {
	Type string
	ID   string
}

// Params are the inputs of a lifecycle operation.
type Params struct {
	Reason   string
	Note     string
	Assignee Assignee
}

// Transition performs a lifecycle operation from the API. Requires
// assets.manage; operations reserved for Inventory are refused.
func (s *Service) Transition(ctx context.Context, c Caller, p Principal, id string, expected *int, op string, params Params) (Asset, error) {
	if err := c.validate(); err != nil {
		return Asset{}, err
	}
	if !p.Manage {
		return Asset{}, ErrForbidden
	}
	r, ok := rules[op]
	if !ok || r.internal {
		return Asset{}, invalid("unknown operation %q", op)
	}
	var out Asset
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		a, err := s.TransitionInTx(ctx, tx, c, id, expected, op, params)
		out = a
		return err
	})
	return out, err
}

// TransitionInTx performs a lifecycle operation in the caller's transaction,
// including the operations reserved for Inventory. It performs no permission
// check: the caller has authorized the action.
func (s *Service) TransitionInTx(ctx context.Context, tx pgx.Tx, c Caller, id string, expected *int, op string, params Params) (Asset, error) {
	r, ok := rules[op]
	if !ok {
		return Asset{}, invalid("unknown operation %q", op)
	}
	var reason string
	if r.reasonRequired {
		cleaned, err := cleanReason(params.Reason)
		if err != nil {
			return Asset{}, err
		}
		reason = cleaned
	}
	note := ""
	if params.Note != "" {
		cleaned, err := cleanNote(params.Note, maxNote, "note")
		if err != nil {
			return Asset{}, err
		}
		note = cleaned
	}
	if r.assignee {
		if err := s.checkAssignee(ctx, params.Assignee); err != nil {
			return Asset{}, err
		}
	}
	cur, err := s.store.LockTx(ctx, tx, id)
	if err != nil {
		return Asset{}, err
	}
	if expected != nil && *expected != cur.Version {
		return Asset{}, ErrVersionConflict
	}
	if !slices.Contains(r.from, cur.Status) {
		return Asset{}, &InvalidTransitionError{Operation: op, From: cur.Status}
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	var previous *Assignment
	if r.closesAssignment {
		closed, err := s.store.ActiveAssignmentTx(ctx, tx, cur.ID)
		if err != nil {
			return Asset{}, err
		}
		previous = closed
		if _, err := s.store.CloseAssignmentTx(ctx, tx, cur.ID, now); err != nil {
			return Asset{}, err
		}
	}
	next := cur
	next.Status = r.to
	next.StatusReason = nil
	if keepsReason(r.to) {
		next.StatusReason = strPtr(reason)
	}
	out, err := s.store.UpdateTx(ctx, tx, next)
	if err != nil {
		return Asset{}, err
	}
	meta := map[string]any{"operation": op}
	if reason != "" {
		meta["reason"] = reason
	}
	if r.assignee {
		var by *string
		if c.Actor.UserID != "" {
			by = &c.Actor.UserID
		}
		if _, err := s.store.OpenAssignmentTx(ctx, tx, Assignment{
			AssetID: cur.ID, AssigneeType: params.Assignee.Type, AssigneeID: params.Assignee.ID, AssignedAt: now, AssignedBy: by, Note: strPtr(note),
		}); err != nil {
			return Asset{}, err
		}
		meta["assigneeType"], meta["assigneeId"] = params.Assignee.Type, params.Assignee.ID
	}
	if previous != nil {
		meta["previousAssigneeType"], meta["previousAssigneeId"] = previous.AssigneeType, previous.AssigneeID
	}
	if err := s.record(ctx, tx, c, "assets.asset."+op, &cur, &out, meta); err != nil {
		return Asset{}, err
	}
	payload := map[string]any{"assetId": out.ID, "status": out.Status, "operation": op}
	switch r.event {
	case "AssetAssigned":
		payload["assigneeType"], payload["assigneeId"] = params.Assignee.Type, params.Assignee.ID
	case "AssetReturned":
		if previous != nil {
			payload["assigneeType"], payload["assigneeId"] = previous.AssigneeType, previous.AssigneeID
		}
	}
	if err := publish(ctx, tx, c, r.event, payload); err != nil {
		return Asset{}, err
	}
	return out, nil
}

func (s *Service) checkAssignee(ctx context.Context, a Assignee) error {
	var active map[string]bool
	var err error
	switch a.Type {
	case AssigneeUser:
		active, err = s.dir.ActiveUsers(ctx, []string{a.ID})
	case AssigneeTeam:
		active, err = s.dir.ActiveTeams(ctx, []string{a.ID})
	case AssigneeLocation:
		active, err = s.dir.ActiveLocations(ctx, []string{a.ID})
	default:
		return invalid("assignee type must be user, team or location")
	}
	if err != nil {
		return fmt.Errorf("check assignee: %w", err)
	}
	if !active[a.ID] {
		return ErrAssigneeInvalid
	}
	return nil
}

// ---- reads ----

// Detail is an asset with its assignment history and the operations its
// status allows.
type Detail struct {
	Asset       Asset
	Assignments []Assignment
	Allowed     []string
	Names       map[string]string
}

func (p Principal) canView() bool { return p.View || p.Manage }

// Get returns an asset the caller may see: any asset with assets.view, the
// assets currently assigned to the caller otherwise (404 for all others). The
// assignment history needs assets.view; a holder sees only the current
// assignment.
func (s *Service) Get(ctx context.Context, p Principal, id string) (Detail, error) {
	a, err := s.store.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	assignments, err := s.store.Assignments(ctx, a.ID)
	if err != nil {
		return Detail{}, err
	}
	if !p.canView() {
		mine := false
		for _, as := range assignments {
			if as.ReturnedAt == nil && as.AssigneeType == AssigneeUser && as.AssigneeID == p.UserID {
				mine = true
			}
		}
		if !mine {
			return Detail{}, ErrNotFound
		}
		active := assignments[:0:0]
		for _, as := range assignments {
			if as.ReturnedAt == nil {
				active = append(active, as)
			}
		}
		assignments = active
	}
	if !p.canView() {
		a = holderView(a)
	}
	d := Detail{Asset: a, Assignments: assignments}
	if p.Manage {
		d.Allowed = AllowedOperations(a.Status)
	}
	d.Names, err = s.names(ctx, a, assignments)
	if err != nil {
		return Detail{}, err
	}
	pn, err := s.ProductNames(ctx, []Asset{a})
	if err != nil {
		return Detail{}, err
	}
	for id, name := range pn {
		d.Names[id] = name
	}
	return d, nil
}

func (s *Service) names(ctx context.Context, a Asset, as []Assignment) (map[string]string, error) {
	var users, teams, locations []string
	for _, x := range as {
		switch x.AssigneeType {
		case AssigneeUser:
			users = append(users, x.AssigneeID)
		case AssigneeTeam:
			teams = append(teams, x.AssigneeID)
		case AssigneeLocation:
			locations = append(locations, x.AssigneeID)
		}
		if x.AssignedBy != nil {
			users = append(users, *x.AssignedBy)
		}
	}
	if a.LocationID != nil {
		locations = append(locations, *a.LocationID)
	}
	out := map[string]string{}
	for _, q := range []struct {
		ids  []string
		load func(context.Context, []string) (map[string]string, error)
	}{{users, s.dir.UserNames}, {teams, s.dir.TeamNames}, {locations, s.dir.LocationNames}} {
		if len(q.ids) == 0 {
			continue
		}
		m, err := q.load(ctx, q.ids)
		if err != nil {
			return nil, fmt.Errorf("load names: %w", err)
		}
		for k, v := range m {
			out[k] = v
		}
	}
	return out, nil
}

// List returns assets. Requires assets.view.
func (s *Service) List(ctx context.Context, p Principal, f Filter) (Result, error) {
	if !p.canView() {
		return Result{}, ErrForbidden
	}
	if f.Status != "" && !slices.Contains(Statuses, f.Status) {
		return Result{}, invalid("unknown status")
	}
	f.AssignedToUser = ""
	f.Query = strings.TrimSpace(f.Query)
	f.Page = f.Page.Normalize()
	return s.store.List(ctx, f)
}

// holderView hides the internal fields (notes, status reason, supplier) from the person an asset is assigned to.
func holderView(a Asset) Asset {
	a.Notes, a.StatusReason, a.SupplierID = nil, nil, nil
	return a
}

// GetPlain returns an asset without any authorization (for contracts used by
// modules that authorized the caller themselves).
func (s *Service) GetPlain(ctx context.Context, id string) (Asset, error) {
	return s.store.Get(ctx, id)
}

// UserHolders returns assetID -> User id for the given assets that are currently assigned to a User.
// It authorizes nothing: callers (other modules through the public contract) decide who may see it.
func (s *Service) UserHolders(ctx context.Context, assetIDs []string) (map[string]string, error) {
	return s.store.UserHolders(ctx, assetIDs)
}

// AssetsHeldByUsers returns userID -> ids of the assets currently assigned to the User (at most limit in total).
func (s *Service) AssetsHeldByUsers(ctx context.Context, userIDs []string, limit int) (map[string][]string, error) {
	return s.store.AssetsHeldByUsers(ctx, userIDs, limit)
}

// ProductNames returns id -> name of the products of the given assets.
func (s *Service) ProductNames(ctx context.Context, assets []Asset) (map[string]string, error) {
	ids := make([]string, 0, len(assets))
	for _, a := range assets {
		ids = append(ids, a.ProductID)
	}
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	found, err := s.products.Products(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load product names: %w", err)
	}
	for id, p := range found {
		out[id] = p.Name
	}
	return out, nil
}

// DeviceSnapshot is the part of an asset a ticket remembers.
type DeviceSnapshot struct {
	AssetID      string
	Reference    string
	Product      string
	SerialNumber *string
	AssetTag     *string
	Status       string
}

// SnapshotFor returns a snapshot of an asset for another module. With a holder
// the asset must currently be assigned to that User (ErrNotFound otherwise, so
// nobody learns about foreign assets). It performs no permission check.
func (s *Service) SnapshotFor(ctx context.Context, assetID, holderUserID string) (DeviceSnapshot, error) {
	a, err := s.store.Get(ctx, assetID)
	if err != nil {
		return DeviceSnapshot{}, err
	}
	if holderUserID != "" {
		assignments, err := s.store.Assignments(ctx, a.ID)
		if err != nil {
			return DeviceSnapshot{}, err
		}
		held := false
		for _, as := range assignments {
			held = held || (as.ReturnedAt == nil && as.AssigneeType == AssigneeUser && as.AssigneeID == holderUserID)
		}
		if !held {
			return DeviceSnapshot{}, ErrNotFound
		}
	}
	names, err := s.ProductNames(ctx, []Asset{a})
	if err != nil {
		return DeviceSnapshot{}, err
	}
	return DeviceSnapshot{AssetID: a.ID, Reference: a.Reference, Product: names[a.ProductID], SerialNumber: a.SerialNumber, AssetTag: a.AssetTag, Status: a.Status}, nil
}

// Mine returns the assets currently assigned to the caller (no permission needed).
func (s *Service) Mine(ctx context.Context, p Principal, page Page) (Result, error) {
	if p.UserID == "" {
		return Result{}, ErrForbidden
	}
	res, err := s.store.List(ctx, Filter{AssignedToUser: p.UserID, Page: page.Normalize()})
	if err != nil {
		return Result{}, err
	}
	for i := range res.Items {
		res.Items[i] = holderView(res.Items[i])
	}
	return res, nil
}

// FindBySerial returns the one asset with this serial number for other modules (Endpoints links
// Devices to Assets this way). ErrNotFound when none matches; ErrConflict when several products
// share the serial number, so the match is ambiguous.
func (s *Service) FindBySerial(ctx context.Context, serial string) (Asset, error) {
	serial = strings.TrimSpace(serial)
	if serial == "" || utf8.RuneCountInString(serial) > maxSerial {
		return Asset{}, ErrNotFound
	}
	found, err := s.store.BySerial(ctx, serial)
	if err != nil {
		return Asset{}, err
	}
	switch len(found) {
	case 0:
		return Asset{}, ErrNotFound
	case 1:
		return found[0], nil
	}
	return Asset{}, ErrConflict
}

// Lookup resolves a scanned code to an asset. Requires assets.view.
func (s *Service) Lookup(ctx context.Context, p Principal, code string) (Asset, error) {
	if !p.canView() {
		return Asset{}, ErrForbidden
	}
	code = strings.TrimSpace(code)
	if code == "" || utf8.RuneCountInString(code) > 200 {
		return Asset{}, invalid("a code of 1-200 characters is required")
	}
	return s.store.Lookup(ctx, code)
}
