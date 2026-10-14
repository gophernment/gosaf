package circuit

import "time"

// Window keeps a bounded, sliding record of recent outcomes.
// It is not safe for concurrent use; the caller serialises access.
type Window interface {
	Record(now time.Time, o Outcome)
	Counts(now time.Time) Counts
	Reset()
}

type countWindow struct {
	ring []Outcome
	next int
	size int
	c    Counts
}

// NewCountWindow returns a window over the last n recorded calls.
func NewCountWindow(n int) Window {
	return &countWindow{ring: make([]Outcome, max(n, 1))}
}

func (w *countWindow) Record(_ time.Time, o Outcome) {
	if w.size == len(w.ring) {
		w.c.sub(w.ring[w.next])
	} else {
		w.size++
	}
	w.ring[w.next] = o
	w.c.add(o)
	w.next = (w.next + 1) % len(w.ring)
}

func (w *countWindow) Counts(time.Time) Counts { return w.c }

func (w *countWindow) Reset() {
	clear(w.ring)
	w.next, w.size, w.c = 0, 0, Counts{}
}

type bucket struct {
	idx  int64
	used bool
	c    Counts
}

type timeWindow struct {
	buckets []bucket
	width   time.Duration
	origin  time.Time
	latest  int64
}

// NewTimeWindow returns a window spanning period, split into n buckets.
// origin is the time bucket numbering starts from.
func NewTimeWindow(n int, period time.Duration, origin time.Time) Window {
	n = max(n, 1)
	return &timeWindow{
		buckets: make([]bucket, n),
		width:   max(period/time.Duration(n), 1),
		origin:  origin,
	}
}

// index maps now to a bucket number. It never moves backwards, so a wall-clock
// step back cannot overwrite newer buckets.
func (w *timeWindow) index(now time.Time) int64 {
	idx := int64(now.Sub(w.origin) / w.width)
	if idx < w.latest {
		idx = w.latest
	}
	w.latest = idx
	return idx
}

func (w *timeWindow) Record(now time.Time, o Outcome) {
	idx := w.index(now)
	b := &w.buckets[idx%int64(len(w.buckets))]
	if !b.used || b.idx != idx {
		*b = bucket{idx: idx, used: true}
	}
	b.c.add(o)
}

func (w *timeWindow) Counts(now time.Time) Counts {
	cur := w.index(now)
	oldest := cur - int64(len(w.buckets))
	var c Counts
	for i := range w.buckets {
		b := &w.buckets[i]
		if b.used && b.idx > oldest && b.idx <= cur {
			c.Total += b.c.Total
			c.Failures += b.c.Failures
			c.Slow += b.c.Slow
		}
	}
	return c
}

func (w *timeWindow) Reset() { clear(w.buckets) }
