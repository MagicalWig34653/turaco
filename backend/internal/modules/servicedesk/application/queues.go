package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Ticket Queues (ADR-0033, docs/product/f13-workbench-views-design.md): a Queue is a desk (IT, HR, Facility) with
// its own key, prefix and committed number counter. Access is explicit per Queue: grants to Users, Teams and roles
// with the levels create, view, work and manage. tickets.view and tickets.manage keep acting as global view and
// work grants in every Queue, so installations upgrade without a change of behavior.

// PermQueuesManage administers Queues (create, rename, archive, grants) and may move Tickets into any Queue.
const PermQueuesManage = "servicedesk.queues.manage"

// Queue grant levels. view, work and manage are cumulative; every level also allows raising Tickets into the
// Queue, create only that.
const (
	LevelCreate = "create"
	LevelView   = "view"
	LevelWork   = "work"
	LevelManage = "manage"
)

// QueueLevels lists the grant levels in ascending order.
var QueueLevels = []string{LevelCreate, LevelView, LevelWork, LevelManage}

// Queue visibility, routing mode and status values.
const (
	QueueInternal = "internal"
	QueuePublic   = "public"

	RoutingEmployeeChoice = "employee_choice"
	RoutingAutomatic      = "automatic"
	RoutingBoth           = "both"

	QueueActive   = "active"
	QueueArchived = "archived"
)

// MoveReasons are the reason codes of a Ticket move.
var MoveReasons = []string{"misrouted", "different_skill", "reorganization", "other"}

const (
	levelNone = iota
	levelCreate
	levelView
	levelWork
	levelManage
)

func levelRank(l string) int {
	switch l {
	case LevelCreate:
		return levelCreate
	case LevelView:
		return levelView
	case LevelWork:
		return levelWork
	case LevelManage:
		return levelManage
	}
	return levelNone
}

// MaxQueues and MaxQueueGrants bound the administration data.
const (
	MaxQueues      = 100
	MaxQueueGrants = 200
)

var (
	queueKeyPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	queuePrefixPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,7}$`)
	uuidPattern        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	referencePattern   = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,7}-[0-9]{1,18}$`)
)

// Queue is a desk. NextNumber, the counter, is deliberately not part of it: no API returns the volume of a desk.
type Queue struct {
	ID              string
	Key             string
	Prefix          string
	Name            string
	Description     string
	PublicLabel     string
	Status          string
	Visibility      string
	RoutingMode     string
	DefaultPriority string
	DefaultTeamID   *string
	DefaultIntake   bool
	NumberPadding   int
	Version         int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ArchivedAt      *time.Time
}

// Label is the neutral desk label shown to someone who may not see the Queue itself.
func (q Queue) Label() string {
	if q.PublicLabel != "" {
		return q.PublicLabel
	}
	return q.Name
}

// QueueRef is the part of a Queue a Ticket carries when the caller may know the Queue.
type QueueRef struct {
	ID     string
	Key    string
	Prefix string
	Name   string
}

// QueueGrant gives a User, Team or role a level in a Queue.
type QueueGrant struct {
	SubjectType string
	SubjectID   string
	Level       string
	GrantedBy   string
	GrantedAt   time.Time
}

// QueueAccessRow is a Queue with the highest level the caller holds in it through grants (0 none, 1 create,
// 2 view, 3 work, 4 manage).
type QueueAccessRow struct {
	Queue Queue
	Level int
}

// RefEntry is one issued display number of a Ticket.
type RefEntry struct {
	Reference string
	QueueID   string
	Kind      string
	IssuedAt  time.Time
}

// QueueStore is the persistence port of Queues, grants, moves and the reference registry. The repository
// implements it next to Store; a store without it supports only the default Queue.
type QueueStore interface {
	// QueueAccess returns every Queue with the caller's grant level in it.
	QueueAccess(ctx context.Context, userID string, teamIDs, roleIDs []string) ([]QueueAccessRow, error)
	QueueAccessTx(ctx context.Context, tx pgx.Tx, userID string, teamIDs, roleIDs []string) ([]QueueAccessRow, error)
	GetQueue(ctx context.Context, id string) (Queue, error)
	QueueByKey(ctx context.Context, key string) (Queue, error)
	DefaultQueue(ctx context.Context) (Queue, error)
	ListQueues(ctx context.Context) ([]Queue, error)
	// LockQueuesTx locks the Queues in id order and returns those that exist.
	LockQueuesTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]Queue, error)
	InsertQueueTx(ctx context.Context, tx pgx.Tx, q Queue) (Queue, error)
	// UpdateQueueTx writes the mutable fields and status, bumps the version and clears other defaults when q is
	// the intake Queue.
	UpdateQueueTx(ctx context.Context, tx pgx.Tx, q Queue) (Queue, error)
	CountOpenTicketsTx(ctx context.Context, tx pgx.Tx, queueID string) (int, error)
	Grants(ctx context.Context, queueID string) ([]QueueGrant, error)
	ReplaceGrantsTx(ctx context.Context, tx pgx.Tx, queueID string, grants []QueueGrant) error
	// MoveTx issues the next number of t.QueueID and stores Queue, number, reference, routing Team, assignee and
	// status of t. The Ticket row must be locked by the caller.
	MoveTx(ctx context.Context, tx pgx.Tx, t Ticket) (Ticket, error)
	// References returns the issued numbers of the Tickets, oldest first.
	References(ctx context.Context, ticketIDs []string) (map[string][]RefEntry, error)
	// ResolveReference finds the Ticket of a current or alias reference.
	ResolveReference(ctx context.Context, reference string) (ticketID string, alias bool, found bool, err error)
}

