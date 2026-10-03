package application

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Audit actions of a management ingestion run. Entries carry counts and enumerated codes only; names,
// rules and raw statuses reported by the provider are never copied into audit.
const (
	auditManagementCompleted = "endpoints.management_sync.completed"
	auditManagementFailed    = "endpoints.management_sync.failed"
	// auditFindingsReconcileFailed: the derived-finding pass failed after the management data was committed.
	auditFindingsReconcileFailed = "endpoints.findings_reconcile.failed"
)

func (r *ManagementResult) add(o ManagementResult) {
	r.FiltersCreated += o.FiltersCreated
	r.FiltersUpdated += o.FiltersUpdated
	r.FiltersUnchanged += o.FiltersUnchanged
	r.FiltersTombstoned += o.FiltersTombstoned
	r.FiltersRejected += o.FiltersRejected
	r.ArtifactsCreated += o.ArtifactsCreated
	r.ArtifactsUpdated += o.ArtifactsUpdated
	r.ArtifactsUnchanged += o.ArtifactsUnchanged
	r.ArtifactsTombstoned += o.ArtifactsTombstoned
	r.ArtifactsRejected += o.ArtifactsRejected
	r.ArtifactsLinked += o.ArtifactsLinked
	r.AssignmentsOpened += o.AssignmentsOpened
	r.AssignmentsClosed += o.AssignmentsClosed
	r.AssignmentsUnchanged += o.AssignmentsUnchanged
	r.AssignmentsRejected += o.AssignmentsRejected
	r.AssignmentEvents += o.AssignmentEvents
	r.ObservationsCreated += o.ObservationsCreated
	r.ObservationsChanged += o.ObservationsChanged
	r.ObservationsUnchanged += o.ObservationsUnchanged
	r.ObservationsSkipped += o.ObservationsSkipped
	r.ObservationsRetired += o.ObservationsRetired
	r.MembershipsOpened += o.MembershipsOpened
	r.MembershipsClosed += o.MembershipsClosed
	r.MembershipsUnchanged += o.MembershipsUnchanged
	r.MembershipsSkipped += o.MembershipsSkipped
	r.ManagementTombstonesSkipped += o.ManagementTombstonesSkipped
	r.ProviderFindingsRaised += o.ProviderFindingsRaised
	r.ProviderFindingsResolved += o.ProviderFindingsResolved
	r.IneffectiveFindingsRaised += o.IneffectiveFindingsRaised
	r.IneffectiveFindingsResolved += o.IneffectiveFindingsResolved
	r.IneffectiveDevicesSkipped += o.IneffectiveDevicesSkipped
}

func (s *Service) recordManagementSync(ctx context.Context, c Caller, action, provider, source string, complete bool, r ManagementResult, reason string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
			return fmt.Errorf("generate id: %w", err)
		}
		meta := map[string]any{
			"provider": provider, "source": source, "complete": complete,
			"filtersCreated": r.FiltersCreated, "filtersUpdated": r.FiltersUpdated, "filtersTombstoned": r.FiltersTombstoned, "filtersRejected": r.FiltersRejected,
			"artifactsCreated": r.ArtifactsCreated, "artifactsUpdated": r.ArtifactsUpdated, "artifactsUnchanged": r.ArtifactsUnchanged,
			"artifactsTombstoned": r.ArtifactsTombstoned, "artifactsRejected": r.ArtifactsRejected, "artifactsLinked": r.ArtifactsLinked,
			"assignmentsOpened": r.AssignmentsOpened, "assignmentsClosed": r.AssignmentsClosed, "assignmentsRejected": r.AssignmentsRejected,
			"observationsCreated": r.ObservationsCreated, "observationsChanged": r.ObservationsChanged, "observationsSkipped": r.ObservationsSkipped, "observationsRetired": r.ObservationsRetired,
			"membershipsOpened": r.MembershipsOpened, "membershipsClosed": r.MembershipsClosed, "membershipsSkipped": r.MembershipsSkipped,
			"tombstonesSkipped": r.ManagementTombstonesSkipped, "providerFindingsRaised": r.ProviderFindingsRaised, "providerFindingsResolved": r.ProviderFindingsResolved,
			"ineffectiveFindingsRaised": r.IneffectiveFindingsRaised, "ineffectiveFindingsResolved": r.IneffectiveFindingsResolved, "ineffectiveDevicesSkipped": r.IneffectiveDevicesSkipped,
		}
		if reason != "" {
			meta["reason"] = reason
		}
		return audit.Record(ctx, tx, audit.Change{
			Action: action, TargetType: "endpoint_sync", TargetID: id, Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: meta,
		})
	})
}

