package application

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// MatchResult counts one match run of an Advisory.
type MatchResult struct {
	Created    int
	Updated    int
	Remediated int
	Reopened   int
	// Stale counts findings that no longer match the criteria without a newer observation (for example
	// after a criteria change); they keep their state.
	Stale int
	// Truncated reports more than MaxMatchDevices affected Devices; the rest is not derived this run.
	Truncated bool
	// Skipped reports an Advisory that is unknown or not in a matchable status.
	Skipped bool
}

var errCriteriaChanged = errors.New("security: criteria changed during match")

func (r MatchResult) changed() bool {
	return r.Created+r.Updated+r.Remediated+r.Reopened > 0
}

type matchKey struct{ device, product string }

type hit struct {
	confidence string
	version    string
	observedAt time.Time
}

// better prefers probable over potential, then the newer observation.
func better(a, b hit) hit {
	if a.confidence == "" {
		return b
	}
	if a.confidence != b.confidence {
		if b.confidence == ConfidenceProbable {
			return b
		}
		return a
	}
	if b.observedAt.After(a.observedAt) {
		return b
	}
	return a
}

// evaluate matches one installation against the criteria: probable when a criterion's normalized product
// (explicit or by exact product name) matches and the version lies inside its rules; potential when the
// product matched only through an alias or the version is not comparable; no match otherwise.
func evaluate(crit []Criterion, in Installation) (string, bool) {
	best := ""
	for _, c := range crit {
		if c.SoftwareProductID == nil || *c.SoftwareProductID != in.SoftwareProductID {
			continue
		}
		if c.OSPlatform != nil && *c.OSPlatform != in.DevicePlatform {
			continue
		}
		affected, comparable := Affected(c.Rules, in.RawVersion)
		switch {
		case !comparable:
			best = ConfidencePotential
		case !affected:
			continue
		case c.MatchMethod != nil && *c.MatchMethod == MethodAlias:
			best = ConfidencePotential
		default:
			return ConfidenceProbable, true
		}
	}
	return best, best != ""
}

func productIDs(crit []Criterion) []string {
	var out []string
	for _, c := range crit {
		if c.SoftwareProductID != nil && !slices.Contains(out, *c.SoftwareProductID) {
			out = append(out, *c.SoftwareProductID)
		}
	}
	return out
}

// remediation is the evidence that a finding's exposure is gone.
type remediation struct {
	reason string
	at     time.Time
}

// MatchCaller is the system caller of a match run.
func MatchCaller(correlationID string) Caller {
	return Caller{Actor: audit.SystemActor(systemActor), CorrelationID: correlationID}
}

// Match derives the Vulnerability Findings of one Advisory from the current software installations
// (endpoints/public). It is idempotent per Advisory and installation snapshot: a matching installation
// creates an open finding or refreshes an existing one (a remediated finding that is observed again
// reopens, reason observed_again); a finding whose Device no longer has a matching installation becomes
// remediated only when a newer observation shows it (the installation was no longer reported, a
// non-matching version was observed after the finding or the Device was tombstoned) - never because a
// task was closed. Runs of the same Advisory are serialized by its row lock. A run that finds the
// criteria changed meanwhile runs again (at most three times; the periodic job catches the rest).
func (s *Service) Match(ctx context.Context, c Caller, advisoryID string) (MatchResult, error) {
	if !uuidPattern.MatchString(advisoryID) {
		return MatchResult{Skipped: true}, nil
	}
	advisoryID = strings.ToLower(advisoryID)
	var res MatchResult
	for range 3 {
		var rev int
		var err error
		if res, rev, err = s.matchOnce(ctx, c, advisoryID); errors.Is(err, errCriteriaChanged) {
			continue
		} else if err != nil || res.Skipped {
			return res, err
		}
		cur, err := s.store.GetAdvisory(ctx, advisoryID)
		if err != nil {
			return res, err
		}
		if cur.CriteriaRevision == rev {
			return res, nil
		}
	}
	return res, errCriteriaChanged
}

