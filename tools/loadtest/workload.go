package main

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Config holds the run parameters that the workload needs.
type Config struct {
	Tag         string
	MaxRetries  int     // retries after a version conflict (read again, then write again)
	HotTickets  int     // size of the hot set of newest tickets
	HotProb     float64 // probability that staff pick from the hot set
	MaxTickets  int     // cap of tickets created by employees
	SeedTickets int
}

// QueueInfo is a Ticket Queue as listed by the API.
type QueueInfo struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Prefix    string `json:"prefix"`
	Status    string `json:"status"`
	CanCreate bool   `json:"canCreate"`
	Level     string `json:"level"`
}

// Env is the shared state of a run.
type Env struct {
	Cfg      Config
	Client   *Client
	Pool     *Pool
	Trk      *Tracker
	Seen     *idRing
	Queues   []QueueInfo
	Sessions map[string][]*Session

	ticketSeq       atomic.Uint64
	created         atomic.Int64
	conflictRetries atomic.Int64
	conflictGiveups atomic.Int64
	staleSkips      atomic.Int64
	probes          atomic.Int64
	leaks           atomic.Int64
}

// OpDef is one operation in a persona class's mix.
type OpDef struct {
	Name   string
	Weight float64
	Fn     func(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand)
}

type ticketDTO struct {
	ID         string  `json:"id"`
	Reference  string  `json:"reference"`
	Status     string  `json:"status"`
	Priority   string  `json:"priority"`
	Version    int     `json:"version"`
	AssigneeID *string `json:"assigneeId"`
	QueueID    string  `json:"queueId"`
}

type queryPage struct {
	Items      []ticketDTO `json:"items"`
	NextCursor string      `json:"nextCursor"`
}

var searchWords = []string{"ORBIS", "WLAN", "Drucker", "Phishing", "Etikettendrucker", "Visitenwagen", "Passwort", "VPN", "Outlook", "Monitor"}

var openStatuses = []string{"new", "open", "in_progress", "waiting"}

// Ops returns the operation mix of each persona class.
func Ops() map[string][]OpDef {
	staffCommon := []OpDef{
		{"tickets.query", 30, opQuery},
		{"tickets.list_all", 8, opListAll},
		{"tickets.open", 18, opOpen},
		{"tickets.assign", 6, opAssign},
		{"tickets.comment", 10, opStaffComment},
		{"tickets.transition", 8, opTransition},
		{"tickets.priority", 2, opPriority},
		{"tickets.move_queue", 1, opMoveQueue},
		{"knowledge.search", 5, opKnowledge},
		{"mywork.items", 4, opMyWorkItems},
		{"mywork.counts", 4, opMyWorkCounts},
		{"views.counts", 4, opViewCounts},
	}
	return map[string][]OpDef{
		ClassEmployee: {
			{"tickets.list_own", 30, opListOwn},
			{"tickets.report", 9, opReport},
			{"tickets.open_own", 15, opOpenOwn},
			{"tickets.comment_own", 10, opCommentOwn},
			{"knowledge.search", 20, opKnowledge},
			{"catalog.list", 10, opCatalog},
			{"queues.for_create", 3, opQueuesForCreate},
			{"notifications.unread", 3, opUnread},
		},
		ClassFirstLevel: staffCommon,
		ClassTechnician: staffCommon,
		ClassLead: {
			{"briefing.feed", 15, opBriefing},
			{"sidebar", 25, opSidebar},
			{"views.counts", 25, opViewCounts},
			{"mywork.items", 8, opMyWorkItems},
			{"mywork.counts", 8, opMyWorkCounts},
			{"tickets.query", 10, opQuery},
			{"tickets.open", 5, opOpen},
			{"tickets.assign", 3, opAssign},
			{"tickets.comment", 2, opStaffComment},
		},
		ClassViewer: {
			{"tickets.query", 40, opQuery},
			{"tickets.open", 30, opOpen},
			{"tickets.list_all", 15, opListAll},
			{"mywork.counts", 10, opMyWorkCounts},
			{"tickets.write_denied", 5, opWriteDenied},
		},
		ClassAdmin: {
			{"users.list", 20, opUsers},
			{"audit.search", 40, opAudit},
			{"tickets.query", 10, opQuery},
			{"views.counts", 10, opViewCounts},
			{"sidebar", 10, opSidebar},
			{"briefing.feed", 10, opBriefing},
		},
		ClassVendor: {
			{"tickets.list_own", 30, opListOwn},
			{"mywork.items", 30, opMyWorkItems},
			{"tickets.probe_foreign", 20, opProbeForeign},
			{"knowledge.search", 10, opKnowledge},
			{"notifications.unread", 10, opUnread},
		},
	}
}