// IngestManagement stores a provider's management snapshot idempotently under the provider's run lock
// (the same one device ingestion takes, so runs never interleave; ErrSyncRunning when another holds it).
// Filters and artifacts are upserted, an artifact's assignments keep an interval history (a changed
// assignment closes its row and opens a new one, an unchanged one only refreshes freshness), the
// provider's observations are stored with a history row only when the normalized state
// changes, and device group memberships are intervals. For a Complete snapshot, artifacts, filters and
// memberships it no longer lists are tombstoned or closed and observations it no longer reports are retired (never when that list is empty, and not when
// that would remove more than half of the provider's live ones). Observations and memberships refer to
// Devices by provider external id: devices not ingested yet are skipped. Requires endpoints.manage.
func (s *Service) IngestManagement(ctx context.Context, c Caller, p Principal, snap ManagementSnapshot) (ManagementResult, error) {
	if err := c.validate(); err != nil {
		return ManagementResult{}, err
	}
	if !p.Manage {
		return ManagementResult{}, ErrForbidden
	}
	if !providerKey.MatchString(snap.Provider) {
		return ManagementResult{}, invalid("provider must match %s", providerKey.String())
	}
	if snap.Source != SourceSync && snap.Source != SourceImport {
		return ManagementResult{}, invalid("source must be %s or %s", SourceSync, SourceImport)
	}
	unlock, ok, err := s.store.TryLockProvider(ctx, snap.Provider)
	if err != nil {
		return ManagementResult{}, err
	}
	if !ok {
		return ManagementResult{}, ErrSyncRunning
	}
	defer unlock()
	return s.ingestManagementLocked(ctx, c, snap)
}

func (s *Service) ingestManagementLocked(ctx context.Context, c Caller, snap ManagementSnapshot) (ManagementResult, error) {
	var total ManagementResult
	fail := func(reason string, err error) (ManagementResult, error) {
		_ = s.recordManagementSync(ctx, c, auditManagementFailed, snap.Provider, snap.Source, snap.Complete, total, reason)
		return total, err
	}
	switch {
	case len(snap.Artifacts) > MaxSnapshotArtifacts, len(snap.Filters) > MaxSnapshotFilters,
		len(snap.Observations) > MaxSnapshotObservations, len(snap.Memberships) > MaxSnapshotMemberships:
		return fail("snapshot_too_large", invalid("the management snapshot exceeds the size limits"))
	}
	// Truncated to the database precision so the tombstone comparisons below are exact.
	runAt := s.now().UTC().Truncate(time.Microsecond)

	if err := s.ingestFilters(ctx, snap, runAt, &total); err != nil {
		return fail("filter_error", err)
	}
	if err := s.ingestArtifacts(ctx, c, snap, runAt, &total); err != nil {
		return fail("artifact_error", err)
	}
	if err := s.ingestObservations(ctx, c, snap, runAt, &total); err != nil {
		return fail("observation_error", err)
	}
	if err := s.ingestMemberships(ctx, snap, runAt, &total); err != nil {
		return fail("membership_error", err)
	}
	// The management data is committed at this point. A failure of the derived-finding pass is audited on its own and
	// does not turn the run into a failed one; the pass continues behind its cursor in the next run.
	if err := s.reconcileIneffective(ctx, c, snap.Provider, &total); err != nil {
		_ = s.recordManagementSync(ctx, c, auditFindingsReconcileFailed, snap.Provider, snap.Source, snap.Complete, ManagementResult{}, "finding_error")
	}
	if err := s.recordManagementSync(ctx, c, auditManagementCompleted, snap.Provider, snap.Source, snap.Complete, total, ""); err != nil {
		return total, err
	}
	return total, nil
}

// guarded reports whether tombstoning n of live objects is refused by the guard.
func guarded(n, live int) bool { return live > MinGuardedDevices && n*2 > live }

// ---- filters ----

type filterInput struct {
	NewFilter
}

func normalizeFilter(snap ManagementSnapshot, r intune.FilterRecord, at time.Time) (in filterInput, extID string, ok bool) {
	id, ok := cleanRequired(r.ExternalID, 200)
	if !ok {
		return filterInput{}, "", false
	}
	name, ok := cleanRequired(r.Name, 256)
	if !ok {
		name = PlaceholderArtifactName
	}
	// A rule is never shortened: that would change its meaning. A rule that is too long or unsafe rejects the filter.
	rule := strings.TrimSpace(r.Rule)
	if utf8.RuneCountInString(rule) > MaxFilterRuleLength || !utf8.ValidString(rule) || safetext.ContainsUnsafe(rule, true) {
		return filterInput{}, id, false
	}
	return filterInput{NewFilter{
		Provider: snap.Provider, ExternalID: id, Name: name, Platform: oneOf(r.Platform, OSPlatforms, "other"), Rule: rule,
		Revision: cleanOptional(r.Revision, 200), Source: snap.Source, ObservedAt: at, SyncedAt: at,
	}}, id, true
}

