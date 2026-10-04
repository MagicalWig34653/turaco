// Package public is the Endpoints module's contract for other modules: batch reads of observed software
// installations and Software Products (used by Security to match advisories). The reads perform no
// permission checks; the caller authorizes its own user and passes a Scope that states which fields the
// caller may receive. Provider-observed values keep their freshness (ObservedAt, RetiredAt). Nothing here
// writes endpoint data.
package public

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Bounds of one read.
const (
	// MaxProductIDs bounds the Software Products of one installation read.
	MaxProductIDs = 100
	// MaxDeviceIDs bounds the Devices of one per-device read.
	MaxDeviceIDs = 500
	// MaxPageSize bounds one page of installations.
	MaxPageSize = 1000
	// MaxLookupIDs bounds one Software Product or Device name lookup.
	MaxLookupIDs = 1000
)

// ErrTooMany refuses a read beyond the bounds above; ErrInvalidCursor a malformed cursor.
var (
	ErrTooMany       = errors.New("endpoints: too many ids in one read")
	ErrInvalidCursor = errors.New("endpoints: invalid cursor")
)

// Scope selects fields the caller is authorized to receive. Its zero value omits device names (only
// device ids, which reveal nothing by themselves).
type Scope struct {
	// IncludeDeviceNames returns provider-reported device names; only for callers holding endpoints.view.
	IncludeDeviceNames bool
}

// Installation is one observed software installation.
type Installation struct {
	ID       string
	DeviceID string
	// DeviceName is empty unless the Scope includes device names.
	DeviceName        string
	DevicePlatform    string
	SoftwareProductID string
	RawVersion        string
	ObservedAt        time.Time
	// Retired reports an installation a later snapshot no longer contained or whose Device was tombstoned.
	Retired bool
	// RetiredAt is when that was observed (the later of both tombstones).
	RetiredAt *time.Time
	// DeviceRetired reports a tombstoned Device.
	DeviceRetired bool
}

// InstallationPage is one page; NextCursor is empty on the last page.
type InstallationPage struct {
	Items      []Installation
	NextCursor string
}

// SoftwareProduct is a normalized Software Product.
type SoftwareProduct struct {
	ID        string
	Name      string
	Publisher string
}

// Match methods of FindSoftwareProduct.
const (
	MatchProduct = "product"
	MatchAlias   = "alias"
)

// Reader is what the contract reads (implemented by the endpoints repository).
type Reader interface {
	InstallationsByProducts(ctx context.Context, productIDs []string, includeRetired bool, after string, limit int) ([]application.InstallationRow, error)
	InstallationsOnDevices(ctx context.Context, deviceIDs, productIDs []string, limit int) ([]application.InstallationRow, error)
	DeviceStates(ctx context.Context, ids []string) (map[string]*time.Time, error)
	DeviceNames(ctx context.Context, ids []string) (map[string]string, error)
	LatestIngestionAt(ctx context.Context) (*time.Time, error)
	LatestObservedByProducts(ctx context.Context, productIDs []string) (map[string]time.Time, error)
	SoftwareProductsByIDs(ctx context.Context, ids []string) ([]application.SoftwareProductRow, error)
	SoftwareProductsByName(ctx context.Context, name, publisher string, limit int) ([]application.SoftwareProductRow, error)
	SoftwareProductByAlias(ctx context.Context, alias string) (*application.SoftwareProductRow, error)
}

// Inventory is the Endpoints module's public inventory read service.
type Inventory struct{ r Reader }