func (s *Service) matchOnce(ctx context.Context, c Caller, id string) (MatchResult, int, error) {
	adv, err := s.store.GetAdvisory(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return MatchResult{Skipped: true}, 0, nil
	}
	if err != nil {
		return MatchResult{}, 0, err
	}
	if !slices.Contains(matchable, adv.Status) {
		return MatchResult{Skipped: true}, adv.CriteriaRevision, nil
	}
	crit, err := s.store.Criteria(ctx, id)
	if err != nil {
		return MatchResult{}, 0, err
	}
	ingestion, err := s.inventory.LatestIngestionAt(ctx)
	if err != nil {
		return MatchResult{}, 0, fmt.Errorf("read latest ingestion: %w", err)
	}
	var res MatchResult
	hits := map[matchKey]hit{}
	devices := map[string]bool{}
	if pids := productIDs(crit); len(pids) > 0 {
		cursor := ""
		for {
			page, next, err := s.inventory.InstallationsByProducts(ctx, pids, cursor, 1000)
			if err != nil {
				return MatchResult{}, 0, fmt.Errorf("read installations: %w", err)
			}
			for _, in := range page {
				conf, ok := evaluate(crit, in)
				if !ok {
					continue
				}
				if !devices[in.DeviceID] && len(devices) >= MaxMatchDevices {
					res.Truncated = true
					break
				}
				devices[in.DeviceID] = true
				k := matchKey{in.DeviceID, in.SoftwareProductID}
				hits[k] = better(hits[k], hit{confidence: conf, version: in.RawVersion, observedAt: in.ObservedAt})
			}
			if res.Truncated {
				break
			}
			if next == "" {
				break
			}
			cursor = next
		}
	}
	remediate, err := s.evidence(ctx, crit, id, hits)
	if err != nil {
		return MatchResult{}, 0, err
	}
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		res.Created, res.Updated, res.Remediated, res.Reopened, res.Stale = 0, 0, 0, 0, 0
		locked, err := s.store.LockAdvisoryTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if !slices.Contains(matchable, locked.Status) {
			res.Skipped = true
			return nil
		}
		// Inventory was read before the row lock. A concurrent criteria edit must never
		// let this run apply findings calculated from an older rule set.
		if locked.CriteriaRevision != adv.CriteriaRevision {
			return errCriteriaChanged
		}
		return s.applyMatch(ctx, tx, c, adv, hits, remediate, ingestion, &res)
	})
	if err != nil {
		return MatchResult{}, 0, err
	}
	return res, adv.CriteriaRevision, nil
}

