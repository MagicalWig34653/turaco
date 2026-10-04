package application

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// commitFinding writes a changed Finding, its transition and VulnerabilityFindingChanged event (when the
// status moved) and its audit entry in the caller's transaction. The Finding must be locked.
func (s *Service) commitFinding(ctx context.Context, tx pgx.Tx, c Caller, cur, next Finding, op, auditOp, reason string, meta map[string]any) (Finding, error) {
	out, err := s.store.UpdateFindingTx(ctx, tx, next)
	if err != nil {
		return Finding{}, err
	}
	if cur.Status != out.Status {
		if err := s.store.InsertFindingTransitionTx(ctx, tx, transition(c, out.ID, &cur.Status, out.Status, op, reason)); err != nil {
			return Finding{}, err
		}
		if err := publishFinding(ctx, tx, c, out, op, &cur.Status, reason); err != nil {
			return Finding{}, err
		}
	}
	if reason != "" {
		if meta == nil {
			meta = map[string]any{}
		}
		meta["reason"] = reason
	}
	meta = withFindingRefs(meta, out)
	if err := recordAudit(ctx, tx, c, "security.finding."+auditOp, "vulnerability_finding", out.ID, findingState(&cur), findingState(&out), meta); err != nil {
		return Finding{}, err
	}
	return out, nil
}

func withFindingRefs(meta map[string]any, f Finding) map[string]any {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["advisoryId"] = f.AdvisoryID
	meta["deviceId"] = f.DeviceID
	return meta
}

func publishFinding(ctx context.Context, tx pgx.Tx, c Caller, f Finding, op string, previous *string, reason string) error {
	payload := map[string]any{"findingId": f.ID, "advisoryId": f.AdvisoryID, "deviceId": f.DeviceID, "operation": op,
		"status": f.Status, "confidence": f.Confidence}
	if previous != nil {
		payload["previousStatus"] = *previous
	}
	if reason != "" {
		payload["reason"] = reason
	}
	return publish(ctx, tx, c, EventFindingChanged, payload)
}

// clearRisk removes the risk acceptance of a finding that leaves risk_accepted.
func clearRisk(f Finding) Finding {
	f.RiskAcceptedBy, f.RiskAcceptedAt, f.RiskReviewBy = nil, nil, nil
	return f
}

// findingOp moves a Finding to status to; allowed is the caller's authority for the operation.
func (s *Service) findingOp(ctx context.Context, c Caller, allowed bool, id string, expected *int, op, auditOp, to, reason string, from ...string) (Finding, error) {
	if err := c.validate(); err != nil {
		return Finding{}, err
	}
	if !allowed {
		return Finding{}, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return Finding{}, err
	}
	var out Finding
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockFinding(ctx, tx, id, exp, op, from...)
		if err != nil {
			return err
		}
		next := clearRisk(cur)
		next.Status, next.StatusReason = to, strPtr(reason)
		out, err = s.commitFinding(ctx, tx, c, cur, next, op, auditOp, reason, nil)
		return err
	})
	return out, err
}

// Investigate starts the triage of an open finding.
func (s *Service) Investigate(ctx context.Context, c Caller, p Principal, id string, expected *int) (Finding, error) {
	return s.findingOp(ctx, c, p.Manage, id, expected, "investigate", "investigating", FindingInvestigating, "", FindingOpen)
}

// AcceptFinding acknowledges an open or investigated exposure for remediation tracking. It is distinct
// from AcceptRisk, which requires its own permission, reason and review date.
func (s *Service) AcceptFinding(ctx context.Context, c Caller, p Principal, id string, expected *int) (Finding, error) {
	return s.findingOp(ctx, c, p.Manage, id, expected, "accept", "accepted", FindingAccepted, "", FindingOpen, FindingInvestigating)
}

// PlanFindingRemediation records that the remediation of an open or investigated finding is planned.
func (s *Service) PlanFindingRemediation(ctx context.Context, c Caller, p Principal, id string, expected *int) (Finding, error) {
	return s.findingOp(ctx, c, p.Manage, id, expected, "plan_remediation", "remediation_planned", FindingRemediationPlanned, "",
		FindingOpen, FindingInvestigating, FindingAccepted)
}

// StartFindingRemediation records that the remediation of a finding started. It becomes remediated only
// when a fresh observation no longer matches, never because a task was closed.
func (s *Service) StartFindingRemediation(ctx context.Context, c Caller, p Principal, id string, expected *int) (Finding, error) {
	return s.findingOp(ctx, c, p.Manage, id, expected, "start_remediation", "remediation_started", FindingRemediating, "",
		FindingOpen, FindingInvestigating, FindingAccepted, FindingRemediationPlanned)
}

// MarkFalsePositive records that a finding does not describe a real exposure (reason code
// FalsePositiveReasons). The matching keeps it a false positive while the installation stays the same.
func (s *Service) MarkFalsePositive(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Finding, error) {
	if err := checkReason(reason, FalsePositiveReasons); err != nil {
		return Finding{}, err
	}
	return s.findingOp(ctx, c, p.Manage, id, expected, "mark_false_positive", "false_positive", FindingFalsePositive, reason, openFinding...)
}

// Reopen returns a risk-accepted or false-positive finding to open (reason code ReopenReasons). Holders of
// security.manage or security.accept_risk may reopen; a risk acceptance ends with it.
func (s *Service) Reopen(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Finding, error) {
	if err := checkReason(reason, ReopenReasons); err != nil {
		return Finding{}, err
	}
	return s.findingOp(ctx, c, p.Manage || p.AcceptRisk, id, expected, "reopen", "reopened", FindingOpen, reason, FindingRiskAccepted, FindingFalsePositive)
}

// AcceptRisk records a risk acceptance of a finding that is open, investigated, planned or being
// remediated: a reason code (AcceptRiskReasons), the accepting User and a review date after today and at
// most MaxRiskAcceptanceMonths months ahead. It needs security.accept_risk, which security.manage does
// not include (separation of triage and acceptance), and expectedVersion.
func (s *Service) AcceptRisk(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string, reviewBy time.Time) (Finding, error) {
	if err := c.validate(); err != nil {
		return Finding{}, err
	}
	if !p.AcceptRisk || p.UserID == "" || p.UserID != c.Actor.UserID {
		return Finding{}, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return Finding{}, err
	}
	if err := checkReason(reason, AcceptRiskReasons); err != nil {
		return Finding{}, err
	}
	now := s.now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	review := time.Date(reviewBy.Year(), reviewBy.Month(), reviewBy.Day(), 0, 0, 0, 0, time.UTC)
	if !review.After(today) || review.After(today.AddDate(0, MaxRiskAcceptanceMonths, 0)) {
		return Finding{}, invalid("reviewBy must be a date after today and at most %d months ahead", MaxRiskAcceptanceMonths)
	}
	var out Finding
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockFinding(ctx, tx, id, exp, "accept_risk", openFinding...)
		if err != nil {
			return err
		}
		next := cur
		user := p.UserID
		next.Status, next.StatusReason = FindingRiskAccepted, &reason
		next.RiskAcceptedBy, next.RiskAcceptedAt, next.RiskReviewBy = &user, &now, &review
		out, err = s.commitFinding(ctx, tx, c, cur, next, "accept_risk", "risk_accepted", reason, map[string]any{"reviewBy": review.Format(time.DateOnly)})
		return err
	})
	return out, err
}
