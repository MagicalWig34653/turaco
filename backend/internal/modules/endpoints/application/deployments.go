package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

// Deployment planning (F9 G2). A Deployment is the Desired Software State of one approved Software Version with
// one intent, rolled out in ordered Deployment Rings. Planning statuses: draft -> pending_approval -> approved ->
// scheduled; draft -> scheduled for plans that are not high impact; cancelled from every planning status.
// Changes need deployments.manage; high-impact plans (a ring targets all Devices, uninstall, supersede) also
// deployments.high_impact and an approved plan Approval (subject deployment) whose approver is neither the owner,
// the creator, an editor nor the submitter. Audit actions endpoints.deployment.<op> carry ids, statuses, reason
// codes, flags and counts only.

var deploymentSystem = audit.SystemActor("deployment-planning")

// ErrHighImpactForbidden means the operation makes or keeps a plan high impact and the caller lacks
// deployments.high_impact.
var ErrHighImpactForbidden = fmt.Errorf("%w: deployments.high_impact is required", ErrForbidden)

// changeWindowOpen reports a Change that is approved or scheduled and whose maintenance window has not ended.
func changeWindowOpen(cw ChangeWindow, ok bool, now time.Time) bool {
	return ok && (cw.Status == "approved" || cw.Status == "scheduled") && cw.WindowStart != nil && cw.WindowEnd != nil && cw.WindowEnd.After(now)
}

func highImpact(d Deployment, rings []DeploymentRing, sets map[string]TargetSet) bool {
	if d.Intent == IntentUninstall || d.Supersede {
		return true
	}
	for _, r := range rings {
		if t, ok := sets[r.TargetSetID]; ok && t.AllDevices {
			return true
		}
	}
	return false
}

// planSHA256 binds a plan: version, intent, supersede and every ring with its gates and the version of its Target
// Set. A change of any of them after submission is detected at scheduling.
func planSHA256(d Deployment, rings []DeploymentRing, sets map[string]TargetSet) string {
	parts := []string{"turaco.deployment_plan.v1", d.SoftwareVersionID, d.Intent, strconv.FormatBool(d.Supersede)}
	for _, r := range rings {
		fresh, change := "", ""
		if r.MinFreshEvidencePercent != nil {
			fresh = strconv.Itoa(*r.MinFreshEvidencePercent)
		}
		if r.ChangeID != nil {
			change = *r.ChangeID
		}
		parts = append(parts, strconv.Itoa(r.Position), r.TargetSetID, strconv.Itoa(sets[r.TargetSetID].Version), strconv.FormatBool(r.ApprovalRequired),
			strconv.Itoa(r.SuccessThresholdPercent), fresh, strconv.Itoa(r.SoakMinutes), change, strconv.FormatBool(r.NoWindowRequired), strconv.Itoa(r.MaxTargets))
	}
	return sha256Hex(strings.Join(parts, "\x00"))
}

func deploymentState(d Deployment) map[string]any {
	return map[string]any{"status": d.Status, "statusReason": d.StatusReason, "version": d.Version, "highImpact": d.HighImpact,
		"softwareVersionId": d.SoftwareVersionID, "intent": d.Intent, "supersede": d.Supersede}
}

func addEditor(d Deployment, user string) Deployment {
	if user == "" || slices.Contains(d.Editors, user) || len(d.Editors) >= maxEditors {
		return d
	}
	d.Editors = append(slices.Clone(d.Editors), user)
	return d
}

func (s *Service) deploymentPreamble(c Caller, p Principal, expected *int, id string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !p.DeploymentsManage {
		return ErrForbidden
	}
	if c.Actor.UserID == "" {
		return invalid("deployments are planned by a person")
	}
	if expected == nil && id != "" {
		return invalid("expectedVersion is required")
	}
	if id != "" && !validUUID(id) {
		return ErrNotFound
	}
	return nil
}

