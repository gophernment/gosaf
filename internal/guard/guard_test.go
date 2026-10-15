package guard_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gophernment/gosaf/internal/circuit"
	"github.com/gophernment/gosaf/internal/guard"
)

const (
	openTimeout  = 10 * time.Second
	probeTimeout = 3 * time.Second
	slow         = time.Second
)

var errBoom = errors.New("boom")

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type recorder struct {
	mu       sync.Mutex
	states   []guard.StateChange
	calls    []guard.CallEvent
	onChange func()
}

func (r *recorder) OnStateChange(ev guard.StateChange) {
	r.mu.Lock()
	r.states = append(r.states, ev)
	r.mu.Unlock()
	if r.onChange != nil {
		r.onChange()
	}
}

func (r *recorder) OnCall(ev guard.CallEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, ev)
}

func (r *recorder) stateChanges() []guard.StateChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]guard.StateChange(nil), r.states...)
}

func newGuard(t *testing.T, halfOpenMax int, mutate func(*guard.Options)) (*guard.Guard, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	m := circuit.NewMachine(circuit.MachineConfig{
		Policy:           circuit.Policy{MinCalls: 2, FailureRate: 0.5, SlowRate: 1},
		OpenTimeout:      openTimeout,
		HalfOpenMaxCalls: halfOpenMax,
		ProbeTimeout:     probeTimeout,
	}, circuit.NewCountWindow(10))
	opts := guard.Options{SlowCallDuration: slow, Clock: clk.Now}
	if mutate != nil {
		mutate(&opts)
	}
	return guard.New(m, opts), clk
}

func mustAllow(t *testing.T, g *guard.Guard) *guard.Permit {
	t.Helper()
	p, err := g.Allow(context.Background())
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	return p
}

func fail(t *testing.T, g *guard.Guard, n int) {
	t.Helper()
	for range n {
		mustAllow(t, g).Done(errBoom)
	}
}

func TestAllowRejectsWhenOpen(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)
	clk.Advance(4 * time.Second)

	_, err := g.Allow(context.Background())
	if !errors.Is(err, guard.ErrOpen) {
		t.Fatalf("err = %v, want ErrOpen", err)
	}
	var rej *guard.RejectedError
	if !errors.As(err, &rej) || rej.State() != circuit.Open ||
		!rej.RetryAt().Equal(clk.Now().Add(6*time.Second)) {
		t.Fatalf("rejected = %+v, want open with RetryAt 6s ahead", rej)
	}
	if errors.Is(err, guard.ErrHalfOpenFull) {
		t.Fatal("open rejection must not match ErrHalfOpenFull")
	}
}

func TestHalfOpenFullRejection(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)
	clk.Advance(openTimeout)
	mustAllow(t, g) // the only probe, still running

	_, err := g.Allow(context.Background())
	if !errors.Is(err, guard.ErrHalfOpenFull) {
		t.Fatalf("err = %v, want ErrHalfOpenFull", err)
	}
}

func TestDoneIsIdempotent(t *testing.T) {
	g, _ := newGuard(t, 1, nil)
	p := mustAllow(t, g)
	p.Done(errBoom)
	p.Done(errBoom)
	p.Done(nil)

	if got := g.Snapshot().Counts; got.Total != 1 || got.Failures != 1 {
		t.Fatalf("counts = %+v, want exactly one failure", got)
	}
}

func TestNilPermitDoneIsSafe(t *testing.T) {
	var p *guard.Permit
	p.Done(errBoom) // must not panic
}

func TestSlowCallIsCounted(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	p := mustAllow(t, g)
	clk.Advance(slow + time.Millisecond)
	p.Done(nil)

	if got := g.Snapshot().Counts; got.Slow != 1 || got.Failures != 0 {
		t.Fatalf("counts = %+v, want one slow call", got)
	}
}

func TestCallAtSlowThresholdIsNotSlow(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	p := mustAllow(t, g)
	clk.Advance(slow)
	p.Done(nil)

	if got := g.Snapshot().Counts; got.Slow != 0 || got.Total != 1 {
		t.Fatalf("counts = %+v, want one non-slow success", got)
	}
}

