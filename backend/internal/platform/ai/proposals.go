package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// AI Proposals (F12 A-C, ADR-0029 decision 4). A write tool call creates a proposal; nothing runs until the creating
// User confirms it in Turaco with the hash of the exact parameters shown. Confirmation is single-use, the proposal
// lives 15 minutes, and the operation runs as the confirming User through the module's own public operation, with
// the module's audit entries marked via=ai and the proposal id. Confirmation is not an Approval.
//
// State machine: proposed -> confirmed -> executed | failed; proposed -> dismissed; proposed -> expired.
const (
	ProposalTTL = 15 * time.Minute
	// proposalLease bounds one execution attempt; a confirmed proposal whose lease expired may be confirmed again
	// (idempotent retry) up to maxProposalAttempts in total.
	proposalLease       = 60 * time.Second
	maxProposalAttempts = 3
	// proposalUnknownAfter: a confirmed proposal whose lease expired this long ago fails with ai.execution_unknown.
	proposalUnknownAfter = 15 * time.Minute
	// proposalKeep is how long the parameters of an ended proposal stay readable for its User; proposalDeleteAfter
	// is when the row goes (the audit keeps ids and the hash).
	proposalKeep        = 24 * time.Hour
	proposalDeleteAfter = 30 * 24 * time.Hour
	// Caps: open proposals per User and proposals per turn.
	maxOpenProposals     = 10
	maxProposalsPerTurn  = 3
	maxListedProposals   = 50
	proposalTargetMaxLen = 300
)

// Proposal states.
const (
	ProposalProposed  = "proposed"
	ProposalConfirmed = "confirmed"
	ProposalExecuted  = "executed"
	ProposalFailed    = "failed"
	ProposalDismissed = "dismissed"
	ProposalExpired   = "expired"
)

// Proposal is the User's view of an AI Proposal. Parameters are the exact stored parameters (nil once purged).
type Proposal struct {
	ID             string
	Tool           string
	Effect         WriteEffect
	Parameters     json.RawMessage
	ParametersHash string
	Target         ResourceRef
	TargetVersion  int
	TargetLabel    string
	State          string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	ConfirmedAt    *time.Time
	EndedAt        *time.Time
	Attempt        int
	OutcomeCode    string
	Result         *ResourceRef
}

var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// proposalHash binds a confirmation to the exact content: tool, target, target version and canonical parameters.
func proposalHash(tool string, target ResourceRef, version int, params json.RawMessage) (string, error) {
	canon, err := canonicalJSON(params)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(struct {
		Tool          string          `json:"tool"`
		TargetType    string          `json:"targetType"`
		TargetID      string          `json:"targetId"`
		TargetVersion int             `json:"targetVersion"`
		Parameters    json.RawMessage `json:"parameters"`
	}{tool, target.Type, strings.ToLower(target.ID), version, canon})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalJSON re-encodes JSON with sorted object keys and no insignificant whitespace (PostgreSQL jsonb reorders
// keys, so the hash is computed over this form both at creation and before execution).
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonical parameters: %w", err)
	}
	return json.Marshal(v)
}

func (s *Service) toProposal(p proposalRow) Proposal {
	out := Proposal{ID: p.ID, Tool: p.ToolName, Parameters: p.Parameters, ParametersHash: p.ParametersHash,
		Target: ResourceRef{Type: p.TargetType, ID: p.TargetID}, TargetVersion: p.TargetVersion, State: p.State,
		CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt, ConfirmedAt: p.ConfirmedAt, EndedAt: p.EndedAt, Attempt: p.Attempt}
	if t, ok := s.reg.get(p.ToolName); ok && t.tool.Write != nil {
		out.Effect = t.tool.Write.Effect
	}
	if p.TargetLabel != nil {
		out.TargetLabel = *p.TargetLabel
	}
	if p.OutcomeCode != nil {
		out.OutcomeCode = *p.OutcomeCode
	}
	if p.ResultType != nil && p.ResultID != nil {
		out.Result = &ResourceRef{Type: *p.ResultType, ID: *p.ResultID}
	}
	// An unconfirmed proposal past its TTL is expired at once, even before the job records it.
	if p.State == ProposalProposed && !p.Now.Before(p.ExpiresAt) {
		out.State = ProposalExpired
	}
	return out
}