// evidence checks the findings without a current match: it reads every installation of their products on
// their Devices and decides per finding whether a newer observation shows the exposure gone. A matching
// current installation found this way (the first read was truncated) counts as a hit.
func (s *Service) evidence(ctx context.Context, crit []Criterion, advisoryID string, hits map[matchKey]hit) (map[string]remediation, error) {
	existing, err := s.store.FindingsOfAdvisory(ctx, advisoryID)
	if err != nil {
		return nil, err
	}
	var missing []Finding
	for _, f := range existing {
		if _, ok := hits[matchKey{f.DeviceID, f.SoftwareProductID}]; !ok && slices.Contains(remediable, f.Status) {
			missing = append(missing, f)
		}
	}
	byDevice := map[string][]Finding{}
	var devices, products []string
	for _, f := range missing {
		if _, ok := byDevice[f.DeviceID]; !ok {
			devices = append(devices, f.DeviceID)
		}
		byDevice[f.DeviceID] = append(byDevice[f.DeviceID], f)
		if !slices.Contains(products, f.SoftwareProductID) {
			products = append(products, f.SoftwareProductID)
		}
	}
	slices.Sort(devices)
	out := map[string]remediation{}
	const deviceChunk, productChunk = 500, 100
	for d := 0; d < len(devices); d += deviceChunk {
		devs := devices[d:min(d+deviceChunk, len(devices))]
		retired, err := s.inventory.DeviceRetired(ctx, devs)
		if err != nil {
			return nil, fmt.Errorf("read device states: %w", err)
		}
		for p := 0; p < len(products); p += productChunk {
			prods := products[p:min(p+productChunk, len(products))]
			if err := s.deviceEvidence(ctx, crit, devs, prods, byDevice, retired, hits, out); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// deviceEvidence decides the findings of one chunk of Devices and Software Products.
func (s *Service) deviceEvidence(ctx context.Context, crit []Criterion, devs, prods []string, byDevice map[string][]Finding,
	retired map[string]*time.Time, hits map[matchKey]hit, out map[string]remediation) error {
	insts, truncated, err := s.inventory.InstallationsOnDevices(ctx, devs, prods)
	if err != nil {
		return fmt.Errorf("read device installations: %w", err)
	}
	byKey := map[matchKey][]Installation{}
	for _, in := range insts {
		k := matchKey{in.DeviceID, in.SoftwareProductID}
		byKey[k] = append(byKey[k], in)
	}
	for _, dev := range devs {
		for _, f := range byDevice[dev] {
			if !slices.Contains(prods, f.SoftwareProductID) {
				continue
			}
			k := matchKey{f.DeviceID, f.SoftwareProductID}
			list := byKey[k]
			if truncated && len(list) == 0 {
				continue // unknown: decide next run
			}
			var found hit
			var at *time.Time
			reason := ReasonNoLongerObserved
			for _, in := range list {
				if !in.Retired {
					if conf, ok := evaluate(crit, in); ok {
						found = better(found, hit{confidence: conf, version: in.RawVersion, observedAt: in.ObservedAt})
						continue
					}
					reason = ReasonVersionChanged
					t := in.ObservedAt
					if at == nil || t.After(*at) {
						at = &t
					}
					continue
				}
				if in.RetiredAt != nil && (at == nil || in.RetiredAt.After(*at)) {
					t := *in.RetiredAt
					at = &t
				}
			}
			if found.confidence != "" {
				hits[k] = found
				continue
			}
			if d := retired[f.DeviceID]; d != nil {
				reason = ReasonDeviceRetired
				if at == nil || d.After(*at) {
					t := *d
					at = &t
				}
			}
			if at != nil && at.After(f.LastSeenAt) {
				out[f.ID] = remediation{reason: reason, at: *at}
			}
		}
	}
	return nil
}

func (s *Service) applyMatch(ctx context.Context, tx pgx.Tx, c Caller, adv Advisory, hits map[matchKey]hit, remediate map[string]remediation,
	ingestion *time.Time, res *MatchResult) error {
	findings, err := s.store.LockFindingsOfAdvisoryTx(ctx, tx, adv.ID)
	if err != nil {
		return err
	}
	byKey := make(map[matchKey]Finding, len(findings))
	for _, f := range findings {
		byKey[matchKey{f.DeviceID, f.SoftwareProductID}] = f
	}
	keys := make([]matchKey, 0, len(hits))
	for k := range hits {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b matchKey) int {
		return cmp.Or(cmp.Compare(a.device, b.device), cmp.Compare(a.product, b.product))
	})
	now := s.now()
	for _, k := range keys {
		h := hits[k]
		cur, ok := byKey[k]
		if !ok {
			f := Finding{AdvisoryID: adv.ID, DeviceID: k.device, SoftwareProductID: k.product, InstalledVersion: h.version,
				Confidence: h.confidence, Status: FindingOpen, FirstSeenAt: h.observedAt, LastSeenAt: h.observedAt}
			out, inserted, err := s.store.InsertFindingTx(ctx, tx, f)
			if err != nil {
				return err
			}
			if !inserted {
				continue
			}
			if err := s.store.InsertFindingTransitionTx(ctx, tx, transition(c, out.ID, nil, out.Status, "create", "")); err != nil {
				return err
			}
			if err := publishFinding(ctx, tx, c, out, "create", nil, ""); err != nil {
				return err
			}
			res.Created++
			continue
		}
		next := cur
		next.Confidence, next.InstalledVersion = h.confidence, h.version
		if h.observedAt.After(cur.LastSeenAt) {
			next.LastSeenAt = h.observedAt
		}
		if h.observedAt.Before(next.FirstSeenAt) {
			next.FirstSeenAt = h.observedAt
		}
		if cur.Status == FindingRemediated {
			next.Status, next.StatusReason, next.RemediatedAt = FindingOpen, strPtr(ReasonObservedAgain), nil
			if _, err := s.commitFinding(ctx, tx, c, cur, next, "observe_again", "observed_again", ReasonObservedAgain, nil); err != nil {
				return err
			}
			res.Reopened++
			continue
		}
		if next.Confidence == cur.Confidence && next.InstalledVersion == cur.InstalledVersion &&
			next.LastSeenAt.Equal(cur.LastSeenAt) && next.FirstSeenAt.Equal(cur.FirstSeenAt) {
			continue
		}
		// Observation refreshes do not bump the version (a user's pending operation stays valid);
		// a changed confidence or version does.
		if err := s.store.ObserveFindingTx(ctx, tx, next); err != nil {
			return err
		}
		if next.Confidence != cur.Confidence || next.InstalledVersion != cur.InstalledVersion {
			res.Updated++
		}
	}
	for _, cur := range findings {
		ev, ok := remediate[cur.ID]
		if !ok {
			continue
		}
		if _, matched := hits[matchKey{cur.DeviceID, cur.SoftwareProductID}]; matched || !slices.Contains(remediable, cur.Status) || ev.at.Before(cur.LastSeenAt) {
			continue
		}
		next := clearRisk(cur)
		next.Status, next.StatusReason, next.RemediatedAt = FindingRemediated, strPtr(ev.reason), &now
		if _, err := s.commitFinding(ctx, tx, c, cur, next, "remediate", "remediated", ev.reason,
			map[string]any{"evidenceAt": ev.at.UTC().Format(time.RFC3339)}); err != nil {
			return err
		}
		res.Remediated++
	}
	for _, cur := range findings {
		if _, matched := hits[matchKey{cur.DeviceID, cur.SoftwareProductID}]; !matched && slices.Contains(remediable, cur.Status) {
			if _, gone := remediate[cur.ID]; !gone {
				res.Stale++
			}
		}
	}
	if err := s.store.RecordMatchTx(ctx, tx, adv.ID, MatchState{Revision: adv.CriteriaRevision, IngestionAt: ingestion, Truncated: res.Truncated}); err != nil {
		return err
	}
	if !res.changed() && !res.Truncated {
		return nil
	}
	return recordAudit(ctx, tx, c, "security.advisory.matched", "security_advisory", adv.ID, nil, nil, map[string]any{
		"criteriaRevision": adv.CriteriaRevision, "created": res.Created, "updated": res.Updated, "remediated": res.Remediated,
		"reopened": res.Reopened, "stale": res.Stale, "truncated": res.Truncated})
}

// HandleMatch is the job handler of MatchJobType (payload {"advisoryId"}).
func (s *Service) HandleMatch(ctx context.Context, job jobs.Job) error {
	var p struct {
		AdvisoryID string `json:"advisoryId"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || !uuidPattern.MatchString(p.AdvisoryID) {
		return jobs.Permanent(fmt.Errorf("invalid %s payload", MatchJobType))
	}
	_, err := s.Match(ctx, MatchCaller("job:"+job.ID), p.AdvisoryID)
	return err
}

// HandleMatchAll is the job handler of MatchAllJobType: it queues the match job of every matchable
// Advisory whose criteria changed since its last match, that was never matched or whose last match
// predates the latest endpoint ingestion (at most MaxMatchAllAdvisories per run).
func (s *Service) HandleMatchAll(ctx context.Context, _ jobs.Job) error {
	ingestion, err := s.inventory.LatestIngestionAt(ctx)
	if err != nil {
		return fmt.Errorf("read latest ingestion: %w", err)
	}
	ids, err := s.store.AdvisoriesToMatch(ctx, matchable, ingestion, MaxMatchAllAdvisories)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		for _, id := range ids {
			if err := enqueueMatch(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
}
