package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// InvariantResult is the outcome of one post-run check.
type InvariantResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass, fail, skipped
	Detail string `json:"detail,omitempty"`
}

func pass(name, detail string) InvariantResult { return InvariantResult{name, "pass", detail} }
func fail(name, format string, args ...any) InvariantResult {
	return InvariantResult{name, "fail", fmt.Sprintf(format, args...)}
}
func skip(name, why string) InvariantResult { return InvariantResult{name, "skipped", why} }

// InvariantInput is what the checks need.
type InvariantInput struct {
	Env    *Env
	Rec    *Recorder
	PGURL  string
	Settle time.Duration // wait for System View count caches (15 s) to expire
}

// CheckInvariants runs the data-integrity checks after the load has stopped.
func CheckInvariants(ctx context.Context, in InvariantInput) []InvariantResult {
	var out []InvariantResult
	total := in.Rec.Total()

	bad := total.Classes[ClassServerError] + total.Classes[ClassNetwork]
	if bad == 0 {
		out = append(out, pass("no_5xx_or_timeouts", "no 5xx answers, timeouts or connection errors"))
	} else {
		out = append(out, fail("no_5xx_or_timeouts", "%d server errors (5xx) and %d timeouts/connection errors", total.Classes[ClassServerError], total.Classes[ClassNetwork]))
	}

	_, counts, writes := in.Env.Trk.Snapshot()
	if n := counts["duplicate_reference"]; n == 0 {
		out = append(out, pass("no_duplicate_ticket_numbers_observed", fmt.Sprintf("%d tickets created, all references distinct", len(in.Env.Trk.Refs()))))
	} else {
		out = append(out, fail("no_duplicate_ticket_numbers_observed", "%d reference(s) issued twice", n))
	}
	if n := counts["lost_update"] + counts["version_regressed"]; n == 0 {
		out = append(out, pass("no_lost_updates_observed", fmt.Sprintf("%d successful conditional writes, every resulting version unique and increasing", writes)))
	} else {
		out = append(out, fail("no_lost_updates_observed", "%d lost update(s) or version regressions (see violations)", n))
	}
	if n := counts["authorization_leak"]; n == 0 {
		out = append(out, pass("no_authorization_leaks", fmt.Sprintf("%d denied-access probes, none answered successfully", in.Env.probes.Load())))
	} else {
		out = append(out, fail("no_authorization_leaks", "%d probe(s) were allowed but must be denied", n))
	}

	if in.PGURL != "" {
		out = append(out, checkSQL(ctx, in)...)
	} else {
		out = append(out, checkVersionsViaAPI(ctx, in))
		out = append(out, skip("sql_reference_registry", "no --pg-url"))
	}
	out = append(out, checkReferencesViaAPI(ctx, in))
	out = append(out, checkQueueCounts(ctx, in)...)
	return out
}

func staffSession(env *Env) *Session {
	for _, class := range []string{ClassFirstLevel, ClassTechnician, ClassLead} {
		for _, s := range env.Sessions[class] {
			if s.Has("tickets.manage") {
				return s
			}
		}
	}
	return nil
}

// checkVersionsViaAPI re-reads a sample of tickets: their version must not be below any version observed.
func checkVersionsViaAPI(ctx context.Context, in InvariantInput) InvariantResult {
	const name = "final_versions_not_below_observed"
	s := staffSession(in.Env)
	if s == nil {
		return skip(name, "no staff session (needs a firstlevel, technician or lead user)")
	}
	ids := in.Env.Trk.TrackedIDs()
	sort.Strings(ids)
	if len(ids) > 200 {
		ids = ids[:200]
	}
	var lower []string
	checked := 0
	for _, id := range ids {
		var t ticketDTO
		r := in.Env.Client.Call(ctx, &JobCtx{Stage: -2}, s, "verify.ticket", http.MethodGet, "/tickets/"+id, nil, &t)
		if !r.OK() {
			continue
		}
		checked++
		if seen, ref := in.Env.Trk.MaxSeen(id); t.Version < seen {
			lower = append(lower, fmt.Sprintf("%s final %d < observed %d", ref, t.Version, seen))
		}
	}
	if len(lower) > 0 {
		return fail(name, "%d ticket(s): %s", len(lower), strings.Join(firstN(lower, 5), "; "))
	}
	return pass(name, fmt.Sprintf("%d tickets re-read via API", checked))
}