// Memberships answers the questions about a User that Queue grants and global permissions need. The composition
// root implements it with Organization and the permission evaluator.
type Memberships interface {
	TeamIDs(ctx context.Context, userID string) ([]string, error)
	RoleIDs(ctx context.Context, userID string) ([]string, error)
	// Permissions returns the effective permissions of any User (to check that an assignee can still see the Ticket).
	Permissions(ctx context.Context, userID string) (map[string]struct{}, error)
}

// WithMemberships sets the membership and permission source for Queue grants.
func (s *Service) WithMemberships(m Memberships) *Service {
	s.members = m
	return s
}

// ---- access ----

// access is what one caller may do with Queues, resolved once per operation. The global flags come from the
// tickets.view and tickets.manage permissions and apply to every Queue, including ones created later.
type access struct {
	allView bool
	allWork bool
	queues  map[string]QueueAccessRow
}

func (a access) canView(qid string) bool {
	return a.allView || a.allWork || a.queues[qid].Level >= levelView
}

func (a access) canWork(qid string) bool { return a.allWork || a.queues[qid].Level >= levelWork }

func (a access) canCreate(qid string) bool {
	r, ok := a.queues[qid]
	if !ok || r.Queue.Status != QueueActive {
		return false
	}
	return a.allView || a.allWork || r.Level >= levelCreate || r.Queue.Visibility == QueuePublic
}

// disclosed reports whether the caller may know which Queue a Ticket is in: they can view it, hold any grant in
// it, or it is public.
func (a access) disclosed(qid string) bool {
	if a.allView || a.allWork {
		return true
	}
	r, ok := a.queues[qid]
	return ok && (r.Level >= levelCreate || r.Queue.Visibility == QueuePublic)
}

