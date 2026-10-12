package wiring_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	assetsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/scopetest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// assetsSubject drives the Asset module through its real application layer (the HTTP layer only adds RequireScoped
// and the Principal conversion tested in the transport package) over a real Location tree.
type assetsSubject struct {
	locFixture
	svc     *assetsapp.Service
	product string
	seq     int
	created []string
}

func (s *assetsSubject) acaller() assetsapp.Caller {
	return assetsapp.Caller{Actor: audit.CLIActor("scope-test"), CorrelationID: "corr-" + s.pfx}
}

func (s *assetsSubject) Create(t *testing.T, locationID string) string {
	t.Helper()
	s.seq++
	in := assetsapp.CreateInput{ProductID: s.product, SerialNumber: s.pfx + "-" + string(rune('A'+s.seq/26)) + string(rune('A'+s.seq%26)) + "-" + hex8()}
	if locationID != "" {
		in.LocationID = &locationID
	}
	a, err := s.svc.Create(context.Background(), s.acaller(), assetsapp.Principal{Manage: true}, in)
	if errors.Is(err, assetsapp.ErrReferenceInvalid) && locationID != "" {
		// The Location is inactive (the fixture needs an asset there, which the operation refuses): register the
		// asset without Location and place it with privileged SQL.
		in.LocationID = nil
		if a, err = s.svc.Create(context.Background(), s.acaller(), assetsapp.Principal{Manage: true}, in); err == nil {
			if _, err = s.pool.Exec(context.Background(), `UPDATE assets.assets SET location_id = $2::uuid WHERE id = $1::uuid`, a.ID, locationID); err != nil {
				t.Fatalf("place asset: %v", err)
			}
		}
	}
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	s.created = append(s.created, a.ID)
	return a.ID
}

func hex8() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func principalOf(p authorization.Principal) assetsapp.Principal {
	return assetsapp.Principal{UserID: p.UserID, View: p.Has("assets.view"), Manage: p.Has("assets.manage"),
		ViewScope: p.ScopeOf("assets.view"), ManageScope: p.ScopeOf("assets.manage")}
}

func (s *assetsSubject) List(t *testing.T, p authorization.Principal) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for {
		res, err := s.svc.List(context.Background(), principalOf(p), assetsapp.Filter{Query: s.pfx, Page: assetsapp.Page{Limit: 50, Cursor: cursor}})
		if errors.Is(err, assetsapp.ErrForbidden) {
			return nil
		}
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, a := range res.Items {
			ids = append(ids, a.ID)
		}
		if res.NextCursor == "" {
			return ids
		}
		cursor = res.NextCursor
	}
}

