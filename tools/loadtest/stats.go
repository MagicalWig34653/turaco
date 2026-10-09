package main

import (
	"errors"
	"math"
	"sort"
	"sync"
	"time"
)

// Class is the outcome class of one request.
type Class int

const (
	ClassOK          Class = iota // 2xx
	ClassConflict                 // 409: optimistic-lock or state conflict, expected under contention
	ClassThrottled                // 429: rate limit, expected and measured separately
	ClassDenied                   // 403/404: authorization denial (expected only for probes)
	ClassReauth                   // 401: session expired, the client logs in again
	ClassInvalid                  // other 4xx: generator or contract problem
	ClassServerError              // 5xx: never expected
	ClassNetwork                  // timeout, reset, refused: never expected
	numClasses
)

var classNames = [numClasses]string{"ok", "conflict", "throttled", "denied", "reauth", "invalid", "server_error", "network"}

// String returns the report name of the class.
func (c Class) String() string { return classNames[c] }

// ClassifyStatus maps an HTTP status (0 with err for transport failures) to a Class.
func ClassifyStatus(status int, err error) Class {
	switch {
	case err != nil:
		return ClassNetwork
	case status >= 200 && status < 400:
		return ClassOK
	case status == 409:
		return ClassConflict
	case status == 429:
		return ClassThrottled
	case status == 401:
		return ClassReauth
	case status == 403 || status == 404:
		return ClassDenied
	case status >= 400 && status < 500:
		return ClassInvalid
	default:
		return ClassServerError
	}
}

// IsTimeout reports whether err is a deadline or timeout error.
func IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	var t interface{ Timeout() bool }
	return errors.Is(err, errTimeout) || (errors.As(err, &t) && t.Timeout())
}

var errTimeout = errors.New("timeout")

const (
	histMin    = 10 * time.Microsecond
	histGrowth = 1.02
	histMax    = 300 * time.Second
)

var histBuckets = int(math.Ceil(math.Log(float64(histMax)/float64(histMin))/math.Log(histGrowth))) + 1

// Hist is a log-bucketed latency histogram with about 2 percent resolution.
// It is not safe for concurrent use; Recorder guards it.
type Hist struct {
	counts []uint64
	n      uint64
	sum    float64
	min    time.Duration
	max    time.Duration
}

// NewHist returns an empty histogram.
func NewHist() *Hist { return &Hist{counts: make([]uint64, histBuckets), min: math.MaxInt64} }

func bucketOf(d time.Duration) int {
	if d <= histMin {
		return 0
	}
	i := int(math.Log(float64(d)/float64(histMin)) / math.Log(histGrowth))
	if i >= histBuckets {
		i = histBuckets - 1
	}
	return i + 1
}

func bucketUpper(i int) time.Duration {
	if i <= 0 {
		return histMin
	}
	return time.Duration(float64(histMin) * math.Pow(histGrowth, float64(i)))
}

// Add records one observation.
func (h *Hist) Add(d time.Duration) {
	if d < 0 {
		d = 0
	}
	i := bucketOf(d)
	if i >= len(h.counts) {
		i = len(h.counts) - 1
	}
	h.counts[i]++
	h.n++
	h.sum += float64(d)
	if d < h.min {
		h.min = d
	}
	if d > h.max {
		h.max = d
	}
}

// Merge adds the observations of o.
func (h *Hist) Merge(o *Hist) {
	if o == nil || o.n == 0 {
		return
	}
	for i, c := range o.counts {
		h.counts[i] += c
	}
	h.n += o.n
	h.sum += o.sum
	if o.min < h.min {
		h.min = o.min
	}
	if o.max > h.max {
		h.max = o.max
	}
}

// Count is the number of observations.
func (h *Hist) Count() uint64 { return h.n }

// Max is the largest observation (exact).
func (h *Hist) Max() time.Duration { return h.max }

// Mean is the arithmetic mean.
func (h *Hist) Mean() time.Duration {
	if h.n == 0 {
		return 0
	}
	return time.Duration(h.sum / float64(h.n))
}

// Quantile returns the q-quantile (0..1), an upper bound within the bucket resolution and never above Max.
func (h *Hist) Quantile(q float64) time.Duration {
	if h.n == 0 {
		return 0
	}
	if q <= 0 {
		return h.min
	}
	want := uint64(math.Ceil(q * float64(h.n)))
	if want < 1 {
		want = 1
	}
	var cum uint64
	for i, c := range h.counts {
		cum += c
		if cum >= want {
			v := bucketUpper(i)
			if v > h.max {
				v = h.max
			}
			if v < h.min {
				v = h.min
			}
			return v
		}
	}
	return h.max
}