// disclosedIDs lists the Queues whose numbers the caller may know (sorted).
func (a access) disclosedIDs() []string {
	out := []string{}
	for id := range a.queues {
		if a.disclosed(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (a access) global() bool { return a.allView || a.allWork }

// viewIDs lists the Queues the caller can view through grants (sorted), or every Queue for a global holder.
func (a access) viewIDs() []string {
	out := []string{}
	for id, r := range a.queues {
		if a.canView(id) && (a.global() || r.Level >= levelView) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// anyView reports whether the caller sees tickets beyond their own.
func (a access) anyView() bool {
	if a.global() {
		return true
	}
	for _, r := range a.queues {
		if r.Level >= levelView {
			return true
		}
	}
	return false
}

// eff is the caller's authority over a Ticket of the Queue: View reads it and its internal comments, Manage works it.
func (a access) eff(p Principal, qid string) Principal {
	p.View, p.Manage = a.canView(qid), a.canWork(qid)
	return p
}

type memberships struct{ teams, roles []string }

func (s *Service) memberships(ctx context.Context, userID string) (memberships, error) {
	if s.members == nil || s.queues == nil {
		return memberships{}, nil
	}
	teams, err := s.members.TeamIDs(ctx, userID)
	if err != nil {
		return memberships{}, fmt.Errorf("load teams of caller: %w", err)
	}
	roles, err := s.members.RoleIDs(ctx, userID)
	if err != nil {
		return memberships{}, fmt.Errorf("load roles of caller: %w", err)
	}
	return memberships{teams: teams, roles: roles}, nil
}

func (s *Service) buildAccess(p Principal, rows []QueueAccessRow) access {
	a := access{allView: p.View || p.Manage, allWork: p.Manage, queues: make(map[string]QueueAccessRow, len(rows))}
	for _, r := range rows {
		a.queues[r.Queue.ID] = r
	}
	return a
}

// resolve loads the caller's access outside a transaction.
func (s *Service) resolve(ctx context.Context, p Principal) (access, error) {
	if s.queues == nil {
		return s.buildAccess(p, nil), nil
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return access{}, err
	}
	rows, err := s.queues.QueueAccess(ctx, p.UserID, m.teams, m.roles)
	if err != nil {
		return access{}, fmt.Errorf("load queue access: %w", err)
	}
	return s.buildAccess(p, rows), nil
}

// resolveTx loads the caller's access inside the transaction that decides the operation, so the grants and the
// Ticket are authorized against one snapshot.
func (s *Service) resolveTx(ctx context.Context, tx pgx.Tx, p Principal, m memberships) (access, error) {
	if s.queues == nil {
		return s.buildAccess(p, nil), nil
	}
	rows, err := s.queues.QueueAccessTx(ctx, tx, p.UserID, m.teams, m.roles)
	if err != nil {
		return access{}, fmt.Errorf("load queue access: %w", err)
	}
	return s.buildAccess(p, rows), nil
}

// ---- disclosure ----

// shape applies the per-row disclosure rules to Tickets about to leave the service: routing is visible only to
// callers who can view the Ticket's Queue; the Queue and its number only to callers who may know the Queue.
// For everybody else the Queue is replaced by the neutral desk label and the reference by the newest number the
// caller may know (an earlier alias from a Queue they know), so an internal Queue's prefix does not leak.
func (s *Service) shape(ctx context.Context, a access, ts ...*Ticket) error {
	var hidden []*Ticket
	for _, t := range ts {
		if t == nil {
			continue
		}
		if !a.canView(t.QueueID) {
			t.QueueTeamID = nil
			t.DuplicateOfID = nil // the other ticket may be in a Queue this caller does not know
		}
		r, known := a.queues[t.QueueID]
		if a.disclosed(t.QueueID) {
			if known {
				t.Queue = &QueueRef{ID: r.Queue.ID, Key: r.Queue.Key, Prefix: r.Queue.Prefix, Name: r.Queue.Name}
			}
			continue
		}
		t.Queue = nil
		// Staff-only fields of the ticket catalog (assignee, location, patient impact) are routing detail of a
		// Queue the caller may not know, so they are withheld like the Queue itself.
		t.AssigneeID, t.AffectedLocationID, t.PatientImpact = nil, nil, false
		if known {
			t.QueueLabel = r.Queue.Label()
		}
		hidden = append(hidden, t)
	}
	if len(hidden) == 0 || s.queues == nil {
		for _, t := range hidden {
			t.QueueID, t.Number = "", 0
		}
		return nil
	}
	ids := make([]string, len(hidden))
	for i, t := range hidden {
		ids[i] = t.ID
	}
	refs, err := s.queues.References(ctx, ids)
	if err != nil {
		return fmt.Errorf("load references: %w", err)
	}
	for _, t := range hidden {
		entries := refs[t.ID]
		chosen := ""
		for i := len(entries) - 1; i >= 0; i-- {
			if a.disclosed(entries[i].QueueID) {
				chosen = entries[i].Reference
				break
			}
		}
		if chosen == "" && len(entries) > 0 {
			// Nothing the caller may know: the number the Ticket was raised with.
			chosen = entries[0].Reference
		}
		if chosen != "" {
			t.Reference = chosen
		}
		t.QueueID, t.Number = "", 0
	}
	return nil
}

// principalOf builds the Principal of a User from their global permissions (none without a membership source).
func (s *Service) principalOf(ctx context.Context, userID string) (Principal, error) {
	p := Principal{UserID: userID}
	if s.members == nil {
		return p, nil
	}
	perms, err := s.members.Permissions(ctx, userID)
	if err != nil {
		return Principal{}, fmt.Errorf("load permissions: %w", err)
	}
	_, p.View = perms[permTicketsView]
	_, p.Manage = perms[permTicketsManage]
	return p, nil
}

// TicketAccess is what Problems and Major Incidents ask of the Ticket authorization (the Service implements it):
// they list and link Tickets of every Queue, so each Ticket is checked against the User's Queue access.
type TicketAccess interface {
	// CanViewTicket reports whether the User may view the Ticket through tickets.view, tickets.manage or a view
	// grant in its Queue. An unknown Ticket is false, so callers cannot tell it from one they may not see.
	CanViewTicket(ctx context.Context, userID, ticketID string) (bool, error)
	// VisibleTickets keeps the Tickets the User may view (or reported or is affected by) and applies the
	// per-row disclosure of Queue and number.
	VisibleTickets(ctx context.Context, userID string, ts []Ticket) ([]Ticket, error)
	// ViewScope returns which Tickets the User may view, so counts on Problems and Major Incidents can be computed
	// from the same rule as the lists (no count may reveal Tickets the list does not show).
	ViewScope(ctx context.Context, userID string) (TicketScope, error)
}

// TicketScope is the set of Tickets one User may view: every Ticket (All), the Tickets of the listed Queues, and
// the Tickets they reported or are affected by.
type TicketScope struct {
	UserID   string
	All      bool
	QueueIDs []string
}

// ViewScope implements TicketAccess.
func (s *Service) ViewScope(ctx context.Context, userID string) (TicketScope, error) {
	if userID == "" {
		return TicketScope{}, nil
	}
	p, err := s.principalOf(ctx, userID)
	if err != nil {
		return TicketScope{}, err
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return TicketScope{}, err
	}
	return TicketScope{UserID: userID, All: a.global(), QueueIDs: a.viewIDs()}, nil
}

// CanViewTicket implements TicketAccess.
func (s *Service) CanViewTicket(ctx context.Context, userID, ticketID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	t, err := s.store.Get(ctx, ticketID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	p, err := s.principalOf(ctx, userID)
	if err != nil {
		return false, err
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return false, err
	}
	return a.canView(t.QueueID), nil
}

// VisibleTickets implements TicketAccess.
func (s *Service) VisibleTickets(ctx context.Context, userID string, ts []Ticket) ([]Ticket, error) {
	p, err := s.principalOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]Ticket, 0, len(ts))
	for _, t := range ts {
		if userID != "" && (a.canView(t.QueueID) || s.isOwner(t, p)) {
			out = append(out, t)
		}
	}
	return out, s.shape(ctx, a, ptrs(out)...)
}

// ReferenceFor returns the display number of the Ticket that the User may know, as shape would show it to them
// (the newest number from a Queue they may know, the oldest otherwise). It is for text that leaves the API for a
// person other than the caller (notifications). Without a membership source (tests) the current number is kept.
func (s *Service) ReferenceFor(ctx context.Context, userID string, t Ticket) (string, error) {
	if s.queues == nil || s.members == nil {
		return t.Reference, nil
	}
	perms, err := s.members.Permissions(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("load permissions of recipient: %w", err)
	}
	_, view := perms[permTicketsView]
	_, manage := perms[permTicketsManage]
	a, err := s.resolve(ctx, Principal{UserID: userID, View: view, Manage: manage})
	if err != nil {
		return "", err
	}
	if err := s.shape(ctx, a, &t); err != nil {
		return "", err
	}
	return t.Reference, nil
}

// aliasesFor lists the earlier references of a Ticket from Queues the caller may know, newest first.
func (s *Service) aliasesFor(ctx context.Context, a access, t Ticket) ([]string, error) {
	if s.queues == nil {
		return nil, nil
	}
	refs, err := s.queues.References(ctx, []string{t.ID})
	if err != nil {
		return nil, fmt.Errorf("load references: %w", err)
	}
	var out []string
	entries := refs[t.ID]
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == "alias" && a.disclosed(entries[i].QueueID) {
			out = append(out, entries[i].Reference)
		}
	}
	return out, nil
}

// ---- Queue reads ----

// QueueView is a Queue as one caller sees it.
type QueueView struct {
	Queue Queue
	// Level is the caller's effective level: none, create, view, work or manage.
	Level     string
	CanCreate bool
	// Grants is set only for callers with servicedesk.queues.manage.
	Grants []QueueGrant
}

func levelName(l int) string {
	switch l {
	case levelCreate:
		return LevelCreate
	case levelView:
		return LevelView
	case levelWork:
		return LevelWork
	case levelManage:
		return LevelManage
	}
	return ""
}

func (a access) levelOf(qid string) int {
	l := a.queues[qid].Level
	if a.allWork {
		l = max(l, levelWork)
	}
	if a.allView {
		l = max(l, levelView)
	}
	return l
}

func (s *Service) view(a access, p Principal, r QueueAccessRow) QueueView {
	q := r.Queue
	v := QueueView{Queue: q, Level: levelName(a.levelOf(q.ID)), CanCreate: a.canCreate(q.ID)}
	if !p.QueuesManage && a.levelOf(q.ID) < levelWork {
		// Routing internals are for the people who run the desk.
		v.Queue.RoutingMode, v.Queue.DefaultTeamID, v.Queue.DefaultPriority = "", nil, ""
	}
	return v
}

// ListQueues returns the Queues the caller may know. With forCreate it lists the Queues to raise a Ticket into:
// employees see public Queues that allow employee choice (and Queues they hold a grant in); people who work a
// Queue also see automatically routed ones.
func (s *Service) ListQueues(ctx context.Context, p Principal, forCreate bool) ([]QueueView, error) {
	if p.UserID == "" {
		return nil, ErrForbidden
	}
	if s.queues == nil {
		return []QueueView{}, nil
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	out := []QueueView{}
	for _, r := range a.queues {
		q := r.Queue
		switch {
		case forCreate:
			if !a.canCreate(q.ID) {
				continue
			}
			if q.RoutingMode == RoutingAutomatic && !a.canView(q.ID) {
				continue
			}
		case p.QueuesManage:
		case q.Status == QueueActive && a.canView(q.ID):
		default:
			continue
		}
		out = append(out, s.view(a, p, r))
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i].Queue, out[j].Queue
		if x.DefaultIntake != y.DefaultIntake {
			return x.DefaultIntake
		}
		if c := strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)); c != 0 {
			return c < 0
		}
		return x.ID < y.ID
	})
	return out, nil
}