func TestCanceledIsIgnoredButDeadlineIsFailure(t *testing.T) {
	g, _ := newGuard(t, 1, nil)
	mustAllow(t, g).Done(context.Canceled)
	if got := g.Snapshot().Counts.Total; got != 0 {
		t.Fatalf("canceled counted: total = %d, want 0", got)
	}
	mustAllow(t, g).Done(context.DeadlineExceeded)
	if got := g.Snapshot().Counts.Failures; got != 1 {
		t.Fatalf("deadline exceeded: failures = %d, want 1", got)
	}
}

func TestAllowChecksContextWithoutUsingASlot(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)
	clk.Advance(openTimeout)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Allow(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	mustAllow(t, g) // the single probe slot must still be free
}

func TestAllowNilContextFails(t *testing.T) {
	g, _ := newGuard(t, 1, nil)
	//lint:ignore SA1012 deliberately passing nil
	if _, err := g.Allow(nil); err == nil {
		t.Fatal("Allow(nil) must return an error")
	}
}

func TestStalePermitAfterResetIsDropped(t *testing.T) {
	g, _ := newGuard(t, 1, nil)
	p := mustAllow(t, g)
	g.Reset()
	p.Done(errBoom)

	if got := g.Snapshot().Counts.Total; got != 0 {
		t.Fatalf("stale result counted: total = %d", got)
	}
}

func TestIgnoredProbeFreesSlot(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)
	clk.Advance(openTimeout)

	mustAllow(t, g).Done(context.Canceled)
	mustAllow(t, g) // slot was released
}

func TestHungProbeIsReclaimed(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)
	clk.Advance(openTimeout)
	hung := mustAllow(t, g)

	clk.Advance(probeTimeout)
	if _, err := g.Allow(context.Background()); !errors.Is(err, guard.ErrOpen) {
		t.Fatalf("err = %v, want ErrOpen after probe timeout", err)
	}
	hung.Done(nil) // late result must not close the circuit
	if got := g.Snapshot().State; got != circuit.Open {
		t.Fatalf("state = %v, want open", got)
	}

	clk.Advance(openTimeout)
	mustAllow(t, g) // can probe again
}

func TestListenerSeesTransitionsAndCalls(t *testing.T) {
	rec := &recorder{}
	g, clk := newGuard(t, 1, func(o *guard.Options) { o.Listener = rec; o.Name = "db" })
	fail(t, g, 2)

	states := rec.stateChanges()
	if len(states) != 1 || states[0].Name != "db" ||
		states[0].From != circuit.Closed || states[0].To != circuit.Open ||
		!states[0].At.Equal(clk.Now()) {
		t.Fatalf("states = %+v, want one closed->open for db at the clock time", states)
	}
	if len(rec.calls) != 2 || rec.calls[0].Outcome != circuit.Failure {
		t.Fatalf("calls = %+v, want two failures", rec.calls)
	}
}

func TestListenerMayReenterBreaker(t *testing.T) {
	rec := &recorder{}
	var g *guard.Guard
	rec.onChange = func() { _ = g.Snapshot(); g.Reset() }
	g, _ = newGuard(t, 1, func(o *guard.Options) { o.Listener = rec })

	done := make(chan struct{})
	go func() { defer close(done); fail(t, g, 2) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: listener re-entering the breaker blocked")
	}
}

type panicListener struct{}

func (panicListener) OnStateChange(guard.StateChange) { panic("listener bug") }
func (panicListener) OnCall(guard.CallEvent)          { panic("listener bug") }

func TestListenerPanicIsContained(t *testing.T) {
	g, _ := newGuard(t, 1, func(o *guard.Options) { o.Listener = panicListener{} })
	fail(t, g, 2) // must not panic
	if got := g.Snapshot().State; got != circuit.Open {
		t.Fatalf("state = %v, want open", got)
	}
}

func TestPanickingIsFailureFailsClosed(t *testing.T) {
	g, _ := newGuard(t, 1, func(o *guard.Options) {
		o.IsFailure = func(error) bool { panic("classifier bug") }
	})
	mustAllow(t, g).Done(errBoom)
	if got := g.Snapshot().Counts.Failures; got != 1 {
		t.Fatalf("failures = %d, want 1 (fail closed)", got)
	}
}