// commitDeployment stores the next state with version+1, appends a transition when the status changed and audits.
func (s *Service) commitDeployment(ctx context.Context, tx pgx.Tx, c Caller, cur, next Deployment, op, reason string, meta map[string]any) (Deployment, error) {
	updated, err := s.store.UpdateDeploymentTx(ctx, tx, next)
	if err != nil {
		return Deployment{}, err
	}
	if cur.Status != updated.Status {
		from := cur.Status
		t := DeploymentTransition{DeploymentID: cur.ID, FromStatus: &from, ToStatus: updated.Status, Operation: op, Reason: strPtr(reason),
			PlanSHA256: updated.PlanSHA256, CorrelationID: c.CorrelationID}
		t.ActorUserID, t.ActorSystem = softwareActor(c)
		if err := s.store.AppendDeploymentTransitionTx(ctx, tx, t); err != nil {
			return Deployment{}, err
		}
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["reference"] = updated.Reference
	if reason != "" {
		meta["reason"] = reason
	}
	return updated, audit.Record(ctx, tx, audit.Change{Action: "endpoints.deployment." + op, TargetType: "deployment", TargetID: cur.ID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: deploymentState(cur), After: deploymentState(updated), Metadata: meta})
}

// versionGate checks (FOR SHARE) that the version and its product are approved.
func (s *Service) versionGate(ctx context.Context, tx pgx.Tx, versionID string) (SoftwareVersion, error) {
	v, err := s.store.ShareVersionTx(ctx, tx, versionID)
	if errors.Is(err, ErrNotFound) {
		return SoftwareVersion{}, invalid("softwareVersionId must name an existing software version")
	}
	if err != nil {
		return SoftwareVersion{}, err
	}
	if v.ApprovalStatus != VersionApproved {
		return SoftwareVersion{}, &GateError{Code: IssueVersionNotApproved}
	}
	prod, err := s.store.ShareProductTx(ctx, tx, v.ProductID)
	if err != nil {
		return SoftwareVersion{}, err
	}
	if prod.ApprovalStatus != ProductApproved {
		return SoftwareVersion{}, &GateError{Code: IssueProductNotApproved}
	}
	return v, nil
}

func validDeploymentInput(in DeploymentInput) (DeploymentInput, error) {
	name, ok := cleanName(in.Name, 150)
	if !ok {
		return DeploymentInput{}, invalid("name must be 1-150 characters without control or invisible formatting characters")
	}
	if !slices.Contains(DeploymentIntents, in.Intent) {
		return DeploymentInput{}, invalid("intent must be one of %s", strings.Join(DeploymentIntents, ", "))
	}
	if !validUUID(in.SoftwareVersionID) {
		return DeploymentInput{}, invalid("softwareVersionId must be a software version id")
	}
	in.Name, in.SoftwareVersionID = name, strings.ToLower(in.SoftwareVersionID)
	return in, nil
}

// CreateDeployment creates a draft plan for an approved version of an approved product. Uninstall and supersede
// are high impact and need deployments.high_impact. Requires deployments.manage.
func (s *Service) CreateDeployment(ctx context.Context, c Caller, p Principal, in DeploymentInput) (Deployment, error) {
	if err := s.deploymentPreamble(c, p, nil, ""); err != nil {
		return Deployment{}, err
	}
	in, err := validDeploymentInput(in)
	if err != nil {
		return Deployment{}, err
	}
	if (in.Intent == IntentUninstall || in.Supersede) && !p.DeploymentsHighImpact {
		return Deployment{}, ErrHighImpactForbidden
	}
	owner, err := s.ownerOf(ctx, c, in.OwnerUserID)
	if err != nil {
		return Deployment{}, err
	}
	var out Deployment
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := s.versionGate(ctx, tx, in.SoftwareVersionID); err != nil {
			return err
		}
		d := Deployment{Name: in.Name, SoftwareVersionID: in.SoftwareVersionID, Intent: in.Intent, Supersede: in.Supersede, Status: DeploymentDraft,
			OwnerUserID: owner, CreatedBy: c.Actor.UserID, Editors: []string{c.Actor.UserID}}
		d.HighImpact = highImpact(d, nil, nil)
		if out, err = s.store.InsertDeploymentTx(ctx, tx, d); err != nil {
			return err
		}
		t := DeploymentTransition{DeploymentID: out.ID, ToStatus: DeploymentDraft, Operation: "created", CorrelationID: c.CorrelationID}
		t.ActorUserID, t.ActorSystem = softwareActor(c)
		if err := s.store.AppendDeploymentTransitionTx(ctx, tx, t); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: "endpoints.deployment.created", TargetType: "deployment", TargetID: out.ID, Actor: c.Actor,
			CorrelationID: c.CorrelationID, After: deploymentState(out), Metadata: map[string]any{"reference": out.Reference}})
	})
	return out, err
}

// draftEdit locks a draft plan, checks expectedVersion, lets edit change it (and its rings) and commits it with
// the caller added to the editors and high impact recomputed. A plan that becomes or stays high impact needs
// deployments.high_impact.
func (s *Service) draftEdit(ctx context.Context, c Caller, p Principal, id string, expected *int, op string,
	edit func(tx pgx.Tx, cur Deployment, next *Deployment, meta map[string]any) error) (Deployment, error) {
	if err := s.deploymentPreamble(c, p, expected, id); err != nil {
		return Deployment{}, err
	}
	var out Deployment
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockDeploymentTx(ctx, tx, strings.ToLower(id))
		if err != nil {
			return err
		}
		if *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Status != DeploymentDraft {
			return &InvalidTransitionError{Operation: op, From: cur.Status}
		}
		next := addEditor(cur, c.Actor.UserID)
		meta := map[string]any{}
		if err := edit(tx, cur, &next, meta); err != nil {
			return err
		}
		rings, err := s.store.RingsTx(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		sets, err := s.store.ShareTargetSetsTx(ctx, tx, ringSetIDs(rings))
		if err != nil {
			return err
		}
		next.HighImpact = highImpact(next, rings, sets)
		if next.HighImpact && !p.DeploymentsHighImpact {
			return ErrHighImpactForbidden
		}
		meta["ringCount"] = len(rings)
		out, err = s.commitDeployment(ctx, tx, c, cur, next, op, "", meta)
		return err
	})
	return out, err
}

