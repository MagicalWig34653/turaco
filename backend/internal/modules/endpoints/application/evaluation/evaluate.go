// Package evaluation is the pure Expected Applicability evaluator of F6 (docs/integrations/
// intune-assignment-intelligence.md, ADR-0020): from the assignments of one Management Artifact and
// the known group memberships of a Device and its primary User it derives whether the artifact should
// apply, with confidence, ordered reason codes and an explainable Assignment Path.
//
// It has no storage and no clock of its own. The result is Turaco-derived and recomputable; it is not
// provider state. Anything it cannot decide safely is "unknown", never silently "not_applicable".
package evaluation

import (
	"slices"
	"sort"
	"time"
)

// Results.
const (
	Applicable    = "applicable"
	Excluded      = "excluded"
	NotApplicable = "not_applicable"
	UnknownResult = "unknown"
)

// Confidence levels.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// Origin tells whether a membership came from the Device itself or from its primary User.
const (
	OriginDevice = "device"
	OriginUser   = "user"
)

// Reason codes, in the order they are reported.
const (
	ReasonIncluded         = "included_by_assignment"
	ReasonExcluded         = "excluded_by_assignment"
	ReasonExclusionWins    = "exclusion_overrides_include"
	ReasonNoAssignments    = "no_assignments"
	ReasonNoMatch          = "no_matching_assignment"
	ReasonTargetAllDevices = "target_all_devices"
	ReasonTargetAllUsers   = "target_all_users"
	ReasonMemberNested     = "member_via_nested_group"
	ReasonMemberViaUser    = "membership_via_user"
	ReasonUserUnknown      = "user_unknown"
	// ReasonMembershipsUnknown: the Device or its User has no synced membership data at all, so "not a member" is not known.
	ReasonMembershipsUnknown = "memberships_unknown"
	// ReasonInputsTruncated: a bounded input lookup (memberships, nesting) was cut, so the membership picture may be incomplete.
	ReasonInputsTruncated = "inputs_truncated"
	// ReasonMixedOrigin: the include and the exclude that would decide reach the Device through different origins
	// (a User group and a Device group). Intune does not apply such mixes reliably, so Turaco does not claim a result.
	ReasonMixedOrigin       = "mixed_origin_exclusion"
	ReasonFilterMatched     = "filter_matched"
	ReasonFilterNotMatched  = "filter_not_matched"
	ReasonFilterUnsupported = "filter_unsupported"
	ReasonFilterMissing     = "filter_missing"
	ReasonFilterUnknown     = "filter_input_missing"
	ReasonInputsStale       = "inputs_stale"
)

var reasonOrder = []string{ReasonIncluded, ReasonExcluded, ReasonExclusionWins, ReasonMixedOrigin, ReasonNoAssignments, ReasonNoMatch, ReasonTargetAllDevices,
	ReasonTargetAllUsers, ReasonMemberNested, ReasonMemberViaUser, ReasonUserUnknown, ReasonMembershipsUnknown, ReasonInputsTruncated, ReasonFilterMatched, ReasonFilterNotMatched,
	ReasonFilterUnsupported, ReasonFilterMissing, ReasonFilterUnknown, ReasonInputsStale}

// FreshnessLimit is the age after which an input lowers the confidence to low.
const FreshnessLimit = 48 * time.Hour

// Target kinds and modes (same values as the management model).
const (
	TargetGroup      = "group"
	TargetAllDevices = "all_devices"
	TargetAllUsers   = "all_users"
	ModeInclude      = "include"
	ModeExclude      = "exclude"
	FilterModeNone   = "none"
	FilterModeInc    = "include"
	FilterModeExc    = "exclude"
)

// Filter outcomes of one assignment.
const (
	FilterNone        = "none"
	FilterMatch       = "match"
	FilterNoMatch     = "no_match"
	FilterUnsupported = "unsupported"
	FilterMissing     = "missing"
	FilterUnknown     = "unknown"
)