// propose turns a write tool call into a proposal (runtime step 3): Prepare checks, as the User, that the operation
// is allowed on the target now and returns its version; the proposal stores the exact validated parameters and their
// hash. The model gets only the proposal id and the fact that the User must confirm it.
func (s *Service) propose(ctx context.Context, c Caller, ex toolExec, t registered, call ToolCall, args map[string]any, ref ResourceRef) (toolOutcome, error) {
	if ex.via != "ai" || ex.proposals == nil {
		return toolOutcome{outcome: "unknown_tool", content: errorContent("unknown_tool", "That tool is not available.")}, nil
	}
	if *ex.proposals >= maxProposalsPerTurn {
		return toolOutcome{outcome: "proposal_limit", target: &ref, content: errorContent("proposal_limit", "Too many proposals in one answer.")}, nil
	}
	hctx, cancel := context.WithTimeout(ctx, toolCallTimeout)
	defer cancel()
	prep, err := t.tool.Write.Prepare(hctx, c, call.Arguments)
	switch {
	case errors.Is(err, ErrToolNotFound):
		return toolOutcome{outcome: "not_found", target: &ref, content: errorContent("not_found", "The record was not found.")}, nil
	case errors.Is(err, ErrToolForbidden):
		return toolOutcome{outcome: "forbidden", target: &ref, content: errorContent("forbidden", "The user is not permitted to do this.")}, nil
	case errors.Is(err, ErrToolInvalid):
		return toolOutcome{outcome: "invalid_arguments", target: &ref, content: errorContent("invalid_arguments", "The arguments were not accepted.")}, nil
	case err != nil:
		s.cfg.Logger.Error("ai tool prepare failed", "tool", t.tool.Name, "request_id", c.CorrelationID, "error", err)
		return toolOutcome{outcome: "tool_error", target: &ref, content: errorContent("tool_error", "The tool failed.")}, nil
	}
	params, err := json.Marshal(args)
	if err != nil {
		return toolOutcome{}, fmt.Errorf("encode proposal parameters: %w", err)
	}
	if params, err = canonicalJSON(params); err != nil {
		return toolOutcome{}, err
	}
	hash, err := proposalHash(t.tool.Name, ref, prep.TargetVersion, params)
	if err != nil {
		return toolOutcome{}, err
	}
	row := proposalRow{TenantID: c.TenantID, UserID: c.UserID, ToolName: t.tool.Name, Parameters: params, ParametersHash: hash,
		TargetType: ref.Type, TargetID: ref.ID, TargetVersion: prep.TargetVersion}
	if ex.targetType == "ai_session" && ex.targetID != "" {
		sid := ex.targetID
		row.SessionRef = &sid
	}
	if label := strings.TrimSpace(prep.TargetLabel); label != "" {
		if r := []rune(label); len(r) > proposalTargetMaxLen {
			label = string(r[:proposalTargetMaxLen-1]) + "…"
		}
		row.TargetLabel = &label
	}
	var created proposalRow
	var ok bool
	err = s.store.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		if created, ok, err = s.store.insertProposal(ctx, tx, row, ProposalTTL, maxOpenProposals); err != nil || !ok {
			return err
		}
		meta := map[string]any{"tool": t.tool.Name, "target": map[string]any{"type": ref.Type, "id": ref.ID}, "targetVersion": prep.TargetVersion,
			"parametersHash": hash, "input": t.schema.auditInput(args), "providerId": ex.providerID, "model": ex.model}
		if row.SessionRef != nil {
			meta["sessionId"] = *row.SessionRef
		}
		return audit.Record(ctx, tx, audit.Change{Action: "ai.proposal.created", TargetType: "ai_proposal", TargetID: created.ID,
			Actor: audit.UserActor(c.UserID), CorrelationID: c.CorrelationID, Metadata: meta, Via: "ai", TenantID: c.TenantID, ProposalID: created.ID})
	})
	if err != nil {
		return toolOutcome{}, err
	}
	if !ok {
		return toolOutcome{outcome: "proposal_limit", target: &ref, content: errorContent("proposal_limit", "The user has too many open proposals.")}, nil
	}
	*ex.proposals++
	p := s.toProposal(created)
	content, _ := json.Marshal(map[string]any{
		"proposal": map[string]any{"id": created.ID, "state": ProposalProposed, "expiresInMinutes": int(ProposalTTL.Minutes())},
		"message":  "Proposed only. Nothing has been executed. The user reviews and confirms or dismisses it in Turaco; you cannot confirm it.",
	})
	return toolOutcome{outcome: "proposed", target: &ref, content: string(content), proposal: &p}, nil
}

