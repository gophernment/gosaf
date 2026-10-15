package gosaf_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gophernment/gosaf"
)

var errBoom = errors.New("boom")

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newBreaker(t *testing.T) (*gosaf.Breaker, *clock) {
	t.Helper()
	clk := &clock{t: time.Unix(1_000_000, 0)}
	cfg := gosaf.DefaultConfig()
	cfg.MinCalls = 2
	cfg.HalfOpenMaxCalls = 1
	cfg.Clock = clk.Now
	b, err := gosaf.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b, clk
}

func failOnce(b *gosaf.Breaker) error {
	_, err := gosaf.Do(context.Background(), b, func(context.Context) (int, error) { return 0, errBoom })
	return err
}

func TestDoReturnsResultAndError(t *testing.T) {
	b, _ := newBreaker(t)

	got, err := gosaf.Do(context.Background(), b, func(context.Context) (string, error) { return "ok", nil })
	if err != nil || got != "ok" {
		t.Fatalf("got %q, %v; want ok, nil", got, err)
	}
	if err := failOnce(b); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want errBoom passed through", err)
	}
}

func TestConsecutiveFailuresTripBeforeMinCalls(t *testing.T) {
	clk := &clock{t: time.Unix(1_000_000, 0)}
	cfg := gosaf.DefaultConfig() // MinCalls is 20
	cfg.ConsecutiveFailures = 3
	cfg.Clock = clk.Now
	b, err := gosaf.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	failOnce(b)
	failOnce(b)
	if b.State() != gosaf.Closed {
		t.Fatalf("State = %v, want closed after 2 failures", b.State())
	}
	failOnce(b)
	if b.State() != gosaf.Open {
		t.Fatalf("State = %v, want open after 3 consecutive failures", b.State())
	}
}

func TestDoFailsFastWhenOpen(t *testing.T) {
	b, _ := newBreaker(t)
	failOnce(b)
	failOnce(b)

	called := false
	_, err := gosaf.Do(context.Background(), b, func(context.Context) (int, error) { called = true; return 0, nil })
	if !errors.Is(err, gosaf.ErrOpen) || called {
		t.Fatalf("err = %v, called = %v; want ErrOpen and fn not called", err, called)
	}
	if b.State() != gosaf.Open {
		t.Fatalf("State = %v, want open", b.State())
	}
}

func TestDoRecoversThroughHalfOpen(t *testing.T) {
	b, clk := newBreaker(t)
	failOnce(b)
	failOnce(b)
	clk.Advance(gosaf.DefaultConfig().OpenTimeout)

	if _, err := gosaf.Do(context.Background(), b, func(context.Context) (int, error) { return 1, nil }); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if b.State() != gosaf.Closed {
		t.Fatalf("State = %v, want closed after successful probe", b.State())
	}
}

func TestDoSkipsFnWhenContextDone(t *testing.T) {
	b, _ := newBreaker(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	_, err := gosaf.Do(ctx, b, func(context.Context) (int, error) { called = true; return 0, nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("err = %v, called = %v; want context.Canceled and fn not called", err, called)
	}
}

func TestDoRecordsPanicAndReraises(t *testing.T) {
	b, _ := newBreaker(t)

	func() {
		defer func() {
			if r := recover(); r != "kaboom" {
				t.Fatalf("recovered %v, want original panic value", r)
			}
		}()
		gosaf.Do(context.Background(), b, func(context.Context) (int, error) { panic("kaboom") })
	}()

	if got := b.Snapshot().Counts.Failures; got != 1 {
		t.Fatalf("failures = %d, want the panic recorded", got)
	}
}

func TestDoFreesProbeSlotOnGoexit(t *testing.T) {
	b, clk := newBreaker(t)
	failOnce(b)
	failOnce(b)
	clk.Advance(gosaf.DefaultConfig().OpenTimeout)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		gosaf.Do(context.Background(), b, func(context.Context) (int, error) {
			runtime.Goexit() // what t.FailNow does inside fn
			return 0, nil
		})
	}()
	wg.Wait()

	// The probe failed (recorded), so the circuit re-opened instead of sticking half-open.
	if got := b.State(); got != gosaf.Open {
		t.Fatalf("State = %v, want open after goexit probe", got)
	}
}

func TestZeroValueBreakerFailsClosed(t *testing.T) {
	var zero gosaf.Breaker
	for name, b := range map[string]*gosaf.Breaker{"nil": nil, "zero": &zero} {
		t.Run(name, func(t *testing.T) {
			called := false
			_, err := gosaf.Do(context.Background(), b, func(context.Context) (int, error) { called = true; return 0, nil })
			if !errors.Is(err, gosaf.ErrInvalidConfig) || called {
				t.Fatalf("err = %v, called = %v; want ErrInvalidConfig and fn not called", err, called)
			}
			b.ForceOpen()
			b.Disable()
			b.Reset()
			if s := b.Snapshot(); s != (gosaf.Snapshot{}) {
				t.Fatalf("Snapshot = %+v, want zero", s)
			}
		})
	}
}

func TestAllowAndDoneManually(t *testing.T) {
	b, _ := newBreaker(t)
	p, err := b.Allow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p.Done(nil)
	if got := b.Snapshot().Counts.Total; got != 1 {
		t.Fatalf("total = %d, want 1", got)
	}
}

type countingListener struct{ changes, calls atomic.Int64 }

func (l *countingListener) OnStateChange(gosaf.StateChange) { l.changes.Add(1) }
func (l *countingListener) OnCall(gosaf.CallEvent)          { l.calls.Add(1) }

func TestConcurrentUse(t *testing.T) {
	lis := &countingListener{}
	cfg := gosaf.DefaultConfig()
	cfg.WindowSize, cfg.MinCalls = 50, 10
	cfg.OpenTimeout = time.Millisecond
	cfg.ProbeTimeout = 5 * time.Millisecond
	cfg.Listener = lis
	b, err := gosaf.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for g := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				gosaf.Do(context.Background(), b, func(context.Context) (int, error) {
					if (g+i)%3 == 0 {
						return 0, errBoom
					}
					return 1, nil
				})
				if i%50 == 0 {
					_ = b.Snapshot()
				}
			}
		}()
	}
	wg.Wait()

	if got := b.Snapshot().Counts.Total; got > cfg.WindowSize {
		t.Fatalf("window holds %d calls, bound is %d", got, cfg.WindowSize)
	}
	if lis.calls.Load() == 0 {
		t.Fatal("listener saw no calls")
	}
}