// Filter is the provider assignment filter referenced by an assignment.
type Filter struct {
	ID      string
	Name    string
	Rule    string
	Deleted bool
}

// Assignment is one current provider assignment of the evaluated artifact.
type Assignment struct {
	ID              string
	TargetKind      string
	GroupExternalID string
	Mode            string
	Intent          string
	Filter          *Filter
	FilterMode      string
	// LastSyncedAt is when the provider last confirmed the assignment (zero: not judged for staleness).
	LastSyncedAt time.Time
}

// Device is the evaluated Device. An empty attribute is unknown.
type Device struct {
	Platform     string
	Ownership    string
	Manufacturer string
	Model        string
	OSVersion    string
	// SyncedAt is when the provider last confirmed the Device (zero: not judged for staleness).
	SyncedAt time.Time
}

// Membership is a direct membership in a group (by the group's provider external id).
type Membership struct {
	GroupExternalID string
	// ObservedAt is when the membership was last confirmed (zero: not judged for staleness).
	ObservedAt time.Time
}

// Input is everything the evaluator knows. Nesting maps a child group to its parents (direct edges);
// members of a child group are members of its parents.
type Input struct {
	Now               time.Time
	Device            Device
	DeviceMemberships []Membership
	// UserKnown is true when the Device's primary User is known; UserMemberships are then that User's
	// direct group memberships.
	UserKnown       bool
	UserMemberships []Membership
	Nesting         map[string][]string
	Assignments     []Assignment

	// DeviceMembershipsUnknown: the Device has no synced membership data (an empty list is then not "member of nothing").
	DeviceMembershipsUnknown bool
	// UserMembershipsUnknown: the User (when known) has no synced directory data.
	UserMembershipsUnknown bool
	// DeviceInputsTruncated / UserInputsTruncated: a bounded lookup behind the Device's / User's memberships or
	// the nesting was cut; a missing group then proves nothing.
	DeviceInputsTruncated bool
	UserInputsTruncated   bool

	// DeviceClosure and UserClosure are optional precomputed closures (see NewClosure); callers evaluating many
	// artifacts for the same Device reuse them. Nil means: compute from the memberships and the nesting.
	DeviceClosure Closure
	UserClosure   Closure
}

// Trace explains how a Device or User reaches a target group: Chain starts at the group the member
// belongs to directly and ends at the target (one element when it is the target itself).
type Trace struct {
	Origin string
	Chain  []string
}

// Nested reports whether the membership is inherited through nested groups.
func (t Trace) Nested() bool { return len(t.Chain) > 1 }

// AssignmentResult is the evaluation of one assignment.
type AssignmentResult struct {
	AssignmentID string
	// Target says whether the Device (or its User) is addressed by the assignment's target, ignoring the filter.
	Target Tri
	// Traces are the ways the target is reached (at most one per origin); empty for all_devices/all_users
	// except that all_users records an OriginUser trace with an empty chain.
	Traces []Trace
	Filter string
	// Hit combines target and filter: yes when the assignment's scope covers the Device.
	Hit     Tri
	Reasons []string
}

// Step kinds of an Assignment Path.
const (
	StepMembership = "membership"
	StepNesting    = "nested_in"
	StepTarget     = "target"
	StepAssignment = "assignment"
	StepFilter     = "filter"
	StepUserUnk    = "user_unknown"
	StepFinal      = "result"
)

// Step is one line of an Assignment Path. Only the fields that belong to its kind are set.
type Step struct {
	Kind            string
	Origin          string
	GroupExternalID string
	AssignmentID    string
	Mode            string
	Intent          string
	TargetKind      string
	FilterResult    string
	Result          string
}

// Result is the evaluation of one artifact for one Device.
type Result struct {
	Result      string
	Confidence  string
	Reasons     []string
	Assignments []AssignmentResult
	Path        []Step
}

// Closure maps every group a subject reaches (directly or through nesting) to the shortest trace.
type Closure map[string]Trace

type closure = Closure