// proposalGate is the common gate of the proposal API: AI on, ai.use, permissions reloaded now (not the snapshot of
// the HTTP authentication) and an active internal account.
func (s *Service) proposalGate(ctx context.Context, c Caller) (Caller, error) {
	if _, err := s.gate(ctx, c); err != nil {
		return Caller{}, err
	}
	fresh, err := s.freshCaller(ctx, c)
	if err != nil {
		return Caller{}, err
	}
	if s.cfg.Account != nil {
		active, external, err := s.cfg.Account(ctx, c.UserID)
		if err != nil {
			return Caller{}, fmt.Errorf("account gate: %w", err)
		}
		if !active || external {
			return Caller{}, ErrForbidden
		}
	}
	return fresh, nil
}

// Proposals lists the caller's open proposals and those that ended within the last day.
func (s *Service) Proposals(ctx context.Context, c Caller) ([]Proposal, error) {
	if _, err := s.gate(ctx, c); err != nil {
		return nil, err
	}
	rows, err := s.store.listProposals(ctx, c, maxListedProposals)
	if err != nil {
		return nil, err
	}
	out := make([]Proposal, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.toProposal(r))
	}
	return out, nil
}

// GetProposal returns one of the caller's proposals; another User's proposal is not found.
func (s *Service) GetProposal(ctx context.Context, c Caller, id string) (Proposal, error) {
	if _, err := s.gate(ctx, c); err != nil {
		return Proposal{}, err
	}
	if !uuidRE.MatchString(id) {
		return Proposal{}, ErrNotFound
	}
	p, err := s.store.findProposal(ctx, c, strings.ToLower(id))
	if err != nil {
		return Proposal{}, err
	}
	return s.toProposal(p), nil
}

// DismissProposal is proposed -> dismissed by the creating User.
func (s *Service) DismissProposal(ctx context.Context, c Caller, id string) (Proposal, error) {
	if _, err := s.gate(ctx, c); err != nil {
		return Proposal{}, err
	}
	if !uuidRE.MatchString(id) {
		return Proposal{}, ErrNotFound
	}
	id = strings.ToLower(id)
	var out proposalRow
	var ok bool
	err := s.store.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		if out, ok, err = s.store.dismissProposal(ctx, tx, c, id); err != nil || !ok {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: "ai.proposal.dismissed", TargetType: "ai_proposal", TargetID: id,
			Actor: audit.UserActor(c.UserID), CorrelationID: c.CorrelationID, Metadata: map[string]any{"tool": out.ToolName},
			Via: "ai", TenantID: c.TenantID, ProposalID: id})
	})
	if err != nil {
		return Proposal{}, err
	}
	if !ok {
		return Proposal{}, s.notPending(ctx, c, id)
	}
	return s.toProposal(out), nil
}

// notPending explains why a proposal could not be confirmed or dismissed.
func (s *Service) notPending(ctx context.Context, c Caller, id string) error {
	p, err := s.store.findProposal(ctx, c, id)
	if err != nil {
		return err
	}
	switch {
	case p.State == ProposalExpired || (p.State == ProposalProposed && !p.Now.Before(p.ExpiresAt)):
		return ErrProposalExpired
	case p.State == ProposalConfirmed:
		return ErrProposalInProgress
	default:
		return ErrProposalNotPending
	}
}