func ringSetIDs(rings []DeploymentRing) []string {
	ids := make([]string, 0, len(rings))
	for _, r := range rings {
		ids = append(ids, r.TargetSetID)
	}
	return uniq(ids)
}

// UpdateDeployment changes a draft plan's name, version, intent, supersede and owner. Requires
// deployments.manage and expectedVersion.
func (s *Service) UpdateDeployment(ctx context.Context, c Caller, p Principal, id string, expected *int, in DeploymentInput) (Deployment, error) {
	in, err := validDeploymentInput(in)
	if err != nil {
		return Deployment{}, err
	}
	var owner string
	if in.OwnerUserID != "" {
		if owner, err = s.ownerOf(ctx, c, in.OwnerUserID); err != nil {
			return Deployment{}, err
		}
	}
	return s.draftEdit(ctx, c, p, id, expected, "updated", func(tx pgx.Tx, _ Deployment, next *Deployment, _ map[string]any) error {
		if _, err := s.versionGate(ctx, tx, in.SoftwareVersionID); err != nil {
			return err
		}
		next.Name, next.SoftwareVersionID, next.Intent, next.Supersede = in.Name, in.SoftwareVersionID, in.Intent, in.Supersede
		if owner != "" {
			next.OwnerUserID = owner
		}
		return nil
	})
}

// validRing checks a ring's input for position pos and its Target Set (FOR SHARE: exists and is not archived;
// a set of all Devices needs deployments.high_impact) and Change window.
func (s *Service) validRing(ctx context.Context, tx pgx.Tx, p Principal, in RingInput, pos int) (DeploymentRing, error) {
	name, ok := cleanName(in.Name, 100)
	if !ok {
		return DeploymentRing{}, invalid("ring name must be 1-100 characters without control or invisible formatting characters")
	}
	if in.SuccessThresholdPercent < 1 || in.SuccessThresholdPercent > 100 {
		return DeploymentRing{}, invalid("successThresholdPercent must be between 1 and 100")
	}
	if f := in.MinFreshEvidencePercent; f != nil && (*f < 1 || *f > 100) {
		return DeploymentRing{}, invalid("minFreshEvidencePercent must be between 1 and 100")
	}
	if in.SoakMinutes < 0 || in.SoakMinutes > MaxSoakMinutes {
		return DeploymentRing{}, invalid("soakMinutes must be between 0 and %d", MaxSoakMinutes)
	}
	maxTargets := MaxTargetDevices
	if in.MaxTargets != nil {
		if *in.MaxTargets < 1 || *in.MaxTargets > MaxTargetDevices {
			return DeploymentRing{}, invalid("maxTargets must be between 1 and %d", MaxTargetDevices)
		}
		maxTargets = *in.MaxTargets
	}
	if in.NoWindowRequired && pos != 1 {
		return DeploymentRing{}, invalid("only the pilot ring (position 1) may run without a maintenance window")
	}
	if in.NoWindowRequired && in.ChangeID != nil {
		return DeploymentRing{}, invalid("a ring either has a change window or runs without one")
	}
	if !validUUID(in.TargetSetID) {
		return DeploymentRing{}, invalid("targetSetId must be a target set id")
	}
	r := DeploymentRing{Position: pos, Name: name, TargetSetID: strings.ToLower(in.TargetSetID), ApprovalRequired: in.ApprovalRequired,
		SuccessThresholdPercent: in.SuccessThresholdPercent, MinFreshEvidencePercent: in.MinFreshEvidencePercent, SoakMinutes: in.SoakMinutes,
		NoWindowRequired: in.NoWindowRequired, MaxTargets: maxTargets}
	sets, err := s.store.ShareTargetSetsTx(ctx, tx, []string{r.TargetSetID})
	if err != nil {
		return DeploymentRing{}, err
	}
	set, ok := sets[r.TargetSetID]
	if !ok {
		return DeploymentRing{}, invalid("targetSetId must name an existing target set")
	}
	if set.ArchivedAt != nil {
		return DeploymentRing{}, &GateError{Code: IssueTargetSetArchived}
	}
	if set.AllDevices && !p.DeploymentsHighImpact {
		return DeploymentRing{}, ErrHighImpactForbidden
	}
	if in.ChangeID != nil {
		if !validUUID(*in.ChangeID) {
			return DeploymentRing{}, invalid("changeId must be a change id")
		}
		id := strings.ToLower(*in.ChangeID)
		found, err := s.changes.Lookup(ctx, []string{id})
		if err != nil {
			return DeploymentRing{}, err
		}
		cw, ok := found[id]
		if !changeWindowOpen(cw, ok, s.now()) {
			return DeploymentRing{}, &GateError{Code: IssueChangeWindowInvalid}
		}
		r.ChangeID = &id
	}
	return r, nil
}

