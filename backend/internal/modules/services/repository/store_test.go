package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/services/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/services/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

type fakeDir struct {
	users, teams, locations map[string]string
}

func (d fakeDir) has(m map[string]string, ids []string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		_, out[id] = m[id]
	}
	return out
}
func (d fakeDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	return d.has(d.users, ids), nil
}
func (d fakeDir) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	return d.has(d.teams, ids), nil
}
func (d fakeDir) ActiveLocations(_ context.Context, ids []string) (map[string]bool, error) {
	return d.has(d.locations, ids), nil
}
func (d fakeDir) LocationNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if n, ok := d.locations[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

type fakeInfra struct {
	mu  sync.Mutex
	vms map[string]application.VMInfo
}

func (f *fakeInfra) VMs(_ context.Context, ids []string) (map[string]application.VMInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]application.VMInfo{}
	for _, id := range ids {
		if v, ok := f.vms[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

type fakeAssets map[string]application.AssetInfo

func (f fakeAssets) Assets(_ context.Context, ids []string) (map[string]application.AssetInfo, error) {
	out := map[string]application.AssetInfo{}
	for _, id := range ids {
		if v, ok := f[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	app    *application.App
	links  *application.VMLinks
	graph  *relationships.Graph
	dir    fakeDir
	infra  *fakeInfra
	assets fakeAssets
	corr   string
	prefix string
	user   string
	team   string
	site   string
	manage application.Principal
	view   application.Principal
	ids    []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	reg := relationships.NewRegistry()
	reg.Register(application.Triples...)
	e := &env{t: t, pool: pool, graph: relationships.New(reg), corr: "svc-" + hex.EncodeToString(b), prefix: "svc" + hex.EncodeToString(b) + " ",
		infra: &fakeInfra{vms: map[string]application.VMInfo{}}, assets: fakeAssets{}}
	e.user, e.team, e.site = e.uuid(), e.uuid(), e.uuid()
	e.dir = fakeDir{users: map[string]string{e.user: "U"}, teams: map[string]string{e.team: "T"}, locations: map[string]string{e.site: "Headquarters"}}
	store := repository.New(pool)
	e.app = application.NewApp(store, e.graph, e.dir, e.infra, e.assets)
	e.links = application.NewVMLinks(store, e.graph, e.infra)
	e.manage = application.Principal{UserID: e.user, Manage: true, InfraView: true, AssetsView: true}
	e.view = application.Principal{UserID: e.user, View: true, InfraView: true, AssetsView: true}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.relationships WHERE source_id = ANY($1::uuid[]) OR target_id = ANY($1::uuid[])`, e.ids)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM services.services WHERE name LIKE $1`, e.prefix+"%")
	})
	return e
}

func (e *env) uuid() string {
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	e.ids = append(e.ids, id)
	return id
}

func (e *env) caller() application.Caller {
	return application.Caller{Actor: audit.UserActor(e.user), CorrelationID: e.corr}
}

func (e *env) service(name string) application.Service {
	e.t.Helper()
	s, err := e.app.Create(context.Background(), e.caller(), e.manage, application.Input{Name: e.prefix + name, Criticality: "medium"})
	if err != nil {
		e.t.Fatalf("create %s: %v", name, err)
	}
	e.ids = append(e.ids, s.ID)
	return s
}

func (e *env) vm(state string, hypervisor *string) string {
	id := e.uuid()
	e.infra.mu.Lock()
	e.infra.vms[id] = application.VMInfo{ID: id, Name: "vm-" + id[:8], State: state, HypervisorAssetID: hypervisor}
	e.infra.mu.Unlock()
	return id
}

func (e *env) asset(status string) string {
	id := e.uuid()
	e.assets[id] = application.AssetInfo{ID: id, Reference: "AST-" + id[:8], Status: status}
	return id
}

func (e *env) dep(from, typ, to string) error {
	_, _, err := e.app.AddDependency(context.Background(), e.caller(), e.manage, from, typ, to)
	return err
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) audits(action string) int {
	return e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = $2`, e.corr, action)
}

func ver(s application.Service) *int { return &s.Version }

func TestCreateValidationAndUniqueness(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.service("Email")
	if s.Status != "operational" || s.Version != 1 || !strings.HasPrefix(s.Reference, "SVC-") {
		t.Fatalf("created: %+v", s)
	}
	if e.audits("services.service.created") != 1 || e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'ServiceCreated'`, e.corr) != 1 {
		t.Fatal("create must audit and publish in its transaction")
	}
	bad := []application.Input{
		{Name: "", Criticality: "low"},
		{Name: e.prefix + "x", Criticality: "urgent"},
		{Name: e.prefix + "x", Criticality: "low", Status: "retired"},
		{Name: e.prefix + "x‮", Criticality: "low"},
		{Name: e.prefix + "x", Criticality: "low", OwnerUserID: "nope"},
	}
	for i, in := range bad {
		if _, err := e.app.Create(ctx, e.caller(), e.manage, in); err == nil {
			t.Errorf("case %d must fail", i)
		}
	}
	if _, err := e.app.Create(ctx, e.caller(), e.manage, application.Input{Name: e.prefix + "x", Criticality: "low", OwnerUserID: e.uuid()}); !errors.Is(err, application.ErrReferenceInvalid) {
		t.Errorf("unknown owner: %v", err)
	}
	if _, err := e.app.Create(ctx, e.caller(), e.manage, application.Input{Name: strings.ToUpper(s.Name), Criticality: "low"}); !errors.Is(err, application.ErrConflict) {
		t.Errorf("case-insensitive duplicate name: %v", err)
	}
	if _, err := e.app.Create(ctx, e.caller(), e.view, application.Input{Name: e.prefix + "y", Criticality: "low"}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("view cannot create: %v", err)
	}
}

func TestUpdateDetailsStatusAndRetire(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.service("Wiki")
	desc, owner, support := "Internal wiki", e.user, e.team
	if _, err := e.app.UpdateDetails(ctx, e.caller(), e.manage, s.ID, nil, application.Details{Description: &desc}); err == nil {
		t.Fatal("expectedVersion is required")
	}
	s2, err := e.app.UpdateDetails(ctx, e.caller(), e.manage, s.ID, ver(s), application.Details{Description: &desc, OwnerUserID: &owner, SupportTeamID: &support})
	if err != nil || s2.Version != 2 || s2.OwnerUserID == nil || *s2.Description != desc {
		t.Fatalf("update: %+v %v", s2, err)
	}
	if _, err := e.app.UpdateDetails(ctx, e.caller(), e.manage, s.ID, ver(s), application.Details{Description: &desc}); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("stale version: %v", err)
	}
	empty := ""
	s3, err := e.app.UpdateDetails(ctx, e.caller(), e.manage, s.ID, ver(s2), application.Details{OwnerUserID: &empty})
	if err != nil || s3.OwnerUserID != nil || s3.SupportTeamID == nil {
		t.Fatalf("clear owner: %+v %v", s3, err)
	}
	if again, err := e.app.UpdateDetails(ctx, e.caller(), e.manage, s.ID, ver(s3), application.Details{OwnerUserID: &empty}); err != nil || again.Version != s3.Version {
		t.Fatalf("no-op update must not bump: %+v %v", again, err)
	}

	if _, err := e.app.ChangeStatus(ctx, e.caller(), e.manage, s.ID, nil, "degraded", "incident"); err == nil {
		t.Fatal("expectedVersion is required")
	}
	if _, err := e.app.ChangeStatus(ctx, e.caller(), e.manage, s.ID, ver(s3), "retired", "other"); err == nil {
		t.Fatal("retire has its own operation")
	}
	if _, err := e.app.ChangeStatus(ctx, e.caller(), e.manage, s.ID, ver(s3), "degraded", "because"); err == nil {
		t.Fatal("reason must be a known code")
	}
	d, err := e.app.ChangeStatus(ctx, e.caller(), e.manage, s.ID, ver(s3), "degraded", "incident")
	if err != nil || d.Status != "degraded" || d.StatusReason == nil || *d.StatusReason != "incident" {
		t.Fatalf("status: %+v %v", d, err)
	}
	if e.audits("services.service.status_changed") != 1 || e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'ServiceStatusChanged'`, e.corr) != 1 {
		t.Fatal("status change must audit and publish")
	}
	// Audit holds ids and codes, not names or descriptions.
	var meta, after []byte
	_ = e.pool.QueryRow(ctx, `SELECT metadata, after_state FROM platform.audit_events WHERE correlation_id = $1 AND action = 'services.service.updated' LIMIT 1`, e.corr).Scan(&meta, &after)
	if strings.Contains(string(meta)+string(after), "Internal wiki") || strings.Contains(string(after), e.prefix) {
		t.Fatalf("audit leaks free text: %s %s", meta, after)
	}

	// Dependencies end with the Service; dependents keep their link.
	other, dependent, asset := e.service("Other"), e.service("Dependent"), e.asset("available")
	if err := e.dep(s.ID, "asset", asset); err != nil {
		t.Fatal(err)
	}
	if err := e.dep(dependent.ID, "service", s.ID); err != nil {
		t.Fatal(err)
	}
	_ = other
	if _, err := e.app.Retire(ctx, e.caller(), e.manage, s.ID, ver(d), "unknown"); err == nil {
		t.Fatal("retire reason must be a known code")
	}
	r, err := e.app.Retire(ctx, e.caller(), e.manage, s.ID, ver(d), "replaced")
	if err != nil || r.Status != "retired" || r.RetiredAt == nil {
		t.Fatalf("retire: %+v %v", r, err)
	}
	out, _, _ := e.graph.Outgoing(ctx, e.pool, relationships.Node{Type: "service", ID: s.ID}, nil, 10)
	in, _, _ := e.graph.Incoming(ctx, e.pool, relationships.Node{Type: "service", ID: s.ID}, nil, 10)
	if len(out) != 0 || len(in) != 1 {
		t.Fatalf("retire ends own dependencies only: out=%d in=%d", len(out), len(in))
	}
	// A retried retire (same reason, version from before) is a no-op; other operations are refused.
	if again, err := e.app.Retire(ctx, e.caller(), e.manage, s.ID, ver(d), "replaced"); err != nil || again.Version != r.Version {
		t.Fatalf("retry: %+v %v", again, err)
	}
	if e.audits("services.service.retired") != 1 {
		t.Fatal("a retried retire must not audit twice")
	}
	if _, err := e.app.Retire(ctx, e.caller(), e.manage, s.ID, ver(r), "merged"); !errors.Is(err, application.ErrRetired) {
		t.Fatalf("other reason: %v", err)
	}
	if _, err := e.app.ChangeStatus(ctx, e.caller(), e.manage, s.ID, ver(r), "operational", "recovered"); !errors.Is(err, application.ErrRetired) {
		t.Fatalf("status after retire: %v", err)
	}
	if err := e.dep(s.ID, "asset", asset); !errors.Is(err, application.ErrRetired) {
		t.Fatalf("dependency of retired service: %v", err)
	}
	if err := e.dep(other.ID, "service", s.ID); !errors.Is(err, application.ErrReferenceInvalid) {
		t.Fatalf("dependency on retired service: %v", err)
	}
	// The name of a retired Service can be reused.
	if _, err := e.app.Create(ctx, e.caller(), e.manage, application.Input{Name: s.Name, Criticality: "low"}); err != nil {
		t.Fatalf("name reuse: %v", err)
	}
}

func TestListFilters(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.service("Alpha")
	e.service("Beta")
	for i := 0; i < 3; i++ {
		e.service("Gamma" + string(rune('a'+i)))
	}
	st, _ := e.app.ChangeStatus(ctx, e.caller(), e.manage, a.ID, ver(a), "outage", "incident")
	_ = st
	res, err := e.app.List(ctx, e.view, application.Filter{Query: e.prefix, Page: application.Page{Limit: 2}})
	if err != nil || len(res.Items) != 2 || res.NextCursor == "" {
		t.Fatalf("page 1: %d %q %v", len(res.Items), res.NextCursor, err)
	}
	res2, _ := e.app.List(ctx, e.view, application.Filter{Query: e.prefix, Page: application.Page{Limit: 10, Cursor: res.NextCursor}})
	if len(res2.Items) != 3 || res2.Items[0].ID <= res.Items[1].ID {
		t.Fatalf("page 2: %d", len(res2.Items))
	}
	res, _ = e.app.List(ctx, e.view, application.Filter{Query: e.prefix, Status: "outage"})
	if len(res.Items) != 1 || res.Items[0].ID != a.ID {
		t.Fatalf("status filter: %d", len(res.Items))
	}
	if res, _ = e.app.List(ctx, e.view, application.Filter{Query: e.prefix, Criticality: "critical"}); len(res.Items) != 0 {
		t.Fatal("criticality filter")
	}
	if _, err := e.app.List(ctx, e.view, application.Filter{Status: "bogus"}); err == nil {
		t.Fatal("unknown status must be refused")
	}
	if _, err := e.app.List(ctx, e.view, application.Filter{Page: application.Page{Cursor: "x"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("cursor: %v", err)
	}
	if _, err := e.app.List(ctx, application.Principal{}, application.Filter{}); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("no permission: %v", err)
	}
}

func TestDependencyValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.service("Core")
	liveVM, deadVM := e.vm("running", nil), e.vm("decommissioned", nil)
	okAsset, goneAsset := e.asset("assigned"), e.asset("disposed")
	cases := []struct {
		name, typ, id string
		want          error
	}{
		{"vm", "vm", liveVM, nil},
		{"asset", "asset", okAsset, nil},
		{"location", "location", e.site, nil},
		{"decommissioned vm", "vm", deadVM, application.ErrReferenceInvalid},
		{"unknown vm", "vm", e.uuid(), application.ErrReferenceInvalid},
		{"disposed asset", "asset", goneAsset, application.ErrReferenceInvalid},
		{"unknown location", "location", e.uuid(), application.ErrReferenceInvalid},
		{"unknown service", "service", e.uuid(), application.ErrReferenceInvalid},
	}
	for _, c := range cases {
		err := e.dep(s.ID, c.typ, c.id)
		if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	var inv *application.InvalidInputError
	if err := e.dep(s.ID, "ticket", e.uuid()); !errors.As(err, &inv) {
		t.Errorf("type not allowed: %v", err)
	}
	if err := e.dep(s.ID, "vm", "'; DROP TABLE x;--"); !errors.As(err, &inv) {
		t.Errorf("malformed id: %v", err)
	}
	if err := e.dep(s.ID, "service", s.ID); !errors.As(err, &inv) {
		t.Errorf("self dependency: %v", err)
	}
	if err := e.dep(e.uuid(), "vm", liveVM); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown service: %v", err)
	}
	if _, _, err := e.app.AddDependency(ctx, e.caller(), e.view, s.ID, "vm", liveVM); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("view cannot add: %v", err)
	}
	if e.audits("services.dependency.added") != 3 {
		t.Errorf("audits: %d", e.audits("services.dependency.added"))
	}
}

func TestAddRemoveDependencyIdempotentAndScoped(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.service("A"), e.service("B")
	asset := e.asset("available")
	l, created, err := e.app.AddDependency(ctx, e.caller(), e.manage, a.ID, "asset", asset)
	if err != nil || !created || l.Confidence != "declared" || l.Node.Reference == nil {
		t.Fatalf("add: %+v %v %v", l, created, err)
	}
	l2, created, err := e.app.AddDependency(ctx, e.caller(), e.manage, a.ID, "asset", strings.ToUpper(asset))
	if err != nil || created || l2.RelationshipID != l.RelationshipID {
		t.Fatalf("duplicate: %+v %v %v", l2, created, err)
	}
	if e.audits("services.dependency.added") != 1 {
		t.Fatal("duplicate must not audit")
	}
	// Another service's id cannot remove this dependency (IDOR on the relationship id).
	if err := e.app.RemoveDependency(ctx, e.caller(), e.manage, b.ID, l.RelationshipID, "other"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("foreign relationship: %v", err)
	}
	if err := e.app.RemoveDependency(ctx, e.caller(), e.manage, a.ID, l.RelationshipID, "free text"); err == nil {
		t.Fatal("reason must be a known code")
	}
	if err := e.app.RemoveDependency(ctx, e.caller(), e.manage, a.ID, e.uuid(), "other"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("unknown relationship: %v", err)
	}
	if err := e.app.RemoveDependency(ctx, e.caller(), e.manage, a.ID, l.RelationshipID, "replaced"); err != nil {
		t.Fatal(err)
	}
	if err := e.app.RemoveDependency(ctx, e.caller(), e.manage, a.ID, l.RelationshipID, "replaced"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if e.audits("services.dependency.removed") != 1 {
		t.Fatal("a retried removal must not audit twice")
	}
	d, err := e.app.Get(ctx, e.view, a.ID)
	if err != nil || len(d.Dependencies) != 0 {
		t.Fatalf("get: %+v %v", d, err)
	}
	// Re-adding after removal creates a new relationship.
	l3, created, err := e.app.AddDependency(ctx, e.caller(), e.manage, a.ID, "asset", asset)
	if err != nil || !created || l3.RelationshipID == l.RelationshipID {
		t.Fatalf("re-add: %+v %v %v", l3, created, err)
	}
}

func TestConcurrentDuplicateDependency(t *testing.T) {
	e := newEnv(t)
	s := e.service("Hot")
	vm := e.vm("running", nil)
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c, err := e.app.AddDependency(context.Background(), e.caller(), e.manage, s.ID, "vm", vm)
			if err != nil {
				t.Errorf("add: %v", err)
			}
			if c {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 || e.count(`SELECT count(*) FROM platform.relationships WHERE source_id = $1::uuid AND valid_until IS NULL`, s.ID) != 1 || e.audits("services.dependency.added") != 1 {
		t.Fatalf("created=%d rows/audits wrong", created.Load())
	}
}

func TestCycleRejection(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.service("A"), e.service("B"), e.service("C")
	if err := e.dep(a.ID, "service", b.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.dep(b.ID, "service", c.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.dep(c.ID, "service", a.ID); !errors.Is(err, application.ErrDependencyCycle) {
		t.Fatalf("3-cycle: %v", err)
	}
	if err := e.dep(b.ID, "service", a.ID); !errors.Is(err, application.ErrDependencyCycle) {
		t.Fatalf("2-cycle: %v", err)
	}
	if err := e.dep(a.ID, "service", c.ID); err != nil {
		t.Fatalf("a diamond is not a cycle: %v", err)
	}
	// A cycle longer than the traversal ceiling (depth 6) is still found.
	chain := make([]string, 10)
	for i := range chain {
		chain[i] = e.service("L" + string(rune('a'+i))).ID
		if i > 0 {
			if err := e.dep(chain[i-1], "service", chain[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := e.dep(chain[9], "service", chain[0]); !errors.Is(err, application.ErrDependencyCycle) {
		t.Fatalf("long cycle: %v", err)
	}
	// After the middle link is removed the closing link is fine.
	d, _ := e.app.Get(context.Background(), e.view, chain[4])
	if err := e.app.RemoveDependency(context.Background(), e.caller(), e.manage, chain[4], d.Dependencies[0].RelationshipID, "other"); err != nil {
		t.Fatal(err)
	}
	if err := e.dep(chain[9], "service", chain[0]); err != nil {
		t.Fatalf("cycle broken: %v", err)
	}
}

func TestConcurrentOppositeDependenciesCannotFormCycle(t *testing.T) {
	e := newEnv(t)
	for round := 0; round < 5; round++ {
		a, b := e.service("P"+string(rune('a'+round))), e.service("Q"+string(rune('a'+round)))
		var wg sync.WaitGroup
		var ok atomic.Int32
		for _, pair := range [][2]string{{a.ID, b.ID}, {b.ID, a.ID}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := e.dep(pair[0], "service", pair[1]); err == nil {
					ok.Add(1)
				} else if !errors.Is(err, application.ErrDependencyCycle) {
					t.Errorf("unexpected: %v", err)
				}
			}()
		}
		wg.Wait()
		if ok.Load() != 1 {
			t.Fatalf("round %d: %d of 2 opposite dependencies succeeded, want exactly 1", round, ok.Load())
		}
	}
}

func TestImpactTraversalRedactionAndCaps(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	hv := e.asset("assigned")
	vm := e.vm("running", &hv)
	if err := e.syncVM(vm); err != nil {
		t.Fatal(err)
	}
	db, web := e.service("DB"), e.service("Web")
	if err := e.dep(db.ID, "vm", vm); err != nil {
		t.Fatal(err)
	}
	if err := e.dep(web.ID, "service", db.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.dep(web.ID, "location", e.site); err != nil {
		t.Fatal(err)
	}

	res, err := e.app.Impact(ctx, e.view, application.ImpactInput{Type: "asset", ID: hv})
	if err != nil {
		t.Fatal(err)
	}
	if res.Direction != "downstream" || len(res.Nodes) != 3 || res.Truncated {
		t.Fatalf("downstream: %+v", res)
	}
	byID := map[string]application.ImpactNode{}
	for _, n := range res.Nodes {
		byID[n.ID] = n
	}
	if n := byID[vm]; n.Depth != 1 || n.Confidence != "derived" || n.Name == nil || len(n.Path) != 1 || n.Path[0].Type != "RUNS_ON" {
		t.Fatalf("vm node: %+v", n)
	}
	if n := byID[db.ID]; n.Depth != 2 || n.Criticality == nil || *n.Criticality != "medium" || n.Confidence != "declared" || len(n.Path) != 2 {
		t.Fatalf("db node: %+v", n)
	}
	if n := byID[web.ID]; n.Depth != 3 || n.Path[2].ToID != web.ID {
		t.Fatalf("web node: %+v", n)
	}

	// Upstream from the top service reaches the location, services, VM and hypervisor.
	up, err := e.app.Impact(ctx, e.view, application.ImpactInput{Type: "service", ID: web.ID, Direction: "upstream"})
	if err != nil || len(up.Nodes) != 4 {
		t.Fatalf("upstream: %+v %v", up, err)
	}
	if up.Start.Name == nil || *up.Start.Name != e.prefix+"Web" {
		t.Fatalf("start info: %+v", up.Start)
	}

	// Redaction: no infrastructure.view hides VM and Location names, no assets.view hides asset references.
	noInfra := application.Principal{UserID: e.user, View: true, AssetsView: true}
	up, _ = e.app.Impact(ctx, noInfra, application.ImpactInput{Type: "service", ID: web.ID, Direction: "upstream"})
	for _, n := range up.Nodes {
		if (n.Type == "vm" || n.Type == "location") && (n.Name != nil || n.Status != nil || n.Missing) {
			t.Fatalf("infra node leaks: %+v", n)
		}
		if n.Type == "asset" && n.Reference == nil {
			t.Fatalf("asset reference with assets.view: %+v", n)
		}
	}
	noAssets := application.Principal{UserID: e.user, View: true, InfraView: true}
	up, _ = e.app.Impact(ctx, noAssets, application.ImpactInput{Type: "service", ID: web.ID, Direction: "upstream"})
	for _, n := range up.Nodes {
		if n.Type == "asset" && (n.Reference != nil || n.Status != nil) {
			t.Fatalf("asset leaks: %+v", n)
		}
	}
	// A start record of a hidden type, or one that does not exist, is not found.
	bare := application.Principal{UserID: e.user, View: true}
	for _, in := range []application.ImpactInput{{Type: "asset", ID: hv}, {Type: "vm", ID: vm}, {Type: "location", ID: e.site}} {
		if _, err := e.app.Impact(ctx, bare, in); !errors.Is(err, application.ErrNotFound) {
			t.Errorf("hidden start %s: %v", in.Type, err)
		}
	}
	if _, err := e.app.Impact(ctx, e.view, application.ImpactInput{Type: "vm", ID: e.uuid()}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown start: %v", err)
	}
	if _, err := e.app.Impact(ctx, application.Principal{}, application.ImpactInput{Type: "service", ID: web.ID}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("no permission: %v", err)
	}
	var inv *application.InvalidInputError
	for _, in := range []application.ImpactInput{{Type: "ticket", ID: web.ID}, {Type: "service", ID: web.ID, Direction: "sideways"}, {Type: "service", ID: web.ID, Depth: 7}} {
		if _, err := e.app.Impact(ctx, e.view, in); !errors.As(err, &inv) {
			t.Errorf("invalid input %+v: %v", in, err)
		}
	}

	// Depth limit: a chain of 9 services truncates at the requested depth.
	chain := make([]string, 9)
	for i := range chain {
		chain[i] = e.service("C" + string(rune('a'+i))).ID
		if i > 0 {
			if err := e.dep(chain[i], "service", chain[i-1]); err != nil {
				t.Fatal(err)
			}
		}
	}
	res, _ = e.app.Impact(ctx, e.view, application.ImpactInput{Type: "service", ID: chain[0], Depth: 3})
	if len(res.Nodes) != 3 || !res.DepthLimited || !res.Truncated || res.NodeLimited {
		t.Fatalf("depth 3: %+v", res)
	}
	res, _ = e.app.Impact(ctx, e.view, application.ImpactInput{Type: "service", ID: chain[0]})
	if len(res.Nodes) != 6 || !res.DepthLimited || res.MaxDepth != 6 {
		t.Fatalf("default depth: %d %+v", len(res.Nodes), res)
	}

	// Node cap: 510 dependents of one location.
	site := e.uuid()
	e.dir.locations[site] = "Big site"
	for i := 0; i < 510; i++ {
		if _, _, err := e.graph.Link(ctx, e.pool, relationships.LinkInput{Source: relationships.Node{Type: "service", ID: e.uuid()}, Type: "DEPENDS_ON",
			Target: relationships.Node{Type: "location", ID: site}, Confidence: "declared"}); err != nil {
			t.Fatal(err)
		}
	}
	res, err = e.app.Impact(ctx, e.view, application.ImpactInput{Type: "location", ID: site})
	if err != nil || len(res.Nodes) != relationships.MaxNodes || !res.NodeLimited || !res.Truncated {
		t.Fatalf("node cap: %d %+v %v", len(res.Nodes), res.Truncated, err)
	}
	if !res.Nodes[0].Missing {
		t.Fatal("dangling nodes are reported as missing, not hidden")
	}
}

// syncVM runs one VM link sync in its own committed transaction.
func (e *env) syncVM(vm string) error {
	return pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		return e.links.Sync(context.Background(), tx, vm, e.corr)
	})
}

func (e *env) runsOn(vm string) []relationships.Relationship {
	out, _, err := e.graph.Outgoing(context.Background(), e.pool, relationships.Node{Type: "vm", ID: vm}, []string{"RUNS_ON"}, 10)
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func TestVMLinkSyncIsIdempotentAndFollowsState(t *testing.T) {
	e := newEnv(t)
	hv1, hv2 := e.asset("assigned"), e.asset("assigned")
	vm := e.vm("running", &hv1)
	for i := 0; i < 3; i++ {
		if err := e.syncVM(vm); err != nil {
			t.Fatal(err)
		}
	}
	got := e.runsOn(vm)
	if len(got) != 1 || got[0].Target.ID != hv1 || got[0].Confidence != "derived" {
		t.Fatalf("link: %+v", got)
	}
	if e.audits("services.vm_link.synced") != 1 {
		t.Fatalf("repeated syncs must audit once, got %d", e.audits("services.vm_link.synced"))
	}
	// New hypervisor: the old link ends, the new one is derived.
	e.infra.mu.Lock()
	e.infra.vms[vm] = application.VMInfo{ID: vm, State: "running", HypervisorAssetID: &hv2}
	e.infra.mu.Unlock()
	_ = e.syncVM(vm)
	got = e.runsOn(vm)
	if len(got) != 1 || got[0].Target.ID != hv2 {
		t.Fatalf("relink: %+v", got)
	}
	if n := e.count(`SELECT count(*) FROM platform.relationships WHERE source_id = $1::uuid AND end_reason = 'hypervisor_changed'`, vm); n != 1 {
		t.Fatalf("old link must end with a reason: %d", n)
	}
	// Cleared hypervisor.
	e.infra.mu.Lock()
	e.infra.vms[vm] = application.VMInfo{ID: vm, State: "running"}
	e.infra.mu.Unlock()
	_ = e.syncVM(vm)
	if len(e.runsOn(vm)) != 0 {
		t.Fatal("cleared hypervisor must end the link")
	}
	// Back, then decommissioned.
	e.infra.mu.Lock()
	e.infra.vms[vm] = application.VMInfo{ID: vm, State: "running", HypervisorAssetID: &hv1}
	e.infra.mu.Unlock()
	_ = e.syncVM(vm)
	e.infra.mu.Lock()
	e.infra.vms[vm] = application.VMInfo{ID: vm, State: "decommissioned", HypervisorAssetID: &hv1}
	e.infra.mu.Unlock()
	_ = e.syncVM(vm)
	if len(e.runsOn(vm)) != 0 {
		t.Fatal("decommissioned VM keeps no link")
	}
	// An unknown VM has no link either and does not fail.
	if err := e.syncVM(e.uuid()); err != nil {
		t.Fatal(err)
	}
}

func TestVMLinkConsumerPayloadAndConcurrency(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	hv := e.asset("assigned")
	vm := e.vm("running", &hv)
	for _, payload := range []string{`{`, `{"virtualMachineId":"nope"}`, `{}`} {
		err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			return e.links.OnVirtualMachineChanged(ctx, tx, events.OutboxEvent{Payload: json.RawMessage(payload), CorrelationID: e.corr})
		})
		if err == nil || !events.IsPermanent(err) {
			t.Errorf("payload %q must be a permanent failure: %v", payload, err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
				return e.links.OnVirtualMachineChanged(ctx, tx, events.OutboxEvent{Payload: json.RawMessage(`{"virtualMachineId":"` + strings.ToUpper(vm) + `"}`), CorrelationID: e.corr})
			})
			if err != nil {
				t.Errorf("consumer: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := e.runsOn(vm); len(got) != 1 || got[0].Target.ID != hv {
		t.Fatalf("concurrent syncs: %+v", got)
	}
	if e.audits("services.vm_link.synced") != 1 {
		t.Fatalf("audits: %d", e.audits("services.vm_link.synced"))
	}
}