// ConfirmProposal confirms the caller's proposal with the hash of the parameters they were shown and executes it as
// the caller. The transition proposed -> confirmed is one conditional update (single-use, race-safe); the tool
// permission and the target version are re-checked right before the module call. A confirmed proposal whose earlier
// attempt left no outcome (lease expired) can be confirmed again; the module call is idempotent per proposal id.
func (s *Service) ConfirmProposal(ctx context.Context, c Caller, id, hash string) (Proposal, error) {
	fresh, err := s.proposalGate(ctx, c)
	if err != nil {
		return Proposal{}, err
	}
	if !uuidRE.MatchString(id) {
		return Proposal{}, ErrNotFound
	}
	id = strings.ToLower(id)
	if !hashRE.MatchString(hash) {
		return Proposal{}, invalid("parametersHash must be the 64-character hex hash of the proposal")
	}
	cur, err := s.store.findProposal(ctx, fresh, id)
	if err != nil {
		return Proposal{}, err
	}
	if subtle.ConstantTimeCompare([]byte(cur.ParametersHash), []byte(hash)) != 1 {
		if aerr := s.record(ctx, fresh, "ai", "ai.proposal.denied", "ai_proposal", id, map[string]any{"reason": "hash_mismatch", "tool": cur.ToolName}); aerr != nil {
			return Proposal{}, aerr
		}
		return Proposal{}, ErrProposalHashMismatch
	}

	var claimed proposalRow
	var token string
	var ok, retry bool
	err = s.store.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		claimed, token, ok, err = s.store.claimProposal(ctx, tx, fresh, id, hash, proposalLease)
		if err != nil {
			return err
		}
		if !ok {
			if claimed, token, ok, err = s.store.reclaimProposal(ctx, tx, fresh, id, hash, proposalLease, maxProposalAttempts); err != nil || !ok {
				return err
			}
			retry = true
		}
		action := "ai.proposal.confirmed"
		if retry {
			action = "ai.proposal.retried"
		}
		if err := audit.Record(ctx, tx, audit.Change{Action: action, TargetType: "ai_proposal", TargetID: id, Actor: audit.UserActor(fresh.UserID),
			CorrelationID: fresh.CorrelationID, Via: "ai", TenantID: fresh.TenantID, ProposalID: id,
			Metadata: map[string]any{"tool": claimed.ToolName, "parametersHash": claimed.ParametersHash, "attempt": claimed.Attempt,
				"target": map[string]any{"type": claimed.TargetType, "id": claimed.TargetID}, "targetVersion": claimed.TargetVersion}}); err != nil {
			return err
		}
		if retry {
			return nil
		}
		actor := fresh.UserID
		return events.Publish(ctx, tx, events.Publication{Type: "AIProposalConfirmed", ActorID: &actor, CorrelationID: fresh.CorrelationID,
			Payload: map[string]any{"proposalId": id, "tool": claimed.ToolName, "targetType": claimed.TargetType, "targetId": claimed.TargetID}})
	})
	if err != nil {
		return Proposal{}, err
	}
	if !ok {
		return Proposal{}, s.notPending(ctx, fresh, id)
	}
	return s.executeProposal(ctx, fresh, claimed, token)
}

// executeProposal runs a claimed proposal through the module's public operation as the confirming User.
func (s *Service) executeProposal(ctx context.Context, c Caller, p proposalRow, token string) (Proposal, error) {
	fail := func(outcome string) (Proposal, error) {
		return s.endProposal(ctx, c, p, token, ProposalFailed, outcome, nil)
	}
	t, ok := s.reg.get(p.ToolName)
	if !ok || t.tool.Risk != RiskWrite || t.tool.Write == nil {
		return fail("tool_unavailable")
	}
	// Re-checks immediately before the module call: the tool permission now (not at proposal time) and the
	// integrity of the stored parameters against the hash the User confirmed.
	if !c.Has(t.tool.Permission) {
		return fail("permission_denied")
	}
	if p.Parameters == nil {
		return fail("parameters_tampered")
	}
	if h, err := proposalHash(p.ToolName, ResourceRef{Type: p.TargetType, ID: p.TargetID}, p.TargetVersion, p.Parameters); err != nil ||
		subtle.ConstantTimeCompare([]byte(h), []byte(p.ParametersHash)) != 1 {
		return fail("parameters_tampered")
	}
	if _, err := t.schema.validate(p.Parameters); err != nil {
		return fail("parameters_tampered")
	}
	actx := audit.WithAssist(ctx, audit.Assist{Via: "ai", TenantID: c.TenantID, ProposalID: p.ID})
	hctx, cancel := context.WithTimeout(actx, toolCallTimeout)
	defer cancel()
	res, err := t.tool.Write.Execute(hctx, c, p.Parameters, Execution{ProposalID: p.ID, IdempotencyKey: p.ID, TargetVersion: p.TargetVersion})
	switch {
	case errors.Is(err, ErrToolStale):
		return fail("ai.proposal_stale")
	case errors.Is(err, ErrToolForbidden):
		return fail("forbidden")
	case errors.Is(err, ErrToolNotFound):
		return fail("not_found")
	case errors.Is(err, ErrToolInvalid):
		return fail("rejected")
	case err != nil:
		// The outcome is unknown: the proposal stays confirmed until its lease expires; the User may confirm again
		// (idempotent), otherwise the expiry job fails it with ai.execution_unknown.
		s.cfg.Logger.Error("ai proposal execution failed", "proposal_id", p.ID, "tool", p.ToolName, "request_id", c.CorrelationID, "error", err)
		return Proposal{}, ErrExecutionUnknown
	}
	if !targetRE.MatchString(res.Result.Type) || !uuidRE.MatchString(res.Result.ID) {
		s.cfg.Logger.Error("ai proposal execution returned an invalid result reference", "proposal_id", p.ID, "tool", p.ToolName)
		return Proposal{}, ErrExecutionUnknown
	}
	return s.endProposal(ctx, c, p, token, ProposalExecuted, "ok", &ResourceRef{Type: res.Result.Type, ID: strings.ToLower(res.Result.ID)})
}