// AddRing appends a ring to a draft plan (the first ring is the pilot). Requires deployments.manage and the plan's
// expectedVersion.
func (s *Service) AddRing(ctx context.Context, c Caller, p Principal, id string, expected *int, in RingInput) (Deployment, DeploymentRing, error) {
	var ring DeploymentRing
	d, err := s.draftEdit(ctx, c, p, id, expected, "ring_added", func(tx pgx.Tx, cur Deployment, _ *Deployment, meta map[string]any) error {
		rings, err := s.store.RingsTx(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		if len(rings) >= MaxRings {
			return invalid("a deployment has at most %d rings", MaxRings)
		}
		r, err := s.validRing(ctx, tx, p, in, len(rings)+1)
		if err != nil {
			return err
		}
		r.DeploymentID = cur.ID
		if ring, err = s.store.InsertRingTx(ctx, tx, r); err != nil {
			return err
		}
		meta["ringId"], meta["position"], meta["targetSetId"] = ring.ID, ring.Position, ring.TargetSetID
		return nil
	})
	return d, ring, err
}

func findRing(rings []DeploymentRing, ringID string) (DeploymentRing, bool) {
	for _, r := range rings {
		if strings.EqualFold(r.ID, ringID) {
			return r, true
		}
	}
	return DeploymentRing{}, false
}

// UpdateRing replaces a ring's name, Target Set and gate configuration. Requires deployments.manage and the
// plan's expectedVersion.
func (s *Service) UpdateRing(ctx context.Context, c Caller, p Principal, id, ringID string, expected *int, in RingInput) (Deployment, DeploymentRing, error) {
	var ring DeploymentRing
	d, err := s.draftEdit(ctx, c, p, id, expected, "ring_updated", func(tx pgx.Tx, cur Deployment, _ *Deployment, meta map[string]any) error {
		rings, err := s.store.RingsTx(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		old, ok := findRing(rings, ringID)
		if !ok {
			return ErrNotFound
		}
		r, err := s.validRing(ctx, tx, p, in, old.Position)
		if err != nil {
			return err
		}
		r.ID, r.DeploymentID = old.ID, cur.ID
		if ring, err = s.store.UpdateRingTx(ctx, tx, r); err != nil {
			return err
		}
		meta["ringId"], meta["position"], meta["targetSetId"] = ring.ID, ring.Position, ring.TargetSetID
		return nil
	})
	return d, ring, err
}

// RemoveRing deletes a ring of a draft plan; the later rings move up. Requires deployments.manage and the plan's
// expectedVersion.
func (s *Service) RemoveRing(ctx context.Context, c Caller, p Principal, id, ringID string, expected *int) (Deployment, error) {
	return s.draftEdit(ctx, c, p, id, expected, "ring_removed", func(tx pgx.Tx, cur Deployment, _ *Deployment, meta map[string]any) error {
		rings, err := s.store.RingsTx(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		old, ok := findRing(rings, ringID)
		if !ok {
			return ErrNotFound
		}
		if err := s.store.DeleteRingTx(ctx, tx, old.ID); err != nil {
			return err
		}
		for _, r := range rings {
			if r.Position > old.Position {
				if r.NoWindowRequired {
					return invalid("only the pilot ring (position 1) may run without a maintenance window")
				}
				r.Position--
				if _, err := s.store.UpdateRingTx(ctx, tx, r); err != nil {
					return err
				}
			}
		}
		meta["ringId"], meta["position"] = old.ID, old.Position
		return nil
	})
}

// ReorderRings sets the ring order: ringIDs lists every ring of the plan exactly once, pilot first. Requires
// deployments.manage and the plan's expectedVersion.
func (s *Service) ReorderRings(ctx context.Context, c Caller, p Principal, id string, expected *int, ringIDs []string) (Deployment, error) {
	return s.draftEdit(ctx, c, p, id, expected, "rings_reordered", func(tx pgx.Tx, cur Deployment, _ *Deployment, meta map[string]any) error {
		rings, err := s.store.RingsTx(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		if len(ringIDs) != len(rings) {
			return invalid("ringIds must list every ring of the deployment exactly once")
		}
		seen := map[string]bool{}
		for i, rid := range ringIDs {
			r, ok := findRing(rings, rid)
			if !ok || seen[r.ID] {
				return invalid("ringIds must list every ring of the deployment exactly once")
			}
			seen[r.ID] = true
			if r.NoWindowRequired && i != 0 {
				return invalid("only the pilot ring (position 1) may run without a maintenance window")
			}
			if r.Position != i+1 {
				r.Position = i + 1
				if _, err := s.store.UpdateRingTx(ctx, tx, r); err != nil {
					return err
				}
			}
		}
		meta["ringCount"] = len(rings)
		return nil
	})
}

// ---- validation ----

// structuralIssues runs the checks that need no evaluation, inside tx (Target Sets, version and product FOR
// SHARE): version and product approval, package gates, rings, Target Sets and Change windows, and whether an
// approval is still required.
func (s *Service) structuralIssues(ctx context.Context, tx pgx.Tx, d Deployment, rings []DeploymentRing) ([]PlanIssue, bool, map[string]TargetSet, error) {
	var issues []PlanIssue
	block := func(code string, ring *string) {
		issues = append(issues, PlanIssue{Code: code, Blocking: true, RingID: ring})
	}
	sets, err := s.store.ShareTargetSetsTx(ctx, tx, ringSetIDs(rings))
	if err != nil {
		return nil, false, nil, err
	}
	if _, err := s.versionGate(ctx, tx, d.SoftwareVersionID); err != nil {
		var g *GateError
		if !errors.As(err, &g) {
			return nil, false, nil, err
		}
		block(g.Code, nil)
	}
	pkgs, err := s.store.ListSoftwarePackages(ctx, SoftwarePackageFilter{VersionID: d.SoftwareVersionID, Page: Page{Limit: MaxLimit}})
	if err != nil {
		return nil, false, nil, err
	}
	published := false
	for _, pk := range pkgs.Items {
		if pk.HashMismatch || pk.PublishedAfterRevoke || pk.VersionRevoked || pk.ProductBlocked {
			block(IssuePackageGateClosed, nil)
			break
		}
		published = published || pk.PublishedAt != nil
	}
	if !published && d.Intent != IntentUninstall {
		issues = append(issues, PlanIssue{Code: IssuePackageNotPublished})
	}
	if len(rings) == 0 {
		block(IssueNoRings, nil)
	}
	var changeIDs []string
	for _, r := range rings {
		if r.ChangeID != nil {
			changeIDs = append(changeIDs, *r.ChangeID)
		}
	}
	windows := map[string]ChangeWindow{}
	if len(changeIDs) > 0 {
		if windows, err = s.changes.Lookup(ctx, uniq(changeIDs)); err != nil {
			return nil, false, nil, err
		}
	}
	now := s.now()
	for _, r := range rings {
		rid := r.ID
		if t, ok := sets[r.TargetSetID]; !ok || t.ArchivedAt != nil {
			block(IssueTargetSetArchived, &rid)
		}
		switch {
		case r.ChangeID != nil:
			cw, ok := windows[*r.ChangeID]
			if !changeWindowOpen(cw, ok, now) {
				block(IssueChangeWindowInvalid, &rid)
			}
		case !(r.NoWindowRequired && r.Position == 1):
			block(IssueWindowRequired, &rid)
		}
	}
	hi := highImpact(d, rings, sets)
	if hi {
		issues = append(issues, PlanIssue{Code: IssueHighImpact})
		if d.Status == DeploymentDraft || d.Status == DeploymentPendingApproval {
			block(IssueApprovalRequired, nil)
		}
	}
	return issues, hi, sets, nil
}

func blocking(issues []PlanIssue, ignore ...string) []PlanIssue {
	var out []PlanIssue
	for _, i := range issues {
		if i.Blocking && !slices.Contains(ignore, i.Code) {
			out = append(out, i)
		}
	}
	return out
}

func validationOf(issues []PlanIssue, hi bool, now time.Time) PlanValidation {
	if issues == nil {
		issues = []PlanIssue{}
	}
	return PlanValidation{Valid: len(blocking(issues)) == 0, HighImpact: hi, Issues: issues, Rings: []RingTargets{}, ValidatedAt: now}
}

// evaluationIssues evaluates each ring's Target Set (bounded) and compares the result with the ring's cap, and
// warns about other submitted, approved or scheduled plans of the same product that reach the same Devices.
func (s *Service) evaluationIssues(ctx context.Context, d Deployment, rings []DeploymentRing, sets map[string]TargetSet) ([]PlanIssue, []RingTargets, error) {
	cache := map[string]TargetEvaluation{}
	eval := func(ctx context.Context, set TargetSet) (TargetEvaluation, error) {
		if ev, ok := cache[set.ID]; ok {
			return ev, nil
		}
		ev, err := s.evaluateDefinition(ctx, set.Definition)
		if err == nil {
			cache[set.ID] = ev
		}
		return ev, err
	}
	var issues []PlanIssue
	counts := []RingTargets{}
	ours := map[string]bool{}
	err := s.store.ReadSnapshot(ctx, func(ctx context.Context) error {
		for _, r := range rings {
			set, ok := sets[r.TargetSetID]
			if !ok {
				continue
			}
			ev, err := eval(ctx, set)
			if err != nil {
				return err
			}
			rid := r.ID
			n := ev.Matched
			counts = append(counts, RingTargets{RingID: r.ID, Matched: ev.Matched, Truncated: ev.Truncated, Incomplete: ev.Incomplete})
			switch {
			case ev.Truncated || ev.Matched > r.MaxTargets:
				issues = append(issues, PlanIssue{Code: IssueTooManyTargets, Blocking: true, RingID: &rid, Count: &n})
			case ev.Matched == 0:
				issues = append(issues, PlanIssue{Code: IssueNoTargets, RingID: &rid})
			}
			if ev.Incomplete {
				issues = append(issues, PlanIssue{Code: IssueEvaluationIncomplete, RingID: &rid})
			}
			for _, id := range ev.deviceIDs {
				ours[id] = true
			}
		}
		v, err := s.store.GetSoftwareVersion(ctx, d.SoftwareVersionID)
		if err != nil {
			return err
		}
		others, err := s.store.ActiveDeploymentsOfProduct(ctx, v.ProductID, d.ID, maxOverlapDeployments)
		if err != nil {
			return err
		}
		for _, o := range others {
			oRings, err := s.store.Rings(ctx, o.ID)
			if err != nil {
				return err
			}
			overlap := map[string]bool{}
			for _, r := range oRings {
				set, err := s.store.GetTargetSet(ctx, r.TargetSetID)
				if err != nil {
					return err
				}
				ev, err := eval(ctx, set)
				if err != nil {
					return err
				}
				for _, id := range ev.deviceIDs {
					if ours[id] {
						overlap[id] = true
					}
				}
			}
			if n := len(overlap); n > 0 {
				oid := o.ID
				issues = append(issues, PlanIssue{Code: IssueOverlap, Count: &n, DeploymentID: &oid})
			}
		}
		return nil
	})
	return issues, counts, err
}

// fullValidation runs the structural checks in a short transaction, then evaluates the rings.
func (s *Service) fullValidation(ctx context.Context, id string) (Deployment, PlanValidation, error) {
	var d Deployment
	var rings []DeploymentRing
	var issues []PlanIssue
	var hi bool
	var sets map[string]TargetSet
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		if d, err = s.store.GetDeployment(ctx, id); err != nil {
			return err
		}
		if rings, err = s.store.RingsTx(ctx, tx, d.ID); err != nil {
			return err
		}
		issues, hi, sets, err = s.structuralIssues(ctx, tx, d, rings)
		return err
	})
	if err != nil {
		return Deployment{}, PlanValidation{}, err
	}
	more, counts, err := s.evaluationIssues(ctx, d, rings, sets)
	if err != nil {
		return Deployment{}, PlanValidation{}, err
	}
	v := validationOf(append(issues, more...), hi, s.now())
	v.Rings, v.Evaluated = counts, true
	return d, v, nil
}

// ValidateDeployment validates a plan now: blocking issues, warnings and the evaluated target count of every ring.
// Requires deployments.manage.
func (s *Service) ValidateDeployment(ctx context.Context, p Principal, id string) (PlanValidation, error) {
	if !p.DeploymentsManage {
		return PlanValidation{}, ErrForbidden
	}
	if !validUUID(id) {
		return PlanValidation{}, ErrNotFound
	}
	_, v, err := s.fullValidation(ctx, strings.ToLower(id))
	return v, err
}

// lifecycle locks a plan in one of from, re-runs the structural checks in the transaction and lets apply decide.
func (s *Service) lifecycle(ctx context.Context, c Caller, id string, expected *int, op string, from []string,
	apply func(tx pgx.Tx, cur Deployment, rings []DeploymentRing, issues []PlanIssue, hi bool, sets map[string]TargetSet, next *Deployment) error) (Deployment, error) {
	var out Deployment
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockDeploymentTx(ctx, tx, strings.ToLower(id))
		if err != nil {
			return err
		}
		if *expected != cur.Version {
			return ErrVersionConflict
		}
		if !slices.Contains(from, cur.Status) {
			return &InvalidTransitionError{Operation: op, From: cur.Status}
		}
		rings, err := s.store.RingsTx(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		issues, hi, sets, err := s.structuralIssues(ctx, tx, cur, rings)
		if err != nil {
			return err
		}
		next := cur
		next.HighImpact = hi
		if err := apply(tx, cur, rings, issues, hi, sets, &next); err != nil {
			return err
		}
		meta := map[string]any{"ringCount": len(rings), "highImpact": hi}
		out, err = s.commitDeployment(ctx, tx, c, cur, next, op, "", meta)
		return err
	})
	return out, err
}

// SubmitDeployment asks for the plan Approval of a high-impact draft: the plan must have no blocking issue, the
// approver is exactly one User or Team and can never be the owner, the creator, an editor or the submitter. The
// Approval is bound to the plan hash. Requires deployments.manage, deployments.high_impact and expectedVersion.
func (s *Service) SubmitDeployment(ctx context.Context, c Caller, p Principal, id string, expected *int, approver Approver) (Deployment, error) {
	if err := s.deploymentPreamble(c, p, expected, id); err != nil {
		return Deployment{}, err
	}
	if !p.DeploymentsHighImpact {
		return Deployment{}, ErrHighImpactForbidden
	}
	if approver.UserID == nil == (approver.TeamID == nil) {
		return Deployment{}, invalid("exactly one approver (user or team) is required")
	}
	for _, a := range []*string{approver.UserID, approver.TeamID} {
		if a != nil {
			if !validUUID(*a) {
				return Deployment{}, invalid("the approver must be a user or team id")
			}
			*a = strings.ToLower(*a)
		}
	}
	if _, v, err := s.fullValidation(ctx, strings.ToLower(id)); err != nil {
		return Deployment{}, err
	} else if b := blocking(v.Issues, IssueApprovalRequired); len(b) > 0 {
		return Deployment{}, &PlanInvalidError{Issues: b}
	}
	return s.lifecycle(ctx, c, id, expected, "submitted", []string{DeploymentDraft},
		func(tx pgx.Tx, cur Deployment, rings []DeploymentRing, issues []PlanIssue, hi bool, sets map[string]TargetSet, next *Deployment) error {
			if b := blocking(issues, IssueApprovalRequired); len(b) > 0 {
				return &PlanInvalidError{Issues: b}
			}
			if !hi {
				return &GateError{Code: "approval_not_required"}
			}
			*next = addEditor(*next, c.Actor.UserID)
			next.HighImpact = hi
			excluded := append([]string{cur.OwnerUserID, cur.CreatedBy}, next.Editors...)
			slices.Sort(excluded)
			excluded = slices.Compact(excluded)
			approvalID, err := s.approvals.RequestInTx(ctx, tx, c.Actor, c.CorrelationID, cur.ID, cur.Reference, approver, excluded)
			if err != nil {
				return err
			}
			now := s.now()
			hash := planSHA256(cur, rings, sets)
			by := c.Actor.UserID
			next.Status, next.StatusReason, next.ApprovalID, next.SubmittedBy, next.SubmittedAt, next.PlanSHA256 =
				DeploymentPendingApproval, nil, &approvalID, &by, &now, &hash
			return nil
		})
}

// ScheduleDeployment schedules a valid plan. A high-impact plan needs deployments.high_impact, an approved plan
// Approval and an unchanged plan (hash of the approved plan; plan_changed otherwise); any other plan is
// scheduled from draft. Requires deployments.manage and expectedVersion.
func (s *Service) ScheduleDeployment(ctx context.Context, c Caller, p Principal, id string, expected *int) (Deployment, error) {
	if err := s.deploymentPreamble(c, p, expected, id); err != nil {
		return Deployment{}, err
	}
	if _, v, err := s.fullValidation(ctx, strings.ToLower(id)); err != nil {
		return Deployment{}, err
	} else if b := blocking(v.Issues, IssueApprovalRequired); len(b) > 0 {
		return Deployment{}, &PlanInvalidError{Issues: b}
	}
	return s.lifecycle(ctx, c, id, expected, "scheduled", []string{DeploymentDraft, DeploymentApproved},
		func(tx pgx.Tx, cur Deployment, rings []DeploymentRing, issues []PlanIssue, hi bool, sets map[string]TargetSet, next *Deployment) error {
			hash := planSHA256(cur, rings, sets)
			if hi {
				if !p.DeploymentsHighImpact {
					return ErrHighImpactForbidden
				}
				if cur.Status != DeploymentApproved {
					return &GateError{Code: IssueApprovalRequired}
				}
			}
			if cur.Status == DeploymentApproved && (cur.PlanSHA256 == nil || *cur.PlanSHA256 != hash) {
				return &GateError{Code: "plan_changed"}
			}
			if b := blocking(issues); len(b) > 0 {
				return &PlanInvalidError{Issues: b}
			}
			now := s.now()
			by := c.Actor.UserID
			next.Status, next.StatusReason, next.ScheduledBy, next.ScheduledAt, next.PlanSHA256 = DeploymentScheduled, nil, &by, &now, &hash
			v, err := s.store.ShareVersionTx(ctx, tx, cur.SoftwareVersionID)
			if err != nil {
				return err
			}
			return publish(ctx, tx, c, EventDeploymentScheduled, map[string]any{"deploymentId": cur.ID, "versionId": cur.SoftwareVersionID,
				"productId": v.ProductID, "intent": cur.Intent, "highImpact": hi, "ringCount": len(rings)})
		})
}

// CancelDeployment cancels a plan in any planning status with a reason code; a pending plan Approval is cancelled
// too. Requires deployments.manage and expectedVersion.
func (s *Service) CancelDeployment(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Deployment, error) {
	if err := s.deploymentPreamble(c, p, expected, id); err != nil {
		return Deployment{}, err
	}
	if !slices.Contains(DeploymentCancelReasons, reason) {
		return Deployment{}, invalid("reason must be one of %s", strings.Join(DeploymentCancelReasons, ", "))
	}
	var out Deployment
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockDeploymentTx(ctx, tx, strings.ToLower(id))
		if err != nil {
			return err
		}
		if *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Status == DeploymentCancelled {
			return &InvalidTransitionError{Operation: "cancel", From: cur.Status}
		}
		if cur.Status == DeploymentPendingApproval {
			if err := s.approvals.CancelBySubjectInTx(ctx, tx, c.Actor, c.CorrelationID, cur.ID); err != nil {
				return err
			}
		}
		next := cur
		now := s.now()
		by := c.Actor.UserID
		next.Status, next.StatusReason, next.CancelledBy, next.CancelledAt = DeploymentCancelled, &reason, &by, &now
		if out, err = s.commitDeployment(ctx, tx, c, cur, next, "cancelled", reason, nil); err != nil {
			return err
		}
		return publish(ctx, tx, c, EventDeploymentCancelled, map[string]any{"deploymentId": cur.ID, "reason": reason, "previousStatus": cur.Status})
	})
	return out, err
}