// GetQueue returns one Queue the caller may know; everything else is ErrQueueNotFound.
func (s *Service) GetQueue(ctx context.Context, p Principal, id string) (QueueView, error) {
	if p.UserID == "" {
		return QueueView{}, ErrForbidden
	}
	if s.queues == nil || !uuidPattern.MatchString(id) {
		return QueueView{}, ErrQueueNotFound
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return QueueView{}, err
	}
	r, ok := a.queues[strings.ToLower(id)]
	if !ok || !(p.QueuesManage || (r.Queue.Status == QueueActive && a.disclosed(r.Queue.ID))) {
		return QueueView{}, ErrQueueNotFound
	}
	v := s.view(a, p, r)
	if p.QueuesManage {
		v.Grants, err = s.queues.Grants(ctx, r.Queue.ID)
		if err != nil {
			return QueueView{}, fmt.Errorf("load grants: %w", err)
		}
	}
	return v, nil
}

// ---- Queue administration ----

// QueueInput creates a Queue.
type QueueInput struct {
	Key             string
	Prefix          string
	Name            string
	Description     string
	PublicLabel     string
	Visibility      string
	RoutingMode     string
	DefaultPriority string
	DefaultTeamID   *string
	NumberPadding   int
}

func validVisibility(v string) bool { return v == QueueInternal || v == QueuePublic }
func validRouting(v string) bool {
	return slices.Contains([]string{RoutingEmployeeChoice, RoutingAutomatic, RoutingBoth}, v)
}
func (s *Service) needQueues() error {
	if s.queues == nil {
		return fmt.Errorf("servicedesk: store does not support queues")
	}
	return nil
}

