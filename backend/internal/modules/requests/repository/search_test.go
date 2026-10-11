package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/requests/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

// The Request text search matches in SQL (substrings of reference and catalog item title, umlauts folded) with
// keyset paging, so a Request older than the newest 200 is found too; own-request scoping still applies.
func TestListSearchSubstringFoldingAndPaging(t *testing.T) {
	r, _ := newRepo(t)
	pool := dbtest.Pool(t)
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	tok := "zq" + hex.EncodeToString(b)
	requester, other := uuid(t, r), uuid(t, r)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM requests.service_requests WHERE catalog_item_title LIKE $1`, tok+"%")
	})
	ins := `INSERT INTO requests.service_requests(catalog_item_id, catalog_item_key, catalog_item_title, definition, answers,
		requester_user_id, requested_for_user_id, status) SELECT uuidv7(), 'k', $1 || $2 || g, '{}', '{}', $3::uuid, $3::uuid, 'in_fulfillment'
		FROM generate_series(1, $4::int) g`
	for _, c := range []struct {
		title string
		n     int
		user  string
	}{{" Gäste-WLAN Zugang ", 1, requester}, {" Etikettendrucker ", 1, other}, {" Filler ", 210, requester}} {
		if _, err := pool.Exec(ctx, ins, tok, c.title, c.user, c.n); err != nil {
			t.Fatal(err)
		}
	}
	find := func(q application.ListQuery) []application.Request {
		t.Helper()
		var all []application.Request
		q.Page = application.Page{Limit: 100}
		for range 10 {
			res, err := r.List(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, res.Items...)
			if q.Page.Cursor = res.NextCursor; q.Page.Cursor == "" {
				break
			}
		}
		return all
	}
	all := func(text string) []application.Request { return find(application.ListQuery{All: true, Search: text}) }
	if got := len(all(tok)); got != 212 {
		t.Errorf("all matches across pages = %d, want 212", got)
	}
	for _, text := range []string{tok + " Etikett", tok + " ETIKETT", tok + " drucker"} {
		if got := all(text); len(got) != 1 {
			t.Errorf("%q (older than 200 newer Requests) = %d hits", text, len(got))
		}
	}
	for _, text := range []string{tok + " Gast", tok + " Gaste", tok + " gäste wlan", tok + " Gast-WLAN", tok + " wlan/gäste"} {
		if got := all(text); len(got) != 1 {
			t.Errorf("folding %q = %d hits, want 1", text, len(got))
		}
	}
	if got := all(tok + " Etikett Gast"); len(got) != 0 {
		t.Errorf("words are ANDed: %d hits", len(got))
	}
	if got := all("%%%"); len(got) != 0 {
		t.Errorf("wildcards are literal: %d hits", len(got))
	}
	// Own-request scoping is not widened by the text: the Etikettendrucker request belongs to another User.
	if got := find(application.ListQuery{UserID: requester, Search: tok + " Etikett"}); len(got) != 0 {
		t.Errorf("scoped search sees another User's request: %d", len(got))
	}
	if got := find(application.ListQuery{UserID: other, Search: tok + " Etikett"}); len(got) != 1 {
		t.Errorf("scoped search of the owner = %d, want 1", len(got))
	}
	one := all(tok + " Etikett")[0]
	if got := all(one.Reference); len(got) != 1 || got[0].ID != one.ID {
		t.Errorf("reference search = %d hits", len(got))
	}
	if _, err := r.List(ctx, application.ListQuery{All: true, Search: tok, Page: application.Page{Cursor: "%%"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("bad cursor: %v", err)
	}
}
