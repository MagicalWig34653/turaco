package application

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

// Queue routing rules (docs/product/f5-service-desk-design.md): an ordered list of declarative rules over the intake
// fields of a new Ticket. The first enabled rule whose conditions all hold decides the Queue; without a match the
// intake Queue is used. Rules only choose a Queue: they never assign people, and a rule never applies when the
// reporter or staff member chose a Queue explicitly.

// Routing limits.
const (
	MaxRoutingRules    = 100
	MaxRoutingKeywords = 10
	maxKeywordLen      = 60
	maxRuleName        = 100
)

// RoutingRule is one rule. Every given condition must hold (keywords: any one of them appears in title or description).
type RoutingRule struct {
	ID            string
	QueueID       string
	Position      int
	Name          string
	Keywords      []string
	Impact        string // one of ReportImpacts or empty
	PatientImpact *bool
	Enabled       bool
	Version       int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// RoutingInput describes a rule to create or fully replace.
type RoutingInput struct {
	QueueID       string
	Name          string
	Keywords      []string
	Impact        string
	PatientImpact *bool
	Enabled       bool
}

// RoutingSample is the intake of a Ticket for matching (create path and test).
type RoutingSample struct {
	Title         string
	Description   string
	Impact        string
	PatientImpact bool
}

// RoutingStore persists the rules; the Repository implements it next to Store.
type RoutingStore interface {
	ListRules(ctx context.Context) ([]RoutingRule, error)
	// LockRulesTx locks every rule row in position order and returns them.
	LockRulesTx(ctx context.Context, tx pgx.Tx) ([]RoutingRule, error)
	InsertRuleTx(ctx context.Context, tx pgx.Tx, r RoutingRule, createdBy string) (RoutingRule, error)
	UpdateRuleTx(ctx context.Context, tx pgx.Tx, r RoutingRule) (RoutingRule, error)
	DeleteRuleTx(ctx context.Context, tx pgx.Tx, id string) error
	// SetPositionsTx writes position = index for the rule ids in order.
	SetPositionsTx(ctx context.Context, tx pgx.Tx, ids []string) error
}

func (r RoutingRule) matches(s RoutingSample) bool {
	if r.Impact != "" && r.Impact != s.Impact {
		return false
	}
	if r.PatientImpact != nil && *r.PatientImpact != s.PatientImpact {
		return false
	}
	if len(r.Keywords) > 0 {
		text := search.FoldWord(s.Title + " " + s.Description)
		hit := false
		for _, k := range r.Keywords {
			if strings.Contains(text, search.FoldWord(k)) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// routeQueue returns the Queue the first enabled matching rule names, if that Queue is active and accepts automatic
// routing. A rule whose Queue no longer qualifies is skipped, not an error: intake must never fail because of it.
func (s *Service) routeQueue(ctx context.Context, sample RoutingSample) (Queue, RoutingRule, bool, error) {
	if s.routing == nil || s.queues == nil {
		return Queue{}, RoutingRule{}, false, nil
	}
	rules, err := s.routing.ListRules(ctx)
	if err != nil {
		return Queue{}, RoutingRule{}, false, err
	}
	for _, r := range rules {
		if !r.Enabled || !r.matches(sample) {
			continue
		}
		q, err := s.queues.GetQueue(ctx, r.QueueID)
		if err != nil {
			if err == ErrNotFound {
				continue
			}
			return Queue{}, RoutingRule{}, false, err
		}
		if q.Status != QueueActive || (q.RoutingMode != RoutingAutomatic && q.RoutingMode != RoutingBoth) {
			continue
		}
		return q, r, true, nil
	}
	return Queue{}, RoutingRule{}, false, nil
}

func (s *Service) needRouting() error {
	if s.routing == nil || s.queues == nil {
		return invalid("routing rules are not available")
	}
	return nil
}

func cleanRule(in RoutingInput) (RoutingInput, error) {
	name, err := cleanText(in.Name, maxRuleName, true, "name")
	if err != nil {
		return in, err
	}
	in.Name = name
	if !uuidPattern.MatchString(strings.ToLower(in.QueueID)) {
		return in, invalid("queueId must be a Queue id")
	}
	in.QueueID = strings.ToLower(in.QueueID)
	var kws []string
	for _, k := range in.Keywords {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if len([]rune(k)) > maxKeywordLen {
			return in, invalid("a keyword has at most %d characters", maxKeywordLen)
		}
		if _, err := cleanText(k, maxKeywordLen, true, "keyword"); err != nil {
			return in, err
		}
		if !slices.ContainsFunc(kws, func(o string) bool { return strings.EqualFold(o, k) }) {
			kws = append(kws, k)
		}
	}
	if len(kws) > MaxRoutingKeywords {
		return in, invalid("at most %d keywords are allowed per rule", MaxRoutingKeywords)
	}
	in.Keywords = kws
	if in.Impact != "" && !slices.Contains(ReportImpacts, in.Impact) {
		return in, invalid("impact must be one of %s", strings.Join(ReportImpacts, ", "))
	}
	if len(kws) == 0 && in.Impact == "" && in.PatientImpact == nil {
		return in, invalid("a rule needs at least one condition: keywords, an impact or the patient-impact flag")
	}
	return in, nil
}

func ruleState(r RoutingRule) map[string]any {
	return map[string]any{"queueId": r.QueueID, "position": r.Position, "name": r.Name, "keywords": r.Keywords, "impact": r.Impact,
		"patientImpact": r.PatientImpact, "enabled": r.Enabled, "version": r.Version}
}

func auditRule(ctx context.Context, tx pgx.Tx, c Caller, action string, before, after *RoutingRule, meta map[string]any) error {
	ch := audit.Change{Action: action, TargetType: "queue_routing_rule", Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: meta}
	if before != nil {
		ch.TargetID, ch.Before = before.ID, ruleState(*before)
	}
	if after != nil {
		ch.TargetID, ch.After = after.ID, ruleState(*after)
	}
	return audit.Record(ctx, tx, ch)
}

func (s *Service) checkRuleQueue(ctx context.Context, queueID string) error {
	q, err := s.queues.GetQueue(ctx, queueID)
	if err != nil {
		if err == ErrNotFound {
			return invalid("the Queue does not exist")
		}
		return err
	}
	if q.Status != QueueActive {
		return invalid("the Queue is archived")
	}
	return nil
}

// ListRoutingRules returns all rules in evaluation order (servicedesk.queues.manage).
func (s *Service) ListRoutingRules(ctx context.Context, p Principal) ([]RoutingRule, error) {
	if !p.QueuesManage {
		return nil, ErrForbidden
	}
	if err := s.needRouting(); err != nil {
		return nil, err
	}
	return s.routing.ListRules(ctx)
}

// CreateRoutingRule appends a rule at the end of the evaluation order.
func (s *Service) CreateRoutingRule(ctx context.Context, c Caller, p Principal, in RoutingInput) (RoutingRule, error) {
	if err := requireQueuesManage(c, p); err != nil {
		return RoutingRule{}, err
	}
	if err := s.needRouting(); err != nil {
		return RoutingRule{}, err
	}
	in, err := cleanRule(in)
	if err != nil {
		return RoutingRule{}, err
	}
	if err := s.checkRuleQueue(ctx, in.QueueID); err != nil {
		return RoutingRule{}, err
	}
	var out RoutingRule
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		all, err := s.routing.LockRulesTx(ctx, tx)
		if err != nil {
			return err
		}
		if len(all) >= MaxRoutingRules {
			return invalid("at most %d routing rules are allowed", MaxRoutingRules)
		}
		out, err = s.routing.InsertRuleTx(ctx, tx, RoutingRule{QueueID: in.QueueID, Position: len(all), Name: in.Name, Keywords: in.Keywords,
			Impact: in.Impact, PatientImpact: in.PatientImpact, Enabled: in.Enabled}, c.Actor.UserID)
		if err != nil {
			return err
		}
		return auditRule(ctx, tx, c, "servicedesk.routing_rule.created", nil, &out, nil)
	})
	return out, err
}

// UpdateRoutingRule replaces the conditions, Queue, name and enabled flag of a rule (version guarded).
func (s *Service) UpdateRoutingRule(ctx context.Context, c Caller, p Principal, id string, expected *int, in RoutingInput) (RoutingRule, error) {
	if err := requireQueuesManage(c, p); err != nil {
		return RoutingRule{}, err
	}
	if err := s.needRouting(); err != nil {
		return RoutingRule{}, err
	}
	if err := needVersion(expected); err != nil {
		return RoutingRule{}, err
	}
	in, err := cleanRule(in)
	if err != nil {
		return RoutingRule{}, err
	}
	if err := s.checkRuleQueue(ctx, in.QueueID); err != nil {
		return RoutingRule{}, err
	}
	var out RoutingRule
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		all, err := s.routing.LockRulesTx(ctx, tx)
		if err != nil {
			return err
		}
		idx := slices.IndexFunc(all, func(r RoutingRule) bool { return r.ID == strings.ToLower(id) })
		if idx < 0 {
			return ErrNotFound
		}
		cur := all[idx]
		if cur.Version != *expected {
			return ErrVersionConflict
		}
		next := cur
		next.QueueID, next.Name, next.Keywords, next.Impact, next.PatientImpact, next.Enabled = in.QueueID, in.Name, in.Keywords, in.Impact, in.PatientImpact, in.Enabled
		out, err = s.routing.UpdateRuleTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return auditRule(ctx, tx, c, "servicedesk.routing_rule.updated", &cur, &out, nil)
	})
	return out, err
}