func requireQueuesManage(c Caller, p Principal) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !p.QueuesManage {
		return ErrForbidden
	}
	return nil
}

func queueState(q Queue) any {
	return map[string]any{"status": q.Status, "visibility": q.Visibility, "routingMode": q.RoutingMode, "defaultPriority": q.DefaultPriority,
		"defaultTeamId": q.DefaultTeamID, "defaultIntake": q.DefaultIntake, "version": q.Version}
}

func (s *Service) checkTeam(ctx context.Context, id *string) error {
	if id == nil || *id == "" {
		return nil
	}
	if !uuidPattern.MatchString(*id) {
		return ErrTeamInvalid
	}
	ok, err := s.dir.ActiveTeams(ctx, []string{*id})
	if err != nil {
		return fmt.Errorf("check team: %w", err)
	}
	if !ok[*id] {
		return ErrTeamInvalid
	}
	return nil
}

// CreateQueue creates a Queue. Key and prefix are permanent; the number counter starts at 1.
func (s *Service) CreateQueue(ctx context.Context, c Caller, p Principal, in QueueInput) (Queue, error) {
	if err := requireQueuesManage(c, p); err != nil {
		return Queue{}, err
	}
	if err := s.needQueues(); err != nil {
		return Queue{}, err
	}
	q := Queue{Key: strings.TrimSpace(in.Key), Prefix: strings.TrimSpace(in.Prefix), Status: QueueActive,
		Visibility: in.Visibility, RoutingMode: in.RoutingMode, DefaultPriority: in.DefaultPriority, NumberPadding: in.NumberPadding}
	if !queueKeyPattern.MatchString(q.Key) {
		return Queue{}, invalid("key must start with a lower-case letter and contain 2 to 31 lower-case letters, digits or hyphens")
	}
	if !queuePrefixPattern.MatchString(q.Prefix) {
		return Queue{}, invalid("prefix must start with a capital letter and contain 2 to 8 capital letters or digits")
	}
	var err error
	if q.Name, err = cleanText(in.Name, 80, true, "name"); err != nil {
		return Queue{}, err
	}
	if q.Description, err = cleanText(in.Description, 500, false, "description"); err != nil {
		return Queue{}, err
	}
	if q.PublicLabel, err = cleanText(in.PublicLabel, 80, false, "public label"); err != nil {
		return Queue{}, err
	}
	if q.Visibility == "" {
		q.Visibility = QueueInternal
	}
	if q.RoutingMode == "" {
		q.RoutingMode = RoutingEmployeeChoice
	}
	if q.DefaultPriority == "" {
		q.DefaultPriority = "normal"
	}
	if q.NumberPadding == 0 {
		q.NumberPadding = 4
	}
	if !validVisibility(q.Visibility) || !validRouting(q.RoutingMode) || !slices.Contains(Priorities, q.DefaultPriority) || q.NumberPadding < 1 || q.NumberPadding > 9 {
		return Queue{}, invalid("visibility, routing mode, default priority or number padding is not valid")
	}
	if in.DefaultTeamID != nil && *in.DefaultTeamID != "" {
		if err := s.checkTeam(ctx, in.DefaultTeamID); err != nil {
			return Queue{}, err
		}
		q.DefaultTeamID = in.DefaultTeamID
	}
	var out Queue
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		all, err := s.queues.ListQueues(ctx)
		if err != nil {
			return err
		}
		if len(all) >= MaxQueues {
			return invalid("at most %d queues can exist", MaxQueues)
		}
		out, err = s.queues.InsertQueueTx(ctx, tx, q)
		if err != nil {
			return err
		}
		return auditQueue(ctx, tx, c, "servicedesk.queue.created", nil, &out, map[string]any{"key": out.Key, "prefix": out.Prefix})
	})
	return out, err
}