func TestDoClosedPathDoesNotAllocate(t *testing.T) {
	b, err := gosaf.New(gosaf.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fn := func(context.Context) (int, error) { return 1, nil }

	if n := testing.AllocsPerRun(1000, func() { gosaf.Do(ctx, b, fn) }); n != 0 {
		t.Fatalf("Do on the Closed path allocates %v times per call, want 0", n)
	}
}

func TestDoRejectPathDoesNotAllocate(t *testing.T) {
	cfg := gosaf.DefaultConfig()
	cfg.MinCalls = 1
	b, err := gosaf.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	failOnce(b)
	fn := func(context.Context) (int, error) { return 1, nil }

	if _, err := gosaf.Do(ctx, b, fn); !errors.Is(err, gosaf.ErrOpen) {
		t.Fatalf("setup: err = %v, want ErrOpen", err)
	}
	if n := testing.AllocsPerRun(1000, func() { gosaf.Do(ctx, b, fn) }); n != 0 {
		t.Fatalf("Do on the Open (reject) path allocates %v times per call, want 0", n)
	}
}

func TestRejectedErrorExposesRetryTime(t *testing.T) {
	b, clk := newBreaker(t)
	failOnce(b)
	failOnce(b)
	opened := clk.Now()

	_, err := gosaf.Do(context.Background(), b, func(context.Context) (int, error) { return 0, nil })
	var rej *gosaf.RejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("err = %v, want *RejectedError", err)
	}
	if rej.State() != gosaf.Open || !rej.RetryAt().Equal(opened.Add(gosaf.DefaultConfig().OpenTimeout)) {
		t.Fatalf("State = %v, RetryAt = %v; want open and OpenTimeout after %v", rej.State(), rej.RetryAt(), opened)
	}
}

func TestForceOpenDisableAndReset(t *testing.T) {
	b, clk := newBreaker(t)

	b.ForceOpen()
	clk.Advance(100 * gosaf.DefaultConfig().OpenTimeout)
	_, err := gosaf.Do(context.Background(), b, func(context.Context) (int, error) { return 1, nil })
	if !errors.Is(err, gosaf.ErrOpen) {
		t.Fatalf("forced open: err = %v, want ErrOpen even long after OpenTimeout", err)
	}
	if s := b.Snapshot(); s.Mode != gosaf.ModeForcedOpen || s.State != gosaf.Open || s.Rejected != 1 {
		t.Fatalf("snapshot = %+v, want forced-open, open, 1 rejected", s)
	}

	b.Reset()
	b.Disable()
	for range 10 {
		failOnce(b)
	}
	if s := b.Snapshot(); s.Mode != gosaf.ModeDisabled || s.State != gosaf.Closed {
		t.Fatalf("snapshot = %+v, want disabled and closed: failures must not trip it", s)
	}

	b.Reset()
	if s := b.Snapshot(); s.Mode != gosaf.ModeNormal || s.State != gosaf.Closed {
		t.Fatalf("snapshot = %+v, want normal and closed after Reset", s)
	}
	failOnce(b)
	failOnce(b)
	if b.State() != gosaf.Open {
		t.Fatalf("State = %v, want normal tripping to work again after Reset", b.State())
	}
}

type rejectCounter struct {
	countingListener
	rejects atomic.Int64
}

func (l *rejectCounter) OnReject(gosaf.RejectEvent) { l.rejects.Add(1) }

func TestRejectListenerThroughFacade(t *testing.T) {
	l := &rejectCounter{}
	cfg := gosaf.DefaultConfig()
	cfg.MinCalls = 1
	cfg.Listener = l
	b, err := gosaf.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	failOnce(b)
	failOnce(b) // rejected
	if l.rejects.Load() != 1 || l.changes.Load() != 1 {
		t.Fatalf("rejects = %d, state changes = %d; want 1 and 1", l.rejects.Load(), l.changes.Load())
	}
}

func TestConcurrentModeChangesAndCalls(t *testing.T) {
	cfg := gosaf.DefaultConfig()
	cfg.MinCalls = 2
	cfg.OpenTimeout = time.Millisecond
	cfg.ProbeTimeout = 2 * time.Millisecond
	b, err := gosaf.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var ops sync.WaitGroup
	ops.Add(1)
	go func() {
		defer ops.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			switch i % 3 {
			case 0:
				b.ForceOpen()
			case 1:
				b.Disable()
			default:
				b.Reset()
			}
			_ = b.Snapshot()
		}
	}()

	var calls sync.WaitGroup
	for g := range 16 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			for i := range 2000 {
				gosaf.Do(context.Background(), b, func(context.Context) (int, error) {
					if (g+i)%2 == 0 {
						return 0, errBoom
					}
					return 1, nil
				})
			}
		}()
	}
	calls.Wait()
	close(stop)
	ops.Wait()

	b.Reset()
	if _, err := gosaf.Do(context.Background(), b, func(context.Context) (int, error) { return 1, nil }); err != nil {
		t.Fatalf("after Reset the breaker must accept calls, got %v", err)
	}
}