// ExpectsDenied lists operations whose 403/404 answers are the expected result.
var ExpectsDenied = map[string]bool{"tickets.probe_foreign": true, "tickets.write_denied": true, "tickets.abilities": true}

func esc(s string) string { return url.QueryEscape(s) }

func (e *Env) title(login string) string {
	return fmt.Sprintf("[lt:%s] %s #%d", e.Cfg.Tag, login, e.ticketSeq.Add(1))
}

func (e *Env) commentBody(rng *rand.Rand) string {
	return fmt.Sprintf("[lt:%s] load test comment %06d", e.Cfg.Tag, rng.Intn(1000000))
}

func cond(field, op string, value any) map[string]any {
	m := map[string]any{"type": "condition", "field": field, "op": op}
	if value != nil {
		m["value"] = value
	}
	return m
}

func and(children ...map[string]any) map[string]any {
	return map[string]any{"type": "group", "logic": "and", "children": children}
}

// ---- employee operations ----

func opListOwn(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	var page queryPage
	if r := e.Client.Call(ctx, jc, s, "tickets.list_own", http.MethodGet, "/tickets?limit=25", nil, &page); r.OK() {
		for _, t := range page.Items {
			e.Trk.Observe(t.ID, t.Version)
		}
	}
}

func opReport(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	if int(e.created.Load()) >= e.Cfg.MaxTickets {
		opListOwn(ctx, e, jc, s, rng)
		return
	}
	e.createTicket(ctx, jc, s, rng, "tickets.report")
}

func (e *Env) createTicket(ctx context.Context, jc *JobCtx, s *Session, rng *rand.Rand, op string) {
	body := map[string]any{
		"title":       e.title(s.P.Login),
		"description": fmt.Sprintf("[lt:%s] Simulated incident report from the load test. Station %d reports a problem (%d).", e.Cfg.Tag, rng.Intn(40), rng.Intn(100000)),
	}
	var t ticketDTO
	r := e.Client.Call(ctx, jc, s, op, http.MethodPost, "/tickets", body, &t)
	if !r.OK() || t.ID == "" {
		return
	}
	e.created.Add(1)
	e.Trk.Created(t.ID, t.Reference, t.Version)
	e.Pool.Add(&Entry{ID: t.ID, Ref: t.Reference, Reporter: s.P.Login})
}

func opOpenOwn(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	ent := e.Pool.PickOwn(rng, s.P.Login)
	if ent == nil {
		opListOwn(ctx, e, jc, s, rng)
		return
	}
	var t ticketDTO
	if r := e.Client.Call(ctx, jc, s, "tickets.open_own", http.MethodGet, "/tickets/"+ent.ID, nil, &t); r.OK() {
		e.Trk.Observe(t.ID, t.Version)
	}
}

func opCommentOwn(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	ent := e.Pool.PickOwn(rng, s.P.Login)
	if ent == nil {
		opListOwn(ctx, e, jc, s, rng)
		return
	}
	e.Client.Call(ctx, jc, s, "tickets.comment_own", http.MethodPost, "/tickets/"+ent.ID+"/comments", map[string]any{"body": e.commentBody(rng)}, nil)
}

func opKnowledge(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	e.Client.Call(ctx, jc, s, "knowledge.search", http.MethodGet, "/knowledge-articles?limit=10&q="+esc(searchWords[rng.Intn(len(searchWords))]), nil, nil)
}

func opCatalog(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	e.Client.Call(ctx, jc, s, "catalog.list", http.MethodGet, "/catalog-items?limit=20", nil, nil)
}

func opQueuesForCreate(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	e.Client.Call(ctx, jc, s, "queues.for_create", http.MethodGet, "/service-desk/queues?for=create", nil, nil)
}