func auditQueue(ctx context.Context, tx pgx.Tx, c Caller, action string, before, after *Queue, meta map[string]any) error {
	ch := audit.Change{Action: action, TargetType: "ticket_queue", Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: meta}
	if before != nil {
		ch.TargetID, ch.Before = before.ID, queueState(*before)
	}
	if after != nil {
		ch.TargetID, ch.After = after.ID, queueState(*after)
	}
	return audit.Record(ctx, tx, ch)
}

// QueueUpdate changes the mutable properties of a Queue; nil leaves a field, an empty string clears an optional one.
type QueueUpdate struct {
	Name            *string
	Description     *string
	PublicLabel     *string
	Visibility      *string
	RoutingMode     *string
	DefaultPriority *string
	DefaultTeamID   *string
}

func needVersion(expected *int) error {
	if expected == nil {
		return invalid("expectedVersion is required")
	}
	return nil
}

// lockQueue locks a Queue and checks the expected version.
func (s *Service) lockQueue(ctx context.Context, tx pgx.Tx, id string, expected int) (Queue, error) {
	if !uuidPattern.MatchString(id) {
		return Queue{}, ErrQueueNotFound
	}
	qs, err := s.queues.LockQueuesTx(ctx, tx, []string{strings.ToLower(id)})
	if err != nil {
		return Queue{}, err
	}
	q, ok := qs[strings.ToLower(id)]
	if !ok {
		return Queue{}, ErrQueueNotFound
	}
	if q.Version != expected {
		return Queue{}, ErrVersionConflict
	}
	return q, nil
}

// UpdateQueue changes name, description, public label, visibility, routing mode, default priority or default Team.
func (s *Service) UpdateQueue(ctx context.Context, c Caller, p Principal, id string, expected *int, in QueueUpdate) (Queue, error) {
	if err := requireQueuesManage(c, p); err != nil {
		return Queue{}, err
	}
	if err := s.needQueues(); err != nil {
		return Queue{}, err
	}
	if err := needVersion(expected); err != nil {
		return Queue{}, err
	}
	var changed []string
	set := func(name string, v *string, limit int, required bool, dst *string) error {
		if v == nil {
			return nil
		}
		t, err := cleanText(*v, limit, required, name)
		if err != nil {
			return err
		}
		*dst = t
		changed = append(changed, name)
		return nil
	}
	var name, desc, label string
	if err := set("name", in.Name, 80, true, &name); err != nil {
		return Queue{}, err
	}
	if err := set("description", in.Description, 500, false, &desc); err != nil {
		return Queue{}, err
	}
	if err := set("public label", in.PublicLabel, 80, false, &label); err != nil {
		return Queue{}, err
	}
	if in.Visibility != nil && !validVisibility(*in.Visibility) || in.RoutingMode != nil && !validRouting(*in.RoutingMode) ||
		in.DefaultPriority != nil && !slices.Contains(Priorities, *in.DefaultPriority) {
		return Queue{}, invalid("visibility, routing mode or default priority is not valid")
	}
	if err := s.checkTeam(ctx, in.DefaultTeamID); err != nil {
		return Queue{}, err
	}
	var out Queue
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockQueue(ctx, tx, id, *expected)
		if err != nil {
			return err
		}
		if cur.Status == QueueArchived {
			return ErrQueueArchived
		}
		next := cur
		if in.Name != nil {
			next.Name = name
		}
		if in.Description != nil {
			next.Description = desc
		}
		if in.PublicLabel != nil {
			next.PublicLabel = label
		}
		if in.Visibility != nil {
			next.Visibility = *in.Visibility
			changed = append(changed, "visibility")
		}
		if in.RoutingMode != nil {
			next.RoutingMode = *in.RoutingMode
			changed = append(changed, "routing mode")
		}
		if in.DefaultPriority != nil {
			next.DefaultPriority = *in.DefaultPriority
			changed = append(changed, "default priority")
		}
		if in.DefaultTeamID != nil {
			next.DefaultTeamID = strPtr(*in.DefaultTeamID)
			changed = append(changed, "default team")
		}
		if next.DefaultIntake && next.Visibility != QueuePublic {
			return invalid("the intake queue must stay public, otherwise employees cannot raise tickets")
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		out, err = s.queues.UpdateQueueTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return auditQueue(ctx, tx, c, "servicedesk.queue.updated", &cur, &out, map[string]any{"changed": changed})
	})
	return out, err
}