func TestCustomIsFailure(t *testing.T) {
	notFound := errors.New("not found")
	g, _ := newGuard(t, 1, func(o *guard.Options) {
		o.IsFailure = func(err error) bool { return !errors.Is(err, notFound) }
	})
	mustAllow(t, g).Done(notFound)
	if got := g.Snapshot().Counts.Total; got != 0 {
		t.Fatalf("excluded error counted: total = %d", got)
	}
}

func TestZeroValuePermitDoneIsSafe(t *testing.T) {
	var p guard.Permit
	p.Done(errBoom) // must not panic
}

type rejectRecorder struct {
	recorder
	rejects []guard.RejectEvent
}

func (r *rejectRecorder) OnReject(ev guard.RejectEvent) { r.rejects = append(r.rejects, ev) }

func TestRejectionErrorIsSharedWhileRetryTimeIsUnchanged(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)

	_, first := g.Allow(context.Background())
	clk.Advance(time.Second)
	_, second := g.Allow(context.Background())
	if first == nil || first != second {
		t.Fatalf("rejections %p and %p should be the same shared value", first, second)
	}

	g.Reset()
	fail(t, g, 2) // re-open at a later time: new retry time
	_, third := g.Allow(context.Background())
	if third == first {
		t.Fatal("a new Open period must produce a new rejection value")
	}
}

func TestSnapshotReportsRejectedCountAndLastChange(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	if s := g.Snapshot(); s.Rejected != 0 || !s.LastChange.IsZero() {
		t.Fatalf("fresh snapshot = %+v, want no rejections and no change", s)
	}
	fail(t, g, 2)
	openedAt := clk.Now()
	clk.Advance(time.Second)
	g.Allow(context.Background())
	g.Allow(context.Background())

	s := g.Snapshot()
	if s.Rejected != 2 || !s.LastChange.Equal(openedAt) || s.State != circuit.Open {
		t.Fatalf("snapshot = %+v, want 2 rejected, last change %v, open", s, openedAt)
	}
}

func TestRejectListenerIsCalledOnlyWhenImplemented(t *testing.T) {
	rec := &rejectRecorder{}
	g, _ := newGuard(t, 1, func(o *guard.Options) { o.Listener = rec; o.Name = "db" })
	fail(t, g, 2)
	g.Allow(context.Background())
	if len(rec.rejects) != 1 || rec.rejects[0] != (guard.RejectEvent{Name: "db", State: circuit.Open}) {
		t.Fatalf("rejects = %+v, want one open rejection for db", rec.rejects)
	}

	plain := &recorder{}
	g2, _ := newGuard(t, 1, func(o *guard.Options) { o.Listener = plain })
	fail(t, g2, 2)
	g2.Allow(context.Background()) // must not panic without OnReject
}

func TestForcedOpenRejectsWithoutRetryTimeUntilReset(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	g.SetMode(circuit.ModeForcedOpen)
	clk.Advance(100 * openTimeout)

	_, err := g.Allow(context.Background())
	var rej *guard.RejectedError
	if !errors.Is(err, guard.ErrOpen) || !errors.As(err, &rej) || !rej.RetryAt().IsZero() || rej.RetryAfter() != 0 {
		t.Fatalf("err = %v, want ErrOpen with no retry time", err)
	}
	if s := g.Snapshot(); s.Mode != circuit.ModeForcedOpen || s.State != circuit.Open {
		t.Fatalf("snapshot = %+v, want forced-open", s)
	}

	g.SetMode(circuit.ModeNormal)
	mustAllow(t, g).Done(nil)
}

func TestDisabledPassesEverythingThroughUnrecorded(t *testing.T) {
	g, _ := newGuard(t, 1, nil)
	g.SetMode(circuit.ModeDisabled)
	fail(t, g, 10)

	s := g.Snapshot()
	if s.Mode != circuit.ModeDisabled || s.State != circuit.Closed || s.Counts.Total != 0 {
		t.Fatalf("snapshot = %+v, want disabled, closed, nothing recorded", s)
	}
}