func opUnread(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	e.Client.Call(ctx, jc, s, "notifications.unread", http.MethodGet, "/notifications/unread-count", nil, nil)
}

// ---- staff operations ----

func queryRoot(rng *rand.Rand, tag string) (root map[string]any, search string) {
	switch rng.Intn(6) {
	case 0:
		return and(cond("status", "in", openStatuses), cond("assignee", "is_empty", nil)), ""
	case 1:
		return and(cond("status", "equals", "new"), cond("priority", "in", []string{"high", "urgent"})), ""
	case 2:
		return cond("title", "contains", "[lt:"+tag+"]"), ""
	case 3:
		return nil, searchWords[rng.Intn(len(searchWords))]
	case 4:
		return cond("status", "equals", "waiting"), ""
	default:
		return and(cond("status", "in", openStatuses), cond("assignee", "is_me", nil)), ""
	}
}

func opQuery(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	root, search := queryRoot(rng, e.Cfg.Tag)
	filter := map[string]any{"v": 1}
	if root != nil {
		filter["root"] = root
	}
	body := map[string]any{
		"filter": filter,
		"sort":   []map[string]any{{"field": "created_at", "dir": "desc"}},
		"limit":  25,
		"count":  rng.Intn(2) == 0,
	}
	if search != "" {
		body["search"] = search
	}
	var page queryPage
	r := e.Client.Call(ctx, jc, s, "tickets.query", http.MethodPost, "/tickets/query", body, &page)
	if !r.OK() {
		return
	}
	for _, t := range page.Items {
		e.Seen.add(t.ID)
		e.Trk.Observe(t.ID, t.Version)
	}
	if page.NextCursor != "" && rng.Intn(4) == 0 {
		body["cursor"] = page.NextCursor
		delete(body, "count")
		e.Client.Call(ctx, jc, s, "tickets.query_next", http.MethodPost, "/tickets/query", body, nil)
	}
}

func opListAll(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	var page queryPage
	if r := e.Client.Call(ctx, jc, s, "tickets.list_all", http.MethodGet, "/tickets?scope=all&open=true&limit=25", nil, &page); r.OK() {
		for _, t := range page.Items {
			e.Seen.add(t.ID)
		}
	}
}

func opOpen(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	id := ""
	if rng.Intn(10) < 6 {
		if ent := e.Pool.Pick(rng, e.Cfg.HotTickets, e.Cfg.HotProb); ent != nil {
			id = ent.ID
		}
	}
	if id == "" {
		id = e.Seen.pick(rng)
	}
	if id == "" {
		opListAll(ctx, e, jc, s, rng)
		return
	}
	var t ticketDTO
	if r := e.Client.Call(ctx, jc, s, "tickets.open", http.MethodGet, "/tickets/"+id, nil, &t); r.OK() {
		e.Trk.Observe(t.ID, t.Version)
	}
}

func opStaffComment(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	ent := e.Pool.Pick(rng, e.Cfg.HotTickets, e.Cfg.HotProb)
	if ent == nil {
		return
	}
	op := "tickets.comment_public"
	internal := rng.Intn(10) < 4
	if internal {
		op = "tickets.comment_internal"
	}
	e.Client.Call(ctx, jc, s, op, http.MethodPost, "/tickets/"+ent.ID+"/comments", map[string]any{"body": e.commentBody(rng), "internal": internal}, nil)
}