func (s *assetsSubject) Count(t *testing.T, p authorization.Principal) int {
	t.Helper()
	// The query engine searches the reference only, so the fixture's assets are selected by their references.
	var children []query.Node
	for _, id := range s.created {
		a, err := s.svc.GetPlain(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		children = append(children, query.Node{Type: "condition", Field: "reference", Op: string(query.OpEquals), Value: json.RawMessage(strconv.Quote(a.Reference))})
	}
	filter := &query.Filter{V: 1, Root: &query.Node{Type: "group", Logic: "or", Children: children}}
	page, err := s.svc.Query(context.Background(), principalOf(p), query.Request{Filter: filter, Count: true, Limit: 5})
	if errors.Is(err, assetsapp.ErrForbidden) {
		return 0
	}
	if err != nil {
		t.Fatalf("query count: %v", err)
	}
	if page.Count == nil {
		t.Fatal("query returned no count")
	}
	return *page.Count
}

func (s *assetsSubject) Get(t *testing.T, p authorization.Principal, id string) bool {
	t.Helper()
	_, err := s.svc.Get(context.Background(), principalOf(p), id)
	if err != nil && !errors.Is(err, assetsapp.ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	return err == nil
}

func (s *assetsSubject) Mutate(t *testing.T, p authorization.Principal, id string) bool {
	t.Helper()
	cur, err := s.svc.GetPlain(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	note := "scope test"
	_, err = s.svc.Update(context.Background(), s.acaller(), principalOf(p), id, cur.Version, assetsapp.UpdateInput{Notes: &note})
	if err != nil && !errors.Is(err, assetsapp.ErrForbidden) && !errors.Is(err, assetsapp.ErrNotFound) {
		t.Fatalf("mutate: %v", err)
	}
	return err == nil
}

func (s *assetsSubject) Move(t *testing.T, p authorization.Principal, id, locationID string) bool {
	t.Helper()
	cur, err := s.svc.GetPlain(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	in := assetsapp.UpdateInput{}
	if locationID == "" {
		in.ClearLocation = true
	} else {
		in.LocationID = &locationID
	}
	_, err = s.svc.Update(context.Background(), s.acaller(), principalOf(p), id, cur.Version, in)
	if err != nil && !errors.Is(err, assetsapp.ErrForbidden) && !errors.Is(err, assetsapp.ErrNotFound) {
		t.Fatalf("move: %v", err)
	}
	return err == nil
}

func (s *assetsSubject) CreateAs(t *testing.T, p authorization.Principal, locationID string) bool {
	t.Helper()
	s.seq++
	in := assetsapp.CreateInput{ProductID: s.product, SerialNumber: s.pfx + "-c" + hex8()}
	if locationID != "" {
		in.LocationID = &locationID
	}
	a, err := s.svc.Create(context.Background(), s.acaller(), principalOf(p), in)
	if err != nil && !errors.Is(err, assetsapp.ErrForbidden) {
		t.Fatalf("create as: %v", err)
	}
	if err == nil {
		s.created = append(s.created, a.ID)
	}
	return err == nil
}

// The Asset module's adversarial Location scope run (ADR-0040, slice L-E): list, query count, detail, mutation,
// creation, move and the global search provider apply the caller's scope, never leak other Sites and answer an
// out-of-scope id exactly like a missing one.
func TestAssetsLocationScope(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	s := &assetsSubject{locFixture: locFixture{pool: pool, org: orgrepository.New(pool), pfx: "SCP" + hex8()}, svc: wiring.Assets(pool)}
	if err := pool.QueryRow(ctx, `INSERT INTO products.products(name, serialized, stock_managed, asset_managed) VALUES ($1, true, false, true) RETURNING id::text`, s.pfx+" device").Scan(&s.product); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, "corr-"+s.pfx)
		_, _ = pool.Exec(c, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, "corr-"+s.pfx)
		_, _ = pool.Exec(c, `DELETE FROM assets.asset_assignments WHERE asset_id IN (SELECT id FROM assets.assets WHERE product_id = $1::uuid)`, s.product)
		_, _ = pool.Exec(c, `DELETE FROM assets.assets WHERE product_id = $1::uuid`, s.product)
		_, _ = pool.Exec(c, `DELETE FROM products.products WHERE id = $1::uuid`, s.product)
		for i := 0; i < 5; i++ {
			_, _ = pool.Exec(c, `DELETE FROM organization.locations WHERE name LIKE $1 || '%' AND id NOT IN (SELECT parent_location_id FROM organization.locations WHERE parent_location_id IS NOT NULL)`, s.pfx)
		}
	})
	searcher := assetspublic.NewAssetSearcher(s.svc)
	scopetest.Run(t, scopetest.Config{
		View: "assets.view", Manage: "assets.manage",
		Surfaces: []scopetest.Surface{{
			Name: "global search provider",
			Visible: func(t *testing.T, p authorization.Principal) []string {
				hits, err := searcher.Search(ctx, p, s.pfx, 50)
				if errors.Is(err, search.ErrNotAllowed) {
					return nil
				}
				if err != nil {
					t.Fatalf("search: %v", err)
				}
				ids := make([]string, 0, len(hits))
				for _, h := range hits {
					ids = append(ids, h.ID)
				}
				return ids
			},
		}},
	}, s)
}
