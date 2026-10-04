package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// checkTarget verifies that the record a Service wants to depend on exists
// and can still be depended on, and returns its normalized (lower-case) id.
// Existence is checked through the owning modules' public contracts. A failing
// lookup is an internal error, an unusable target ErrReferenceInvalid. A target
// of a type the caller may not see (Virtual Machine and Location need
// infrastructure.view, Asset assets.view) is ErrReferenceInvalid without any
// lookup, so adding a dependency never confirms hidden records exist.
func (s *App) checkTarget(ctx context.Context, p Principal, serviceID, targetType, targetID string) (string, error) {
	if !oneOf(targetType, DependencyTargets) {
		return "", invalid("targetType must be one of %s", strings.Join(DependencyTargets, ", "))
	}
	if err := checkIDs(targetID); err != nil {
		return "", err
	}
	id := strings.ToLower(targetID)
	if p.hides(targetType) {
		return "", ErrReferenceInvalid
	}
	switch targetType {
	case NodeService:
		if id == serviceID {
			return "", invalid("a service cannot depend on itself")
		}
		t, err := s.store.Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return "", ErrReferenceInvalid
		}
		if err != nil {
			return "", err
		}
		if t.Status == StatusRetired {
			return "", ErrReferenceInvalid
		}
	case NodeVM:
		found, err := s.infra.VMs(ctx, []string{id})
		if err != nil {
			return "", fmt.Errorf("check virtual machine: %w", err)
		}
		if v, ok := found[id]; !ok || v.Decommissioned() {
			return "", ErrReferenceInvalid
		}
	case NodeAsset:
		found, err := s.assets.Assets(ctx, []string{id})
		if err != nil {
			return "", fmt.Errorf("check asset: %w", err)
		}
		if a, ok := found[id]; !ok || !a.Usable() {
			return "", ErrReferenceInvalid
		}
	case NodeLocation:
		ok, err := s.dir.ActiveLocations(ctx, []string{id})
		if err != nil {
			return "", fmt.Errorf("check location: %w", err)
		}
		if !ok[id] {
			return "", ErrReferenceInvalid
		}
	}
	return id, nil
}