// mutate is the read-modify-write loop of a real client: read the ticket, send a
// conditional write with the version that was read and, on a version conflict,
// read again and retry (up to MaxRetries). Conflicts are recorded as class
// conflict on the write operation, they are never hidden.
func (e *Env) mutate(ctx context.Context, jc *JobCtx, s *Session, rng *rand.Rand, build func(t *ticketDTO) (op, path string, body map[string]any, ok bool)) {
	ent := e.Pool.Pick(rng, e.Cfg.HotTickets, e.Cfg.HotProb)
	if ent == nil {
		return
	}
	for attempt := 0; ; attempt++ {
		var t ticketDTO
		r := e.Client.Call(ctx, jc, s, "ticket.read", http.MethodGet, "/tickets/"+ent.ID, nil, &t)
		if !r.OK() {
			return
		}
		e.Trk.Observe(t.ID, t.Version)
		if t.Status == "closed" || t.Status == "cancelled" {
			e.Pool.Remove(ent.ID)
			e.staleSkips.Add(1)
			return
		}
		op, path, body, ok := build(&t)
		if !ok {
			e.staleSkips.Add(1)
			return
		}
		body["expectedVersion"] = t.Version
		var out ticketDTO
		w := e.Client.Call(ctx, jc, s, op, http.MethodPost, path, body, &out)
		switch {
		case w.OK():
			e.Trk.Write(t.ID, t.Version, out.Version, op)
			if out.Status == "closed" || out.Status == "cancelled" {
				e.Pool.Remove(ent.ID)
			}
			return
		case w.Status == http.StatusConflict && w.Code == "tickets.version_conflict":
			if attempt >= e.Cfg.MaxRetries {
				e.conflictGiveups.Add(1)
				return
			}
			e.conflictRetries.Add(1)
		default:
			return
		}
	}
}

func opAssign(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	me := s.UserID()
	e.mutate(ctx, jc, s, rng, func(t *ticketDTO) (string, string, map[string]any, bool) {
		if t.AssigneeID != nil && *t.AssigneeID == me {
			return "", "", nil, false
		}
		return "tickets.assign", "/tickets/" + t.ID + "/assign", map[string]any{"assigneeId": me}, true
	})
}

func opPriority(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	prios := []string{"low", "normal", "high", "urgent"}
	e.mutate(ctx, jc, s, rng, func(t *ticketDTO) (string, string, map[string]any, bool) {
		p := prios[rng.Intn(len(prios))]
		if p == t.Priority {
			return "", "", nil, false
		}
		return "tickets.priority", "/tickets/" + t.ID + "/priority", map[string]any{"priority": p}, true
	})
}

func opTransition(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	e.mutate(ctx, jc, s, rng, func(t *ticketDTO) (string, string, map[string]any, bool) {
		base := "/tickets/" + t.ID + "/"
		switch t.Status {
		case "new", "open":
			return "tickets.start", base + "start", map[string]any{}, true
		case "in_progress":
			if rng.Intn(3) == 0 {
				return "tickets.wait", base + "wait", map[string]any{"reason": "hardware"}, true
			}
			return "tickets.resolve", base + "resolve", map[string]any{"reason": "Resolved by the load test"}, true
		case "waiting":
			return "tickets.resume", base + "resume", map[string]any{}, true
		case "resolved":
			if rng.Intn(10) == 0 {
				return "tickets.reopen", base + "reopen", map[string]any{"reason": "Reopened by the load test"}, true
			}
			return "tickets.close", base + "close", map[string]any{}, true
		}
		return "", "", nil, false
	})
}

func opMoveQueue(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	var targets []QueueInfo
	for _, q := range e.Queues {
		if q.Status == "active" && q.CanCreate {
			targets = append(targets, q)
		}
	}
	if len(targets) < 2 {
		return // a single Queue: nothing to move to
	}
	e.mutate(ctx, jc, s, rng, func(t *ticketDTO) (string, string, map[string]any, bool) {
		if t.Status == "resolved" {
			return "", "", nil, false
		}
		q := targets[rng.Intn(len(targets))]
		if q.ID == t.QueueID {
			return "", "", nil, false
		}
		return "tickets.move_queue", "/tickets/" + t.ID + "/move-queue", map[string]any{"targetQueueId": q.ID, "reasonCode": "other"}, true
	})
}

func opMyWorkItems(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	e.Client.Call(ctx, jc, s, "mywork.items", http.MethodGet, "/my-work/items?limit=50", nil, nil)
}

func opMyWorkCounts(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	e.Client.Call(ctx, jc, s, "mywork.counts", http.MethodGet, "/my-work/counts", nil, nil)
}

func opViewCounts(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	e.Client.Call(ctx, jc, s, "views.counts", http.MethodGet, "/views/counts?ids="+esc(viewIDs(e.Queues)), nil, nil)
}