func (s *Service) ingestFilters(ctx context.Context, snap ManagementSnapshot, runAt time.Time, total *ManagementResult) error {
	seen := map[string]bool{}
	keep := []string{}
	const batchSize = 200
	for start := 0; start < len(snap.Filters); start += batchSize {
		var batch []filterInput
		for _, r := range snap.Filters[start:min(start+batchSize, len(snap.Filters))] {
			in, extID, ok := normalizeFilter(snap, r, runAt)
			if extID != "" && seen[extID] {
				ok = false
			}
			if !ok {
				total.FiltersRejected++
				if extID != "" && !seen[extID] {
					seen[extID] = true
					keep = append(keep, extID)
				}
				continue
			}
			seen[extID] = true
			keep = append(keep, extID)
			batch = append(batch, in)
		}
		var got ManagementResult
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			got = ManagementResult{}
			for _, in := range batch {
				if err := s.upsertFilter(ctx, tx, &got, in); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		total.add(got)
	}
	if snap.Complete && len(snap.Filters) > 0 && len(keep) > 0 {
		return s.store.InTx(ctx, func(tx pgx.Tx) error {
			ids, live, err := s.store.FilterTombstoneCandidatesTx(ctx, tx, snap.Provider, runAt, keep)
			if err != nil || len(ids) == 0 {
				return err
			}
			if guarded(len(ids), live) {
				total.ManagementTombstonesSkipped += len(ids)
				return nil
			}
			n, err := s.store.TombstoneFiltersTx(ctx, tx, ids, runAt)
			total.FiltersTombstoned += n
			return err
		})
	}
	return nil
}

func (s *Service) upsertFilter(ctx context.Context, tx pgx.Tx, run *ManagementResult, in filterInput) error {
	f, err := s.store.LockFilterByExternalTx(ctx, tx, in.Provider, in.ExternalID)
	if err != nil {
		return err
	}
	if f == nil {
		_, ok, err := s.store.InsertFilterTx(ctx, tx, in.NewFilter)
		if err != nil {
			return err
		}
		if ok {
			run.FiltersCreated++
			return nil
		}
		if f, err = s.store.LockFilterByExternalTx(ctx, tx, in.Provider, in.ExternalID); err != nil || f == nil {
			return fmt.Errorf("lock filter after concurrent create: %w", err)
		}
	}
	changed := f.DeletedObservedAt != nil || f.Name != in.Name || f.Platform != in.Platform || f.Rule != in.Rule || !sameStr(f.Revision, in.Revision)
	if !changed {
		run.FiltersUnchanged++
		return s.store.TouchFilterTx(ctx, tx, f.ID, in.ObservedAt, in.SyncedAt, in.Source)
	}
	f.Name, f.Platform, f.Rule, f.Revision, f.Source = in.Name, in.Platform, in.Rule, in.Revision, in.Source
	f.ObservedAt, f.LastSyncedAt, f.DeletedObservedAt = in.ObservedAt, in.SyncedAt, nil
	if _, err := s.store.UpdateFilterTx(ctx, tx, *f); err != nil {
		return err
	}
	run.FiltersUpdated++
	return nil
}

// ---- artifacts and assignments ----

type artifactInput struct {
	NewArtifact
	Assignments []AssignmentInput
	// ReconcileAssignments is false when the assignments are unknown or any of them was invalid: the
	// previous ones are then kept.
	ReconcileAssignments bool
	rejectedAssignments  int
	filterRefs           []string // filter external id per assignment ("" for none), parallel to Assignments
}

func normalizeArtifact(snap ManagementSnapshot, r intune.ArtifactRecord, at time.Time) (in artifactInput, extID string, ok bool) {
	id, ok := cleanRequired(r.ExternalID, 200)
	if !ok {
		return artifactInput{}, "", false
	}
	kind := strings.ToLower(strings.TrimSpace(r.Kind))
	if !slices.Contains(ArtifactKinds, kind) {
		return artifactInput{}, id, false
	}
	name, ok := cleanRequired(r.Name, 256)
	if !ok {
		name = PlaceholderArtifactName
	}
	in = artifactInput{NewArtifact: NewArtifact{
		Provider: snap.Provider, ExternalID: id, Kind: kind, Name: name, Platform: oneOf(r.Platform, OSPlatforms, "other"),
		Revision: cleanOptional(r.Revision, 200), Source: snap.Source, ObservedAt: at, SyncedAt: at,
	}}
	if !r.AssignmentsKnown {
		return in, id, true
	}
	if len(r.Assignments) > MaxAssignmentsPerArtifact {
		in.rejectedAssignments = len(r.Assignments)
		return in, id, true
	}
	seen := map[string]bool{}
	for _, a := range r.Assignments {
		ai, ref, ok := normalizeAssignment(a)
		if !ok || seen[ai.ProviderAssignmentID] {
			in.rejectedAssignments++
			continue
		}
		seen[ai.ProviderAssignmentID] = true
		in.Assignments = append(in.Assignments, ai)
		in.filterRefs = append(in.filterRefs, ref)
	}
	in.ReconcileAssignments = in.rejectedAssignments == 0
	return in, id, true
}

// normalizeAssignment validates one assignment; ref is the external id of its filter, if any.
func normalizeAssignment(a intune.AssignmentRecord) (in AssignmentInput, ref string, ok bool) {
	id, ok := cleanRequired(a.ProviderAssignmentID, 200)
	if !ok {
		return AssignmentInput{}, "", false
	}
	kind, mode := strings.ToLower(strings.TrimSpace(a.TargetKind)), strings.ToLower(strings.TrimSpace(a.Mode))
	if !slices.Contains(TargetKinds, kind) || !slices.Contains(AssignmentModes, mode) {
		return AssignmentInput{}, "", false
	}
	in = AssignmentInput{ProviderAssignmentID: id, TargetKind: kind, Mode: mode, Intent: oneOf(a.Intent, AssignmentIntents, "none"), FilterMode: "none"}
	if kind == "group" {
		g, ok := cleanRequired(a.TargetGroupExternalID, 200)
		if !ok {
			return AssignmentInput{}, "", false
		}
		in.TargetGroupExternalID = &g
	}
	fm := strings.ToLower(strings.TrimSpace(a.FilterMode))
	if fm == "" {
		fm = "none"
	}
	if !slices.Contains(FilterModes, fm) {
		return AssignmentInput{}, "", false
	}
	if fm != "none" {
		ref, ok = cleanRequired(a.FilterExternalID, 200)
		if !ok {
			return AssignmentInput{}, "", false
		}
		in.FilterMode = fm
	} else if strings.TrimSpace(a.FilterExternalID) != "" {
		return AssignmentInput{}, "", false
	}
	return in, ref, true
}

func (s *Service) ingestArtifacts(ctx context.Context, c Caller, snap ManagementSnapshot, runAt time.Time, total *ManagementResult) error {
	seen := map[string]bool{}
	keep := []string{}
	for start := 0; start < len(snap.Artifacts); {
		var batch []artifactInput
		assignments := 0
		for start < len(snap.Artifacts) && len(batch) < 50 {
			in, extID, ok := normalizeArtifact(snap, snap.Artifacts[start], runAt)
			if extID != "" && seen[extID] {
				ok = false
			}
			if !ok {
				total.ArtifactsRejected++
				if extID != "" && !seen[extID] {
					// Invalid, but the provider did report it: it must not be tombstoned.
					seen[extID] = true
					keep = append(keep, extID)
				}
				start++
				continue
			}
			if len(batch) > 0 && assignments+len(in.Assignments) > MaxBatchInstallations {
				break
			}
			seen[extID] = true
			keep = append(keep, extID)
			assignments += len(in.Assignments)
			batch = append(batch, in)
			start++
		}
		if len(batch) == 0 {
			continue
		}
		var got ManagementResult
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			got = ManagementResult{}
			return s.ingestArtifactBatch(ctx, tx, c, &got, batch)
		})
		if err != nil {
			return err
		}
		total.add(got)
	}
	if snap.Complete && len(snap.Artifacts) > 0 && len(keep) > 0 {
		return s.tombstoneArtifacts(ctx, c, snap, runAt, keep, total)
	}
	return nil
}

