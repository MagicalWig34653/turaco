package main

import (
	"fmt"
	"math/rand"
	"sync"
)

// Entry is a ticket created by this run (and therefore tagged and deletable).
type Entry struct {
	ID       string
	Ref      string
	Reporter string // login of the reporting persona
}

type entrySet struct {
	items []*Entry
	idx   map[string]int
}

func newEntrySet() *entrySet { return &entrySet{idx: map[string]int{}} }

func (s *entrySet) add(e *Entry) {
	if _, ok := s.idx[e.ID]; ok {
		return
	}
	s.idx[e.ID] = len(s.items)
	s.items = append(s.items, e)
}

func (s *entrySet) remove(id string) {
	i, ok := s.idx[id]
	if !ok {
		return
	}
	last := len(s.items) - 1
	s.items[i] = s.items[last]
	s.idx[s.items[i].ID] = i
	s.items = s.items[:last]
	delete(s.idx, id)
}

func (s *entrySet) pick(rng *rand.Rand, hot int, hotProb float64) *Entry {
	n := len(s.items)
	if n == 0 {
		return nil
	}
	if hot > 0 && hot < n && rng.Float64() < hotProb {
		return s.items[n-1-rng.Intn(hot)]
	}
	return s.items[rng.Intn(n)]
}

// Pool is the set of work-in-progress tickets that the simulated users act on.
// Picking is biased towards the newest tickets (the hot set) so that several
// technicians really work the same ticket and version conflicts occur.
type Pool struct {
	mu    sync.Mutex
	all   *entrySet
	byRep map[string]*entrySet
	total int
}

// NewPool returns an empty pool.
func NewPool() *Pool { return &Pool{all: newEntrySet(), byRep: map[string]*entrySet{}} }

// Add inserts a ticket.
func (p *Pool) Add(e *Entry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.all.add(e)
	set := p.byRep[e.Reporter]
	if set == nil {
		set = newEntrySet()
		p.byRep[e.Reporter] = set
	}
	set.add(e)
	p.total++
}

// Remove drops a ticket that reached a terminal state.
func (p *Pool) Remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i, ok := p.all.idx[id]; ok {
		e := p.all.items[i]
		p.all.remove(id)
		if set := p.byRep[e.Reporter]; set != nil {
			set.remove(id)
		}
	}
}

// Pick returns a ticket for staff to work on.
func (p *Pool) Pick(rng *rand.Rand, hot int, hotProb float64) *Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.all.pick(rng, hot, hotProb)
}

// PickOwn returns a ticket reported by login.
func (p *Pool) PickOwn(rng *rand.Rand, login string) *Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	if set := p.byRep[login]; set != nil {
		return set.pick(rng, 0, 0)
	}
	return nil
}

// PickForeign returns a ticket reported by somebody else (a few attempts).
func (p *Pool) PickForeign(rng *rand.Rand, login string) *Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := 0; i < 8; i++ {
		if e := p.all.pick(rng, 0, 0); e != nil && e.Reporter != login {
			return e
		}
	}
	return nil
}

// Len is the number of live tickets in the pool.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.all.items)
}

// Created is the number of tickets ever added.
func (p *Pool) Created() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total
}

// idRing remembers recently seen ticket ids (read-only candidates from lists).
type idRing struct {
	mu   sync.Mutex
	ids  []string
	next int
}

func newIDRing(n int) *idRing { return &idRing{ids: make([]string, 0, n)} }

func (r *idRing) add(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.ids) < cap(r.ids) {
		r.ids = append(r.ids, id)
		return
	}
	r.ids[r.next] = id
	r.next = (r.next + 1) % len(r.ids)
}

func (r *idRing) pick(rng *rand.Rand) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.ids) == 0 {
		return ""
	}
	return r.ids[rng.Intn(len(r.ids))]
}

// Violation is an invariant breach found while running or afterwards.
type Violation struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// Tracker verifies per-ticket version and reference invariants from what the
// clients observed: a version returned by two successful conditional writes
// means one of them was lost.
type Tracker struct {
	mu         sync.Mutex
	tickets    map[string]*tState
	refs       map[string]string
	violations []Violation
	counts     map[string]int
	writesOK   int
}

type tState struct {
	ref     string
	maxSeen int
	writes  map[int]string // resulting version -> op that produced it
}

const maxViolationDetails = 50

// NewTracker returns an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{tickets: map[string]*tState{}, refs: map[string]string{}, counts: map[string]int{}}
}

func (t *Tracker) violate(kind, format string, args ...any) {
	t.counts[kind]++
	if len(t.violations) < maxViolationDetails {
		t.violations = append(t.violations, Violation{Kind: kind, Detail: fmt.Sprintf(format, args...)})
	}
}

// Violate records an externally detected breach (for example an authorization leak).
func (t *Tracker) Violate(kind, format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.violate(kind, format, args...)
}

// Created registers a new ticket and checks that its reference is unique.
func (t *Tracker) Created(id, ref string, version int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if other, ok := t.refs[ref]; ok && other != id {
		t.violate("duplicate_reference", "reference %s issued to tickets %s and %s", ref, other, id)
	}
	t.refs[ref] = id
	t.tickets[id] = &tState{ref: ref, maxSeen: version, writes: map[int]string{}}
}

// Observe records a version seen in a read.
func (t *Tracker) Observe(id string, version int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.tickets[id]; s != nil && version > s.maxSeen {
		s.maxSeen = version
	}
}

// Write records a successful conditional write that was sent with expected and answered got.
func (t *Tracker) Write(id string, expected, got int, op string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.tickets[id]
	if s == nil {
		return
	}
	t.writesOK++
	if got < expected {
		t.violate("version_regressed", "ticket %s: write %s sent expectedVersion %d and got version %d", s.ref, op, expected, got)
		return
	}
	if got == expected {
		return // a no-op write that changed nothing
	}
	if prev, dup := s.writes[got]; dup {
		t.violate("lost_update", "ticket %s: version %d was produced by two successful writes (%s and %s)", s.ref, got, prev, op)
	}
	s.writes[got] = op
	if got > s.maxSeen {
		s.maxSeen = got
	}
}

// TrackedIDs returns the ids of all created tickets.
func (t *Tracker) TrackedIDs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.tickets))
	for id := range t.tickets {
		out = append(out, id)
	}
	return out
}

// MaxSeen returns the highest version observed for a ticket and its reference.
func (t *Tracker) MaxSeen(id string) (version int, ref string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.tickets[id]; s != nil {
		return s.maxSeen, s.ref
	}
	return 0, ""
}

// Refs returns reference to id for all created tickets.
func (t *Tracker) Refs() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]string, len(t.refs))
	for k, v := range t.refs {
		out[k] = v
	}
	return out
}

// Snapshot returns the recorded violations, counts and the number of successful writes.
func (t *Tracker) Snapshot() (list []Violation, counts map[string]int, writesOK int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	counts = map[string]int{}
	for k, v := range t.counts {
		counts[k] = v
	}
	return append([]Violation(nil), t.violations...), counts, t.writesOK
}