func viewIDs(queues []QueueInfo) string {
	ids := []string{"system:tickets:unassigned", "system:tickets:my-open"}
	for _, q := range queues {
		if q.Status == "active" && len(ids) < 30 {
			ids = append(ids, "queue:"+q.ID)
		}
	}
	return strings.Join(ids, ",")
}

// ---- lead, admin, viewer, vendor operations ----

func opBriefing(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	e.Client.Call(ctx, jc, s, "briefing.feed", http.MethodGet, "/briefing/feed", nil, nil)
}

func opSidebar(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	e.Client.Call(ctx, jc, s, "sidebar", http.MethodGet, "/me/sidebar", nil, nil)
}

func opUsers(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	e.Client.Call(ctx, jc, s, "users.list", http.MethodGet, "/users?limit=50", nil, nil)
}

func opAudit(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	from := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	prefixes := []string{"servicedesk.", "authorization.", "auth.", "demo."}
	e.Client.Call(ctx, jc, s, "audit.search", http.MethodGet, "/audit-events?limit=50&actionPrefix="+esc(prefixes[rng.Intn(len(prefixes))])+"&from="+esc(from), nil, nil)
}

// ticketAbilities is the part of the ticket detail the leak probe needs: what the caller may do with the ticket.
type ticketAbilities struct {
	Abilities *struct {
		Comment         bool `json:"comment"`
		InternalComment bool `json:"internalComment"`
	} `json:"abilities"`
}

// probeWrites decides which comment probes must be denied for a caller, from the abilities the API reports for
// the ticket (queue grants and permissions): a person who works the ticket's Queue (the Infrastruktur team in its
// own Queue) legitimately comments and is not probed for the public comment. Without abilities (an older server)
// both kinds of comment must be denied.
func probeWrites(a *ticketAbilities) (public, internal bool) {
	if a == nil || a.Abilities == nil {
		return true, true
	}
	return !a.Abilities.Comment, !a.Abilities.InternalComment
}

// opWriteDenied tries writes that the ticket's abilities say the persona must not be allowed to do. It reads the
// ticket first: a ticket the persona may not see is a denied read and needs no write probe.
func opWriteDenied(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	ent := e.Pool.Pick(rng, e.Cfg.HotTickets, e.Cfg.HotProb)
	if ent == nil {
		return
	}
	var detail ticketAbilities
	if r := e.Client.Call(ctx, jc, s, "tickets.abilities", http.MethodGet, "/tickets/"+ent.ID, nil, &detail); !r.OK() {
		return
	}
	public, internal := probeWrites(&detail)
	if public {
		e.probes.Add(1)
		r := e.Client.Call(ctx, jc, s, "tickets.write_denied", http.MethodPost, "/tickets/"+ent.ID+"/comments", map[string]any{"body": e.commentBody(rng)}, nil)
		if r.OK() {
			e.leaks.Add(1)
			e.Trk.Violate("authorization_leak", "%s (abilities.comment=false) added a comment to ticket %s", s.P.Login, ent.Ref)
		}
	}
	if internal && !public {
		e.probes.Add(1)
		r := e.Client.Call(ctx, jc, s, "tickets.write_denied", http.MethodPost, "/tickets/"+ent.ID+"/comments", map[string]any{"body": e.commentBody(rng), "internal": true}, nil)
		if r.OK() {
			e.leaks.Add(1)
			e.Trk.Violate("authorization_leak", "%s (abilities.internalComment=false) added an internal comment to ticket %s", s.P.Login, ent.Ref)
		}
	}
}

// opProbeForeign reads a ticket of somebody else. 403/404 is the expected answer.
func opProbeForeign(ctx context.Context, e *Env, jc *JobCtx, s *Session, rng *rand.Rand) {
	ent := e.Pool.PickForeign(rng, s.P.Login)
	if ent == nil {
		return
	}
	e.probes.Add(1)
	r := e.Client.Call(ctx, jc, s, "tickets.probe_foreign", http.MethodGet, "/tickets/"+ent.ID, nil, nil)
	if r.OK() {
		e.leaks.Add(1)
		e.Trk.Violate("authorization_leak", "%s read ticket %s reported by %s", s.P.Login, ent.Ref, ent.Reporter)
	}
}