func (s *Service) ingestArtifactBatch(ctx context.Context, tx pgx.Tx, c Caller, run *ManagementResult, batch []artifactInput) error {
	var filterRefs, aliasKeys []string
	for _, in := range batch {
		for _, ref := range in.filterRefs {
			if ref != "" {
				filterRefs = append(filterRefs, ref)
			}
		}
		if in.Kind == "application" {
			aliasKeys = append(aliasKeys, NormalizeSoftwareName(in.Name))
		}
	}
	filters, err := s.store.ResolveFiltersTx(ctx, tx, batch[0].Provider, filterRefs, batch[0].SyncedAt)
	if err != nil {
		return err
	}
	products, err := s.store.ResolveAliasesTx(ctx, tx, aliasKeys)
	if err != nil {
		return err
	}
	for i := range batch {
		in := &batch[i]
		if in.Kind == "application" {
			if id, ok := products[NormalizeSoftwareName(in.Name)]; ok {
				in.SoftwareProductID = &id
			}
		}
		if in.ReconcileAssignments {
			for j, ref := range in.filterRefs {
				if ref == "" {
					continue
				}
				id, ok := filters[ref]
				if !ok {
					// A reference to a filter the provider did not report in this run (or that was rejected): the assignment cannot be stored faithfully.
					in.rejectedAssignments++
					in.ReconcileAssignments = false
					break
				}
				in.Assignments[j].FilterID = &id
			}
		}
		art, err := s.upsertArtifact(ctx, tx, run, *in)
		if err != nil {
			return err
		}
		run.AssignmentsRejected += in.rejectedAssignments
		if !in.ReconcileAssignments {
			continue
		}
		if err := s.reconcileAssignments(ctx, tx, c, run, art, *in); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) upsertArtifact(ctx context.Context, tx pgx.Tx, run *ManagementResult, in artifactInput) (Artifact, error) {
	a, err := s.store.LockArtifactByExternalTx(ctx, tx, in.Provider, in.ExternalID)
	if err != nil {
		return Artifact{}, err
	}
	if a == nil {
		created, ok, err := s.store.InsertArtifactTx(ctx, tx, in.NewArtifact)
		if err != nil {
			return Artifact{}, err
		}
		if ok {
			run.ArtifactsCreated++
			if created.SoftwareProductID != nil {
				run.ArtifactsLinked++
			}
			return created, nil
		}
		if a, err = s.store.LockArtifactByExternalTx(ctx, tx, in.Provider, in.ExternalID); err != nil || a == nil {
			return Artifact{}, fmt.Errorf("lock artifact after concurrent create: %w", err)
		}
	}
	changed := a.DeletedObservedAt != nil || a.Kind != in.Kind || a.Name != in.Name || a.Platform != in.Platform ||
		!sameStr(a.Revision, in.Revision) || !sameStr(a.SoftwareProductID, in.SoftwareProductID)
	if !changed {
		run.ArtifactsUnchanged++
		if err := s.store.TouchArtifactTx(ctx, tx, a.ID, in.ObservedAt, in.SyncedAt, in.Source); err != nil {
			return Artifact{}, err
		}
		return *a, nil
	}
	if in.SoftwareProductID != nil && !sameStr(a.SoftwareProductID, in.SoftwareProductID) {
		run.ArtifactsLinked++
	}
	a.Kind, a.Name, a.Platform, a.Revision, a.SoftwareProductID, a.Source = in.Kind, in.Name, in.Platform, in.Revision, in.SoftwareProductID, in.Source
	a.ObservedAt, a.LastSyncedAt, a.DeletedObservedAt = in.ObservedAt, in.SyncedAt, nil
	updated, err := s.store.UpdateArtifactTx(ctx, tx, *a)
	if err != nil {
		return Artifact{}, err
	}
	run.ArtifactsUpdated++
	return updated, nil
}

// reconcileAssignments makes the artifact's current assignments equal the snapshot's: unchanged ones
// only get fresh freshness, changed ones are closed and opened anew, missing ones are closed, new ones
// opened. One ManagementAssignmentChanged event is published per artifact when anything changed.
func (s *Service) reconcileAssignments(ctx context.Context, tx pgx.Tx, c Caller, run *ManagementResult, art Artifact, in artifactInput) error {
	current, err := s.store.CurrentAssignmentsTx(ctx, tx, art.ID)
	if err != nil {
		return err
	}
	byID := map[string]Assignment{}
	for _, a := range current {
		byID[a.ProviderAssignmentID] = a
	}
	var closeIDs, touchIDs []string
	var open []AssignmentInput
	wanted := map[string]bool{}
	for _, a := range in.Assignments {
		wanted[a.ProviderAssignmentID] = true
		cur, ok := byID[a.ProviderAssignmentID]
		switch {
		case ok && a.same(cur):
			touchIDs = append(touchIDs, cur.ID)
			run.AssignmentsUnchanged++
		case ok:
			closeIDs = append(closeIDs, cur.ID)
			open = append(open, a)
		default:
			open = append(open, a)
		}
	}
	for _, cur := range current {
		if !wanted[cur.ProviderAssignmentID] {
			closeIDs = append(closeIDs, cur.ID)
		}
	}
	if err := s.store.TouchAssignmentsTx(ctx, tx, touchIDs, in.ObservedAt, in.SyncedAt, in.Source); err != nil {
		return err
	}
	// Close before opening: the current row of a provider assignment is unique.
	if err := s.store.CloseAssignmentsTx(ctx, tx, closeIDs, in.SyncedAt); err != nil {
		return err
	}
	for _, a := range open {
		if err := s.store.InsertAssignmentTx(ctx, tx, art.ID, a, in.Source, in.SyncedAt, in.ObservedAt); err != nil {
			return err
		}
	}
	run.AssignmentsClosed += len(closeIDs)
	run.AssignmentsOpened += len(open)
	if len(closeIDs)+len(open) == 0 {
		return nil
	}
	run.AssignmentEvents++
	return publish(ctx, tx, c, "ManagementAssignmentChanged", map[string]any{
		"artifactId": art.ID, "provider": art.Provider, "opened": len(open), "closed": len(closeIDs),
	})
}

// tombstoneArtifacts tombstones the provider's live artifacts the complete snapshot did not mention,
// unless that would remove more than half of them. Their current assignments are closed (one event per
// artifact) and the provider-reported-error findings of devices that had observations of them are re-evaluated.
func (s *Service) tombstoneArtifacts(ctx context.Context, c Caller, snap ManagementSnapshot, runAt time.Time, keep []string, total *ManagementResult) error {
	var got ManagementResult
	var devices []string
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		got = ManagementResult{}
		devices = nil
		ids, live, err := s.store.ArtifactTombstoneCandidatesTx(ctx, tx, snap.Provider, runAt, keep)
		if err != nil || len(ids) == 0 {
			return err
		}
		if guarded(len(ids), live) {
			got.ManagementTombstonesSkipped += len(ids)
			return nil
		}
		closed, ds, err := s.store.TombstoneArtifactsTx(ctx, tx, ids, runAt)
		if err != nil {
			return err
		}
		devices = ds
		got.ArtifactsTombstoned += len(ids)
		for _, id := range ids {
			n := closed[id]
			if n == 0 {
				continue
			}
			got.AssignmentsClosed += n
			got.AssignmentEvents++
			if err := publish(ctx, tx, c, "ManagementAssignmentChanged", map[string]any{"artifactId": id, "provider": snap.Provider, "opened": 0, "closed": n}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	total.add(got)
	// The tombstoning is committed; the findings of the affected devices follow in batches of their own.
	return s.reconcileDevices(ctx, c, total, devices)
}

// ---- observations ----

type observationItem struct {
	deviceExt, artifactExt, state, raw string
	observedAt                         time.Time
}

func normalizeObservation(r intune.ObservationRecord, runAt time.Time) (observationItem, bool) {
	dev, ok := cleanRequired(r.ExternalDeviceID, 200)
	if !ok {
		return observationItem{}, false
	}
	art, ok := cleanRequired(r.ArtifactExternalID, 200)
	if !ok {
		return observationItem{}, false
	}
	at := runAt
	if !r.ObservedAt.IsZero() {
		if t := r.ObservedAt.UTC().Truncate(time.Microsecond); t.Before(runAt) {
			at = t
		}
	}
	return observationItem{deviceExt: dev, artifactExt: art, state: oneOf(r.State, ObservationStates, "unknown"), raw: derefOr(cleanOptional(r.RawStatus, 200), ""), observedAt: at}, true
}

func (s *Service) ingestObservations(ctx context.Context, c Caller, snap ManagementSnapshot, runAt time.Time, total *ManagementResult) error {
	type pair struct{ d, a string }
	seen := map[pair]bool{}
	var items []observationItem
	for _, r := range snap.Observations {
		it, ok := normalizeObservation(r, runAt)
		if !ok || seen[pair{it.deviceExt, it.artifactExt}] {
			total.ObservationsSkipped++
			continue
		}
		seen[pair{it.deviceExt, it.artifactExt}] = true
		items = append(items, it)
	}
	touched := map[string]bool{}
	for start := 0; start < len(items); start += ObservationBatchSize {
		batch := items[start:min(start+ObservationBatchSize, len(items))]
		var got ManagementResult
		var devices []string
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			got = ManagementResult{}
			var err error
			devices, err = s.ingestObservationBatch(ctx, tx, c, &got, snap, batch, runAt)
			return err
		})
		if err != nil {
			return err
		}
		total.add(got)
		for _, d := range devices {
			touched[d] = true
		}
	}
	if snap.Complete && len(items) > 0 {
		retired, err := s.retireStaleObservations(ctx, snap, runAt, total)
		if err != nil {
			return err
		}
		for _, d := range retired {
			touched[d] = true
		}
	}
	// One reconciliation per device after all observation writes: the finding follows the device's whole
	// current state, and a device in several batches is not evaluated repeatedly.
	ids := make([]string, 0, len(touched))
	for d := range touched {
		ids = append(ids, d)
	}
	return s.reconcileDevices(ctx, c, total, ids)
}

// retireStaleObservations retires, in chunks of separate transactions, the provider's active observations
// that a complete snapshot did not report, unless that would retire more than half of them. It returns the
// affected devices.
func (s *Service) retireStaleObservations(ctx context.Context, snap ManagementSnapshot, runAt time.Time, total *ManagementResult) ([]string, error) {
	var skip bool
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		stale, live, err := s.store.StaleObservationsTx(ctx, tx, snap.Provider, runAt)
		if err != nil || stale == 0 {
			skip = true
			return err
		}
		if guarded(stale, live) {
			total.ManagementTombstonesSkipped += stale
			skip = true
		}
		return nil
	})
	if err != nil || skip {
		return nil, err
	}
	var devices []string
	for {
		var n int
		var ds []string
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			var err error
			n, ds, err = s.store.RetireStaleObservationsTx(ctx, tx, snap.Provider, runAt, runAt, StaleChunkSize)
			return err
		})
		if err != nil {
			return devices, err
		}
		total.ObservationsRetired += n
		devices = append(devices, ds...)
		if n < StaleChunkSize {
			return devices, nil
		}
	}
}