// OnApprovalDecided moves a plan whose Approval was decided: approve makes it approved, reject returns it to
// draft with approval_rejected. It runs in the dispatcher's claim transaction and is idempotent: events of other
// subjects and stale events (the plan is no longer pending) change nothing. An event without an approval id, or
// naming another approval than the pending one, is a permanent error.
func (s *Service) OnApprovalDecided(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		ApprovalID  string `json:"approvalId"`
		SubjectType string `json:"subjectType"`
		SubjectID   string `json:"subjectId"`
		Decision    string `json:"decision"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode ApprovalDecided payload: %w", err))
	}
	if p.SubjectType != DeploymentApprovalSubject {
		return nil
	}
	if p.Decision != "approve" && p.Decision != "reject" {
		return events.Permanent(fmt.Errorf("approval decision %q of deployment %s is unknown", p.Decision, p.SubjectID))
	}
	if p.ApprovalID == "" {
		return events.Permanent(fmt.Errorf("approval decision of deployment %s carries no approval id", p.SubjectID))
	}
	if !validUUID(p.SubjectID) {
		return events.Permanent(errors.New("approval decision names an invalid deployment id"))
	}
	cur, err := s.store.LockDeploymentTx(ctx, tx, strings.ToLower(p.SubjectID))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if cur.Status != DeploymentPendingApproval {
		return nil
	}
	if cur.ApprovalID == nil || !strings.EqualFold(*cur.ApprovalID, p.ApprovalID) {
		return events.Permanent(fmt.Errorf("approval %s is not the pending approval of deployment %s", p.ApprovalID, cur.ID))
	}
	c := Caller{Actor: deploymentSystem, CorrelationID: ev.CorrelationID}
	next := cur
	meta := map[string]any{"approvalId": cur.ApprovalID}
	if p.Decision == "approve" {
		now := s.now()
		next.Status, next.ApprovedAt = DeploymentApproved, &now
		_, err := s.commitDeployment(ctx, tx, c, cur, next, "approved", "", meta)
		return err
	}
	rejected := ReasonApprovalRejected
	next.Status, next.StatusReason = DeploymentDraft, &rejected
	_, err = s.commitDeployment(ctx, tx, c, cur, next, "approval_rejected", ReasonApprovalRejected, meta)
	return err
}

// ---- reads ----

// ListDeployments lists plans. Without deployments read access only the caller's own (owner or creator).
func (s *Service) ListDeployments(ctx context.Context, p Principal, f DeploymentFilter) (DeploymentResult, error) {
	if f.Status != "" && !slices.Contains(DeploymentStatuses, f.Status) {
		return DeploymentResult{}, invalid("status must be one of %s", strings.Join(DeploymentStatuses, ", "))
	}
	if (f.ProductID != "" && !validUUID(f.ProductID)) || (f.VersionID != "" && !validUUID(f.VersionID)) {
		return DeploymentResult{Items: []Deployment{}}, nil
	}
	f.ProductID, f.VersionID = strings.ToLower(f.ProductID), strings.ToLower(f.VersionID)
	f.MineOf = ""
	if !p.canViewDeployments() {
		if p.UserID == "" {
			return DeploymentResult{}, ErrForbidden
		}
		f.MineOf = p.UserID
	}
	f.Page = f.Page.Normalize()
	return s.store.ListDeployments(ctx, f)
}

// GetDeployment returns a plan with its rings, history, approvals, Change windows and structural validation.
// Readers: deployments read access, the owner, the creator and the plan's approvers; others get not found.
func (s *Service) GetDeployment(ctx context.Context, p Principal, id string) (DeploymentDetail, error) {
	if !validUUID(id) {
		return DeploymentDetail{}, ErrNotFound
	}
	id = strings.ToLower(id)
	d, err := s.store.GetDeployment(ctx, id)
	if err != nil {
		return DeploymentDetail{}, err
	}
	if !p.canViewDeployments() && (p.UserID == "" || (p.UserID != d.OwnerUserID && p.UserID != d.CreatedBy)) {
		ok := false
		if p.UserID != "" {
			if ok, err = s.approvals.CanView(ctx, d.ID, p.UserID); err != nil {
				return DeploymentDetail{}, err
			}
		}
		if !ok {
			return DeploymentDetail{}, ErrNotFound
		}
	}
	out := DeploymentDetail{Deployment: d, Changes: map[string]ChangeWindow{}}
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		rings, err := s.store.RingsTx(ctx, tx, d.ID)
		if err != nil {
			return err
		}
		issues, hi, _, err := s.structuralIssues(ctx, tx, d, rings)
		if err != nil {
			return err
		}
		out.Rings, out.Validation = rings, validationOf(issues, hi, s.now())
		return nil
	})
	if err != nil {
		return DeploymentDetail{}, err
	}
	if out.Transitions, err = s.store.DeploymentTransitions(ctx, d.ID); err != nil {
		return DeploymentDetail{}, err
	}
	if out.Approvals, err = s.approvals.ForSubject(ctx, d.ID); err != nil {
		return DeploymentDetail{}, err
	}
	var changeIDs []string
	for _, r := range out.Rings {
		if r.ChangeID != nil {
			changeIDs = append(changeIDs, *r.ChangeID)
		}
	}
	if len(changeIDs) > 0 {
		if out.Changes, err = s.changes.Lookup(ctx, uniq(changeIDs)); err != nil {
			return DeploymentDetail{}, err
		}
	}
	return out, nil
}