// checkReferencesViaAPI resolves a sample of issued references (now current or alias) back to their ticket.
func checkReferencesViaAPI(ctx context.Context, in InvariantInput) InvariantResult {
	const name = "references_resolve_to_their_ticket"
	s := staffSession(in.Env)
	if s == nil {
		return skip(name, "no staff session")
	}
	refs := in.Env.Trk.Refs()
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 100 {
		step := len(keys) / 100
		var sample []string
		for i := 0; i < len(keys) && len(sample) < 100; i += step {
			sample = append(sample, keys[i])
		}
		keys = sample
	}
	var wrong []string
	checked := 0
	for _, ref := range keys {
		var out struct {
			TicketID string `json:"ticketId"`
		}
		r := in.Env.Client.Call(ctx, &JobCtx{Stage: -2}, s, "verify.by_reference", http.MethodGet, "/tickets/by-reference?reference="+url.QueryEscape(ref), nil, &out)
		if r.Missing {
			return skip(name, "GET /tickets/by-reference does not exist in this API build")
		}
		if !r.OK() {
			wrong = append(wrong, fmt.Sprintf("%s -> status %d", ref, r.Status))
			continue
		}
		checked++
		if out.TicketID != refs[ref] {
			wrong = append(wrong, fmt.Sprintf("%s -> %s, expected %s", ref, out.TicketID, refs[ref]))
		}
	}
	if len(wrong) > 0 {
		return fail(name, "%d of %d: %s", len(wrong), len(keys), strings.Join(firstN(wrong, 5), "; "))
	}
	return pass(name, fmt.Sprintf("%d references resolved", checked))
}

// checkQueueCounts compares the sidebar/view counts with the list counts of the same query engine, per Queue.
func checkQueueCounts(ctx context.Context, in InvariantInput) []InvariantResult {
	const name = "queue_counts_equal_list_counts"
	s := staffSession(in.Env)
	if s == nil {
		return []InvariantResult{skip(name, "no staff session")}
	}
	var queues []QueueInfo
	for _, q := range in.Env.Queues {
		if q.Status == "active" {
			queues = append(queues, q)
		}
	}
	if len(queues) == 0 {
		return []InvariantResult{skip(name, "no Queues listed")}
	}
	if in.Settle > 0 {
		sleepCtx(ctx, in.Settle)
	}
	type target struct {
		id, view string
		filter   map[string]any
	}
	targets := []target{{"unassigned", "system:tickets:unassigned", and(cond("status", "in", openStatuses), cond("assignee", "is_empty", nil))}}
	for _, q := range queues {
		targets = append(targets, target{"queue " + q.Key, "queue:" + q.ID, and(cond("status", "in", openStatuses), cond("queue", "equals", q.ID))})
	}
	var mismatches []string
	compared, skipped := 0, 0
	for _, t := range targets {
		var vc struct {
			Items []struct {
				ID     string `json:"id"`
				Count  int    `json:"count"`
				Capped bool   `json:"capped"`
				Status string `json:"status"`
			} `json:"items"`
		}
		r := retry429(ctx, func() Result {
			return in.Env.Client.Call(ctx, &JobCtx{Stage: -2}, s, "verify.view_counts", http.MethodGet, "/views/counts?ids="+url.QueryEscape(t.view), nil, &vc)
		})
		if !r.OK() || len(vc.Items) != 1 || vc.Items[0].Status != "ok" || vc.Items[0].Capped {
			skipped++
			continue
		}
		var page struct {
			Count       *int `json:"count"`
			CountCapped bool `json:"countCapped"`
		}
		body := map[string]any{"filter": map[string]any{"v": 1, "root": t.filter}, "limit": 1, "count": true}
		r = retry429(ctx, func() Result {
			return in.Env.Client.Call(ctx, &JobCtx{Stage: -2}, s, "verify.query_count", http.MethodPost, "/tickets/query", body, &page)
		})
		if !r.OK() || page.Count == nil || page.CountCapped {
			skipped++
			continue
		}
		compared++
		if *page.Count != vc.Items[0].Count {
			mismatches = append(mismatches, fmt.Sprintf("%s: view count %d, list count %d", t.id, vc.Items[0].Count, *page.Count))
		}
		if in.PGURL != "" && strings.HasPrefix(t.view, "queue:") {
			if n, err := sqlQueueOpenCount(ctx, in.PGURL, strings.TrimPrefix(t.view, "queue:")); err == nil && n != *page.Count {
				mismatches = append(mismatches, fmt.Sprintf("%s: list count %d, SQL count %d", t.id, *page.Count, n))
			}
		}
	}
	switch {
	case len(mismatches) > 0:
		return []InvariantResult{fail(name, "%s", strings.Join(mismatches, "; "))}
	case compared == 0:
		return []InvariantResult{skip(name, fmt.Sprintf("no comparable counts (%d unavailable or capped at 1000)", skipped))}
	}
	return []InvariantResult{pass(name, fmt.Sprintf("%d counts compared, %d skipped (capped or unavailable)", compared, skipped))}
}

func retry429(ctx context.Context, f func() Result) Result {
	var r Result
	for i := 0; i < 6; i++ {
		r = f()
		if r.Status != http.StatusTooManyRequests && r.Status != http.StatusServiceUnavailable {
			return r
		}
		if !sleepCtx(ctx, 1500*time.Millisecond) {
			return r
		}
	}
	return r
}