// ingestObservationBatch stores one batch and returns the live devices it touched.
func (s *Service) ingestObservationBatch(ctx context.Context, tx pgx.Tx, c Caller, run *ManagementResult, snap ManagementSnapshot, batch []observationItem, runAt time.Time) ([]string, error) {
	var devExt, artExt []string
	for _, it := range batch {
		devExt, artExt = append(devExt, it.deviceExt), append(artExt, it.artifactExt)
	}
	devices, err := s.store.ResolveDevicesTx(ctx, tx, snap.Provider, devExt)
	if err != nil {
		return nil, err
	}
	artifacts, err := s.store.ResolveArtifactsTx(ctx, tx, snap.Provider, artExt)
	if err != nil {
		return nil, err
	}
	var in []ObservationInput
	for _, it := range batch {
		d, dok := devices[it.deviceExt]
		a, aok := artifacts[it.artifactExt]
		if !dok || !aok || d.Deleted || a.Deleted {
			run.ObservationsSkipped++
			continue
		}
		in = append(in, ObservationInput{ArtifactID: a.ID, DeviceID: d.ID, State: it.state, RawStatus: it.raw, ObservedAt: it.observedAt})
	}
	if len(in) == 0 {
		return nil, nil
	}
	outcomes, err := s.store.UpsertObservationsTx(ctx, tx, in, snap.Source, runAt)
	if err != nil {
		return nil, err
	}
	var changed []ObservationInput
	deviceSet := map[string]bool{}
	var deviceIDs []string
	for _, o := range outcomes {
		switch {
		case o.Created:
			run.ObservationsCreated++
		case o.Changed:
			run.ObservationsChanged++
		default:
			run.ObservationsUnchanged++
		}
		if o.Changed {
			changed = append(changed, o.ObservationInput)
		}
		if !deviceSet[o.DeviceID] {
			deviceSet[o.DeviceID] = true
			deviceIDs = append(deviceIDs, o.DeviceID)
		}
	}
	return deviceIDs, s.store.AppendObservationHistoryTx(ctx, tx, changed, snap.Source)
}

