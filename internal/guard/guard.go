// Package guard runs calls through a circuit.Machine. It owns the mutex, turns
// errors and durations into outcomes, and notifies the Listener outside the lock.
package guard

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gophernment/gosaf/internal/circuit"
)

var errNilContext = errors.New("gosaf: nil context")

// StateChange is delivered to Listener.OnStateChange.
type StateChange struct {
	Name     string
	From, To circuit.State
	At       time.Time
}

// CallEvent is delivered to Listener.OnCall. It carries no error text or payload.
type CallEvent struct {
	Name     string
	Outcome  circuit.Outcome
	Duration time.Duration
}

// Listener observes the breaker. Methods run after the lock is released, so
// they may call back into the breaker. A panic in a method is recovered and
// dropped. Ordering across goroutines is not guaranteed.
type Listener interface {
	OnStateChange(StateChange)
	OnCall(CallEvent)
}

// Snapshot is a point-in-time copy of breaker state.
type Snapshot struct {
	Name   string
	State  circuit.State
	Counts circuit.Counts
}

// Options are the non-machine settings of a Guard. Zero Clock and IsFailure
// select time.Now and DefaultIsFailure.
type Options struct {
	Name             string
	SlowCallDuration time.Duration
	IsFailure        func(error) bool
	Clock            func() time.Time
	Listener         Listener
}

// DefaultIsFailure counts every error except context.Canceled, which means
// the caller gave up rather than the dependency failing. A deadline that
// expires while waiting on the dependency is a failure.
func DefaultIsFailure(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled)
}

// Guard serialises access to a Machine.
type Guard struct {
	mu   sync.Mutex
	m    *circuit.Machine
	opts Options
}

// New returns a Guard around m.
func New(m *circuit.Machine, opts Options) *Guard {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.IsFailure == nil {
		opts.IsFailure = DefaultIsFailure
	}
	return &Guard{m: m, opts: opts}
}

// Permit is the right to make one call. Call Done exactly once with the
// call's error; further calls are ignored.
type Permit struct {
	g     *Guard
	gen   uint64
	start time.Time
	done  atomic.Bool
}

// Allow asks to make a call. It returns ctx.Err() if ctx is already done,
// without using a probe slot, or a *RejectedError if the circuit refuses.
func (g *Guard) Allow(ctx context.Context) (*Permit, error) {
	if ctx == nil {
		return nil, errNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	now := g.opts.Clock()
	g.mu.Lock()
	adm, ts := g.m.Admit(now)
	g.mu.Unlock()
	g.notifyStates(ts, now)

	switch adm.Reject {
	case circuit.RejectedOpen:
		return nil, &RejectedError{State: circuit.Open, RetryAfter: adm.RetryAfter}
	case circuit.RejectedHalfOpenFull:
		return nil, &RejectedError{State: circuit.HalfOpen, RetryAfter: adm.RetryAfter}
	}
	return &Permit{g: g, gen: adm.Gen, start: now}, nil
}

// Done reports the call's result. It is safe on a nil Permit and idempotent.
func (p *Permit) Done(err error) {
	if p == nil || !p.done.CompareAndSwap(false, true) {
		return
	}
	g := p.g
	now := g.opts.Clock()
	elapsed := max(now.Sub(p.start), 0)
	outcome := g.classify(err, elapsed)

	g.mu.Lock()
	ts := g.m.Record(now, p.gen, outcome)
	g.mu.Unlock()

	g.notifyStates(ts, now)
	g.notifyCall(CallEvent{Name: g.opts.Name, Outcome: outcome, Duration: elapsed})
}

// Snapshot returns a copy of the current state.
func (g *Guard) Snapshot() Snapshot {
	now := g.opts.Clock()
	g.mu.Lock()
	defer g.mu.Unlock()
	s, c := g.m.Snapshot(now)
	return Snapshot{Name: g.opts.Name, State: s, Counts: c}
}

// Reset closes the circuit, clears its window and drops in-flight results.
func (g *Guard) Reset() {
	now := g.opts.Clock()
	g.mu.Lock()
	ts := g.m.Reset(now)
	g.mu.Unlock()
	g.notifyStates(ts, now)
}

// classify runs user code (IsFailure) outside the lock.
func (g *Guard) classify(err error, elapsed time.Duration) circuit.Outcome {
	if err == nil {
		if elapsed > g.opts.SlowCallDuration {
			return circuit.Slow
		}
		return circuit.Success
	}
	if g.isFailure(err) {
		return circuit.Failure
	}
	return circuit.Ignored
}

// isFailure treats a panicking IsFailure as a failure (fail closed).
func (g *Guard) isFailure(err error) (failed bool) {
	defer func() {
		if recover() != nil {
			failed = true
		}
	}()
	return g.opts.IsFailure(err)
}

func (g *Guard) notifyStates(ts []circuit.Transition, at time.Time) {
	if g.opts.Listener == nil {
		return
	}
	for _, t := range ts {
		ev := StateChange{Name: g.opts.Name, From: t.From, To: t.To, At: at}
		safely(func() { g.opts.Listener.OnStateChange(ev) })
	}
}

func (g *Guard) notifyCall(ev CallEvent) {
	if g.opts.Listener == nil {
		return
	}
	safely(func() { g.opts.Listener.OnCall(ev) })
}

func safely(f func()) {
	defer func() { _ = recover() }()
	f()
}
