package application

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// App performs Services operations. Audit actions: services.service.created|updated|status_changed|retired
// and services.dependency.added|removed, written in the mutation's transaction
// with ids, states and reason codes only (no names or descriptions).
type App struct {
	store  Store
	graph  *relationships.Graph
	dir    Directory
	infra  Infrastructure
	assets Assets
}

func NewApp(store Store, graph *relationships.Graph, dir Directory, infra Infrastructure, assets Assets) *App {
	return &App{store: store, graph: graph, dir: dir, infra: infra, assets: assets}
}

func publish(ctx context.Context, tx pgx.Tx, c Caller, typ string, payload map[string]any) error {
	var actor *string
	if c.Actor.UserID != "" {
		a := c.Actor.UserID
		actor = &a
	}
	return events.Publish(ctx, tx, events.Publication{Type: typ, ActorID: actor, CorrelationID: c.CorrelationID, Payload: payload})
}

func recordAudit(ctx context.Context, tx pgx.Tx, c Caller, action, targetType, targetID string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: targetType, TargetID: targetID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
}

func requireVersion(expected *int) (int, error) {
	if expected == nil {
		return 0, invalid("expectedVersion is required")
	}
	return *expected, nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func checkIDs(ids ...string) error {
	for _, id := range ids {
		if !uuidPattern.MatchString(id) {
			return invalid("ids must be UUIDs")
		}
	}
	return nil
}

func oneOf(s string, set []string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

func cleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxName || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return "", invalid("name must be 1-%d characters without control or invisible formatting characters", maxName)
	}
	return s, nil
}

func cleanDescription(s string) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(s) > maxDesc || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, true) {
		return nil, invalid("description must be at most %d characters without control or invisible formatting characters", maxDesc)
	}
	return &s, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// owners holds validated, normalized organization references.
type owners struct{ user, team, support *string }

// checkOwners validates optional organization references: an active User and
// active Teams. Empty strings mean none.
func (s *App) checkOwners(ctx context.Context, user, team, support string) (owners, error) {
	var o owners
	for _, id := range []string{user, team, support} {
		if id != "" {
			if err := checkIDs(id); err != nil {
				return o, err
			}
		}
	}
	lower := func(id string) *string {
		if id == "" {
			return nil
		}
		l := strings.ToLower(id)
		return &l
	}
	o.user, o.team, o.support = lower(user), lower(team), lower(support)
	if o.user != nil {
		ok, err := s.dir.ActiveUsers(ctx, []string{*o.user})
		if err != nil {
			return o, fmt.Errorf("check owner: %w", err)
		}
		if !ok[*o.user] {
			return o, ErrReferenceInvalid
		}
	}
	var teams []string
	for _, t := range []*string{o.team, o.support} {
		if t != nil {
			teams = append(teams, *t)
		}
	}
	if len(teams) > 0 {
		ok, err := s.dir.ActiveTeams(ctx, teams)
		if err != nil {
			return o, fmt.Errorf("check teams: %w", err)
		}
		for _, t := range teams {
			if !ok[t] {
				return o, ErrReferenceInvalid
			}
		}
	}
	return o, nil
}

func serviceState(v *Service) any {
	if v == nil {
		return nil
	}
	return map[string]any{"criticality": v.Criticality, "status": v.Status, "ownerUserId": v.OwnerUserID, "ownerTeamId": v.OwnerTeamID, "supportTeamId": v.SupportTeamID, "version": v.Version}
}

// Create registers a Service. Requires services.manage.
func (s *App) Create(ctx context.Context, c Caller, p Principal, in Input) (Service, error) {
	if err := c.validate(); err != nil {
		return Service{}, err
	}
	if err := p.require(true); err != nil {
		return Service{}, err
	}
	name, err := cleanName(in.Name)
	if err != nil {
		return Service{}, err
	}
	desc, err := cleanDescription(in.Description)
	if err != nil {
		return Service{}, err
	}
	if !oneOf(in.Criticality, Criticalities) {
		return Service{}, invalid("criticality must be one of %s", strings.Join(Criticalities, ", "))
	}
	if in.Status == "" {
		in.Status = StatusOperational
	}
	if !oneOf(in.Status, SettableStatuses) {
		return Service{}, invalid("status must be one of %s", strings.Join(SettableStatuses, ", "))
	}
	o, err := s.checkOwners(ctx, in.OwnerUserID, in.OwnerTeamID, in.SupportTeamID)
	if err != nil {
		return Service{}, err
	}
	rec := Service{Name: name, Description: desc, OwnerUserID: o.user, OwnerTeamID: o.team, SupportTeamID: o.support,
		Criticality: in.Criticality, Status: in.Status, CreatedBy: strPtr(c.Actor.UserID)}
	var out Service
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		out, err = s.store.InsertTx(ctx, tx, rec)
		if err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "services.service.created", "service", out.ID, nil, serviceState(&out), nil); err != nil {
			return err
		}
		return publish(ctx, tx, c, "ServiceCreated", map[string]any{"serviceId": out.ID, "criticality": out.Criticality, "status": out.Status})
	})
	return out, err
}