// ArchiveQueue archives a Queue. It is refused while open Tickets exist (move them first) and for the intake
// Queue. An archived Queue keeps its prefix forever.
func (s *Service) ArchiveQueue(ctx context.Context, c Caller, p Principal, id string, expected *int) (Queue, error) {
	return s.queueLifecycle(ctx, c, p, id, expected, "servicedesk.queue.archived", func(ctx context.Context, tx pgx.Tx, q *Queue) error {
		if q.Status == QueueArchived {
			return ErrQueueArchived
		}
		if q.DefaultIntake {
			return ErrQueueIsDefault
		}
		n, err := s.queues.CountOpenTicketsTx(ctx, tx, q.ID)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrQueueHasOpenTickets
		}
		now := time.Now().UTC()
		q.Status, q.ArchivedAt = QueueArchived, &now
		return nil
	})
}

// RestoreQueue makes an archived Queue active again.
func (s *Service) RestoreQueue(ctx context.Context, c Caller, p Principal, id string, expected *int) (Queue, error) {
	return s.queueLifecycle(ctx, c, p, id, expected, "servicedesk.queue.restored", func(_ context.Context, _ pgx.Tx, q *Queue) error {
		if q.Status == QueueActive {
			return invalid("the queue is not archived")
		}
		q.Status, q.ArchivedAt = QueueActive, nil
		return nil
	})
}

// MakeDefaultQueue makes an active, public Queue the one Tickets are raised into when none is chosen.
func (s *Service) MakeDefaultQueue(ctx context.Context, c Caller, p Principal, id string, expected *int) (Queue, error) {
	return s.queueLifecycle(ctx, c, p, id, expected, "servicedesk.queue.made_default", func(_ context.Context, _ pgx.Tx, q *Queue) error {
		if q.Status != QueueActive {
			return ErrQueueArchived
		}
		if q.Visibility != QueuePublic {
			return invalid("the intake queue must be public, otherwise employees cannot raise tickets")
		}
		q.DefaultIntake = true
		return nil
	})
}

func (s *Service) queueLifecycle(ctx context.Context, c Caller, p Principal, id string, expected *int, action string,
	apply func(context.Context, pgx.Tx, *Queue) error) (Queue, error) {
	if err := requireQueuesManage(c, p); err != nil {
		return Queue{}, err
	}
	if err := s.needQueues(); err != nil {
		return Queue{}, err
	}
	if err := needVersion(expected); err != nil {
		return Queue{}, err
	}
	var out Queue
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockQueue(ctx, tx, id, *expected)
		if err != nil {
			return err
		}
		next := cur
		if err := apply(ctx, tx, &next); err != nil {
			return err
		}
		out, err = s.queues.UpdateQueueTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return auditQueue(ctx, tx, c, action, &cur, &out, nil)
	})
	return out, err
}

// GrantInput is one subject of a full grant replacement.
type GrantInput struct {
	SubjectType string
	SubjectID   string
	Level       string
}