// NewClosure expands direct memberships over the nesting edges.
func NewClosure(origin string, direct []Membership, nesting map[string][]string) Closure {
	return expand(origin, direct, nesting)
}

// Groups returns the sorted external ids of the groups of the closure.
func (c Closure) Groups() []string {
	out := make([]string, 0, len(c))
	for g := range c {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// expand returns, for every group reachable from the direct memberships over the nesting edges, the shortest trace.
func expand(origin string, direct []Membership, nesting map[string][]string) closure {
	out := closure{}
	type item struct {
		group string
		chain []string
	}
	var queue []item
	for _, m := range direct {
		if _, seen := out[m.GroupExternalID]; seen {
			continue
		}
		out[m.GroupExternalID] = Trace{Origin: origin, Chain: []string{m.GroupExternalID}}
		queue = append(queue, item{m.GroupExternalID, []string{m.GroupExternalID}})
	}
	for len(queue) > 0 && len(out) < 10000 {
		it := queue[0]
		queue = queue[1:]
		parents := append([]string(nil), nesting[it.group]...)
		sort.Strings(parents)
		for _, p := range parents {
			if _, seen := out[p]; seen {
				continue
			}
			chain := append(append([]string(nil), it.chain...), p)
			out[p] = Trace{Origin: origin, Chain: chain}
			queue = append(queue, item{p, chain})
		}
	}
	return out
}

// Groups returns the sorted external ids of every group the direct memberships reach over the nesting edges
// (the groups themselves and all their ancestors).
func Groups(direct []Membership, nesting map[string][]string) []string {
	c := expand(OriginDevice, direct, nesting)
	out := make([]string, 0, len(c))
	for g := range c {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// Evaluate derives the expected applicability of the artifact whose current assignments are in.Assignments.
func Evaluate(in Input) Result {
	dev := in.DeviceClosure
	if dev == nil {
		dev = expand(OriginDevice, in.DeviceMemberships, in.Nesting)
	}
	usr := in.UserClosure
	if usr == nil && in.UserKnown {
		usr = expand(OriginUser, in.UserMemberships, in.Nesting)
	}
	gaps := memberGaps{devUnknown: in.DeviceMembershipsUnknown, devTruncated: in.DeviceInputsTruncated,
		usrUnknown: in.UserKnown && in.UserMembershipsUnknown, usrTruncated: in.UserKnown && in.UserInputsTruncated}

	res := Result{}
	reasons := map[string]bool{}
	stale := false
	markStale := func(t time.Time) {
		if !t.IsZero() && !in.Now.IsZero() && in.Now.Sub(t) > FreshnessLimit {
			stale = true
		}
	}
	markStale(in.Device.SyncedAt)
	for _, m := range in.DeviceMemberships {
		markStale(m.ObservedAt)
	}
	if in.UserKnown {
		for _, m := range in.UserMemberships {
			markStale(m.ObservedAt)
		}
	}

	for _, a := range in.Assignments {
		markStale(a.LastSyncedAt)
		res.Assignments = append(res.Assignments, evaluateAssignment(a, in.Device, dev, usr, in.UserKnown, gaps))
	}

	// Aggregate: exclude beats include.
	var incYes, incUnk, excYes, excUnk []int
	for i, a := range in.Assignments {
		r := res.Assignments[i]
		switch {
		case a.Mode == ModeExclude && r.Hit == Yes:
			excYes = append(excYes, i)
		case a.Mode == ModeExclude && r.Hit == Unknown:
			excUnk = append(excUnk, i)
		case a.Mode != ModeExclude && r.Hit == Yes:
			incYes = append(incYes, i)
		case a.Mode != ModeExclude && r.Hit == Unknown:
			incUnk = append(incUnk, i)
		}
	}

	var decisive []int
	switch {
	case len(in.Assignments) == 0:
		res.Result = NotApplicable
		reasons[ReasonNoAssignments] = true
	case len(excYes) > 0 && len(incYes) > 0 && !shareOrigin(res.Assignments, incYes, excYes):
		// Intune does not apply an exclusion of a User group to a Device-group include (or the reverse) reliably.
		res.Result = UnknownResult
		decisive = append(append(decisive, incYes...), excYes...)
		reasons[ReasonExcluded], reasons[ReasonMixedOrigin] = true, true
	case len(excYes) > 0 && len(incYes) > 0:
		res.Result = Excluded
		decisive = append(append(decisive, incYes...), excYes...)
		reasons[ReasonExcluded], reasons[ReasonExclusionWins] = true, true
	case len(excYes) > 0 && len(incUnk) > 0:
		res.Result = UnknownResult
		decisive = append(append(decisive, incUnk...), excYes...)
		reasons[ReasonExcluded] = true
	case len(excYes) > 0:
		res.Result = NotApplicable
		decisive = excYes
		reasons[ReasonExcluded], reasons[ReasonNoMatch] = true, true
	case len(excUnk) > 0 && (len(incYes) > 0 || len(incUnk) > 0):
		res.Result = UnknownResult
		decisive = append(append(decisive, incYes...), incUnk...)
		decisive = append(decisive, excUnk...)
	case len(incYes) > 0:
		res.Result = Applicable
		decisive = incYes
		reasons[ReasonIncluded] = true
	case len(incUnk) > 0:
		res.Result = UnknownResult
		decisive = incUnk
	default:
		res.Result = NotApplicable
		reasons[ReasonNoMatch] = true
		// Includes that addressed the Device but were cut off by their filter explain the outcome.
		for i, a := range in.Assignments {
			if a.Mode != ModeExclude && res.Assignments[i].Target == Yes {
				decisive = append(decisive, i)
			}
		}
	}

	nested, viaUser := false, false
	for _, i := range decisive {
		r := res.Assignments[i]
		for _, rs := range r.Reasons {
			reasons[rs] = true
		}
		if res.Result != Applicable && res.Result != Excluded {
			continue
		}
		for _, t := range r.Traces {
			nested = nested || t.Nested()
			viaUser = viaUser || t.Origin == OriginUser
		}
	}
	if res.Result == Applicable || res.Result == Excluded {
		// Only the reasons of the traces that carried the decision.
		delete(reasons, ReasonMemberNested)
		delete(reasons, ReasonMemberViaUser)
		if nested {
			reasons[ReasonMemberNested] = true
		}
		if viaUser {
			reasons[ReasonMemberViaUser] = true
		}
	}
	if stale {
		reasons[ReasonInputsStale] = true
	}

	switch {
	case res.Result == UnknownResult || stale:
		res.Confidence = ConfidenceLow
	case nested || viaUser:
		res.Confidence = ConfidenceMedium
	default:
		res.Confidence = ConfidenceHigh
	}
	for _, rc := range reasonOrder {
		if reasons[rc] {
			res.Reasons = append(res.Reasons, rc)
		}
	}
	res.Path = buildPath(in, res, decisive)
	return res
}

// memberGaps says why a missing group membership of the Device or its User proves nothing.
type memberGaps struct{ devUnknown, devTruncated, usrUnknown, usrTruncated bool }

// shareOrigin reports whether an include and an exclude that both hit reach the subject through a common origin.
func shareOrigin(rs []AssignmentResult, inc, exc []int) bool {
	origins := func(idx []int) map[string]bool {
		m := map[string]bool{}
		for _, i := range idx {
			for _, t := range rs[i].Traces {
				m[t.Origin] = true
			}
		}
		return m
	}
	incO, excO := origins(inc), origins(exc)
	for o := range incO {
		if excO[o] {
			return true
		}
	}
	return false
}

func evaluateAssignment(a Assignment, dev Device, devC, usrC closure, userKnown bool, gaps memberGaps) AssignmentResult {
	r := AssignmentResult{AssignmentID: a.ID, Filter: FilterNone}
	add := func(rs ...string) { r.Reasons = append(r.Reasons, rs...) }
	switch a.TargetKind {
	case TargetAllDevices:
		r.Target = Yes
		r.Traces = []Trace{{Origin: OriginDevice}}
		add(ReasonTargetAllDevices)
	case TargetAllUsers:
		add(ReasonTargetAllUsers)
		if userKnown {
			r.Target = Yes
			r.Traces = []Trace{{Origin: OriginUser}}
		} else {
			r.Target = Unknown
			add(ReasonUserUnknown)
		}
	case TargetGroup:
		if t, ok := devC[a.GroupExternalID]; ok {
			r.Traces = append(r.Traces, t)
		}
		if t, ok := usrC[a.GroupExternalID]; ok {
			r.Traces = append(r.Traces, t)
		}
		switch {
		case len(r.Traces) > 0:
			r.Target = Yes
			for _, t := range r.Traces {
				if t.Nested() {
					add(ReasonMemberNested)
				}
				if t.Origin == OriginUser {
					add(ReasonMemberViaUser)
				}
			}
		default:
			// No trace: "not a member" is only known when every input behind it is complete.
			if !userKnown {
				add(ReasonUserUnknown)
			}
			if gaps.devUnknown || gaps.usrUnknown {
				add(ReasonMembershipsUnknown)
			}
			if gaps.devTruncated || gaps.usrTruncated {
				add(ReasonInputsTruncated)
			}
			if len(r.Reasons) > 0 {
				r.Target = Unknown
			} else {
				r.Target = No
			}
		}
	default:
		r.Target = Unknown
	}

	if r.Target == No {
		r.Hit = No
		return r
	}
	pass := Yes
	if a.FilterMode != FilterModeNone && a.FilterMode != "" {
		switch {
		case a.Filter == nil || a.Filter.Deleted:
			r.Filter, pass = FilterMissing, Unknown
			add(ReasonFilterMissing)
		default:
			v, ok := EvalFilter(a.Filter.Rule, dev)
			switch {
			case !ok:
				r.Filter, pass = FilterUnsupported, Unknown
				add(ReasonFilterUnsupported)
			case v == Unknown:
				r.Filter, pass = FilterUnknown, Unknown
				add(ReasonFilterUnknown)
			default:
				if v == Yes {
					r.Filter = FilterMatch
				} else {
					r.Filter = FilterNoMatch
				}
				if a.FilterMode == FilterModeExc {
					v = not(v)
				}
				pass = v
				if v == Yes {
					add(ReasonFilterMatched)
				} else {
					add(ReasonFilterNotMatched)
				}
			}
		}
	}
	r.Hit = and(r.Target, pass)
	return r
}

func buildPath(in Input, res Result, decisive []int) []Step {
	var steps []Step
	for _, i := range decisive {
		a, r := in.Assignments[i], res.Assignments[i]
		if r.Target == Unknown && slices.Contains(r.Reasons, ReasonUserUnknown) {
			steps = append(steps, Step{Kind: StepUserUnk, Origin: OriginUser})
		}
		for _, t := range r.Traces {
			switch {
			case a.TargetKind == TargetAllDevices || a.TargetKind == TargetAllUsers:
				steps = append(steps, Step{Kind: StepTarget, Origin: t.Origin, TargetKind: a.TargetKind})
			default:
				steps = append(steps, Step{Kind: StepMembership, Origin: t.Origin, GroupExternalID: t.Chain[0]})
				for _, g := range t.Chain[1:] {
					steps = append(steps, Step{Kind: StepNesting, Origin: t.Origin, GroupExternalID: g})
				}
			}
		}
		steps = append(steps, Step{Kind: StepAssignment, AssignmentID: a.ID, Mode: a.Mode, Intent: a.Intent, TargetKind: a.TargetKind, GroupExternalID: a.GroupExternalID})
		if r.Filter != FilterNone {
			steps = append(steps, Step{Kind: StepFilter, AssignmentID: a.ID, FilterResult: r.Filter})
		}
	}
	return append(steps, Step{Kind: StepFinal, Result: res.Result})
}