// UpdateDetails changes name, description, owners, support team and
// criticality. expectedVersion is required. Requires services.manage.
func (s *App) UpdateDetails(ctx context.Context, c Caller, p Principal, id string, expected *int, in Details) (Service, error) {
	if err := c.validate(); err != nil {
		return Service{}, err
	}
	if err := p.require(true); err != nil {
		return Service{}, err
	}
	if _, err := requireVersion(expected); err != nil {
		return Service{}, err
	}
	var name string
	var desc *string
	var err error
	if in.Name != nil {
		if name, err = cleanName(*in.Name); err != nil {
			return Service{}, err
		}
	}
	if in.Description != nil {
		if desc, err = cleanDescription(*in.Description); err != nil {
			return Service{}, err
		}
	}
	if in.Criticality != nil && !oneOf(*in.Criticality, Criticalities) {
		return Service{}, invalid("criticality must be one of %s", strings.Join(Criticalities, ", "))
	}
	// Only the references the request changes are validated, so an owner who
	// has since left does not block unrelated edits.
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	o, err := s.checkOwners(ctx, deref(in.OwnerUserID), deref(in.OwnerTeamID), deref(in.SupportTeamID))
	if err != nil {
		return Service{}, err
	}
	if !uuidPattern.MatchString(id) {
		return Service{}, ErrNotFound
	}
	var out Service
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != *expected {
			return ErrVersionConflict
		}
		if cur.Status == StatusRetired {
			return ErrRetired
		}
		next := cur
		var changed []string
		if in.Name != nil && name != cur.Name {
			next.Name = name
			changed = append(changed, "name")
		}
		if in.Description != nil && !samePtr(desc, cur.Description) {
			next.Description = desc
			changed = append(changed, "description")
		}
		if in.OwnerUserID != nil && !samePtr(o.user, cur.OwnerUserID) {
			next.OwnerUserID = o.user
			changed = append(changed, "ownerUserId")
		}
		if in.OwnerTeamID != nil && !samePtr(o.team, cur.OwnerTeamID) {
			next.OwnerTeamID = o.team
			changed = append(changed, "ownerTeamId")
		}
		if in.SupportTeamID != nil && !samePtr(o.support, cur.SupportTeamID) {
			next.SupportTeamID = o.support
			changed = append(changed, "supportTeamId")
		}
		if in.Criticality != nil && *in.Criticality != cur.Criticality {
			next.Criticality = *in.Criticality
			changed = append(changed, "criticality")
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		out, err = s.store.UpdateTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "services.service.updated", "service", id, serviceState(&cur), serviceState(&out), map[string]any{"changedFields": changed})
	})
	return out, err
}

// ChangeStatus moves a Service between operational, degraded, outage and
// planned with a reason code. Retiring has its own operation. expectedVersion
// is required. Requires services.manage.
func (s *App) ChangeStatus(ctx context.Context, c Caller, p Principal, id string, expected *int, status, reason string) (Service, error) {
	if err := c.validate(); err != nil {
		return Service{}, err
	}
	if err := p.require(true); err != nil {
		return Service{}, err
	}
	if _, err := requireVersion(expected); err != nil {
		return Service{}, err
	}
	if !oneOf(status, SettableStatuses) {
		return Service{}, invalid("status must be one of %s", strings.Join(SettableStatuses, ", "))
	}
	if !oneOf(reason, StatusReasons) {
		return Service{}, invalid("reason must be one of %s", strings.Join(StatusReasons, ", "))
	}
	if !uuidPattern.MatchString(id) {
		return Service{}, ErrNotFound
	}
	var out Service
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != *expected {
			return ErrVersionConflict
		}
		if cur.Status == StatusRetired {
			return ErrRetired
		}
		if cur.Status == status {
			out = cur
			return nil
		}
		next := cur
		next.Status, next.StatusReason = status, &reason
		out, err = s.store.UpdateTx(ctx, tx, next)
		if err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "services.service.status_changed", "service", id, serviceState(&cur), serviceState(&out), map[string]any{"reason": reason}); err != nil {
			return err
		}
		return publish(ctx, tx, c, "ServiceStatusChanged", map[string]any{
			"serviceId": id, "operation": "status_changed", "status": status, "previousStatus": cur.Status, "reason": reason})
	})
	return out, err
}