// AddDependency records that a Service depends on another Service, a Virtual
// Machine, an Asset or a Location. The link is a declared platform
// Relationship. Adding an existing dependency returns it unchanged (created is
// false). A Service dependency that would close a cycle is refused with
// ErrDependencyCycle; service-to-service changes are serialized by a database
// lock for that check. Requires services.manage.
func (s *App) AddDependency(ctx context.Context, c Caller, p Principal, serviceID, targetType, targetID string) (l Link, created bool, err error) {
	if err := c.validate(); err != nil {
		return Link{}, false, err
	}
	if err := p.require(true); err != nil {
		return Link{}, false, err
	}
	if !uuidPattern.MatchString(serviceID) {
		return Link{}, false, ErrNotFound
	}
	serviceID = strings.ToLower(serviceID)
	targetID, err = s.checkTarget(ctx, p, serviceID, targetType, targetID)
	if err != nil {
		return Link{}, false, err
	}
	src := relationships.Node{Type: NodeService, ID: serviceID}
	dst := relationships.Node{Type: targetType, ID: targetID}
	var rel relationships.Relationship
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		if targetType == NodeService {
			// Bound how long this transaction may wait for the graph lock and
			// work on the cycle check, so a slow walk cannot stall every
			// other dependency change.
			if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true)`,
				fmt.Sprintf("%dms", s.lockTimeout.Milliseconds()), fmt.Sprintf("%dms", s.statementTimeout.Milliseconds())); err != nil {
				return fmt.Errorf("set dependency timeouts: %w", err)
			}
			if err := s.store.LockDependencyGraphTx(ctx, tx); err != nil {
				return err
			}
		}
		cur, err := s.store.LockTx(ctx, tx, serviceID)
		if err != nil {
			return err
		}
		if cur.Status == StatusRetired {
			return ErrRetired
		}
		if targetType == NodeService {
			// Re-read the target under a share lock, after the graph lock: it
			// may have been retired since the first check, and a retire that
			// starts now waits for this transaction (and then ends the new link).
			t, err := s.store.ShareLockTx(ctx, tx, dst.ID)
			if errors.Is(err, ErrNotFound) {
				return ErrReferenceInvalid
			}
			if err != nil {
				return err
			}
			if t.Status == StatusRetired {
				return ErrReferenceInvalid
			}
			// Adding src -> dst closes a cycle when src is already reachable
			// from dst. The walk only follows DEPENDS_ON between Services.
			reaches, err := s.graph.Reaches(ctx, tx, dst, src, relationships.Forward, []string{RelDependsOn}, []string{NodeService})
			switch {
			case errors.Is(err, relationships.ErrTooLarge):
				return ErrCycleCheck
			case err != nil:
				return fmt.Errorf("check dependency cycle: %w", err)
			case reaches:
				return ErrDependencyCycle
			}
		}
		rel, created, err = s.graph.Link(ctx, tx, relationships.LinkInput{
			Owner: RelationshipOwner, Source: src, Type: RelDependsOn, Target: dst, Confidence: relationships.ConfidenceDeclared,
			CreatedBy: c.Actor.UserID, RecordedBy: "services",
		})
		if err != nil {
			return fmt.Errorf("link dependency: %w", err)
		}
		if !created {
			return nil
		}
		return recordAudit(ctx, tx, c, "services.dependency.added", "service", serviceID, nil, nil,
			map[string]any{"relationshipId": rel.ID, "targetType": targetType, "targetId": targetID})
	})
	if err != nil {
		return Link{}, false, mapDependencyError(err)
	}
	info, err := s.describe(ctx, p, []relationships.Node{dst})
	if err != nil {
		return Link{}, false, err
	}
	return link(rel, dst, info), created, nil
}

// mapDependencyError turns the database timeouts set for service-to-service
// changes into domain errors: waiting too long for the graph lock is ErrBusy,
// a cycle check cut off by the statement timeout ErrCycleCheck.
func mapDependencyError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch {
		case pg.Code == "55P03":
			return ErrBusy
		case pg.Code == "57014" && strings.Contains(pg.Message, "statement timeout"):
			return ErrCycleCheck
		}
	}
	return err
}

// RemoveDependency ends a Service dependency with a reason code. Repeating it
// for an already ended dependency succeeds without a second audit entry. A
// relationship id that is not a dependency of this Service is ErrNotFound.
// Requires services.manage.
func (s *App) RemoveDependency(ctx context.Context, c Caller, p Principal, serviceID, relationshipID, reason string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if err := p.require(true); err != nil {
		return err
	}
	if !oneOf(reason, DependencyRemovalReasons) {
		return invalid("reason must be one of %s", strings.Join(DependencyRemovalReasons, ", "))
	}
	if !uuidPattern.MatchString(serviceID) || !uuidPattern.MatchString(relationshipID) {
		return ErrNotFound
	}
	serviceID = strings.ToLower(serviceID)
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := s.store.LockTx(ctx, tx, serviceID); err != nil {
			return err
		}
		rel, err := s.graph.Get(ctx, tx, relationshipID)
		if errors.Is(err, relationships.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if rel.Source.Type != NodeService || rel.Source.ID != serviceID || rel.Type != RelDependsOn {
			return ErrNotFound
		}
		_, ended, err := s.graph.Unlink(ctx, tx, RelationshipOwner, rel.ID, reason, c.Actor.UserID)
		if err != nil {
			return fmt.Errorf("unlink dependency: %w", err)
		}
		if !ended {
			return nil
		}
		return recordAudit(ctx, tx, c, "services.dependency.removed", "service", serviceID, nil, nil,
			map[string]any{"relationshipId": rel.ID, "targetType": rel.Target.Type, "targetId": rel.Target.ID, "reason": reason})
	})
}

// masker replaces the ids of records the caller may not see with opaque
// placeholders (hidden-1, hidden-2, ...), numbered in order of first use, so a
// response keeps its shape (paths still connect) without disclosing real ids.
type masker struct {
	ids map[string]string
}

func newMasker() *masker { return &masker{ids: map[string]string{}} }

func (m *masker) id(real string) string {
	ph, ok := m.ids[real]
	if !ok {
		ph = fmt.Sprintf("hidden-%d", len(m.ids)+1)
		m.ids[real] = ph
	}
	return ph
}

func (m *masker) node(n NodeInfo) NodeInfo {
	if n.Hidden {
		n.ID = m.id(n.ID)
	}
	return n
}

// describe resolves nodes to what the caller may see. Services need
// services.view (already required); Virtual Machine and Location names need
// infrastructure.view; Asset references and statuses need assets.view. A
// lookup that the caller may not use does not run, so absence is never
// disclosed for hidden types.
func (s *App) describe(ctx context.Context, p Principal, nodes []relationships.Node) (map[relationships.Node]NodeInfo, error) {
	byType := map[string][]string{}
	seen := map[relationships.Node]bool{}
	out := make(map[relationships.Node]NodeInfo, len(nodes))
	for _, n := range nodes {
		if seen[n] {
			continue
		}
		seen[n] = true
		byType[n.Type] = append(byType[n.Type], n.ID)
		out[n] = NodeInfo{Type: n.Type, ID: n.ID, Hidden: p.hides(n.Type)}
	}
	chunks := func(ids []string, fn func([]string) error) error {
		for len(ids) > 0 {
			n := min(len(ids), MaxLookupIDs)
			if err := fn(ids[:n]); err != nil {
				return err
			}
			ids = ids[n:]
		}
		return nil
	}
	set := func(typ, id string, f func(*NodeInfo)) {
		k := relationships.Node{Type: typ, ID: id}
		v := out[k]
		f(&v)
		out[k] = v
	}
	if ids := byType[NodeService]; len(ids) > 0 {
		found := map[string]Service{}
		if err := chunks(ids, func(part []string) error {
			recs, err := s.store.ByIDs(ctx, part)
			for _, r := range recs {
				found[r.ID] = r
			}
			return err
		}); err != nil {
			return nil, err
		}
		for _, id := range ids {
			r, ok := found[id]
			set(NodeService, id, func(n *NodeInfo) {
				if !ok {
					n.Missing = true
					return
				}
				n.Name, n.Reference, n.Status, n.Criticality = &r.Name, &r.Reference, &r.Status, &r.Criticality
			})
		}
	}
	if ids := byType[NodeVM]; len(ids) > 0 && p.InfraView {
		found := map[string]VMInfo{}
		if err := chunks(ids, func(part []string) error {
			m, err := s.infra.VMs(ctx, part)
			for k, v := range m {
				found[k] = v
			}
			return err
		}); err != nil {
			return nil, err
		}
		for _, id := range ids {
			v, ok := found[id]
			set(NodeVM, id, func(n *NodeInfo) {
				if !ok {
					n.Missing = true
					return
				}
				n.Name, n.Status = &v.Name, &v.State
			})
		}
	}
	if ids := byType[NodeLocation]; len(ids) > 0 && p.InfraView {
		found := map[string]string{}
		if err := chunks(ids, func(part []string) error {
			m, err := s.dir.LocationNames(ctx, part)
			for k, v := range m {
				found[k] = v
			}
			return err
		}); err != nil {
			return nil, err
		}
		for _, id := range ids {
			name, ok := found[id]
			set(NodeLocation, id, func(n *NodeInfo) {
				if !ok {
					n.Missing = true
					return
				}
				n.Name = &name
			})
		}
	}
	if ids := byType[NodeAsset]; len(ids) > 0 && p.AssetsView {
		found := map[string]AssetInfo{}
		if err := chunks(ids, func(part []string) error {
			m, err := s.assets.Assets(ctx, part)
			for k, v := range m {
				found[k] = v
			}
			return err
		}); err != nil {
			return nil, err
		}
		for _, id := range ids {
			a, ok := found[id]
			set(NodeAsset, id, func(n *NodeInfo) {
				if !ok {
					n.Missing = true
					return
				}
				n.Reference, n.Status = &a.Reference, &a.Status
			})
		}
	}
	return out, nil
}

// Impact answers "what is affected if this record is down" (downstream: the
// records that depend on it, directly or through other records) or "what does
// it depend on" (upstream). The walk is bounded to depth 6 and 500 records and
// reports truncation. Requires services.view; a start record whose type the
// caller may not see (Virtual Machines and Locations need infrastructure.view,
// Assets assets.view) or that does not exist is ErrNotFound.
func (s *App) Impact(ctx context.Context, p Principal, in ImpactInput) (ImpactResult, error) {
	if err := p.require(false); err != nil {
		return ImpactResult{}, err
	}
	if !oneOf(in.Type, DependencyTargets) {
		return ImpactResult{}, invalid("type must be one of %s", strings.Join(DependencyTargets, ", "))
	}
	if in.Direction == "" {
		in.Direction = Downstream
	}
	if in.Direction != Downstream && in.Direction != Upstream {
		return ImpactResult{}, invalid("direction must be downstream or upstream")
	}
	if in.Depth < 0 || in.Depth > relationships.MaxDepth {
		return ImpactResult{}, invalid("depth must be between 1 and %d", relationships.MaxDepth)
	}
	if in.Depth == 0 {
		in.Depth = relationships.MaxDepth
	}
	if !uuidPattern.MatchString(in.ID) {
		return ImpactResult{}, ErrNotFound
	}
	start := relationships.Node{Type: in.Type, ID: strings.ToLower(in.ID)}
	if p.hides(in.Type) {
		return ImpactResult{}, ErrNotFound
	}
	// One traversal per user at a time (per process): it is the expensive read.
	if !s.acquireImpact(p.UserID) {
		return ImpactResult{}, ErrImpactBusy
	}
	defer s.releaseImpact(p.UserID)
	startInfo, err := s.describe(ctx, p, []relationships.Node{start})
	if err != nil {
		return ImpactResult{}, err
	}
	if startInfo[start].Missing {
		return ImpactResult{}, ErrNotFound
	}
	dir := relationships.Reverse
	if in.Direction == Upstream {
		dir = relationships.Forward
	}
	// Only the dependency graph counts: other modules' relationships (for
	// example "change AFFECTS service") never appear as impacted records.
	walk, err := s.graph.Traverse(ctx, s.store.Q(), relationships.TraverseInput{Start: start, Direction: dir, MaxDepth: in.Depth,
		Types: ImpactRelationshipTypes, NodeTypes: DependencyTargets})
	if err != nil {
		return ImpactResult{}, fmt.Errorf("impact traversal: %w", err)
	}
	nodes := make([]relationships.Node, len(walk.Nodes))
	for i, w := range walk.Nodes {
		nodes[i] = w.Node
	}
	info, err := s.describe(ctx, p, nodes)
	if err != nil {
		return ImpactResult{}, err
	}
	res := ImpactResult{Start: startInfo[start], Direction: in.Direction, MaxDepth: in.Depth,
		Truncated: walk.Truncated, DepthLimited: walk.DepthLimited, NodeLimited: walk.NodeLimited,
		Nodes: make([]ImpactNode, 0, len(walk.Nodes))}
	// Hidden records stay in the walk (their dependents still matter) but
	// appear under placeholder ids, on the node and on every path edge.
	m := newMasker()
	hidden := func(n relationships.Node) bool { return p.hides(n.Type) }
	for _, w := range walk.Nodes {
		n := ImpactNode{NodeInfo: m.node(info[w.Node]), Depth: w.Depth, Path: make([]PathEdge, len(w.Path))}
		for i, e := range w.Path {
			pe := PathEdge{RelationshipID: e.RelationshipID, FromType: e.From.Type, FromID: e.From.ID, ToType: e.To.Type, ToID: e.To.ID, Type: e.Type, Confidence: e.Confidence}
			if hidden(e.From) {
				pe.FromID = m.id(e.From.ID)
			}
			if hidden(e.To) {
				pe.ToID = m.id(e.To.ID)
			}
			if hidden(e.From) || hidden(e.To) {
				pe.RelationshipID = m.id(e.RelationshipID)
			}
			n.Path[i] = pe
		}
		if len(w.Path) > 0 {
			n.Confidence = w.Path[len(w.Path)-1].Confidence
		}
		res.Nodes = append(res.Nodes, n)
	}
	return res, nil
}

func (s *App) acquireImpact(user string) bool {
	s.impactMu.Lock()
	defer s.impactMu.Unlock()
	if s.impactRunning[user] {
		return false
	}
	s.impactRunning[user] = true
	return true
}

func (s *App) releaseImpact(user string) {
	s.impactMu.Lock()
	delete(s.impactRunning, user)
	s.impactMu.Unlock()
}
