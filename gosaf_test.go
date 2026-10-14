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