// Retire ends a Service for good (terminal tombstone) with a reason code. Its
// own dependencies end with it; Services that depend on it keep their links so
// their owners see the retired dependency. Repeating a successful retire with
// the same reason returns the current record. expectedVersion is required.
// Requires services.manage.
func (s *App) Retire(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Service, error) {
	if err := c.validate(); err != nil {
		return Service{}, err
	}
	if err := p.require(true); err != nil {
		return Service{}, err
	}
	if _, err := requireVersion(expected); err != nil {
		return Service{}, err
	}
	if !oneOf(reason, RetireReasons) {
		return Service{}, invalid("reason must be one of %s", strings.Join(RetireReasons, ", "))
	}
	if !uuidPattern.MatchString(id) {
		return Service{}, ErrNotFound
	}
	var out Service
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Status == StatusRetired {
			if cur.StatusReason != nil && *cur.StatusReason == reason && (*expected == cur.Version-1 || *expected == cur.Version) {
				out = cur
				return nil
			}
			return ErrRetired
		}
		if cur.Version != *expected {
			return ErrVersionConflict
		}
		next := cur
		next.Status, next.StatusReason = StatusRetired, &reason
		out, err = s.store.UpdateTx(ctx, tx, next)
		if err != nil {
			return err
		}
		ended, err := s.graph.UnlinkAll(ctx, tx, relationships.Node{Type: NodeService, ID: id}, relationships.Forward, reasonServiceRetired, c.Actor.UserID)
		if err != nil {
			return fmt.Errorf("end dependencies of retired service: %w", err)
		}
		if err := recordAudit(ctx, tx, c, "services.service.retired", "service", id, serviceState(&cur), serviceState(&out),
			map[string]any{"reason": reason, "endedDependencies": ended}); err != nil {
			return err
		}
		return publish(ctx, tx, c, "ServiceStatusChanged", map[string]any{
			"serviceId": id, "operation": "retired", "status": StatusRetired, "previousStatus": cur.Status, "reason": reason})
	})
	return out, err
}

// Get returns one Service with its dependencies and dependents. Requires services.view.
func (s *App) Get(ctx context.Context, p Principal, id string) (Detail, error) {
	if err := p.require(false); err != nil {
		return Detail{}, err
	}
	if !uuidPattern.MatchString(id) {
		return Detail{}, ErrNotFound
	}
	rec, err := s.store.Get(ctx, strings.ToLower(id))
	if err != nil {
		return Detail{}, err
	}
	node := relationships.Node{Type: NodeService, ID: rec.ID}
	types := []string{RelDependsOn}
	out, outCut, err := s.graph.Outgoing(ctx, s.store.Q(), node, types, MaxDependencyList)
	if err != nil {
		return Detail{}, fmt.Errorf("list dependencies: %w", err)
	}
	in, inCut, err := s.graph.Incoming(ctx, s.store.Q(), node, types, MaxDependencyList)
	if err != nil {
		return Detail{}, fmt.Errorf("list dependents: %w", err)
	}
	nodes := make([]relationships.Node, 0, len(out)+len(in))
	for _, r := range out {
		nodes = append(nodes, r.Target)
	}
	for _, r := range in {
		nodes = append(nodes, r.Source)
	}
	info, err := s.describe(ctx, p, nodes)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Service: rec, DependenciesCutOff: outCut, DependentsCutOff: inCut}
	for _, r := range out {
		d.Dependencies = append(d.Dependencies, link(r, r.Target, info))
	}
	for _, r := range in {
		d.Dependents = append(d.Dependents, link(r, r.Source, info))
	}
	return d, nil
}

func link(r relationships.Relationship, other relationships.Node, info map[relationships.Node]NodeInfo) Link {
	return Link{RelationshipID: r.ID, Type: r.Type, Confidence: r.Confidence, Since: r.ValidFrom, Node: info[other]}
}

// List lists Services. Requires services.view.
func (s *App) List(ctx context.Context, p Principal, f Filter) (Result[Service], error) {
	if err := p.require(false); err != nil {
		return Result[Service]{}, err
	}
	if f.Status != "" && !oneOf(f.Status, Statuses) {
		return Result[Service]{}, invalid("unknown status")
	}
	if f.Criticality != "" && !oneOf(f.Criticality, Criticalities) {
		return Result[Service]{}, invalid("unknown criticality")
	}
	for _, id := range []string{f.OwnerUserID, f.TeamID} {
		if id != "" {
			if err := checkIDs(id); err != nil {
				return Result[Service]{}, err
			}
		}
	}
	f.OwnerUserID, f.TeamID = strings.ToLower(f.OwnerUserID), strings.ToLower(f.TeamID)
	f.Query = strings.TrimSpace(f.Query)
	if utf8.RuneCountInString(f.Query) > maxName {
		return Result[Service]{}, invalid("query must be at most %d characters", maxName)
	}
	f.Page = f.Page.Normalize()
	return s.store.List(ctx, f)
}
