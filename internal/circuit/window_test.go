package circuit_test

import (
	"testing"
	"time"

	"github.com/gophernment/gosaf/internal/circuit"
)

var t0 = time.Unix(1_000_000, 0)

func TestCountWindowSlides(t *testing.T) {
	w := circuit.NewCountWindow(3)
	for _, o := range []circuit.Outcome{circuit.Failure, circuit.Success, circuit.Slow} {
		w.Record(t0, o)
	}
	if got, want := w.Counts(t0), (circuit.Counts{Total: 3, Failures: 1, Slow: 1}); got != want {
		t.Fatalf("before wrap: got %+v, want %+v", got, want)
	}

	w.Record(t0, circuit.Success) // overwrites the Failure
	if got, want := w.Counts(t0), (circuit.Counts{Total: 3, Slow: 1}); got != want {
		t.Fatalf("after wrap: got %+v, want %+v", got, want)
	}

	w.Reset()
	if got := w.Counts(t0); got != (circuit.Counts{}) {
		t.Fatalf("after reset: got %+v, want zero", got)
	}
}

func TestTimeWindowExpiresOldBuckets(t *testing.T) {
	w := circuit.NewTimeWindow(4, 4*time.Second, t0) // 1s buckets
	w.Record(t0, circuit.Failure)

	if got := w.Counts(t0.Add(3 * time.Second)).Total; got != 1 {
		t.Fatalf("still inside window: got total %d, want 1", got)
	}
	if got := w.Counts(t0.Add(4 * time.Second)).Total; got != 0 {
		t.Fatalf("expired: got total %d, want 0", got)
	}
}

func TestTimeWindowReusesBucketSlot(t *testing.T) {
	w := circuit.NewTimeWindow(2, 2*time.Second, t0)
	w.Record(t0, circuit.Failure)
	w.Record(t0.Add(2*time.Second), circuit.Success) // same slot, newer bucket

	got := w.Counts(t0.Add(2 * time.Second))
	if want := (circuit.Counts{Total: 1}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestTimeWindowIgnoresClockGoingBackwards(t *testing.T) {
	w := circuit.NewTimeWindow(4, 4*time.Second, t0)
	w.Record(t0.Add(10*time.Second), circuit.Failure)
	w.Record(t0.Add(2*time.Second), circuit.Failure) // clock stepped back

	got := w.Counts(t0.Add(10 * time.Second))
	if got.Total != 2 {
		t.Fatalf("got total %d, want 2 (late record lands in newest bucket)", got.Total)
	}
}