// reconcileDevices reconciles the provider-reported-error findings of the devices in sorted batches, one
// transaction per batch.
func (s *Service) reconcileDevices(ctx context.Context, c Caller, total *ManagementResult, deviceIDs []string) error {
	ids := slices.Clone(deviceIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	for start := 0; start < len(ids); start += ReconcileBatchSize {
		batch := ids[start:min(start+ReconcileBatchSize, len(ids))]
		var got ManagementResult
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			got = ManagementResult{}
			return s.reconcileProviderErrors(ctx, tx, c, &got, batch)
		})
		if err != nil {
			return err
		}
		total.add(got)
	}
	return nil
}

// reconcileProviderErrors raises or resolves each device's provider_reported_error finding from its
// current failed and conflicting observations. The finding detail carries counts only.
func (s *Service) reconcileProviderErrors(ctx context.Context, tx pgx.Tx, c Caller, run *ManagementResult, deviceIDs []string) error {
	errs, err := s.store.ProviderErrorsTx(ctx, tx, deviceIDs)
	if err != nil {
		return err
	}
	var fr counters
	for _, id := range deviceIDs {
		pe, ok := errs[id]
		if !ok {
			continue
		}
		want := pe.Failed+pe.Conflict > 0
		if err := s.reconcileFinding(ctx, tx, c, &fr, FindingProviderReportedError, id, want, map[string]any{"failed": pe.Failed, "conflict": pe.Conflict}); err != nil {
			return err
		}
	}
	run.ProviderFindingsRaised += fr.FindingsRaised
	run.ProviderFindingsResolved += fr.FindingsResolved
	return nil
}

