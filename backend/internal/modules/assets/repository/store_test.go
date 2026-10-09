package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/assets/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type dir struct{ active map[string]bool }

func (d dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	return d.pick(ids), nil
}
func (d dir) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	return d.pick(ids), nil
}
func (d dir) ActiveLocations(_ context.Context, ids []string) (map[string]bool, error) {
	return d.pick(ids), nil
}
func (d dir) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (d dir) TeamNames(_ context.Context, ids []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (d dir) LocationNames(_ context.Context, ids []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (d dir) pick(ids []string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = d.active[id]
	}
	return out
}

type products map[string]application.ProductInfo

func (p products) Products(_ context.Context, ids []string) (map[string]application.ProductInfo, error) {
	out := map[string]application.ProductInfo{}
	for _, id := range ids {
		if v, ok := p[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

// SearchProducts is the optional ProductSearcher capability: a plain substring match on the fake product names.
func (p products) SearchProducts(_ context.Context, text string, _ int) ([]string, error) {
	out := []string{}
	for id, v := range p {
		if strings.Contains(strings.ToLower(v.Name), strings.ToLower(text)) {
			out = append(out, id)
		}
	}
	return out, nil
}

// hosts is a fake DeviceSearcher: hostname -> asset id.
type hosts map[string]string

func (h hosts) AssetIDsByHostname(_ context.Context, text string, _ int) ([]string, error) {
	out := []string{}
	for name, id := range h {
		if strings.Contains(strings.ToLower(name), strings.ToLower(text)) {
			out = append(out, id)
		}
	}
	return out, nil
}

type env struct {
	t                                     *testing.T
	pool                                  *pgxpool.Pool
	svc                                   *application.Service
	corr                                  string
	manager, holder, stranger, other, loc string
	laptop, mouse, dock                   string
	manage, view                          application.Principal
	created                               []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	e := &env{t: t, pool: pool, corr: "assets-" + hex.EncodeToString(b)}
	for _, dst := range []*string{&e.manager, &e.holder, &e.stranger, &e.other, &e.loc, &e.laptop, &e.mouse, &e.dock} {
		if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	d := dir{active: map[string]bool{e.holder: true, e.other: true, e.manager: true, e.loc: true}}
	pr := products{
		e.laptop: {ID: e.laptop, Name: "Laptop", Active: true, AssetManaged: true, Serialized: true},
		e.mouse:  {ID: e.mouse, Name: "Mouse", Active: true, AssetManaged: true},
		e.dock:   {ID: e.dock, Name: "Dock", Active: true, AssetManaged: false},
	}
	e.svc = application.NewService(repository.New(pool), d, pr, nil)
	e.manage = application.Principal{UserID: e.manager, Manage: true}
	e.view = application.Principal{UserID: e.stranger, View: true}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM assets.assets WHERE product_id = ANY($1::uuid[])`, []string{e.laptop, e.mouse, e.dock})
	})
	return e
}

func (e *env) caller(user string) application.Caller {
	return application.Caller{Actor: audit.UserActor(user), CorrelationID: e.corr}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) create(in application.CreateInput) application.Asset {
	e.t.Helper()
	a, err := e.svc.Create(context.Background(), e.caller(e.manager), e.manage, in)
	if err != nil {
		e.t.Fatalf("create: %v", err)
	}
	return a
}

func (e *env) op(a application.Asset, op string, p application.Params) (application.Asset, error) {
	return e.svc.Transition(context.Background(), e.caller(e.manager), e.manage, a.ID, nil, op, p)
}

func (e *env) mustOp(a application.Asset, op string, p application.Params) application.Asset {
	e.t.Helper()
	out, err := e.op(a, op, p)
	if err != nil {
		e.t.Fatalf("%s: %v", op, err)
	}
	return out
}

func TestLifecycleAndAssignmentHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.create(application.CreateInput{ProductID: e.laptop, SerialNumber: "SN-" + e.corr, AssetTag: "TAG-" + e.corr, Status: "received"})
	if a.Status != "received" || a.Reference == "" || a.ProvisioningStatus != "not_required" || a.Version != 1 {
		t.Fatalf("created = %+v", a)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'assets.asset.created'`, e.corr) != 1 ||
		e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'AssetCreated'`, e.corr) != 1 {
		t.Error("creating must write the audit event and AssetCreated")
	}
	a = e.mustOp(a, application.OpMakeAvailable, application.Params{})
	a = e.mustOp(a, application.OpAssign, application.Params{Assignee: application.Assignee{Type: "user", ID: e.holder}, Note: "welcome"})
	if a.Status != "assigned" {
		t.Fatalf("status = %s", a.Status)
	}
	// Reassigning closes the first assignment and opens another one.
	a = e.mustOp(a, application.OpReassign, application.Params{Assignee: application.Assignee{Type: "user", ID: e.other}})
	a = e.mustOp(a, application.OpReturn, application.Params{})
	if a.Status != "returned" {
		t.Fatalf("status = %s", a.Status)
	}
	d, err := e.svc.Get(ctx, e.manage, a.ID)
	if err != nil || len(d.Assignments) != 2 {
		t.Fatalf("history = %+v %v", d.Assignments, err)
	}
	for _, as := range d.Assignments {
		if as.ReturnedAt == nil {
			t.Errorf("assignment %+v is still active after the return", as)
		}
	}
	if e.count(`SELECT count(*) FROM assets.asset_assignments WHERE asset_id = $1::uuid AND returned_at IS NULL`, a.ID) != 0 {
		t.Error("a returned asset has no active assignment")
	}
	a = e.mustOp(a, application.OpSendToRepair, application.Params{Reason: "display defect"})
	if a.StatusReason == nil || *a.StatusReason != "display defect" {
		t.Errorf("reason = %v", a.StatusReason)
	}
	a = e.mustOp(a, application.OpFinishRepair, application.Params{})
	if a.StatusReason != nil {
		t.Errorf("the reason must be cleared after the repair: %v", a.StatusReason)
	}
	if _, err := e.op(a, application.OpDispose, application.Params{Reason: "x"}); err == nil {
		t.Error("an available asset must not be disposed directly")
	}
	a = e.mustOp(a, application.OpRetire, application.Params{Reason: "end of life"})
	a = e.mustOp(a, application.OpDispose, application.Params{Reason: "recycled"})
	if a.Status != "disposed" {
		t.Fatalf("status = %s", a.Status)
	}
	var tr *application.InvalidTransitionError
	for _, op := range []string{application.OpMakeAvailable, application.OpAssign, application.OpRecover, application.OpMarkLost} {
		if _, err := e.op(a, op, application.Params{Reason: "x", Assignee: application.Assignee{Type: "user", ID: e.holder}}); !errors.As(err, &tr) {
			t.Errorf("%s on a disposed asset: %v", op, err)
		}
	}
	if _, err := e.svc.Update(ctx, e.caller(e.manager), e.manage, a.ID, a.Version, application.UpdateInput{Notes: strp("late")}); !errors.As(err, &tr) {
		t.Errorf("update of a disposed asset: %v", err)
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'AssetAssigned'`, e.corr) != 2 ||
		e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'AssetReturned'`, e.corr) != 1 {
		t.Error("assignments and returns must be published")
	}
}

func strp(s string) *string { return &s }

func TestReasonsAreRequiredAndInternalOperationsRefusedFromTheAPI(t *testing.T) {
	e := newEnv(t)
	a := e.create(application.CreateInput{ProductID: e.mouse})
	var inv *application.InvalidInputError
	if _, err := e.op(a, application.OpMarkLost, application.Params{}); !errors.As(err, &inv) {
		t.Errorf("mark lost without a reason: %v", err)
	}
	for _, op := range []string{application.OpReserve, application.OpReleaseReservation, application.OpAssignReserved, "nonsense"} {
		if _, err := e.op(a, op, application.Params{}); !errors.As(err, &inv) {
			t.Errorf("%s via the API: %v", op, err)
		}
	}
	if _, err := e.op(a, application.OpAssign, application.Params{Assignee: application.Assignee{Type: "user", ID: e.stranger}}); !errors.Is(err, application.ErrAssigneeInvalid) {
		t.Errorf("assigning to an inactive user: %v", err)
	}
	if _, err := e.op(a, application.OpAssign, application.Params{Assignee: application.Assignee{Type: "robot", ID: e.holder}}); !errors.As(err, &inv) {
		t.Errorf("unknown assignee type: %v", err)
	}
	if _, err := e.svc.Transition(context.Background(), e.caller(e.stranger), application.Principal{UserID: e.stranger, View: true}, a.ID, nil, application.OpMakeAvailable, application.Params{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("view-only caller: %v", err)
	}
}

func TestReservationOperationsThroughTheContract(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.create(application.CreateInput{ProductID: e.laptop, SerialNumber: "R-" + e.corr})
	run := func(op string, p application.Params) (application.Asset, error) {
		var out application.Asset
		err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			var err error
			out, err = e.svc.TransitionInTx(ctx, tx, e.caller(e.manager), a.ID, nil, op, p)
			return err
		})
		return out, err
	}
	if out, err := run(application.OpReserve, application.Params{}); err != nil || out.Status != "reserved" {
		t.Fatalf("reserve = %+v %v", out, err)
	}
	var tr *application.InvalidTransitionError
	if _, err := run(application.OpReserve, application.Params{}); !errors.As(err, &tr) {
		t.Errorf("a reserved asset cannot be reserved again: %v", err)
	}
	if _, err := run(application.OpAssign, application.Params{Assignee: application.Assignee{Type: "user", ID: e.holder}}); !errors.As(err, &tr) {
		t.Errorf("a reserved asset cannot be assigned directly: %v", err)
	}
	if out, err := run(application.OpAssignReserved, application.Params{Assignee: application.Assignee{Type: "user", ID: e.holder}}); err != nil || out.Status != "assigned" {
		t.Fatalf("assign reserved = %+v %v", out, err)
	}
}

func TestUniquenessAndProductRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.create(application.CreateInput{ProductID: e.laptop, SerialNumber: "U-" + e.corr, AssetTag: "UT-" + e.corr})
	if _, err := e.svc.Create(ctx, e.caller(e.manager), e.manage, application.CreateInput{ProductID: e.laptop, SerialNumber: "u-" + e.corr}); !errors.Is(err, application.ErrConflict) {
		t.Errorf("duplicate serial number (case-insensitive): %v", err)
	}
	if _, err := e.svc.Create(ctx, e.caller(e.manager), e.manage, application.CreateInput{ProductID: e.mouse, AssetTag: "ut-" + e.corr}); !errors.Is(err, application.ErrConflict) {
		t.Errorf("duplicate asset tag: %v", err)
	}
	// The same serial number on another product is allowed (it may collide across manufacturers).
	e.create(application.CreateInput{ProductID: e.mouse, SerialNumber: "U-" + e.corr})
	var inv *application.InvalidInputError
	if _, err := e.svc.Create(ctx, e.caller(e.manager), e.manage, application.CreateInput{ProductID: e.laptop}); !errors.As(err, &inv) {
		t.Errorf("a serialized product needs a serial number: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.caller(e.manager), e.manage, application.CreateInput{ProductID: e.dock}); !errors.Is(err, application.ErrProductInvalid) {
		t.Errorf("a product that is not asset-managed: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.caller(e.manager), e.manage, application.CreateInput{ProductID: e.mouse, LocationID: strp(e.stranger)}); !errors.Is(err, application.ErrReferenceInvalid) {
		t.Errorf("unknown location: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.caller(e.stranger), e.view, application.CreateInput{ProductID: e.mouse}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("create without assets.manage: %v", err)
	}
}

func TestUpdateAndProvisioning(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.create(application.CreateInput{ProductID: e.laptop, SerialNumber: "P-" + e.corr})
	if _, err := e.svc.Update(ctx, e.caller(e.manager), e.manage, a.ID, a.Version+1, application.UpdateInput{Notes: strp("x")}); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	var inv *application.InvalidInputError
	if _, err := e.svc.Update(ctx, e.caller(e.manager), e.manage, a.ID, a.Version, application.UpdateInput{SerialNumber: strp("")}); !errors.As(err, &inv) {
		t.Errorf("clearing the serial number of a serialized product: %v", err)
	}
	loc := e.loc
	out, err := e.svc.Update(ctx, e.caller(e.manager), e.manage, a.ID, a.Version, application.UpdateInput{LocationID: &loc, Notes: strp("shelf 3")})
	if err != nil || out.Version != 2 || out.LocationID == nil || *out.LocationID != loc {
		t.Fatalf("update = %+v %v", out, err)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'assets.asset.updated' AND metadata::text NOT LIKE '%shelf%'`, e.corr) != 1 {
		t.Error("the update must be audited without the notes text")
	}
	out, err = e.svc.SetProvisioning(ctx, e.caller(e.manager), e.manage, a.ID, nil, "in_progress")
	if err != nil || out.ProvisioningStatus != "in_progress" {
		t.Fatalf("provisioning = %+v %v", out, err)
	}
	if _, err := e.svc.SetProvisioning(ctx, e.caller(e.manager), e.manage, a.ID, nil, "weird"); !errors.As(err, &inv) {
		t.Errorf("unknown provisioning status: %v", err)
	}
}

func TestAccessRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.create(application.CreateInput{ProductID: e.mouse})
	a = e.mustOp(a, application.OpAssign, application.Params{Assignee: application.Assignee{Type: "user", ID: e.holder}})
	a = e.mustOp(a, application.OpReassign, application.Params{Assignee: application.Assignee{Type: "user", ID: e.holder}, Note: "again"})

	holder := application.Principal{UserID: e.holder}
	d, err := e.svc.Get(ctx, holder, a.ID)
	if err != nil || len(d.Assignments) != 1 || d.Assignments[0].ReturnedAt != nil || len(d.Allowed) != 0 {
		t.Fatalf("the holder sees only the current assignment and no actions: %+v %v", d, err)
	}
	if _, err := e.svc.Get(ctx, application.Principal{UserID: e.other}, a.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("a stranger must get not found: %v", err)
	}
	if _, err := e.svc.Get(ctx, application.Principal{UserID: e.holder}, "not-a-uuid"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
	full, err := e.svc.Get(ctx, e.view, a.ID)
	if err != nil || len(full.Assignments) != 2 {
		t.Fatalf("a viewer sees the history: %+v %v", full.Assignments, err)
	}
	if _, err := e.svc.List(ctx, application.Principal{UserID: e.holder}, application.Filter{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("list without assets.view: %v", err)
	}
	mine, err := e.svc.Mine(ctx, holder, application.Page{})
	found := false
	for _, m := range mine.Items {
		found = found || m.ID == a.ID
	}
	if err != nil || !found {
		t.Errorf("my assets = %+v %v", mine, err)
	}
	if _, err := e.svc.Lookup(ctx, holder, a.Reference); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("lookup without assets.view: %v", err)
	}
	got, err := e.svc.Lookup(ctx, e.view, " "+a.Reference+" ")
	if err != nil || got.ID != a.ID {
		t.Errorf("lookup by reference = %+v %v", got, err)
	}
	if _, err := e.svc.Lookup(ctx, e.view, "does-not-exist-"+e.corr); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown code: %v", err)
	}
}

func TestLookupByTagAndAmbiguousSerial(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tagged := e.create(application.CreateInput{ProductID: e.mouse, AssetTag: "LK-" + e.corr, SerialNumber: "S1-" + e.corr})
	e.create(application.CreateInput{ProductID: e.laptop, SerialNumber: "SHARED-" + e.corr})
	e.create(application.CreateInput{ProductID: e.mouse, SerialNumber: "shared-" + e.corr})
	if got, err := e.svc.Lookup(ctx, e.view, "lk-"+e.corr); err != nil || got.ID != tagged.ID {
		t.Errorf("by tag = %+v %v", got, err)
	}
	if got, err := e.svc.Lookup(ctx, e.view, "S1-"+e.corr); err != nil || got.ID != tagged.ID {
		t.Errorf("by serial = %+v %v", got, err)
	}
	if _, err := e.svc.Lookup(ctx, e.view, "shared-"+e.corr); !errors.Is(err, application.ErrConflict) {
		t.Errorf("a serial shared by two products is ambiguous: %v", err)
	}
	res, err := e.svc.List(ctx, e.view, application.Filter{Query: "lk-" + e.corr})
	if err != nil || len(res.Items) != 1 {
		t.Errorf("prefix search = %+v %v", res, err)
	}
}

func TestConcurrentAssignmentsHaveOneWinner(t *testing.T) {
	e := newEnv(t)
	a := e.create(application.CreateInput{ProductID: e.mouse})
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		to := e.holder
		if i%2 == 1 {
			to = e.other
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.op(a, application.OpAssign, application.Params{Assignee: application.Assignee{Type: "user", ID: to}})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		var tr *application.InvalidTransitionError
		switch {
		case err == nil:
			won++
		case errors.As(err, &tr):
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if won != 1 {
		t.Errorf("%d assignments succeeded, want exactly 1", won)
	}
	if e.count(`SELECT count(*) FROM assets.asset_assignments WHERE asset_id = $1::uuid AND returned_at IS NULL`, a.ID) != 1 {
		t.Error("exactly one active assignment is expected")
	}
}

func TestDatabaseInvariants(t *testing.T) {
	e := newEnv(t)
	a := e.create(application.CreateInput{ProductID: e.mouse})
	for name, sql := range map[string]string{
		"unknown status":       `UPDATE assets.assets SET status = 'broken' WHERE id = $1`,
		"unknown provisioning": `UPDATE assets.assets SET provisioning_status = 'x' WHERE id = $1`,
		"blank serial":         `UPDATE assets.assets SET serial_number = ' ' WHERE id = $1`,
		"source without id":    `UPDATE assets.assets SET source_type = 'goods_receipt' WHERE id = $1`,
		"unknown ownership":    `UPDATE assets.assets SET ownership_type = 'stolen' WHERE id = $1`,
	} {
		if _, err := e.pool.Exec(context.Background(), sql, a.ID); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestReservedAssetsLeaveOnlyThroughInventoryAndHoldersSeeAReducedView(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.create(application.CreateInput{ProductID: e.mouse, Notes: "internal remark", SerialNumber: "R-" + e.corr})
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := e.svc.TransitionInTx(ctx, tx, e.caller(e.manager), a.ID, nil, application.OpReserve, application.Params{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var tr *application.InvalidTransitionError
	if _, err := e.op(a, application.OpMarkLost, application.Params{Reason: "gone"}); !errors.As(err, &tr) {
		t.Errorf("a reserved asset was marked lost: %v", err)
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := e.svc.TransitionInTx(ctx, tx, e.caller(e.manager), a.ID, nil, application.OpAssignReserved, application.Params{Assignee: application.Assignee{Type: "user", ID: e.holder}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.Get(ctx, application.Principal{UserID: e.holder}, a.ID)
	if err != nil || d.Asset.Notes != nil || d.Asset.SupplierID != nil || d.Asset.StatusReason != nil {
		t.Fatalf("holder view = %+v %v", d.Asset, err)
	}
	mine, _ := e.svc.Mine(ctx, application.Principal{UserID: e.holder}, application.Page{})
	for _, m := range mine.Items {
		if m.Notes != nil {
			t.Error("my assets leaked the internal notes")
		}
	}
	if full, _ := e.svc.Get(ctx, e.view, a.ID); full.Asset.Notes == nil {
		t.Error("staff must still see the notes")
	}
	// Changing the serial number is traceable: the audit state carries the old and the new value.
	if _, err := e.svc.Update(ctx, e.caller(e.manager), e.manage, a.ID, d.Asset.Version, application.UpdateInput{SerialNumber: strp("CHANGED-" + e.corr)}); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'assets.asset.updated' AND before_data::text LIKE '%R-%' AND after_data::text LIKE '%CHANGED-%'`, e.corr) != 1 {
		t.Error("the serial number change must be audited with old and new value")
	}
}

func TestUserHoldersAndHeldAssets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	repo := repository.New(e.pool)
	mk := func(serial string) application.Asset {
		a := e.create(application.CreateInput{ProductID: e.laptop, SerialNumber: serial + e.corr, AssetTag: "T" + serial + e.corr, Status: "received"})
		return e.mustOp(a, application.OpMakeAvailable, application.Params{})
	}
	held, returned, free := mk("h"), mk("r"), mk("f")
	held = e.mustOp(held, application.OpAssign, application.Params{Assignee: application.Assignee{Type: "user", ID: e.holder}})
	returned = e.mustOp(returned, application.OpAssign, application.Params{Assignee: application.Assignee{Type: "user", ID: e.holder}})
	e.mustOp(returned, application.OpReturn, application.Params{})

	got, err := repo.UserHolders(ctx, []string{held.ID, returned.ID, free.ID})
	if err != nil || len(got) != 1 || got[held.ID] != e.holder {
		t.Fatalf("holders = %v, %v (returned and unassigned assets have no holder)", got, err)
	}
	byUser, err := repo.AssetsHeldByUsers(ctx, []string{e.holder, e.other}, 10)
	if err != nil || len(byUser[e.holder]) != 1 || byUser[e.holder][0] != held.ID || len(byUser[e.other]) != 0 {
		t.Fatalf("held = %v, %v", byUser, err)
	}
	// Malformed ids are ignored instead of failing the lookup; the row limit is applied.
	if got, err := repo.UserHolders(ctx, []string{"not-a-uuid", held.ID}); err != nil || got[held.ID] != e.holder {
		t.Fatalf("holders with a malformed id = %v, %v", got, err)
	}
	if got, err := repo.AssetsHeldByUsers(ctx, []string{"nope", e.holder}, 10); err != nil || len(got[e.holder]) != 1 {
		t.Fatalf("held with a malformed id = %v, %v", got, err)
	}
	more := e.mustOp(mk("h2"), application.OpAssign, application.Params{Assignee: application.Assignee{Type: "user", ID: e.holder}})
	if got, err := repo.AssetsHeldByUsers(ctx, []string{e.holder}, 1); err != nil || len(got[e.holder]) != 1 {
		t.Fatalf("limit 1 = %v, %v (asset %s)", got, err, more.ID)
	}
}

func TestAssetsByIDsIsBoundedAndTolerant(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.create(application.CreateInput{ProductID: e.laptop, SerialNumber: "bi1" + e.corr, AssetTag: "TBI1" + e.corr, Status: "received"})
	b := e.create(application.CreateInput{ProductID: e.laptop, SerialNumber: "bi2" + e.corr, AssetTag: "TBI2" + e.corr, Status: "received"})
	pub := assetspublic.New(e.svc)
	got, err := pub.AssetsByIDs(ctx, []string{a.ID, strings.ToUpper(b.ID), "not-a-uuid", "00000000-0000-7000-8000-000000000001"})
	if err != nil || len(got) != 2 || got[a.ID].Reference != a.Reference || got[b.ID].ID != b.ID {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	if got, err := pub.AssetsByIDs(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty lookup: %+v %v", got, err)
	}
	if _, err := pub.AssetsByIDs(ctx, make([]string, application.MaxLookupIDs+1)); err == nil {
		t.Fatal("lookup must be bounded")
	}
}

func TestAssetSearchMatchesTagsProductsHostnamesAndTokens(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ws := e.create(application.CreateInput{ProductID: e.laptop, AssetTag: "KIS-WS-" + e.corr, SerialNumber: "SN-" + e.corr})
	other := e.create(application.CreateInput{ProductID: e.mouse, AssetTag: "MS-" + e.corr})
	e.svc.WithDeviceSearch(hosts{"orbis-pc-" + e.corr: other.ID})
	ids := func(p application.Principal, q string) []string {
		res, err := e.svc.List(ctx, p, application.Filter{Query: q})
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		out := []string{}
		for _, a := range res.Items {
			out = append(out, a.ID)
		}
		return out
	}
	has := func(got []string, a application.Asset) bool { return slices.Contains(got, a.ID) }
	// A short word matches the start of a part of the tag (two characters are below the trigram minimum).
	if got := ids(e.view, "ws"); !has(got, ws) || has(got, other) {
		t.Errorf("ws = %v", got)
	}
	// Three characters or more match anywhere in the reference, tag or serial number.
	for _, q := range []string{"-ws-" + e.corr, "kis-ws", "sn-" + e.corr, ws.Reference[len(ws.Reference)-4:] + ""} {
		if got := ids(e.view, q); !has(got, ws) {
			t.Errorf("%q misses the workstation: %v", q, got)
		}
	}
	// The product name matches (fake product search), and words narrow the result (AND).
	if got := ids(e.view, "laptop"); !has(got, ws) || has(got, other) {
		t.Errorf("product name = %v", got)
	}
	if got := ids(e.view, "laptop kis"); !has(got, ws) {
		t.Errorf("two words = %v", got)
	}
	if got := ids(e.view, "laptop ms-"+e.corr); has(got, ws) || has(got, other) {
		t.Errorf("words must all match: %v", got)
	}
	// Hostnames are searched only for callers who may see devices (endpoints.view).
	if got := ids(e.view, "orbis-pc-"+e.corr); len(got) != 0 {
		t.Errorf("hostname search without endpoints.view found %v", got)
	}
	withHosts := e.view
	withHosts.Hostnames = true
	if got := ids(withHosts, "orbis-pc-"+e.corr); !has(got, other) {
		t.Errorf("hostname search = %v", got)
	}
	// Wildcards are literals, and the lookup contract finds assets without authorization.
	if got := ids(e.view, "%"); len(got) != 0 {
		t.Errorf("a percent sign must not match everything: %v", got)
	}
	hits, err := e.svc.Search(ctx, "kis-ws-"+e.corr, 5)
	if err != nil || len(hits) != 1 || hits[0].Asset.ID != ws.ID || hits[0].ProductName != "Laptop" {
		t.Errorf("Search = %+v %v", hits, err)
	}
}
