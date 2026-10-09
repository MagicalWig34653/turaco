package main

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

func TestHistQuantiles(t *testing.T) {
	h := NewHist()
	for i := 1; i <= 1000; i++ {
		h.Add(time.Duration(i) * time.Millisecond)
	}
	cases := map[float64]time.Duration{0.5: 500 * time.Millisecond, 0.95: 950 * time.Millisecond, 0.99: 990 * time.Millisecond}
	for q, want := range cases {
		got := h.Quantile(q)
		if math.Abs(float64(got-want))/float64(want) > 0.03 {
			t.Errorf("q%.2f = %v, want about %v", q, got, want)
		}
	}
	if h.Max() != time.Second {
		t.Errorf("max = %v, want exactly 1s", h.Max())
	}
	if h.Quantile(1) > h.Max() {
		t.Error("quantile above max")
	}
	if h.Count() != 1000 {
		t.Errorf("count %d", h.Count())
	}
	if mean := h.Mean(); mean < 495*time.Millisecond || mean > 506*time.Millisecond {
		t.Errorf("mean %v", mean)
	}
}

func TestHistEdgesAndMerge(t *testing.T) {
	h := NewHist()
	if h.Quantile(0.99) != 0 || h.Mean() != 0 {
		t.Error("empty histogram should report zeros")
	}
	h.Add(0)
	h.Add(-time.Second)
	h.Add(10 * time.Minute) // beyond the range: lands in the last bucket, max stays exact
	if h.Max() != 10*time.Minute {
		t.Errorf("max = %v", h.Max())
	}
	o := NewHist()
	o.Add(5 * time.Millisecond)
	o.Add(7 * time.Millisecond)
	h.Merge(o)
	if h.Count() != 5 {
		t.Errorf("merged count %d", h.Count())
	}
	h.Merge(nil)
	if h.Quantile(0.5) > 10*time.Millisecond {
		t.Errorf("median %v", h.Quantile(0.5))
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		err    error
		want   Class
	}{
		{200, nil, ClassOK}, {201, nil, ClassOK}, {204, nil, ClassOK},
		{409, nil, ClassConflict}, {429, nil, ClassThrottled}, {401, nil, ClassReauth},
		{403, nil, ClassDenied}, {404, nil, ClassDenied}, {400, nil, ClassInvalid}, {422, nil, ClassInvalid},
		{500, nil, ClassServerError}, {503, nil, ClassServerError}, {0, errors.New("reset"), ClassNetwork},
	}
	for _, c := range cases {
		if got := ClassifyStatus(c.status, c.err); got != c.want {
			t.Errorf("ClassifyStatus(%d, %v) = %v, want %v", c.status, c.err, got, c.want)
		}
	}
}

func TestRecorderAggregation(t *testing.T) {
	r := NewRecorder()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				cls := ClassOK
				status := 200
				if i%10 == 0 {
					cls, status = ClassConflict, 409
				}
				r.Add(Sample{Stage: g % 2, Op: "tickets.assign", Response: time.Duration(i+1) * time.Millisecond, Service: time.Millisecond, Class: cls, Status: status, Bytes: 10})
			}
		}(g)
	}
	wg.Wait()
	r.Add(Sample{Stage: -1, Op: "auth.login", Response: time.Second, Class: ClassOK, Status: 204})

	names, cells := r.ByOp()
	if len(names) != 2 || names[0] != "auth.login" || names[1] != "tickets.assign" {
		t.Fatalf("names %v", names)
	}
	c := cells["tickets.assign"]
	if c.Total() != 800 || c.Classes[ClassConflict] != 80 || c.Statuses[409] != 80 || c.Bytes != 8000 {
		t.Errorf("cell: total %d conflicts %d bytes %d", c.Total(), c.Classes[ClassConflict], c.Bytes)
	}
	if got := r.TotalRun().Total(); got != 800 {
		t.Errorf("run total %d (setup samples must be excluded)", got)
	}
	if got := r.Total().Total(); got != 801 {
		t.Errorf("total %d", got)
	}
	if st := r.ByStage(); st[0].Total() != 400 || st[1].Total() != 400 {
		t.Errorf("stage totals %d %d", st[0].Total(), st[1].Total())
	}
	w := r.TakeWindow()
	if w.Count != 801 || w.P99 == 0 {
		t.Errorf("window %+v", w)
	}
	if w2 := r.TakeWindow(); w2.Count != 0 {
		t.Errorf("window not reset: %+v", w2)
	}
}

// Coordinated omission: a request that waited in the queue is measured from its scheduled start.
func TestResponseIncludesQueueWait(t *testing.T) {
	r := NewRecorder()
	c := &Client{rec: r}
	sched := time.Now().Add(-2 * time.Second)
	jc := &JobCtx{Stage: 0, Sched: sched}
	if got := jc.take(); !got.Equal(sched) {
		t.Fatalf("first take should return the scheduled start")
	}
	if got := jc.take(); !got.IsZero() {
		t.Fatalf("second call of the same operation must not reuse the scheduled start")
	}
	_ = c
}

func TestSaturationSignals(t *testing.T) {
	c := newCell()
	for i := 0; i < 100; i++ {
		c.Response.Add(2 * time.Second)
		c.Classes[ClassOK]++
	}
	for i := 0; i < 20; i++ {
		c.Classes[ClassThrottled]++
	}
	sr := StageReport{Offered: 100, Finished: 50, Dropped: 10}
	sig := saturationSignals(sr, c, time.Second)
	if len(sig) != 4 { // dropped, backlog, p99, throttled
		t.Fatalf("signals: %v", sig)
	}
	healthy := newCell()
	healthy.Response.Add(5 * time.Millisecond)
	healthy.Classes[ClassOK]++
	if sig := saturationSignals(StageReport{Offered: 10, Finished: 10}, healthy, time.Second); len(sig) != 0 {
		t.Fatalf("healthy stage flagged: %v", sig)
	}
}
