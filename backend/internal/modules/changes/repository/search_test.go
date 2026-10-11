package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

// The Change text search matches in SQL (substrings of reference and title, umlauts folded) with keyset paging, so
// a Change older than the newest 200 is found too.
func TestListSearchSubstringFoldingAndPaging(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	r := repository.New(pool)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	tok := "zq" + hex.EncodeToString(b)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM changes.changes WHERE title LIKE $1`, tok+"%") })
	var user string
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO changes.changes(title, kind, risk, requester_user_id) VALUES ($1, 'normal', 'low', $2::uuid)`
	for _, title := range []string{tok + " Gäste-WLAN Erneuerung", tok + " Etikettendrucker tauschen"} {
		if _, err := pool.Exec(ctx, ins, title, user); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO changes.changes(title, kind, risk, requester_user_id)
		SELECT $1 || ' Filler ' || g, 'normal', 'low', $2::uuid FROM generate_series(1, 210) g`, tok, user); err != nil {
		t.Fatal(err)
	}
	find := func(text string) []application.Change {
		t.Helper()
		var all []application.Change
		cursor := ""
		for range 10 {
			res, err := r.List(ctx, application.Filter{Search: text, Page: application.Page{Limit: 100, Cursor: cursor}})
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, res.Items...)
			if cursor = res.NextCursor; cursor == "" {
				break
			}
		}
		return all
	}
	if got := len(find(tok)); got != 212 {
		t.Errorf("all matches across pages = %d, want 212", got)
	}
	for _, text := range []string{tok + " Etikett", tok + " ETIKETT", tok + " drucker"} {
		if got := find(text); len(got) != 1 || got[0].Title != tok+" Etikettendrucker tauschen" {
			t.Errorf("%q (older than 200 newer Changes) = %d hits", text, len(got))
		}
	}
	for _, text := range []string{tok + " Gast", tok + " Gaste", tok + " gäste wlan", tok + " Gast-WLAN", tok + " wlan/gäste"} {
		if got := find(text); len(got) != 1 {
			t.Errorf("folding %q = %d hits, want 1", text, len(got))
		}
	}
	// Every word must match; the reference is searched too; wildcards of the text are literal.
	if got := find(tok + " Etikett Gast"); len(got) != 0 {
		t.Errorf("words are ANDed: %d hits", len(got))
	}
	if got := find("%%%"); len(got) != 0 {
		t.Errorf("wildcards are literal: %d hits", len(got))
	}
	one := find(tok + " Etikett")[0]
	if got := find(one.Reference); len(got) != 1 || got[0].ID != one.ID {
		t.Errorf("reference search = %d hits", len(got))
	}
	if _, err := r.List(ctx, application.Filter{Search: tok, Page: application.Page{Cursor: "%%"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("bad cursor: %v", err)
	}
}
