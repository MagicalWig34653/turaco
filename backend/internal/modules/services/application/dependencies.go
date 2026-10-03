package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// checkTarget verifies that the record a Service wants to depend on exists
// and can still be depended on, and returns its normalized (lower-case) id.
// Existence is checked through the owning modules' public contracts. A failing
// lookup is an internal error, an unusable target ErrReferenceInvalid.
func (s *App) checkTarget(ctx context.Context, serviceID, targetType, targetID string) (string, error) {
	if !oneOf(targetType, DependencyTargets) {
		return "", invalid("targetType must be one of %s", strings.Join(DependencyTargets, ", "))
	}
	if err := checkIDs(targetID); err != nil {
		return "", err
	}
	id := strings.ToLower(targetID)
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
	targetID, err = s.checkTarget(ctx, serviceID, targetType, targetID)
	if err != nil {
		return Link{}, false, err
	}
	src := relationships.Node{Type: NodeService, ID: serviceID}
	dst := relationships.Node{Type: targetType, ID: targetID}
	var rel relationships.Relationship
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		if targetType == NodeService {
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
			// Adding src -> dst closes a cycle when src is already reachable from dst.
			reaches, err := s.graph.Reaches(ctx, tx, dst, src, relationships.Forward, []string{RelDependsOn})
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
			Source: src, Type: RelDependsOn, Target: dst, Confidence: relationships.ConfidenceDeclared,
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
		return Link{}, false, err
	}
	info, err := s.describe(ctx, p, []relationships.Node{dst})
	if err != nil {
		return Link{}, false, err
	}
	return link(rel, dst, info), created, nil
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
		_, ended, err := s.graph.Unlink(ctx, tx, rel.ID, reason, c.Actor.UserID)
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
		out[n] = NodeInfo{Type: n.Type, ID: n.ID}
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
	if (in.Type == NodeVM || in.Type == NodeLocation) && !p.InfraView || in.Type == NodeAsset && !p.AssetsView {
		return ImpactResult{}, ErrNotFound
	}
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
	walk, err := s.graph.Traverse(ctx, s.store.Q(), relationships.TraverseInput{Start: start, Direction: dir, MaxDepth: in.Depth})
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
	for _, w := range walk.Nodes {
		n := ImpactNode{NodeInfo: info[w.Node], Depth: w.Depth, Path: make([]PathEdge, len(w.Path))}
		for i, e := range w.Path {
			n.Path[i] = PathEdge{RelationshipID: e.RelationshipID, FromType: e.From.Type, FromID: e.From.ID, ToType: e.To.Type, ToID: e.To.ID, Type: e.Type, Confidence: e.Confidence}
		}
		if len(w.Path) > 0 {
			n.Confidence = w.Path[len(w.Path)-1].Confidence
		}
		res.Nodes = append(res.Nodes, n)
	}
	return res, nil
}