func TestBeginEndWithoutPermit(t *testing.T) {
	g, _ := newGuard(t, 1, nil)
	tk, err := g.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g.End(tk, errBoom)
	if got := g.Snapshot().Counts; got.Total != 1 || got.Failures != 1 {
		t.Fatalf("counts = %+v, want one failure", got)
	}
}

func TestDefaultClockWorksWithoutOverride(t *testing.T) {
	m := circuit.NewMachine(circuit.MachineConfig{
		Policy:           circuit.Policy{MinCalls: 1, FailureRate: 1, SlowRate: 1},
		OpenTimeout:      time.Hour,
		HalfOpenMaxCalls: 1,
		ProbeTimeout:     time.Hour,
	}, circuit.NewCountWindow(4))
	g := guard.New(m, guard.Options{SlowCallDuration: time.Hour})

	mustAllow(t, g).Done(errBoom)
	_, err := g.Allow(context.Background())
	var rej *guard.RejectedError
	if !errors.As(err, &rej) || rej.RetryAfter() <= 0 || rej.RetryAfter() > time.Hour {
		t.Fatalf("err = %v, want rejection with a retry time within the hour", err)
	}
	if s := g.Snapshot(); time.Since(s.LastChange) > time.Minute || s.LastChange.IsZero() {
		t.Fatalf("LastChange = %v, want a recent wall-clock time", s.LastChange)
	}
}

// The lock-free reject window must never outlive the state it was taken from.

func TestFastRejectStopsWhenOpenTimeoutPasses(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)
	for range 3 { // served from the lock-free window
		if _, err := g.Allow(context.Background()); !errors.Is(err, guard.ErrOpen) {
			t.Fatalf("err = %v, want ErrOpen", err)
		}
	}

	clk.Advance(openTimeout)
	mustAllow(t, g) // the window must not keep rejecting after OpenTimeout
	if got := g.Snapshot().State; got != circuit.HalfOpen {
		t.Fatalf("State = %v, want half-open", got)
	}
}

func TestFastRejectStopsAfterReset(t *testing.T) {
	g, _ := newGuard(t, 1, nil)
	fail(t, g, 2)
	g.Allow(context.Background()) // publishes the window
	g.Reset()
	mustAllow(t, g)
}

func TestFastRejectFollowsClockStepBack(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)
	_, before := g.Allow(context.Background())

	clk.Advance(-time.Hour) // earlier than the moment the circuit opened
	_, after := g.Allow(context.Background())
	var rej *guard.RejectedError
	if !errors.As(after, &rej) || !rej.RetryAt().Equal(clk.Now().Add(openTimeout)) {
		t.Fatalf("err = %v, want RetryAt restarted from the new time %v", after, clk.Now().Add(openTimeout))
	}
	if after == before {
		t.Fatal("the rejection value must change when the retry time changes")
	}

	clk.Advance(openTimeout)
	mustAllow(t, g) // and it still recovers
}

func TestFastRejectCountsEveryRejection(t *testing.T) {
	g, _ := newGuard(t, 1, nil)
	fail(t, g, 2)
	for range 5 {
		g.Allow(context.Background())
	}
	if got := g.Snapshot().Rejected; got != 5 {
		t.Fatalf("Rejected = %d, want 5", got)
	}
}

func TestStaleFastWindowDoesNotOutliveTheOpenState(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)
	g.Allow(context.Background()) // publishes the Open window [opened, opened+OpenTimeout)

	clk.Advance(openTimeout)
	mustAllow(t, g) // probe admitted: the circuit is Half-Open and the window is stale

	// The clock steps back into the old window. The Half-Open probe is now "too far
	// in the future", so the machine must re-open itself rather than let the stale
	// window answer for it.
	clk.Advance(-5 * time.Second)
	g.Allow(context.Background())
	if got := g.Snapshot().State; got != circuit.Open {
		t.Fatalf("State = %v, want open: the stale window must not hide the Half-Open handling", got)
	}
}
