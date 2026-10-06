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
// Changes need deployments.manage; high-impact plans (uninstall, supersede, a ring on a Target Set selecting all
// Devices or a root group with its nested groups, or rings targeting together at least HighImpactTargetThreshold
// Devices or HighImpactFleetPercent of the live Devices) also deployments.high_impact and an approved plan Approval
// (subject deployment) decided by a holder of deployments.approve who is neither the owner, the creator, an editor,
// the submitter nor an author or last editor of the plan's Target Sets. Audit actions endpoints.deployment.<op>
// carry ids, statuses, reason codes, flags and counts only.

var deploymentSystem = audit.SystemActor("deployment-planning")

// ErrHighImpactForbidden means the operation makes or keeps a plan high impact and the caller lacks
// deployments.high_impact.
var ErrHighImpactForbidden = fmt.Errorf("%w: deployments.high_impact is required", ErrForbidden)

// changeWindowOpen reports a Change that is approved or scheduled and whose maintenance window has not ended.
func changeWindowOpen(cw ChangeWindow, ok bool, now time.Time) bool {
	return ok && (cw.Status == "approved" || cw.Status == "scheduled") && cw.WindowStart != nil && cw.WindowEnd != nil && cw.WindowEnd.After(now)
}

// changeVisible applies the Changes read rule: changes.view|manage|execute, or being the requester or owner.
func changeVisible(p Principal, cw ChangeWindow) bool {
	return p.ChangesRead || (p.UserID != "" && (cw.RequesterID == p.UserID || (cw.OwnerID != nil && *cw.OwnerID == p.UserID)))
}

var highImpactOrder = []string{HighImpactUninstall, HighImpactSupersede, HighImpactAllDevices, HighImpactNestedRootGroup, HighImpactTargetCount}

func firstReason(reasons map[string]bool) string {
	for _, r := range highImpactOrder {
		if reasons[r] {
			return r
		}
	}
	return ""
}

// staticHighImpact is the high-impact reason that needs no evaluation: uninstall, supersede, or a ring on a Target
// Set whose saved definition is high impact (snapshot). Drafts store it; lifecycle steps recompute fully.
func staticHighImpact(d Deployment, rings []DeploymentRing, sets map[string]TargetSet) string {
	reasons := map[string]bool{HighImpactUninstall: d.Intent == IntentUninstall, HighImpactSupersede: d.Supersede}
	for _, r := range rings {
		if t, ok := sets[r.TargetSetID]; ok && t.HighImpactReason != nil {
			reasons[*t.HighImpactReason] = true
		}
	}
	return firstReason(reasons)
}