func NewInventory(r Reader) *Inventory { return &Inventory{r: r} }

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ids keeps the well-formed ids, lower-cased and without duplicates.
func ids(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, id := range in {
		if !uuidPattern.MatchString(id) {
			continue
		}
		id = strings.ToLower(id)
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func convert(rows []application.InstallationRow, scope Scope) []Installation {
	out := make([]Installation, 0, len(rows))
	for _, r := range rows {
		in := Installation{ID: r.ID, DeviceID: r.DeviceID, DevicePlatform: r.DevicePlatform, SoftwareProductID: r.SoftwareProductID,
			RawVersion: r.RawVersion, ObservedAt: r.ObservedAt, DeviceRetired: r.DeviceRetiredAt != nil}
		if scope.IncludeDeviceNames {
			in.DeviceName = r.DeviceName
		}
		for _, at := range []*time.Time{r.RetiredAt, r.DeviceRetiredAt} {
			if at != nil && (in.RetiredAt == nil || at.After(*in.RetiredAt)) {
				t := *at
				in.RetiredAt = &t
			}
		}
		in.Retired = in.RetiredAt != nil
		out = append(out, in)
	}
	return out
}

// InstallationsByProducts lists installations in (Software Product id, installation id) order.
// The cursor is the pair from the previous page. Without includeRetired only current
// installations on live Devices are returned.
func (v *Inventory) InstallationsByProducts(ctx context.Context, productIDs []string, includeRetired bool, scope Scope, cursor string, limit int) (InstallationPage, error) {
	pids := ids(productIDs)
	if len(pids) > MaxProductIDs {
		return InstallationPage{}, ErrTooMany
	}
	if cursor != "" {
		parts := strings.Split(cursor, ":")
		if len(parts) != 2 || !uuidPattern.MatchString(parts[0]) || !uuidPattern.MatchString(parts[1]) {
			return InstallationPage{}, ErrInvalidCursor
		}
		cursor = strings.ToLower(cursor)
	}
	if limit <= 0 || limit > MaxPageSize {
		limit = MaxPageSize
	}
	if len(pids) == 0 {
		return InstallationPage{}, nil
	}
	rows, err := v.r.InstallationsByProducts(ctx, pids, includeRetired, strings.ToLower(cursor), limit+1)
	if err != nil {
		return InstallationPage{}, err
	}
	page := InstallationPage{}
	if len(rows) > limit {
		rows = rows[:limit]
		page.NextCursor = fmt.Sprintf("%s:%s", rows[limit-1].SoftwareProductID, rows[limit-1].ID)
	}
	page.Items = convert(rows, scope)
	return page, nil
}

// InstallationsOnDevices lists every installation (retired ones included) of the Software Products on up
// to MaxDeviceIDs Devices, by device and id; truncated reports more than limit rows.
func (v *Inventory) InstallationsOnDevices(ctx context.Context, deviceIDs, productIDs []string, scope Scope, limit int) ([]Installation, bool, error) {
	dids, pids := ids(deviceIDs), ids(productIDs)
	if len(dids) > MaxDeviceIDs || len(pids) > MaxProductIDs {
		return nil, false, ErrTooMany
	}
	if limit <= 0 || limit > 10*MaxPageSize {
		limit = 10 * MaxPageSize
	}
	if len(dids) == 0 || len(pids) == 0 {
		return nil, false, nil
	}
	rows, err := v.r.InstallationsOnDevices(ctx, dids, pids, limit+1)
	if err != nil {
		return nil, false, err
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	return convert(rows, scope), truncated, nil
}

// DeviceRetired returns id -> tombstone time (nil while live) for the known Devices among up to
// MaxLookupIDs ids; unknown ids are absent.
func (v *Inventory) DeviceRetired(ctx context.Context, deviceIDs []string) (map[string]*time.Time, error) {
	dids := ids(deviceIDs)
	if len(dids) > MaxLookupIDs {
		return nil, ErrTooMany
	}
	if len(dids) == 0 {
		return map[string]*time.Time{}, nil
	}
	return v.r.DeviceStates(ctx, dids)
}

// DeviceNames returns id -> provider-reported device name for up to MaxLookupIDs ids. Only for callers
// holding endpoints.view; the caller checks that.
func (v *Inventory) DeviceNames(ctx context.Context, deviceIDs []string) (map[string]string, error) {
	dids := ids(deviceIDs)
	if len(dids) > MaxLookupIDs {
		return nil, ErrTooMany
	}
	if len(dids) == 0 {
		return map[string]string{}, nil
	}
	return v.r.DeviceNames(ctx, dids)
}

// LatestIngestionAt returns when endpoint data was last ingested (provider synchronization or import);
// nil when never.
func (v *Inventory) LatestIngestionAt(ctx context.Context) (*time.Time, error) {
	return v.r.LatestIngestionAt(ctx)
}

// LatestObservedByProducts returns the newest installation observation or tombstone per
// Software Product. Missing products have no observed installations.
func (v *Inventory) LatestObservedByProducts(ctx context.Context, productIDs []string) (map[string]time.Time, error) {
	pids := ids(productIDs)
	if len(pids) > MaxProductIDs {
		return nil, ErrTooMany
	}
	if len(pids) == 0 {
		return map[string]time.Time{}, nil
	}
	return v.r.LatestObservedByProducts(ctx, pids)
}

func product(p application.SoftwareProductRow) SoftwareProduct {
	out := SoftwareProduct{ID: p.ID, Name: p.Name}
	if p.Publisher != nil {
		out.Publisher = *p.Publisher
	}
	return out
}

// SoftwareProducts returns id -> Software Product for the known ids among up to MaxLookupIDs.
func (v *Inventory) SoftwareProducts(ctx context.Context, productIDs []string) (map[string]SoftwareProduct, error) {
	pids := ids(productIDs)
	if len(pids) > MaxLookupIDs {
		return nil, ErrTooMany
	}
	out := map[string]SoftwareProduct{}
	if len(pids) == 0 {
		return out, nil
	}
	rows, err := v.r.SoftwareProductsByIDs(ctx, pids)
	if err != nil {
		return nil, err
	}
	for _, p := range rows {
		out[p.ID] = product(p)
	}
	return out, nil
}

// FindSoftwareProduct resolves a product name (and optional publisher) to one Software Product: first by
// exact product name (case-insensitive; with a publisher, restricted to it; several products with that
// name and no publisher are ambiguous and not found), then by the alias key of the name. method is
// MatchProduct or MatchAlias; found is false when nothing (unambiguous) matches.
func (v *Inventory) FindSoftwareProduct(ctx context.Context, name, publisher string) (p SoftwareProduct, method string, found bool, err error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 300 || len(publisher) > 200 {
		return SoftwareProduct{}, "", false, nil
	}
	byName, err := v.r.SoftwareProductsByName(ctx, name, publisher, 2)
	if err != nil {
		return SoftwareProduct{}, "", false, err
	}
	if len(byName) == 1 {
		return product(byName[0]), MatchProduct, true, nil
	}
	if len(byName) > 1 {
		return SoftwareProduct{}, "", false, nil
	}
	alias, err := v.r.SoftwareProductByAlias(ctx, application.NormalizeSoftwareName(name))
	if err != nil || alias == nil {
		return SoftwareProduct{}, "", false, err
	}
	if p := strings.TrimSpace(publisher); p != "" && (alias.Publisher == nil || !strings.EqualFold(*alias.Publisher, p)) {
		return SoftwareProduct{}, "", false, nil
	}
	return product(*alias), MatchAlias, true, nil
}
