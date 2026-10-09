package repository_test

import (
	"errors"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
)

// The calendar contract (used by Planning's maintenance calendar) lists the
// approved, scheduled and in-progress Changes whose window overlaps [from, to).
func TestCalendarOverlapAndBounds(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	byStatus := map[string]application.Change{}
	for _, st := range []string{"draft", "approved", "scheduled", "in_progress", "completed", "cancelled"} {
		byStatus[st] = e.drive("normal", "low", st)
	}
	ws, we := *byStatus["scheduled"].WindowStart, *byStatus["scheduled"].WindowEnd
	listed := func(from, to time.Time) map[string]application.CalendarEntry {
		t.Helper()
		page, err := e.svc.Calendar(ctx, from, to, 0)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]application.CalendarEntry{}
		for _, en := range page.Entries {
			for st, c := range byStatus {
				if en.Change.ID == c.ID {
					out[st] = en
				}
			}
		}
		return out
	}
	all := listed(ws.Add(-time.Hour), we.Add(time.Hour))
	if len(all) != 3 || all["approved"].Change.ID == "" || all["scheduled"].Change.ID == "" || all["in_progress"].Change.ID == "" {
		t.Fatalf("listed statuses = %v", keys(all))
	}
	if len(all["scheduled"].Affected) != 1 || all["scheduled"].Affected[0].Type != "service" {
		t.Errorf("affected = %+v", all["scheduled"].Affected)
	}
	for name, r := range map[string][2]time.Time{
		"ends at window start":   {ws.Add(-time.Hour), ws},
		"starts at window end":   {we, we.Add(time.Hour)},
		"entirely before":        {ws.Add(-3 * time.Hour), ws.Add(-time.Hour)},
		"entirely after":         {we.Add(time.Hour), we.Add(3 * time.Hour)},
		"one second before":      {ws.Add(-time.Hour), ws.Add(-time.Second)},
		"one second after (end)": {we.Add(time.Second), we.Add(time.Hour)},
	} {
		if got := listed(r[0], r[1]); len(got) != 0 {
			t.Errorf("%s: listed %v", name, keys(got))
		}
	}
	for name, r := range map[string][2]time.Time{
		"overlaps the start by a second": {ws.Add(-time.Hour), ws.Add(time.Second)},
		"overlaps the end by a second":   {we.Add(-time.Second), we.Add(time.Hour)},
		"inside the window":              {ws.Add(time.Minute), ws.Add(2 * time.Minute)},
	} {
		if got := listed(r[0], r[1]); len(got) != 3 {
			t.Errorf("%s: listed %v", name, keys(got))
		}
	}
	page, err := e.svc.Calendar(ctx, ws.Add(-time.Hour), we, 1)
	if err != nil || len(page.Entries) != 1 || !page.Truncated {
		t.Errorf("limit 1 = %d entries, truncated %v, %v", len(page.Entries), page.Truncated, err)
	}
	var inv *application.InvalidInputError
	if _, err := e.svc.Calendar(ctx, ws, ws.Add(application.MaxCalendarRange+time.Second), 0); !errors.As(err, &inv) {
		t.Errorf("range too long: %v", err)
	}
	if _, err := e.svc.Calendar(ctx, ws, ws, 0); !errors.As(err, &inv) {
		t.Errorf("empty range: %v", err)
	}
	found, err := e.svc.Lookup(ctx, []string{byStatus["draft"].ID, e.uuid()})
	if err != nil || len(found) != 1 || found[byStatus["draft"].ID].Status != "draft" {
		t.Errorf("lookup = %v %v", found, err)
	}
	if _, err := e.svc.Lookup(ctx, make([]string, application.MaxLookupIDs+1)); !errors.As(err, &inv) {
		t.Errorf("lookup cap: %v", err)
	}
}

func keys(m map[string]application.CalendarEntry) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Submitted Changes (assessment, pending approval) show their proposed window in the extended calendar only;
// drafts never show, and the plain calendar (briefing) stays limited to approved work.
func TestCalendarWithProposedShowsSubmittedWindows(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	draft := e.drive("normal", "low", "draft")
	submitted := e.drive("normal", "low", "assessment")
	pending := e.drive("normal", "medium", "pending_approval")
	approved := e.drive("normal", "low", "approved")
	if submitted.WindowStart == nil || !submitted.Proposed() || !pending.Proposed() || approved.Proposed() || draft.Proposed() {
		t.Fatalf("proposed flags: %v %v %v %v", submitted.Proposed(), pending.Proposed(), approved.Proposed(), draft.Proposed())
	}
	from, to := submitted.WindowStart.Add(-time.Hour), submitted.WindowEnd.Add(time.Hour)
	ids := func(p application.CalendarPage) map[string]bool {
		out := map[string]bool{}
		for _, en := range p.Entries {
			out[en.Change.ID] = true
		}
		return out
	}
	plain, err := e.svc.Calendar(ctx, from, to, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(plain); got[submitted.ID] || got[pending.ID] || got[draft.ID] || !got[approved.ID] {
		t.Errorf("plain calendar = %v", got)
	}
	ext, err := e.svc.CalendarWithProposed(ctx, from, to, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(ext); !got[submitted.ID] || !got[pending.ID] || !got[approved.ID] || got[draft.ID] {
		t.Errorf("extended calendar = %v", got)
	}
}