// ---- memberships ----

func (s *Service) ingestMemberships(ctx context.Context, snap ManagementSnapshot, runAt time.Time, total *ManagementResult) error {
	type key struct{ d, g string }
	seen := map[key]bool{}
	type item struct{ dev, group string }
	var items []item
	for _, m := range snap.Memberships {
		d, dok := cleanRequired(m.ExternalDeviceID, 200)
		g, gok := cleanRequired(m.GroupExternalID, 200)
		if !dok || !gok || seen[key{d, g}] {
			total.MembershipsSkipped++
			continue
		}
		seen[key{d, g}] = true
		items = append(items, item{d, g})
	}
	for start := 0; start < len(items); start += MembershipBatchSize {
		batch := items[start:min(start+MembershipBatchSize, len(items))]
		var got ManagementResult
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			got = ManagementResult{}
			ext := make([]string, len(batch))
			for i, it := range batch {
				ext[i] = it.dev
			}
			devices, err := s.store.ResolveDevicesTx(ctx, tx, snap.Provider, ext)
			if err != nil {
				return err
			}
			var in []MembershipInput
			for _, it := range batch {
				d, ok := devices[it.dev]
				if !ok || d.Deleted {
					got.MembershipsSkipped++
					continue
				}
				in = append(in, MembershipInput{DeviceID: d.ID, GroupExternalID: it.group})
			}
			opened, err := s.store.UpsertMembershipsTx(ctx, tx, snap.Provider, snap.Source, in, runAt)
			got.MembershipsOpened += opened
			got.MembershipsUnchanged += len(in) - opened
			return err
		})
		if err != nil {
			return err
		}
		total.add(got)
	}
	if !snap.Complete || len(items) == 0 {
		return nil
	}
	skip := false
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		stale, live, err := s.store.StaleMembershipsTx(ctx, tx, snap.Provider, runAt)
		if err != nil || stale == 0 {
			skip = true
			return err
		}
		if guarded(stale, live) {
			total.ManagementTombstonesSkipped += stale
			skip = true
		}
		return nil
	})
	if err != nil || skip {
		return err
	}
	// Closed in chunks of separate transactions so a large provider does not hold one long transaction.
	for {
		var n int
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			var err error
			n, err = s.store.CloseStaleMembershipsTx(ctx, tx, snap.Provider, runAt, runAt, StaleChunkSize)
			return err
		})
		if err != nil {
			return err
		}
		total.MembershipsClosed += n
		if n < StaleChunkSize {
			return nil
		}
	}
}