// DeleteRoutingRule removes a rule (a disabled rule is the reversible alternative).
func (s *Service) DeleteRoutingRule(ctx context.Context, c Caller, p Principal, id string, expected *int) error {
	if err := requireQueuesManage(c, p); err != nil {
		return err
	}
	if err := s.needRouting(); err != nil {
		return err
	}
	if err := needVersion(expected); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		all, err := s.routing.LockRulesTx(ctx, tx)
		if err != nil {
			return err
		}
		idx := slices.IndexFunc(all, func(r RoutingRule) bool { return r.ID == strings.ToLower(id) })
		if idx < 0 {
			return ErrNotFound
		}
		cur := all[idx]
		if cur.Version != *expected {
			return ErrVersionConflict
		}
		if err := s.routing.DeleteRuleTx(ctx, tx, cur.ID); err != nil {
			return err
		}
		rest := make([]string, 0, len(all)-1)
		for _, r := range all {
			if r.ID != cur.ID {
				rest = append(rest, r.ID)
			}
		}
		if err := s.routing.SetPositionsTx(ctx, tx, rest); err != nil {
			return err
		}
		return auditRule(ctx, tx, c, "servicedesk.routing_rule.deleted", &cur, nil, nil)
	})
}

// ReorderRoutingRules sets the evaluation order. ids must name every rule exactly once.
func (s *Service) ReorderRoutingRules(ctx context.Context, c Caller, p Principal, ids []string) ([]RoutingRule, error) {
	if err := requireQueuesManage(c, p); err != nil {
		return nil, err
	}
	if err := s.needRouting(); err != nil {
		return nil, err
	}
	want := make([]string, len(ids))
	for i, id := range ids {
		want[i] = strings.ToLower(id)
	}
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		all, err := s.routing.LockRulesTx(ctx, tx)
		if err != nil {
			return err
		}
		have := make([]string, len(all))
		for i, r := range all {
			have[i] = r.ID
		}
		a, b := slices.Clone(have), slices.Clone(want)
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			return invalid("the order must list every routing rule exactly once")
		}
		if slices.Equal(have, want) {
			return nil
		}
		if err := s.routing.SetPositionsTx(ctx, tx, want); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: "servicedesk.routing_rule.reordered", TargetType: "queue_routing_rule", TargetID: want[0],
			Actor: c.Actor, CorrelationID: c.CorrelationID, Before: have, After: want})
	})
	if err != nil {
		return nil, err
	}
	return s.routing.ListRules(ctx)
}

// RoutingTestResult is what TestRouting reports.
type RoutingTestResult struct {
	Rule        *RoutingRule
	Queue       Queue
	DefaultUsed bool
}

// TestRouting shows which rule would route a sample Ticket and into which Queue, without creating anything.
func (s *Service) TestRouting(ctx context.Context, p Principal, sample RoutingSample) (RoutingTestResult, error) {
	if !p.QueuesManage {
		return RoutingTestResult{}, ErrForbidden
	}
	if err := s.needRouting(); err != nil {
		return RoutingTestResult{}, err
	}
	if sample.Impact != "" && !slices.Contains(ReportImpacts, sample.Impact) {
		return RoutingTestResult{}, invalid("impact must be one of %s", strings.Join(ReportImpacts, ", "))
	}
	q, r, ok, err := s.routeQueue(ctx, sample)
	if err != nil {
		return RoutingTestResult{}, err
	}
	if ok {
		return RoutingTestResult{Rule: &r, Queue: q}, nil
	}
	def, err := s.queues.DefaultQueue(ctx)
	if err != nil {
		return RoutingTestResult{}, err
	}
	return RoutingTestResult{Queue: def, DefaultUsed: true}, nil
}
