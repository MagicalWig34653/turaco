package application

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application/evaluation"
)

// Turaco-derived findings (F6 slice 4). assignment_ineffective says: an include assignment covers the Device, the
// evaluator expects the artifact to apply with confidence high or medium, and the provider shows nothing - or
// not_applicable - for it for longer than IneffectiveAfter. It is Turaco's inference from local data; the
// provider's own failed/conflict statements are the separate provider_reported_error finding and the two are never
// merged. The step runs once per management run, after the run's data is stored, over the live Devices in sorted
// batches; it raises or resolves findings idempotently and publishes EndpointFindingRaised only for a new one.
const (
	// IneffectiveAfter is how long the evidence must have existed before the finding is raised.
	IneffectiveAfter = 7 * 24 * time.Hour
	// MaxReconcileDevices bounds the live Devices evaluated per run; the remainder is counted as skipped.
	MaxReconcileDevices = 20000
	// reconcileEvalBatch is the Devices evaluated and written per step.
	reconcileEvalBatch = 100
)

type ineffectiveResult struct {
	deviceID      string
	absent        int
	notApplicable int
	// complete is false when the artifact scan of the Device was cut: an existing finding must not be resolved on it.
	complete bool
}

// reconcileIneffective raises and resolves assignment_ineffective for the provider's live Devices. It runs only for
// the provider the views evaluate (Directory lookups are limited to that key).
func (s *Service) reconcileIneffective(ctx context.Context, c Caller, provider string, total *ManagementResult) error {
	if provider != s.viewProvider {
		return nil
	}
	after, evaluated := "", 0
	for evaluated < MaxReconcileDevices {
		var devs []Device
		var results []ineffectiveResult
		err := s.store.ReadSnapshot(ctx, func(ctx context.Context) error {
			var err error
			devs, err = s.store.LiveDevicesPage(ctx, provider, after, min(reconcileEvalBatch, MaxReconcileDevices-evaluated))
			if err != nil || len(devs) == 0 {
				return err
			}
			// The finding is derived from all inputs regardless of who triggered the run; its detail carries counts only.
			ec, err := s.newEvalCtx(ctx, devs, evalOpts{holders: true})
			if err != nil {
				return err
			}
			for _, d := range devs {
				r, err := s.deviceIneffective(ctx, ec, d)
				if err != nil {
					return err
				}
				results = append(results, r)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(devs) == 0 {
			break
		}
		var fr counters
		err = s.store.InTx(ctx, func(tx pgx.Tx) error {
			fr = counters{}
			for _, r := range results {
				want := r.absent+r.notApplicable > 0
				if !want && !r.complete {
					continue
				}
				detail := map[string]any{"absent": r.absent, "notApplicable": r.notApplicable, "afterDays": int(IneffectiveAfter / (24 * time.Hour))}
				if err := s.reconcileFinding(ctx, tx, c, &fr, FindingAssignmentIneffective, r.deviceID, want, detail); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		total.IneffectiveFindingsRaised += fr.FindingsRaised
		total.IneffectiveFindingsResolved += fr.FindingsResolved
		evaluated += len(devs)
		after = devs[len(devs)-1].ID
		if len(devs) < reconcileEvalBatch {
			break
		}
	}
	if evaluated >= MaxReconcileDevices {
		skipped, err := s.store.CountLiveDevicesAfter(ctx, provider, after)
		if err != nil {
			return err
		}
		total.IneffectiveDevicesSkipped += skipped
	}
	// A tombstoned Device had its findings resolved by the device ingestion.
	return nil
}

// deviceIneffective counts the artifacts that are assigned and expected on the Device but absent or
// not_applicable at the provider for longer than IneffectiveAfter.
func (s *Service) deviceIneffective(ctx context.Context, ec *evalCtx, d Device) (ineffectiveResult, error) {
	out := ineffectiveResult{deviceID: d.ID, complete: true}
	// The evidence cannot be older than what Turaco has known about the Device and its groups.
	base := latest(d.CreatedAt, ec.devMemFrom[d.ID])
	q := ReachQuery{Provider: s.viewProvider, DeviceID: d.ID, GroupExternalIDs: ec.groupsOf(d), AllDevices: true, AllUsers: true, Limit: deviceViewBatch}
	scanned := 0
	for {
		arts, err := s.store.ReachableArtifacts(ctx, q)
		if err != nil {
			return out, err
		}
		if len(arts) == 0 {
			return out, nil
		}
		artIDs := make([]string, 0, len(arts))
		for _, a := range arts {
			artIDs = append(artIDs, a.ID)
		}
		assigns, err := s.store.CurrentAssignmentsOf(ctx, artIDs)
		if err != nil {
			return out, err
		}
		obsMap, err := s.observationMap(ctx, artIDs, []string{d.ID})
		if err != nil {
			return out, err
		}
		reported, err := s.store.ArtifactsWithObservations(ctx, artIDs)
		if err != nil {
			return out, err
		}
		var naIDs []string
		for id, o := range obsMap {
			if o.NormalizedState == "not_applicable" {
				naIDs = append(naIDs, id.artifact)
			}
		}
		since, err := s.store.ObservationStateSince(ctx, d.ID, naIDs)
		if err != nil {
			return out, err
		}
		for _, a := range arts {
			q.AfterID = a.ID
			scanned++
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
				if reported[a.ID] && ec.now.Sub(start) > IneffectiveAfter {
					out.absent++
				}
			case o.NormalizedState == "not_applicable":
				if ec.now.Sub(latest(start, since[a.ID])) > IneffectiveAfter {
					out.notApplicable++
				}
			}
		}
		if len(arts) < q.Limit {
			return out, nil
		}
		if scanned >= MaxViewScan {
			out.complete = false
			return out, nil
		}
	}
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
