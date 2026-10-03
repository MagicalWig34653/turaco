package application

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application/evaluation"
)

// Turaco-derived findings (F6 slice 4). assignment_ineffective says: an include assignment covers the Device, the
// evaluator expects the artifact to apply with confidence high or medium, and the provider shows nothing - or
// not_applicable - for it for longer than IneffectiveAfter. It is Turaco's inference from local data; the
// provider's own failed/conflict statements are the separate provider_reported_error finding and the two are never
// merged.
//
// The step runs once per management run, after the run's data is stored, over the live Devices in id order in
// snapshots of reconcileEvalBatch Devices. Every snapshot reads its inputs for all its Devices at once (assignments,
// observations, history), then the findings of the snapshot are raised or resolved idempotently in one transaction
// that also advances the rotating cursor (provider_sync_state.ineffective_cursor); EndpointFindingRaised is published
// only for a new finding. A run stops after MaxReconcileDevices Devices or ReconcileBudget of wall time and the next
// run continues behind the cursor, wrapping around at the end, so every live Device is evaluated again within
// ceil(live Devices / MaxReconcileDevices) runs (more when the time box cuts runs short). Until then its open finding
// keeps its last evaluated state: a finding is never left open forever, but it can lag behind that many runs.
const (
	// IneffectiveAfter is how long the evidence must have existed before the finding is raised.
	IneffectiveAfter = 7 * 24 * time.Hour
	// MaxReconcileDevices bounds the live Devices evaluated per run; the cursor continues with the rest next run.
	MaxReconcileDevices = 20000
	// reconcileEvalBatch is the Devices read, evaluated and written per step.
	reconcileEvalBatch = 25
	// DefaultReconcileBudget is the wall time after which a run stops evaluating and leaves the rest to the next run.
	DefaultReconcileBudget = 60 * time.Second
)

type ineffectiveResult struct {
	deviceID      string
	absent        int
	notApplicable int
	// complete is false when the artifact scan of the Device was cut: an existing finding must not be resolved on it.
	complete bool
}

// ineffCand is an (artifact, Device) pair that is assigned and expected on the Device and not observed (absent) or
// observed as not_applicable, with the start of the evidence known so far.
type ineffCand struct {
	dev   int
	pair  ObsPair
	start time.Time
	na    bool
}

// reconcileIneffective raises and resolves assignment_ineffective for the provider's live Devices. It runs only for
// the provider the views evaluate (Directory lookups are limited to that key).
func (s *Service) reconcileIneffective(ctx context.Context, c Caller, provider string, total *ManagementResult) error {
	if provider != s.viewProvider {
		return nil
	}
	origin, err := s.store.IneffectiveCursor(ctx, provider)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(s.reconcileBudget)
	type segment struct{ after, upTo string }
	segments := []segment{{after: origin}}
	if origin != "" {
		// Wrap around: the Devices before the cursor, up to it.
		segments = append(segments, segment{upTo: origin})
	}
	evaluated, stopped := 0, false
	for _, seg := range segments {
		after := seg.after
		for {
			if evaluated >= s.reconcileMax || time.Now().After(deadline) {
				stopped = true
				break
			}
			want := min(reconcileEvalBatch, s.reconcileMax-evaluated)
			var devs []Device
			var results []ineffectiveResult
			err := s.store.ReadSnapshot(ctx, func(ctx context.Context) error {
				var err error
				devs, err = s.store.LiveDevicesPage(ctx, provider, after, want)
				if err != nil {
					return err
				}
				if seg.upTo != "" {
					devs = slices.DeleteFunc(devs, func(d Device) bool { return d.ID > seg.upTo })
				}
				if len(devs) == 0 {
					return nil
				}
				results, err = s.evaluateIneffective(ctx, devs)
				return err
			})
			if err != nil {
				return err
			}
			// The segment ends when the page was short (or cut at the wrap point).
			lastPage := len(devs) < want
			if len(devs) == 0 {
				if after != "" || origin != "" {
					if err := s.store.InTx(ctx, func(tx pgx.Tx) error { return s.store.SaveIneffectiveCursorTx(ctx, tx, provider, "") }); err != nil {
						return err
					}
				}
				break
			}
			// The cursor follows the last written Device; the end of a segment restarts from the beginning (a wrap
			// that is cut short then re-evaluates the first Devices, which is harmless).
			next := devs[len(devs)-1].ID
			if lastPage {
				next = ""
			}
			var fr counters
			err = s.store.InTx(ctx, func(tx pgx.Tx) error {
				fr = counters{}
				for _, r := range results {
					raise := r.absent+r.notApplicable > 0
					if !raise && !r.complete {
						continue
					}
					detail := map[string]any{"absent": r.absent, "notApplicable": r.notApplicable, "afterDays": int(IneffectiveAfter / (24 * time.Hour))}
					if err := s.reconcileFinding(ctx, tx, c, &fr, FindingAssignmentIneffective, r.deviceID, raise, detail); err != nil {
						return err
					}
				}
				return s.store.SaveIneffectiveCursorTx(ctx, tx, provider, next)
			})
			if err != nil {
				return err
			}
			total.IneffectiveFindingsRaised += fr.FindingsRaised
			total.IneffectiveFindingsResolved += fr.FindingsResolved
			evaluated += len(devs)
			after = devs[len(devs)-1].ID
			if lastPage {
				break
			}
		}
		if stopped {
			break
		}
	}
	if stopped {
		live, err := s.store.CountLiveDevicesAfter(ctx, provider, "")
		if err != nil {
			return err
		}
		total.IneffectiveDevicesSkipped += max(0, live-evaluated)
	}
	// A tombstoned Device had its findings resolved by the device ingestion.
	return nil
}