func sqlQueueOpenCount(ctx context.Context, pgURL, queueID string) (int, error) {
	conn, err := pgx.Connect(ctx, pgURL)
	if err != nil {
		return 0, err
	}
	defer conn.Close(ctx)
	var n int
	err = conn.QueryRow(ctx, `SELECT count(*)::int FROM servicedesk.tickets WHERE queue_id = $1::uuid AND status IN ('new','open','in_progress','waiting')`, queueID).Scan(&n)
	return n, err
}

// checkSQL verifies the reference registry and the version bookkeeping straight in the database.
func checkSQL(ctx context.Context, in InvariantInput) []InvariantResult {
	conn, err := pgx.Connect(ctx, in.PGURL)
	if err != nil {
		return []InvariantResult{skip("sql_checks", "cannot connect: "+err.Error())}
	}
	defer conn.Close(ctx)
	var out []InvariantResult

	one := func(name, okMsg, sql string, args ...any) {
		var n int64
		if err := conn.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			out = append(out, skip(name, "query failed: "+err.Error()))
			return
		}
		if n == 0 {
			out = append(out, pass(name, okMsg))
		} else {
			out = append(out, fail(name, "%d violation(s)", n))
		}
	}
	one("sql_no_duplicate_references", "every ticket reference is unique",
		`SELECT count(*)::bigint FROM (SELECT reference FROM servicedesk.tickets GROUP BY reference HAVING count(*) > 1) d`)
	one("sql_no_duplicate_queue_numbers", "(queue, number) is unique",
		`SELECT count(*)::bigint FROM (SELECT queue_id, number FROM servicedesk.tickets GROUP BY queue_id, number HAVING count(*) > 1) d`)
	one("sql_registry_one_current_per_ticket", "every ticket has exactly one current registry row",
		`SELECT count(*)::bigint FROM servicedesk.tickets t WHERE (SELECT count(*) FROM servicedesk.reference_registry r WHERE r.ticket_id = t.id AND r.kind = 'current') <> 1`)
	one("sql_registry_current_equals_ticket_reference", "the current registry row carries the ticket's reference",
		`SELECT count(*)::bigint FROM servicedesk.tickets t JOIN servicedesk.reference_registry r ON r.ticket_id = t.id AND r.kind = 'current' WHERE r.reference <> t.reference`)
	one("sql_counter_ahead_of_issued_numbers", "no queue counter is at or below an issued number",
		`SELECT count(*)::bigint FROM servicedesk.tickets t JOIN servicedesk.queues q ON q.id = t.queue_id WHERE t.number >= q.next_number`)

	// Tagged tickets that the API acknowledged must exist; extra ones are creates whose answer was lost (timeouts).
	var inDB int64
	if err := conn.QueryRow(ctx, `SELECT count(*)::bigint FROM servicedesk.tickets WHERE strpos(title, $1) = 1`, ticketPrefix(in.Env.Cfg.Tag)).Scan(&inDB); err == nil {
		acked := int64(len(in.Env.Trk.TrackedIDs()))
		if inDB < acked {
			out = append(out, fail("sql_created_tickets_exist", "API acknowledged %d tickets, the database holds %d", acked, inDB))
		} else {
			out = append(out, pass("sql_created_tickets_exist", fmt.Sprintf("%d acknowledged, %d in the database (difference = creates whose answer timed out)", acked, inDB)))
		}
	}

	// Final versions.
	ids := in.Env.Trk.TrackedIDs()
	var lower, missing []string
	for start := 0; start < len(ids); start += 5000 {
		end := min(start+5000, len(ids))
		rows, err := conn.Query(ctx, `SELECT id::text, version FROM servicedesk.tickets WHERE id = ANY($1::uuid[])`, ids[start:end])
		if err != nil {
			out = append(out, skip("final_versions_not_below_observed", "query failed: "+err.Error()))
			return out
		}
		found := map[string]bool{}
		for rows.Next() {
			var id string
			var v int
			if err := rows.Scan(&id, &v); err != nil {
				break
			}
			found[id] = true
			if seen, ref := in.Env.Trk.MaxSeen(id); v < seen {
				lower = append(lower, fmt.Sprintf("%s final %d < observed %d", ref, v, seen))
			}
		}
		rows.Close()
		for _, id := range ids[start:end] {
			if !found[id] {
				_, ref := in.Env.Trk.MaxSeen(id)
				missing = append(missing, ref)
			}
		}
	}
	switch {
	case len(lower) > 0:
		out = append(out, fail("final_versions_not_below_observed", "%d ticket(s): %s", len(lower), strings.Join(firstN(lower, 5), "; ")))
	case len(missing) > 0:
		out = append(out, fail("final_versions_not_below_observed", "%d acknowledged ticket(s) missing: %s", len(missing), strings.Join(firstN(missing, 5), ", ")))
	default:
		out = append(out, pass("final_versions_not_below_observed", fmt.Sprintf("%d tickets compared in SQL", len(ids))))
	}
	return out
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
