package wiring_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	orgapp "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/scopetest"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// locFixture is the Location part of a scopetest Subject: fresh Locations in a real tree, subtree expansion, guarded
// moves and fresh user ids. Modules embed it.
type locFixture struct {
	pool *pgxpool.Pool
	org  *orgrepository.Repository
	pfx  string
}

// mover is the organization repository with its access guards, as the API runs it.
func (s *locFixture) mover(t *testing.T) *orgrepository.Repository {
	t.Helper()
	repo, err := wiring.Organization(s.pool, wiring.OrganizationConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func (s *locFixture) caller() orgapp.Caller {
	return orgapp.Caller{Actor: audit.CLIActor("scope-test"), CorrelationID: "corr-" + s.pfx, PlatformAdmin: true}
}

func (s *locFixture) Locations(t *testing.T) scopetest.Locations {
	t.Helper()
	ctx := context.Background()
	mk := func(name string, parent *string) orgapp.Location {
		in := orgapp.NewLocationInput{Kind: orgapp.LocationSite, Name: s.pfx + " " + name}
		if parent != nil {
			in.Kind, in.ParentID = orgapp.LocationArea, parent
		}
		l, err := s.org.CreateLocation(ctx, s.caller(), in)
		if err != nil {
			t.Fatalf("create location %s: %v", name, err)
		}
		return l
	}
	a := mk("Site A", nil)
	a1 := mk("A1", &a.ID)
	a2 := mk("A2", &a.ID)
	b := mk("Site B", nil)
	b1 := mk("B1", &b.ID)
	if _, err := s.org.SetLocationActive(ctx, s.caller(), a2.ID, a2.Version, false, true); err != nil {
		t.Fatalf("deactivate A2: %v", err)
	}
	return scopetest.Locations{SiteA: a.ID, AreaA1: a1.ID, AreaA2Inactive: a2.ID, SiteB: b.ID, AreaB1: b1.ID}
}

func (s *locFixture) Expand(t *testing.T, roots []string) []string {
	t.Helper()
	ids, err := s.org.LocationSubtreeIDs(context.Background(), roots)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func (s *locFixture) MoveLocation(t *testing.T, id, parentID string) {
	t.Helper()
	cur, err := s.org.GetLocation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.mover(t).MoveLocation(context.Background(), s.caller(), id, cur.Version, &parentID); err != nil {
		t.Fatalf("move location: %v", err)
	}
}

func (s *locFixture) User(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := s.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