// endProposal records the terminal outcome, its audit entry and event in one transaction. If another attempt took
// the claim over, its outcome stands and is returned.
func (s *Service) endProposal(ctx context.Context, c Caller, p proposalRow, token, state, outcome string, result *ResourceRef) (Proposal, error) {
	var out proposalRow
	var ok bool
	err := s.store.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		if out, ok, err = s.store.finishProposal(ctx, tx, p.ID, token, state, outcome, result); err != nil || !ok {
			return err
		}
		meta := map[string]any{"tool": p.ToolName, "outcome": outcome, "attempt": p.Attempt, "parametersHash": p.ParametersHash,
			"target": map[string]any{"type": p.TargetType, "id": p.TargetID}, "targetVersion": p.TargetVersion}
		payload := map[string]any{"proposalId": p.ID, "tool": p.ToolName}
		event, action := "AIProposalFailed", "ai.proposal.failed"
		if state == ProposalExecuted {
			event, action = "AIProposalExecuted", "ai.proposal.executed"
			meta["result"] = map[string]any{"type": result.Type, "id": result.ID}
			payload["targetType"], payload["targetId"], payload["resultType"], payload["resultId"] = p.TargetType, p.TargetID, result.Type, result.ID
		} else {
			payload["outcome"] = outcome
		}
		if err := audit.Record(ctx, tx, audit.Change{Action: action, TargetType: "ai_proposal", TargetID: p.ID, Actor: audit.UserActor(c.UserID),
			CorrelationID: c.CorrelationID, Metadata: meta, Via: "ai", TenantID: c.TenantID, ProposalID: p.ID}); err != nil {
			return err
		}
		actor := c.UserID
		return events.Publish(ctx, tx, events.Publication{Type: event, ActorID: &actor, CorrelationID: c.CorrelationID, Payload: payload})
	})
	if err != nil {
		return Proposal{}, err
	}
	if !ok {
		cur, err := s.store.findProposal(ctx, c, p.ID)
		if err != nil {
			return Proposal{}, err
		}
		return s.toProposal(cur), nil
	}
	return s.toProposal(out), nil
}

// Job type and schedule of the proposal expiry (registered by the worker; runs whether or not AI is enabled).
const (
	ProposalsExpireJobType    = "ai.proposals.expire"
	ProposalsExpireJobTimeout = time.Minute
	ProposalsExpireInterval   = time.Minute
)

// HandleProposalsExpire expires unconfirmed proposals past their TTL, fails confirmed ones whose outcome stayed
// unknown, removes parameters a day after a proposal ended and deletes old rows. One counts-only audit summary and
// an AIProposalFailed event per failed proposal.
func (s *Service) HandleProposalsExpire(ctx context.Context, job jobs.Job) error {
	return s.store.inTx(ctx, func(tx pgx.Tx) error {
		sw, err := s.store.sweepProposals(ctx, tx, proposalUnknownAfter, proposalKeep, proposalDeleteAfter, maxProposalAttempts)
		if err != nil {
			return err
		}
		corr := "job:" + job.ID
		for _, p := range sw.Unknown {
			if err := events.Publish(ctx, tx, events.Publication{Type: "AIProposalFailed", CorrelationID: corr,
				Payload: map[string]any{"proposalId": p.ID, "tool": p.ToolName, "outcome": "ai.execution_unknown"}}); err != nil {
				return err
			}
		}
		if len(sw.Expired)+len(sw.Unknown) == 0 && sw.Purged+sw.Deleted == 0 {
			return nil
		}
		ids := func(rows []proposalRow) []string {
			out := make([]string, 0, len(rows))
			for _, r := range rows {
				out = append(out, r.ID)
			}
			return out
		}
		return audit.Record(ctx, tx, audit.Change{Action: "ai.proposals.swept", TargetType: "ai_proposals", TargetID: "job",
			Actor: audit.SystemActor("ai-proposals"), CorrelationID: corr,
			Metadata: map[string]any{"expired": ids(sw.Expired), "executionUnknown": ids(sw.Unknown), "parametersPurged": sw.Purged, "deleted": sw.Deleted}})
	})
}