// ---- reads ----

// ListArtifacts lists Management Artifacts. Requires endpoint.management.view or endpoints.manage.
func (s *Service) ListArtifacts(ctx context.Context, p Principal, f ArtifactFilter) (ArtifactResult, error) {
	if !p.canViewManagement() {
		return ArtifactResult{}, ErrForbidden
	}
	if f.Kind != "" && !slices.Contains(ArtifactKinds, f.Kind) {
		return ArtifactResult{}, invalid("kind must be one of %s", strings.Join(ArtifactKinds, ", "))
	}
	if f.Platform != "" && !slices.Contains(OSPlatforms, f.Platform) {
		return ArtifactResult{}, invalid("platform must be one of %s", strings.Join(OSPlatforms, ", "))
	}
	f.Page = f.Page.Normalize()
	return s.store.ListArtifacts(ctx, f)
}

// GetArtifact returns an artifact with its assignments (current first, then at most MaxClosedAssignments
// of the most recently closed ones) and the number of live devices per active observation state. The
// assignments' provider group ids are returned only to callers who also hold organization.directory.view.
// Requires endpoint.management.view or endpoints.manage.
func (s *Service) GetArtifact(ctx context.Context, p Principal, id string) (ArtifactDetail, error) {
	if !p.canViewManagement() {
		return ArtifactDetail{}, ErrForbidden
	}
	if !validUUID(id) {
		return ArtifactDetail{}, ErrNotFound
	}
	a, err := s.store.GetArtifact(ctx, id)
	if err != nil {
		return ArtifactDetail{}, err
	}
	as, err := s.store.ArtifactAssignments(ctx, id)
	if err != nil {
		return ArtifactDetail{}, err
	}
	counts, err := s.store.ArtifactObservationCounts(ctx, id)
	if err != nil {
		return ArtifactDetail{}, err
	}
	if !p.DirectoryView {
		// Provider group ids reveal Directory data: only callers who may view Directory Groups see them.
		for i := range as {
			as[i].TargetGroupExternalID = nil
		}
	}
	return ArtifactDetail{Artifact: a, Assignments: as, ObservationCounts: counts}, nil
}

// ListManagementFilters lists provider assignment filters. Requires endpoint.management.view or endpoints.manage.
func (s *Service) ListManagementFilters(ctx context.Context, p Principal, f FilterListFilter) (ManagementFilterResult, error) {
	if !p.canViewManagement() {
		return ManagementFilterResult{}, ErrForbidden
	}
	if f.Platform != "" && !slices.Contains(OSPlatforms, f.Platform) {
		return ManagementFilterResult{}, invalid("platform must be one of %s", strings.Join(OSPlatforms, ", "))
	}
	f.Page = f.Page.Normalize()
	return s.store.ListManagementFilters(ctx, f)
}

// ListDeviceObservations lists the provider-reported results of a device. A device's observations
// reveal the device, so both device access (endpoints.view or endpoints.manage) and management access
// (endpoint.management.view or endpoints.manage) are required.
func (s *Service) ListDeviceObservations(ctx context.Context, p Principal, deviceID string, page Page) (ObservationResult, error) {
	if !p.canViewManagement() || !p.canView() {
		return ObservationResult{}, ErrForbidden
	}
	if !validUUID(deviceID) {
		return ObservationResult{}, ErrNotFound
	}
	if _, err := s.store.GetDevice(ctx, deviceID); err != nil {
		return ObservationResult{}, err
	}
	return s.store.ListDeviceObservations(ctx, deviceID, page.Normalize())
}