// evaluateIneffective counts, for each Device, the artifacts that are assigned and expected on it but absent or
// not_applicable at the provider for longer than IneffectiveAfter. All reads are batched across the Devices.
func (s *Service) evaluateIneffective(ctx context.Context, devs []Device) ([]ineffectiveResult, error) {
	// The finding is derived from all inputs regardless of who triggered the run; its detail carries counts only.
	ec, err := s.newEvalCtx(ctx, devs, evalOpts{holders: true})
	if err != nil {
		return nil, err
	}
	results := make([]ineffectiveResult, len(devs))
	arts := make([][]Artifact, len(devs))
	artByID := map[string]bool{}
	for i, d := range devs {
		results[i] = ineffectiveResult{deviceID: d.ID, complete: true}
		q := ReachQuery{Provider: s.viewProvider, DeviceID: d.ID, GroupExternalIDs: ec.groupsOf(d), AllDevices: true, AllUsers: true, Limit: deviceViewBatch}
		for {
			page, err := s.store.ReachableArtifacts(ctx, q)
			if err != nil {
				return nil, err
			}
			arts[i] = append(arts[i], page...)
			if len(page) < q.Limit {
				break
			}
			if len(arts[i]) >= MaxViewScan {
				results[i].complete = false
				break
			}
			q.AfterID = page[len(page)-1].ID
		}
		for _, a := range arts[i] {
			artByID[a.ID] = true
		}
	}
	artIDs := make([]string, 0, len(artByID))
	for id := range artByID {
		artIDs = append(artIDs, id)
	}
	slices.Sort(artIDs)
	if len(artIDs) == 0 {
		return results, nil
	}
	assigns, err := s.store.CurrentAssignmentsOf(ctx, artIDs)
	if err != nil {
		return nil, err
	}
	obsMap, err := s.observationMap(ctx, artIDs, ids(devs))
	if err != nil {
		return nil, err
	}
	reported, err := s.store.ArtifactsWithObservations(ctx, artIDs)
	if err != nil {
		return nil, err
	}
	var cands []ineffCand
	for i, d := range devs {
		// The evidence cannot be older than what Turaco has known about the Device and its groups.
		base := latest(d.CreatedAt, ec.devMemFrom[d.ID])
		for _, a := range arts[i] {
			as := assigns[a.ID]
			res := ec.evaluate(d, as)
			if res.Result != ExpectedApplicable || (res.Confidence != evaluation.ConfidenceHigh && res.Confidence != evaluation.ConfidenceMedium) || !includeHit(as, res) {
				continue
			}
			start := latest(base, assignedSince(as, res))
			o, observed := obsMap[obsKey{a.ID, d.ID}]
			switch {
			case !observed:
				// A provider that reports nothing for this artifact on any Device says nothing about this one.
				if reported[a.ID] {
					cands = append(cands, ineffCand{dev: i, pair: ObsPair{ArtifactID: a.ID, DeviceID: d.ID}, start: start})
				}
			case o.NormalizedState == "not_applicable":
				cands = append(cands, ineffCand{dev: i, pair: ObsPair{ArtifactID: a.ID, DeviceID: d.ID}, start: start, na: true})
			}
		}
	}
	// The clocks only move the start later, so a candidate that is too young already is dropped before the history reads.
	var naPairs, absentPairs []ObsPair
	for _, k := range cands {
		if ec.now.Sub(k.start) <= IneffectiveAfter {
			continue
		}
		if k.na {
			naPairs = append(naPairs, k.pair)
		} else {
			absentPairs = append(absentPairs, k.pair)
		}
	}
	since, err := s.store.ObservationStateSince(ctx, naPairs)
	if err != nil {
		return nil, err
	}
	retired, err := s.store.ObservationsRetiredAt(ctx, absentPairs)
	if err != nil {
		return nil, err
	}
	for _, k := range cands {
		start := k.start
		if k.na {
			start = latest(start, since[k.pair])
		} else {
			// Absent since the observation was last seen: a retired observation of the pair ends the earlier evidence.
			start = latest(start, retired[k.pair])
		}
		if ec.now.Sub(start) <= IneffectiveAfter {
			continue
		}
		if k.na {
			results[k.dev].notApplicable++
		} else {
			results[k.dev].absent++
		}
	}
	return results, nil
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// assignedSince is the newest start of the include assignments that cover the Device: the configuration has been in
// place at least since then.
func assignedSince(as []Assignment, res evaluation.Result) time.Time {
	var t time.Time
	for i, a := range as {
		if a.Mode == evaluation.ModeInclude && res.Assignments[i].Hit == evaluation.Yes {
			t = latest(t, a.ValidFrom)
		}
	}
	return t
}