// ReplaceGrants replaces all grants of a Queue (full replace, version guarded). Users and Teams must be active;
// a role id is stored as given (an unknown role grants nothing).
func (s *Service) ReplaceGrants(ctx context.Context, c Caller, p Principal, id string, expected *int, in []GrantInput) (QueueView, error) {
	if err := requireQueuesManage(c, p); err != nil {
		return QueueView{}, err
	}
	if err := s.needQueues(); err != nil {
		return QueueView{}, err
	}
	if err := needVersion(expected); err != nil {
		return QueueView{}, err
	}
	if len(in) > MaxQueueGrants {
		return QueueView{}, invalid("at most %d grants are allowed per queue", MaxQueueGrants)
	}
	var users, teams []string
	seen := map[GrantInput]bool{}
	grants := make([]QueueGrant, 0, len(in))
	for _, g := range in {
		g.SubjectID = strings.ToLower(g.SubjectID)
		if !slices.Contains([]string{"user", "team", "role"}, g.SubjectType) || !uuidPattern.MatchString(g.SubjectID) || levelRank(g.Level) == levelNone {
			return QueueView{}, invalid("a grant needs a subject type (user, team or role), a subject id and a level (create, view, work or manage)")
		}
		if seen[g] {
			continue
		}
		seen[g] = true
		switch g.SubjectType {
		case "user":
			users = append(users, g.SubjectID)
		case "team":
			teams = append(teams, g.SubjectID)
		}
		grants = append(grants, QueueGrant{SubjectType: g.SubjectType, SubjectID: g.SubjectID, Level: g.Level, GrantedBy: c.Actor.UserID})
	}
	if len(users) > 0 {
		ok, err := s.dir.ActiveUsers(ctx, users)
		if err != nil {
			return QueueView{}, fmt.Errorf("check users: %w", err)
		}
		for _, u := range users {
			if !ok[u] {
				return QueueView{}, ErrUserInvalid
			}
		}
	}
	if len(teams) > 0 {
		ok, err := s.dir.ActiveTeams(ctx, teams)
		if err != nil {
			return QueueView{}, fmt.Errorf("check teams: %w", err)
		}
		for _, t := range teams {
			if !ok[t] {
				return QueueView{}, ErrTeamInvalid
			}
		}
	}
	var out Queue
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockQueue(ctx, tx, id, *expected)
		if err != nil {
			return err
		}
		before, err := s.queues.Grants(ctx, cur.ID)
		if err != nil {
			return err
		}
		if err := s.queues.ReplaceGrantsTx(ctx, tx, cur.ID, grants); err != nil {
			return err
		}
		next := cur
		out, err = s.queues.UpdateQueueTx(ctx, tx, next)
		if err != nil {
			return err
		}
		added, removed := grantDiff(before, grants)
		return auditQueue(ctx, tx, c, "servicedesk.queue.grants_replaced", &cur, &out, map[string]any{
			"grantsBefore": len(before), "grantsAfter": len(grants), "added": added, "removed": removed})
	})
	if err != nil {
		return QueueView{}, err
	}
	return s.GetQueue(ctx, p, out.ID)
}

// SidebarScope is what the sidebar needs to know about a caller: whether they work with Tickets beyond their own
// and which active Queues they view.
type SidebarScope struct {
	AnyView bool
	Queues  []QueueRef
	// Key digests the caller's Queue scope (global flags and the viewable Queue ids), for count cache keys.
	Key string
}

// SidebarScope returns the caller's Queue scope.
func (s *Service) SidebarScope(ctx context.Context, p Principal) (SidebarScope, error) {
	if p.UserID == "" {
		return SidebarScope{}, ErrForbidden
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return SidebarScope{}, err
	}
	out := SidebarScope{AnyView: a.anyView()}
	h := sha256.New()
	fmt.Fprintf(h, "%t/%t", a.allView, a.allWork)
	for _, id := range a.viewIDs() {
		fmt.Fprintf(h, "|%s", id)
		if r := a.queues[id]; r.Queue.Status == QueueActive {
			out.Queues = append(out.Queues, QueueRef{ID: r.Queue.ID, Key: r.Queue.Key, Prefix: r.Queue.Prefix, Name: r.Queue.Name})
		}
	}
	sort.SliceStable(out.Queues, func(i, j int) bool {
		x, y := strings.ToLower(out.Queues[i].Name), strings.ToLower(out.Queues[j].Name)
		if x != y {
			return x < y
		}
		return out.Queues[i].ID < out.Queues[j].ID
	})
	out.Key = hex.EncodeToString(h.Sum(nil))[:32]
	return out, nil
}

// grantDiff lists the grants that a replace adds and removes as (subjectType, subjectId, level) tuples, so the
// audit trail shows who gained or lost access to a Queue, not only how many grants exist (ids only, no names).
func grantDiff(before, after []QueueGrant) (added, removed []map[string]string) {
	type key struct{ t, id, level string }
	keyOf := func(g QueueGrant) key { return key{g.SubjectType, strings.ToLower(g.SubjectID), g.Level} }
	was, is := map[key]bool{}, map[key]bool{}
	for _, g := range before {
		was[keyOf(g)] = true
	}
	for _, g := range after {
		is[keyOf(g)] = true
	}
	added, removed = []map[string]string{}, []map[string]string{}
	emit := func(dst *[]map[string]string, src []QueueGrant, in, notIn map[key]bool) {
		for _, g := range src {
			if k := keyOf(g); in[k] && !notIn[k] {
				*dst = append(*dst, map[string]string{"subjectType": k.t, "subjectId": k.id, "level": k.level})
				in[k] = false
			}
		}
	}
	emit(&added, after, is, was)
	emit(&removed, before, was, is)
	return added, removed
}
