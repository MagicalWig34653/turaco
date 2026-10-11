package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

// The Problem text search matches in SQL (substrings of reference and title, umlauts folded) with keyset paging, so
// a Problem older than the newest 200 is found too.
func TestListProblemsSearchSubstringFoldingAndPaging(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	r := repository.New(pool)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	tok := "zq" + hex.EncodeToString(b)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM servicedesk.problems WHERE title LIKE $1`, tok+"%") })
	for _, title := range []string{tok + " Gäste-WLAN Ausfall", tok + " Etikettendrucker hängt"} {
		if _, err := pool.Exec(ctx, `INSERT INTO servicedesk.problems(title) VALUES ($1)`, title); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO servicedesk.problems(title) SELECT $1 || ' Filler ' || g FROM generate_series(1, 210) g`, tok); err != nil {
		t.Fatal(err)
	}
	find := func(text string) []application.Problem {
		t.Helper()
		var all []application.Problem
		cursor := ""
		for range 10 {
			res, err := r.ListProblems(ctx, "", text, application.Page{Limit: 100, Cursor: cursor})
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
		if got := find(text); len(got) != 1 || got[0].Title != tok+" Etikettendrucker hängt" {
			t.Errorf("%q (older than 200 newer Problems) = %d hits", text, len(got))
		}
	}
	for _, text := range []string{tok + " Gast", tok + " Gaste", tok + " gäste wlan", tok + " Gast-WLAN", tok + " wlan/gäste", tok + " haengt"} {
		want := 1
		if text == tok+" haengt" {
			want = 0 // the fold maps umlauts to the base letter (ä to a), not to a digraph: hängt is hangt
		}
		if got := find(text); len(got) != want {
			t.Errorf("folding %q = %d hits, want %d", text, len(got), want)
		}
	}
	if got := find(tok + " hangt"); len(got) != 1 {
		t.Errorf("folded umlaut: %d hits", len(got))
	}
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
	if res, err := r.ListProblems(ctx, "closed", tok, application.Page{}); err != nil || len(res.Items) != 0 {
		t.Errorf("the status filter still applies: %d %v", len(res.Items), err)
	}
	if _, err := r.ListProblems(ctx, "", tok, application.Page{Cursor: "%%"}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("bad cursor: %v", err)
	}
}