// countIsHighImpact reports a target count at or above the threshold or the fleet share (percent 0: off).
func countIsHighImpact(n, live, threshold, percent int) bool {
	return n >= threshold || (percent > 0 && live > 0 && n > 0 && n*100 >= percent*live)
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

// planSHA256 binds a plan: version, intent, supersede and every ring with its gates and the version of its Target
// Set. A change of any of them after validation or submission is detected (plan_changed).
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

// addEditor adds user to the plan's editors; a full list refuses the edit (ErrEditorsFull) instead of dropping
// the editor, because editors are excluded from approving the plan.
func addEditor(d Deployment, user string) (Deployment, error) {
	if user == "" || slices.Contains(d.Editors, user) {
		return d, nil
	}
	if len(d.Editors) >= maxEditors {
		return Deployment{}, ErrEditorsFull
	}
	d.Editors = append(slices.Clone(d.Editors), user)
	return d, nil
}

// planExclusions are the Users who can never approve the plan: owner, creator, editors, extra (the submitter) and
// the authors and last editors of its Target Sets.
func planExclusions(d Deployment, sets map[string]TargetSet, extra ...string) []string {
	out := append([]string{d.OwnerUserID, d.CreatedBy}, d.Editors...)
	out = append(out, extra...)
	for _, t := range sets {
		out = append(out, t.CreatedBy, t.UpdatedBy)
	}
	out = uniq(out)
	slices.Sort(out)
	return out
}

func (s *Service) holdsApprove(ctx context.Context, userID string) (bool, error) {
	perms, err := s.approvers.Permissions(ctx, userID)
	if err != nil {
		return false, err
	}
	_, ok := perms[PermDeploymentsApprove]
	return ok, nil
}

// checkApprover refuses (ErrNoEligibleApprover) an approver User who is excluded or lacks deployments.approve, and an
// approver Team without a current, not excluded member holding deployments.approve.
func (s *Service) checkApprover(ctx context.Context, a Approver, excluded []string) error {
	candidates := []string{}
	if a.UserID != nil {
		candidates = append(candidates, *a.UserID)
	} else {
		members, err := s.approvers.TeamMemberIDs(ctx, *a.TeamID)
		if err != nil {
			return err
		}
		candidates = members
	}
	for _, u := range candidates {
		if slices.Contains(excluded, u) {
			continue
		}
		ok, err := s.holdsApprove(ctx, u)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return ErrNoEligibleApprover
}

// isPlanApprover reports whether the User is the approver User or the decider of one of the plan's Approvals, or a
// member of the approver Team of a pending one.
func (s *Service) isPlanApprover(ctx context.Context, deploymentID, userID string) (bool, error) {
	list, err := s.approvals.ForSubject(ctx, deploymentID)
	if err != nil {
		return false, err
	}
	var teams []string
	loaded := false
	for _, a := range list {
		if (a.ApproverUserID != nil && *a.ApproverUserID == userID) || (a.DecidedByUserID != nil && *a.DecidedByUserID == userID) {
			return true, nil
		}
		if a.Status == approvalPending && a.ApproverTeamID != nil {
			if !loaded {
				if teams, err = s.approvers.TeamIDsOfUser(ctx, userID); err != nil {
					return false, err
				}
				loaded = true
			}
			if slices.Contains(teams, *a.ApproverTeamID) {
				return true, nil
			}
		}
	}
	return false, nil
}

// approvalPending and approvalApproved/approvalRejected are the Approvals statuses (approvals/public).
const (
	approvalPending  = "pending"
	approvalApproved = "approved"
	approvalRejected = "rejected"
)

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

// lookupWindows reads the Changes of the rings through the Changes contract. It runs before any transaction.
func (s *Service) lookupWindows(ctx context.Context, rings []DeploymentRing) (map[string]ChangeWindow, error) {
	var ids []string
	for _, r := range rings {
		if r.ChangeID != nil {
			ids = append(ids, *r.ChangeID)
		}
	}
	if len(ids) == 0 {
		return map[string]ChangeWindow{}, nil
	}
	return s.changes.Lookup(ctx, uniq(ids))
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
		d.HighImpact = staticHighImpact(d, nil, nil) != ""
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
// the caller added to the editors and the high-impact snapshot recomputed. A plan that becomes or stays high impact
// needs deployments.high_impact.
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
		next, err := addEditor(cur, c.Actor.UserID)
		if err != nil {
			return err
		}
		meta := map[string]any{}
		if err := edit(tx, cur, &next, meta); err != nil {
			return err
		}
		if err := s.store.CheckRingPositionsTx(ctx, tx); err != nil {
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
		next.HighImpact = staticHighImpact(next, rings, sets) != ""
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
	if err := s.deploymentPreamble(c, p, expected, id); err != nil {
		return Deployment{}, err
	}
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

// ringWindows reads the Change a ring input names before the edit transaction (nil when it names none or an
// invalid id; validRing refuses those).
func (s *Service) ringWindows(ctx context.Context, in RingInput) (map[string]ChangeWindow, error) {
	if in.ChangeID == nil || !validUUID(*in.ChangeID) {
		return map[string]ChangeWindow{}, nil
	}
	return s.changes.Lookup(ctx, []string{strings.ToLower(*in.ChangeID)})
}

// validRing checks a ring's input for position pos, its Target Set (FOR SHARE: exists and is not archived; a
// high-impact set needs deployments.high_impact and a maintenance window) and its Change (looked up before the
// transaction into windows): readable by the caller, approved or scheduled, window not ended.
func (s *Service) validRing(ctx context.Context, tx pgx.Tx, p Principal, in RingInput, pos int, windows map[string]ChangeWindow) (DeploymentRing, error) {
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
	if set.HighImpactReason != nil && !p.DeploymentsHighImpact {
		return DeploymentRing{}, ErrHighImpactForbidden
	}
	if set.HighImpactReason != nil && in.NoWindowRequired {
		return DeploymentRing{}, &GateError{Code: IssueNoWindowHighImpact}
	}
	if in.ChangeID != nil {
		if !validUUID(*in.ChangeID) {
			return DeploymentRing{}, invalid("changeId must be a change id")
		}
		id := strings.ToLower(*in.ChangeID)
		cw, ok := windows[id]
		// An unknown Change and one the caller may not read are refused alike.
		if !ok || !changeVisible(p, cw) || !changeWindowOpen(cw, ok, s.now()) {
			return DeploymentRing{}, &GateError{Code: IssueChangeWindowInvalid}
		}
		r.ChangeID = &id
	}
	return r, nil
}

// AddRing appends a ring to a draft plan (the first ring is the pilot). Requires deployments.manage and the plan's
// expectedVersion.
func (s *Service) AddRing(ctx context.Context, c Caller, p Principal, id string, expected *int, in RingInput) (Deployment, DeploymentRing, error) {
	if err := s.deploymentPreamble(c, p, expected, id); err != nil {
		return Deployment{}, DeploymentRing{}, err
	}
	windows, err := s.ringWindows(ctx, in)
	if err != nil {
		return Deployment{}, DeploymentRing{}, err
	}
	var ring DeploymentRing
	d, err := s.draftEdit(ctx, c, p, id, expected, "ring_added", func(tx pgx.Tx, cur Deployment, _ *Deployment, meta map[string]any) error {
		rings, err := s.store.RingsTx(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		if len(rings) >= MaxRings {
			return invalid("a deployment has at most %d rings", MaxRings)
		}
		r, err := s.validRing(ctx, tx, p, in, len(rings)+1, windows)
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
	if err := s.deploymentPreamble(c, p, expected, id); err != nil {
		return Deployment{}, DeploymentRing{}, err
	}
	windows, err := s.ringWindows(ctx, in)
	if err != nil {
		return Deployment{}, DeploymentRing{}, err
	}
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
		r, err := s.validRing(ctx, tx, p, in, old.Position, windows)
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
// SHARE): version and product approval, package gates, rings, Target Sets and Change windows. windows are the
// rings' Changes, read before the transaction; a Change missing from it counts as unusable.
func (s *Service) structuralIssues(ctx context.Context, tx pgx.Tx, d Deployment, rings []DeploymentRing, windows map[string]ChangeWindow) ([]PlanIssue, map[string]TargetSet, error) {
	var issues []PlanIssue
	block := func(code string, ring *string) {
		issues = append(issues, PlanIssue{Code: code, Blocking: true, RingID: ring})
	}
	sets, err := s.store.ShareTargetSetsTx(ctx, tx, ringSetIDs(rings))
	if err != nil {
		return nil, nil, err
	}
	if _, err := s.versionGate(ctx, tx, d.SoftwareVersionID); err != nil {
		var g *GateError
		if !errors.As(err, &g) {
			return nil, nil, err
		}
		block(g.Code, nil)
	}
	closed, published, err := s.store.PackageGateTx(ctx, tx, d.SoftwareVersionID)
	if err != nil {
		return nil, nil, err
	}
	if closed {
		block(IssuePackageGateClosed, nil)
	}
	if !published && d.Intent != IntentUninstall {
		issues = append(issues, PlanIssue{Code: IssuePackageNotPublished})
	}
	if len(rings) == 0 {
		block(IssueNoRings, nil)
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
	return issues, sets, nil
}

// highImpactIssues adds the high-impact warning and, while the plan still needs its Approval, approval_required.
func highImpactIssues(status string, hi bool) []PlanIssue {
	if !hi {
		return nil
	}
	out := []PlanIssue{{Code: IssueHighImpact}}
	if status == DeploymentDraft || status == DeploymentPendingApproval {
		out = append(out, PlanIssue{Code: IssueApprovalRequired, Blocking: true})
	}
	return out
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

func validationOf(issues []PlanIssue, hi bool, reason string, now time.Time) PlanValidation {
	if issues == nil {
		issues = []PlanIssue{}
	}
	return PlanValidation{Valid: len(blocking(issues)) == 0, HighImpact: hi, HighImpactReason: reason, Issues: issues, Rings: []RingTargets{}, ValidatedAt: now}
}

// planCheck is a validated plan: the state it read (deployment version, rings, Target Set versions, plan hash), the
// Changes it used and the outcome of the evaluation. Lifecycle steps re-check under their locks that the plan is
// still exactly this one (plan_changed otherwise), so the evaluation they gate on belongs to the plan they change.
type planCheck struct {
	d          Deployment
	sets       map[string]TargetSet
	windows    map[string]ChangeWindow
	hash       string
	evalIssues []PlanIssue
	validation PlanValidation
}

// ringEvaluation is the evaluation part of a validation.
type ringEvaluation struct {
	issues     []PlanIssue
	counts     []RingTargets
	total      int
	incomplete bool
	reasons    map[string]bool
}

// evaluateRings evaluates each ring's Target Set within the request budget b, compares the result with the ring's
// cap, decides high impact (static reasons, the Target Sets' current reasons, and the summed target count against
// the thresholds) and refuses rings without a window that are high impact. With overlap it also warns about other
// binding plans of the same product that reach the same Devices (at most maxOverlapSets of their Target Sets).
func (s *Service) evaluateRings(ctx context.Context, d Deployment, rings []DeploymentRing, sets map[string]TargetSet, b *evalBudget, overlap bool) (ringEvaluation, error) {
	out := ringEvaluation{counts: []RingTargets{}, reasons: map[string]bool{HighImpactUninstall: d.Intent == IntentUninstall, HighImpactSupersede: d.Supersede}}
	// The Target Sets' high-impact reasons are recomputed now (the directory may have changed since they were saved);
	// the directory is read before the snapshot transaction.
	setReasons := map[string]string{}
	for id, t := range sets {
		r, err := s.setHighImpact(ctx, t.Definition)
		if err != nil {
			return ringEvaluation{}, err
		}
		setReasons[id] = r
		out.reasons[r] = r != ""
	}
	cache := map[string]TargetEvaluation{}
	eval := func(ctx context.Context, set TargetSet) (TargetEvaluation, error) {
		if ev, ok := cache[set.ID]; ok {
			return ev, nil
		}
		ev, err := s.evaluateDefinition(ctx, set.Definition, b)
		if err == nil {
			cache[set.ID] = ev
		}
		return ev, err
	}
	ours := map[string]bool{}
	err := s.store.ReadSnapshot(ctx, func(ctx context.Context) error {
		live, err := s.store.CountLiveDevicesAfter(ctx, s.viewProvider, "")
		if err != nil {
			return err
		}
		ringCounts := map[string]int{}
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
			ringCounts[r.ID] = n
			out.total += n
			out.counts = append(out.counts, RingTargets{RingID: r.ID, Matched: ev.Matched, Truncated: ev.Truncated, Incomplete: ev.Incomplete})
			switch {
			case ev.Truncated || ev.Matched > r.MaxTargets:
				out.issues = append(out.issues, PlanIssue{Code: IssueTooManyTargets, Blocking: true, RingID: &rid, Count: &n})
			case ev.Matched == 0:
				out.issues = append(out.issues, PlanIssue{Code: IssueNoTargets, RingID: &rid})
			}
			if ev.Incomplete {
				out.incomplete = true
				out.issues = append(out.issues, PlanIssue{Code: IssueEvaluationIncomplete, Blocking: true, RingID: &rid})
			}
			for _, id := range ev.deviceIDs {
				ours[id] = true
			}
		}
		out.reasons[HighImpactTargetCount] = countIsHighImpact(out.total, live, s.hiTargets, s.hiPercent)
		for _, r := range rings {
			if r.NoWindowRequired && (setReasons[r.TargetSetID] != "" || countIsHighImpact(ringCounts[r.ID], live, s.hiTargets, s.hiPercent)) {
				rid := r.ID
				out.issues = append(out.issues, PlanIssue{Code: IssueNoWindowHighImpact, Blocking: true, RingID: &rid})
			}
		}
		if !overlap {
			return nil
		}
		return s.overlapIssues(ctx, d, ours, eval, &out)
	})
	return out, err
}

func (s *Service) overlapIssues(ctx context.Context, d Deployment, ours map[string]bool, eval func(context.Context, TargetSet) (TargetEvaluation, error), out *ringEvaluation) error {
	v, err := s.store.GetSoftwareVersion(ctx, d.SoftwareVersionID)
	if err != nil {
		return err
	}
	others, err := s.store.ActiveDeploymentsOfProduct(ctx, v.ProductID, d.ID, DeploymentBindingStatuses, maxOverlapDeployments)
	if err != nil {
		return err
	}
	evaluated := map[string]bool{}
	for _, o := range others {
		oRings, err := s.store.Rings(ctx, o.ID)
		if err != nil {
			return err
		}
		overlap := map[string]bool{}
		for _, r := range oRings {
			if !evaluated[r.TargetSetID] && len(evaluated) >= maxOverlapSets {
				out.issues = append(out.issues, PlanIssue{Code: IssueOverlapTruncated})
				return nil
			}
			evaluated[r.TargetSetID] = true
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
			out.issues = append(out.issues, PlanIssue{Code: IssueOverlap, Count: &n, DeploymentID: &oid})
		}
	}
	return nil
}

// fullValidation validates a plan: the rings' Changes are read first (no transaction), then the structural checks
// run in a short transaction, then the rings are evaluated within one request budget. overlap adds the overlap
// warnings (explicit Validate only).
func (s *Service) fullValidation(ctx context.Context, id string, overlap bool) (planCheck, error) {
	pre, err := s.store.Rings(ctx, id)
	if err != nil {
		return planCheck{}, err
	}
	pc := planCheck{}
	if pc.windows, err = s.lookupWindows(ctx, pre); err != nil {
		return planCheck{}, err
	}
	var rings []DeploymentRing
	var structural []PlanIssue
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		if pc.d, err = s.store.DeploymentTx(ctx, tx, id); err != nil {
			return err
		}
		if rings, err = s.store.RingsTx(ctx, tx, pc.d.ID); err != nil {
			return err
		}
		structural, pc.sets, err = s.structuralIssues(ctx, tx, pc.d, rings, pc.windows)
		return err
	})
	if err != nil {
		return planCheck{}, err
	}
	pc.hash = planSHA256(pc.d, rings, pc.sets)
	ev, err := s.evaluateRings(ctx, pc.d, rings, pc.sets, s.newBudget(), overlap)
	if err != nil {
		return planCheck{}, err
	}
	reason := firstReason(ev.reasons)
	pc.evalIssues = append(ev.issues, highImpactIssues(pc.d.Status, reason != "")...)
	pc.validation = validationOf(append(structural, pc.evalIssues...), reason != "", reason, s.now())
	pc.validation.Rings, pc.validation.Evaluated, pc.validation.TotalTargets, pc.validation.Incomplete = ev.counts, true, ev.total, ev.incomplete
	return pc, nil
}

// ValidateDeployment validates a plan now: blocking issues, warnings, high impact with its reason and the
// evaluated target count of every ring. Requires deployments.manage; one evaluation per user at a time.
func (s *Service) ValidateDeployment(ctx context.Context, p Principal, id string) (PlanValidation, error) {
	if !p.DeploymentsManage {
		return PlanValidation{}, ErrForbidden
	}
	if !validUUID(id) {
		return PlanValidation{}, ErrNotFound
	}
	done, err := s.beginEvaluation(p.UserID)
	if err != nil {
		return PlanValidation{}, err
	}
	defer done()
	pc, err := s.fullValidation(ctx, strings.ToLower(id), true)
	return pc.validation, err
}

// lifecycle locks a plan in one of from and checks that it is exactly the validated plan pc: same deployment
// version, same Target Set versions (FOR SHARE) and same plan hash, else plan_changed. It re-runs the structural
// checks in the transaction, adds pc's evaluation issues, stores pc's high impact and lets apply decide.
func (s *Service) lifecycle(ctx context.Context, c Caller, id string, expected *int, op string, from []string, pc planCheck,
	apply func(tx pgx.Tx, cur Deployment, rings []DeploymentRing, issues []PlanIssue, next *Deployment) error) (Deployment, error) {
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
		if cur.Version != pc.d.Version {
			return &GateError{Code: CodePlanChanged}
		}
		rings, err := s.store.RingsTx(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		structural, sets, err := s.structuralIssues(ctx, tx, cur, rings, pc.windows)
		if err != nil {
			return err
		}
		if len(sets) != len(pc.sets) {
			return &GateError{Code: CodePlanChanged}
		}
		for id, t := range sets {
			if was, ok := pc.sets[id]; !ok || was.Version != t.Version {
				return &GateError{Code: CodePlanChanged}
			}
		}
		if planSHA256(cur, rings, sets) != pc.hash {
			return &GateError{Code: CodePlanChanged}
		}
		next := cur
		next.HighImpact = pc.validation.HighImpact
		if err := apply(tx, cur, rings, append(structural, pc.evalIssues...), &next); err != nil {
			return err
		}
		meta := map[string]any{"ringCount": len(rings), "highImpact": next.HighImpact, "highImpactReason": pc.validation.HighImpactReason,
			"totalTargets": pc.validation.TotalTargets}
		out, err = s.commitDeployment(ctx, tx, c, cur, next, op, "", meta)
		return err
	})
	return out, err
}

func normalizeApprover(approver Approver) (Approver, error) {
	if approver.UserID == nil == (approver.TeamID == nil) {
		return Approver{}, invalid("exactly one approver (user or team) is required")
	}
	out := Approver{}
	for _, a := range []struct {
		in  *string
		out **string
	}{{approver.UserID, &out.UserID}, {approver.TeamID, &out.TeamID}} {
		if a.in != nil {
			if !validUUID(*a.in) {
				return Approver{}, invalid("the approver must be a user or team id")
			}
			v := strings.ToLower(*a.in)
			*a.out = &v
		}
	}
	return out, nil
}

// SubmitDeployment asks for the plan Approval of a high-impact draft: the plan must have no blocking issue, the
// approver is exactly one User or Team that holds deployments.approve (a Team: at least one current member) and
// can never be the owner, the creator, an editor, the submitter or an author or last editor of its Target Sets.
// The Approval is bound to the plan hash. Requires deployments.manage, deployments.high_impact and expectedVersion.
func (s *Service) SubmitDeployment(ctx context.Context, c Caller, p Principal, id string, expected *int, approver Approver) (Deployment, error) {
	if err := s.deploymentPreamble(c, p, expected, id); err != nil {
		return Deployment{}, err
	}
	if !p.DeploymentsHighImpact {
		return Deployment{}, ErrHighImpactForbidden
	}
	approver, err := normalizeApprover(approver)
	if err != nil {
		return Deployment{}, err
	}
	done, err := s.beginEvaluation(c.Actor.UserID)
	if err != nil {
		return Deployment{}, err
	}
	defer done()
	pc, err := s.fullValidation(ctx, strings.ToLower(id), false)
	if err != nil {
		return Deployment{}, err
	}
	if b := blocking(pc.validation.Issues, IssueApprovalRequired); len(b) > 0 {
		return Deployment{}, &PlanInvalidError{Issues: b}
	}
	excluded := planExclusions(pc.d, pc.sets, c.Actor.UserID)
	if err := s.checkApprover(ctx, approver, excluded); err != nil {
		return Deployment{}, err
	}
	return s.lifecycle(ctx, c, id, expected, "submitted", []string{DeploymentDraft}, pc,
		func(tx pgx.Tx, cur Deployment, _ []DeploymentRing, issues []PlanIssue, next *Deployment) error {
			if b := blocking(issues, IssueApprovalRequired); len(b) > 0 {
				return &PlanInvalidError{Issues: b}
			}
			if !next.HighImpact {
				return &GateError{Code: "approval_not_required"}
			}
			hi := next.HighImpact
			edited, err := addEditor(*next, c.Actor.UserID)
			if err != nil {
				return err
			}
			*next = edited
			next.HighImpact = hi
			approvalID, err := s.approvals.RequestInTx(ctx, tx, c.Actor, c.CorrelationID, cur.ID, cur.Reference, approver, excluded)
			if err != nil {
				return err
			}
			now := s.now()
			by := c.Actor.UserID
			hash := pc.hash
			next.Status, next.StatusReason, next.ApprovalID, next.SubmittedBy, next.SubmittedAt, next.PlanSHA256 =
				DeploymentPendingApproval, nil, &approvalID, &by, &now, &hash
			return nil
		})
}

// ScheduleDeployment schedules a valid plan. A high-impact plan needs deployments.high_impact, an approved plan
// Approval and an unchanged plan (hash of the approved plan; plan_changed otherwise); any other plan is
// scheduled from draft. An incomplete evaluation blocks. Requires deployments.manage and expectedVersion.
func (s *Service) ScheduleDeployment(ctx context.Context, c Caller, p Principal, id string, expected *int) (Deployment, error) {
	if err := s.deploymentPreamble(c, p, expected, id); err != nil {
		return Deployment{}, err
	}
	done, err := s.beginEvaluation(c.Actor.UserID)
	if err != nil {
		return Deployment{}, err
	}
	defer done()
	pc, err := s.fullValidation(ctx, strings.ToLower(id), false)
	if err != nil {
		return Deployment{}, err
	}
	if b := blocking(pc.validation.Issues, IssueApprovalRequired); len(b) > 0 {
		return Deployment{}, &PlanInvalidError{Issues: b}
	}
	return s.lifecycle(ctx, c, id, expected, "scheduled", []string{DeploymentDraft, DeploymentApproved}, pc,
		func(tx pgx.Tx, cur Deployment, rings []DeploymentRing, issues []PlanIssue, next *Deployment) error {
			hi := next.HighImpact
			if hi {
				if !p.DeploymentsHighImpact {
					return ErrHighImpactForbidden
				}
				if cur.Status != DeploymentApproved {
					return &GateError{Code: IssueApprovalRequired}
				}
			}
			if cur.Status == DeploymentApproved && (cur.PlanSHA256 == nil || *cur.PlanSHA256 != pc.hash) {
				return &GateError{Code: CodePlanChanged}
			}
			if b := blocking(issues); len(b) > 0 {
				return &PlanInvalidError{Issues: b}
			}
			now := s.now()
			by := c.Actor.UserID
			hash := pc.hash
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
	originalManage := p.DeploymentsManage
	if !p.DeploymentsManage && p.DeploymentsExecute {
		// An executor may cancel Deployments in execution; the status decides below.
		p.DeploymentsManage = true
	}
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
		if slices.Contains(executionCancelable, cur.Status) {
			if !p.DeploymentsExecute {
				return ErrForbidden
			}
			st, err := s.lockExec(ctx, tx, cur.ID, nil, "cancel")
			if err != nil {
				return err
			}
			out, err = s.cancelRunning(ctx, tx, c, st, reason)
			return err
		}
		if cur.Status == DeploymentCancelled || !slices.Contains(DeploymentBindingStatuses, cur.Status) && cur.Status != DeploymentDraft {
			return &InvalidTransitionError{Operation: "cancel", From: cur.Status}
		}
		if !originalManage {
			return ErrForbidden
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

// executionCancelable are the execution statuses CancelDeployment cancels (with deployments.execute).
var executionCancelable = []string{DeploymentResolvingTargets, DeploymentReady, DeploymentRunning, DeploymentPaused}

// toDraft returns a pending plan to draft and clears its approval binding.
func toDraft(cur Deployment, reason string) Deployment {
	next := cur
	next.Status, next.StatusReason = DeploymentDraft, &reason
	next.ApprovalID, next.PlanSHA256, next.SubmittedBy, next.SubmittedAt = nil, nil, nil, nil
	return next
}

// verifyDecision checks a decided plan Approval through the Approvals contract: it is the pending one, its status
// matches the decision and the decider holds deployments.approve and did not take part in the plan. It returns
// the decider, or a refusal code for the audit (empty when verified).
func (s *Service) verifyDecision(ctx context.Context, tx pgx.Tx, cur Deployment, approvalID, decision string) (string, string, error) {
	if cur.ApprovalID == nil || !strings.EqualFold(*cur.ApprovalID, approvalID) {
		return "", "approval_mismatch", nil
	}
	list, err := s.approvals.ForSubject(ctx, cur.ID)
	if err != nil {
		return "", "", err
	}
	want := approvalApproved
	if decision == "reject" {
		want = approvalRejected
	}
	for _, a := range list {
		if !strings.EqualFold(a.ID, approvalID) {
			continue
		}
		if a.Status != want {
			return "", "status_mismatch", nil
		}
		if a.DecidedByUserID == nil {
			return "", "no_decider", nil
		}
		decider := *a.DecidedByUserID
		rings, err := s.store.RingsTx(ctx, tx, cur.ID)
		if err != nil {
			return "", "", err
		}
		sets, err := s.store.ShareTargetSetsTx(ctx, tx, ringSetIDs(rings))
		if err != nil {
			return "", "", err
		}
		var submitter []string
		if cur.SubmittedBy != nil {
			submitter = append(submitter, *cur.SubmittedBy)
		}
		if slices.Contains(planExclusions(cur, sets, submitter...), decider) {
			return decider, "decider_excluded", nil
		}
		ok, err := s.holdsApprove(ctx, decider)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return decider, "decider_lacks_permission", nil
		}
		return decider, "", nil
	}
	return "", "approval_unknown", nil
}

// OnApprovalDecided moves a plan whose Approval was decided: approve makes it approved, reject returns it to draft
// with approval_rejected. The decision is verified through the Approvals contract (verifyDecision); a decision that
// cannot be verified returns the plan to draft with approver_not_authorized and cancels its pending Approvals. It
// runs in the dispatcher's claim transaction and is idempotent: events of other subjects and stale events (the
// plan is no longer pending) change nothing.
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
	if p.SubjectType == RingApprovalSubject && (p.Decision == "approve" || p.Decision == "reject") && p.ApprovalID != "" {
		return s.onRingApprovalDecided(ctx, tx, ev, p.SubjectID, p.ApprovalID, p.Decision)
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
	c := Caller{Actor: deploymentSystem, CorrelationID: ev.CorrelationID}
	decider, refused, err := s.verifyDecision(ctx, tx, cur, p.ApprovalID, p.Decision)
	if err != nil {
		return err
	}
	meta := map[string]any{"approvalId": cur.ApprovalID, "eventApprovalId": p.ApprovalID, "decision": p.Decision}
	if decider != "" {
		meta["decidedBy"] = decider
	}
	if refused != "" {
		meta["refusal"] = refused
		if err := s.approvals.CancelBySubjectInTx(ctx, tx, c.Actor, c.CorrelationID, cur.ID); err != nil {
			return err
		}
		_, err := s.commitDeployment(ctx, tx, c, cur, toDraft(cur, ReasonApproverNotAuthorized), ReasonApproverNotAuthorized, ReasonApproverNotAuthorized, meta)
		return err
	}
	if p.Decision == "approve" {
		next := cur
		now := s.now()
		next.Status, next.ApprovedAt = DeploymentApproved, &now
		_, err := s.commitDeployment(ctx, tx, c, cur, next, "approved", "", meta)
		return err
	}
	_, err = s.commitDeployment(ctx, tx, c, cur, toDraft(cur, ReasonApprovalRejected), "approval_rejected", ReasonApprovalRejected, meta)
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

// GetDeployment returns a plan with its rings, history, approvals, Change windows and structural validation (with
// the high-impact snapshot). Readers: deployments read access, the owner, the creator and the plan's approvers
// (approver Team members while the Approval is pending); others get not found. A Change the reader may not read
// is returned as a hidden placeholder (id only).
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
			if ok, err = s.isPlanApprover(ctx, d.ID, p.UserID); err != nil {
				return DeploymentDetail{}, err
			}
		}
		if !ok {
			return DeploymentDetail{}, ErrNotFound
		}
	}
	pre, err := s.store.Rings(ctx, d.ID)
	if err != nil {
		return DeploymentDetail{}, err
	}
	windows, err := s.lookupWindows(ctx, pre)
	if err != nil {
		return DeploymentDetail{}, err
	}
	out := DeploymentDetail{Deployment: d, Changes: map[string]ChangeWindow{}}
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		rings, err := s.store.RingsTx(ctx, tx, d.ID)
		if err != nil {
			return err
		}
		issues, sets, err := s.structuralIssues(ctx, tx, d, rings, windows)
		if err != nil {
			return err
		}
		hi, reason := d.HighImpact, staticHighImpact(d, rings, sets)
		switch {
		case !hi:
			reason = ""
		case reason == "":
			reason = HighImpactTargetCount
		}
		out.Rings, out.Validation = rings, validationOf(append(issues, highImpactIssues(d.Status, hi)...), hi, reason, s.now())
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
	for cid, cw := range windows {
		if changeVisible(p, cw) {
			out.Changes[cid] = cw
		} else {
			out.Changes[cid] = ChangeWindow{ID: cid, Hidden: true}
		}
	}
	return out, nil
}