// Cell collects the results of one operation in one stage.
type Cell struct {
	Response *Hist // from the scheduled start (coordinated-omission aware)
	Service  *Hist // from the moment the request was actually sent
	Classes  [numClasses]uint64
	Statuses map[int]uint64
	Bytes    uint64
	Missing  uint64 // 404 without an error envelope: the route does not exist in this API build
}

func newCell() *Cell {
	return &Cell{Response: NewHist(), Service: NewHist(), Statuses: map[int]uint64{}}
}

func (c *Cell) merge(o *Cell) {
	c.Response.Merge(o.Response)
	c.Service.Merge(o.Service)
	for i := range c.Classes {
		c.Classes[i] += o.Classes[i]
	}
	for s, n := range o.Statuses {
		c.Statuses[s] += n
	}
	c.Bytes += o.Bytes
	c.Missing += o.Missing
}

// Total is the number of recorded requests.
func (c *Cell) Total() uint64 {
	var n uint64
	for _, v := range c.Classes {
		n += v
	}
	return n
}

// Sample is one finished request.
type Sample struct {
	Stage    int
	Op       string
	Response time.Duration
	Service  time.Duration
	Class    Class
	Status   int
	Bytes    int
	Missing  bool
}

// Recorder collects samples per stage and operation, plus a rolling window for the timeline.
type Recorder struct {
	mu     sync.Mutex
	cells  map[cellKey]*Cell
	window *Hist
	winN   uint64
	winErr uint64
	winThr uint64
}

type cellKey struct {
	stage int
	op    string
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder {
	return &Recorder{cells: map[cellKey]*Cell{}, window: NewHist()}
}

// Add records a finished request.
func (r *Recorder) Add(s Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := cellKey{s.Stage, s.Op}
	c := r.cells[k]
	if c == nil {
		c = newCell()
		r.cells[k] = c
	}
	c.Response.Add(s.Response)
	c.Service.Add(s.Service)
	c.Classes[s.Class]++
	c.Statuses[s.Status]++
	c.Bytes += uint64(s.Bytes)
	if s.Missing {
		c.Missing++
	}
	r.window.Add(s.Response)
	r.winN++
	switch s.Class {
	case ClassServerError, ClassNetwork:
		r.winErr++
	case ClassThrottled:
		r.winThr++
	}
}

// Window is the rolling window that feeds the timeline.
type Window struct {
	Count     uint64
	Errors    uint64
	Throttled uint64
	P50, P99  time.Duration
}

// TakeWindow returns and resets the rolling window.
func (r *Recorder) TakeWindow() Window {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := Window{Count: r.winN, Errors: r.winErr, Throttled: r.winThr, P50: r.window.Quantile(0.5), P99: r.window.Quantile(0.99)}
	r.window = NewHist()
	r.winN, r.winErr, r.winThr = 0, 0, 0
	return w
}

// ByOp merges all stages per operation, sorted by name.
func (r *Recorder) ByOp() (names []string, cells map[string]*Cell) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cells = map[string]*Cell{}
	for k, c := range r.cells {
		m := cells[k.op]
		if m == nil {
			m = newCell()
			cells[k.op] = m
		}
		m.merge(c)
	}
	for n := range cells {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, cells
}

// ByStage merges all operations per stage.
func (r *Recorder) ByStage() map[int]*Cell {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[int]*Cell{}
	for k, c := range r.cells {
		m := out[k.stage]
		if m == nil {
			m = newCell()
			out[k.stage] = m
		}
		m.merge(c)
	}
	return out
}

// OpInStage returns the cell of one operation in one stage (nil when empty).
func (r *Recorder) OpInStage(stage int, op string) *Cell {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.cells[cellKey{stage, op}]; c != nil {
		cp := newCell()
		cp.merge(c)
		return cp
	}
	return nil
}

// Total merges everything.
func (r *Recorder) Total() *Cell {
	t := newCell()
	for _, c := range r.ByStage() {
		t.merge(c)
	}
	return t
}

// TotalRun merges the stages of the schedule only (setup, login and verification samples have negative stages).
func (r *Recorder) TotalRun() *Cell {
	t := newCell()
	for stage, c := range r.ByStage() {
		if stage >= 0 {
			t.merge(c)
		}
	}
	return t
}
