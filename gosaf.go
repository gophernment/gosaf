// Package gosaf is a circuit breaker: it stops calling a failing or slow
// dependency, lets it recover, then probes before trusting it again.
//
//	cb, err := gosaf.New(gosaf.DefaultConfig())
//	if err != nil { /* bad config */ }
//	res, err := gosaf.Do(ctx, cb, func(ctx context.Context) (string, error) {
//		return callDependency(ctx)
//	})
//	if errors.Is(err, gosaf.ErrOpen) { /* fail fast or fall back */ }
package gosaf

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gophernment/gosaf/internal/circuit"
	"github.com/gophernment/gosaf/internal/guard"
)

// Re-exported types. Their definitions live in internal packages.
type (
	// State is Closed, Open or HalfOpen.
	State = circuit.State
	// Outcome is how a finished call was classified.
	Outcome = circuit.Outcome
	// Counts summarises calls in the current window.
	Counts = circuit.Counts
	// Mode is an operator override: ModeNormal, ModeForcedOpen or ModeDisabled.
	Mode = circuit.Mode
	// Snapshot is a point-in-time copy of breaker state.
	Snapshot = guard.Snapshot
	// StateChange is delivered to Listener.OnStateChange.
	StateChange = guard.StateChange
	// CallEvent is delivered to Listener.OnCall.
	CallEvent = guard.CallEvent
	// Listener observes a Breaker. Methods run on the goroutine that caused the
	// event, after the breaker's lock is released: a slow Listener slows that
	// caller, but it may safely call back into the breaker. Events from different
	// goroutines can arrive out of order (use StateChange.At). A panic in a
	// method is recovered and dropped.
	Listener = guard.Listener
	// Permit is the right to make one call; report the result with Done.
	Permit = guard.Permit
	// RejectListener is an optional extension of Listener that is told about
	// every rejected call.
	RejectListener = guard.RejectListener
	// RejectEvent is delivered to RejectListener.OnReject.
	RejectEvent = guard.RejectEvent
	// RejectedError is returned when the circuit refuses a call. It is
	// immutable and may be shared between callers; read it with State, RetryAt
	// and RetryAfter.
	RejectedError = guard.RejectedError
)

// Circuit states.
const (
	Closed   = circuit.Closed
	Open     = circuit.Open
	HalfOpen = circuit.HalfOpen
)

// Operator modes.
const (
	ModeNormal     = circuit.ModeNormal
	ModeForcedOpen = circuit.ModeForcedOpen
	ModeDisabled   = circuit.ModeDisabled
)

// Call outcomes.
const (
	Success = circuit.Success
	Failure = circuit.Failure
	Slow    = circuit.Slow
	Ignored = circuit.Ignored
)

// Rejection errors. Match with errors.Is.
var (
	ErrOpen         = guard.ErrOpen
	ErrHalfOpenFull = guard.ErrHalfOpenFull
)

var (
	errNotInitialized = fmt.Errorf("%w: Breaker must be created with New", ErrInvalidConfig)
	errPanicked       = errors.New("gosaf: call panicked")
)

// Breaker is a circuit breaker. Create it with New; it is safe for concurrent use.
type Breaker struct {
	g *guard.Guard
}

// New validates cfg and returns a Breaker in the Closed state.
func New(cfg Config) (*Breaker, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	clock, wall := cfg.Clock, cfg.Clock
	if clock == nil {
		clock, wall = guard.MonotonicClock(), time.Now
	}

	var win circuit.Window
	if cfg.WindowPeriod > 0 {
		win = circuit.NewTimeWindow(cfg.WindowSize, cfg.WindowPeriod, clock())
	} else {
		win = circuit.NewCountWindow(cfg.WindowSize)
	}
	m := circuit.NewMachine(circuit.MachineConfig{
		Policy: circuit.Policy{
			MinCalls:    cfg.MinCalls,
			FailureRate: cfg.FailureRate,
			SlowRate:    cfg.SlowCallRate,

			ConsecutiveFailures: cfg.ConsecutiveFailures,
		},
		OpenTimeout:      cfg.OpenTimeout,
		HalfOpenMaxCalls: cfg.HalfOpenMaxCalls,
		ProbeTimeout:     cfg.ProbeTimeout,
	}, win)

	return &Breaker{g: guard.New(m, guard.Options{
		Name:             cfg.Name,
		SlowCallDuration: cfg.SlowCallDuration,
		IsFailure:        cfg.IsFailure,
		Clock:            clock,
		Wall:             wall,
		Listener:         cfg.Listener,
	})}, nil
}

// Allow asks to make one call. On success the caller must call Permit.Done
// with the call's error (nil on success). Prefer Do, which does this for you.
// Errors: ctx.Err() if ctx is done, or a *RejectedError (ErrOpen, ErrHalfOpenFull).
func (b *Breaker) Allow(ctx context.Context) (*Permit, error) {
	if b == nil || b.g == nil {
		return nil, errNotInitialized
	}
	return b.g.Allow(ctx)
}

// Snapshot returns a copy of the current state and counts. A zero Breaker
// returns a zero Snapshot.
//
// The state is the stored one and changes lazily, on the next Allow: an Open
// circuit becomes HalfOpen only when a call arrives after OpenTimeout, and a
// probe that never finished re-opens the circuit only when a call arrives after
// ProbeTimeout. Until then Snapshot and State keep reporting the old state.
func (b *Breaker) Snapshot() Snapshot {
	if b == nil || b.g == nil {
		return Snapshot{}
	}
	return b.g.Snapshot()
}

// State returns the stored state; see Snapshot for when it changes.
func (b *Breaker) State() State { return b.Snapshot().State }

// ForceOpen opens the circuit and keeps it open: every call is rejected with
// ErrOpen until Reset. The circuit never moves to Half-Open by itself. Use it
// to shed load from a dependency during an incident.
func (b *Breaker) ForceOpen() {
	if b == nil || b.g == nil {
		return
	}
	b.g.SetMode(ModeForcedOpen)
}

// Disable turns the breaker into a pass-through: every call is allowed and
// nothing is recorded, until Reset. It removes protection, so use it only when
// the breaker itself is the problem.
func (b *Breaker) Disable() {
	if b == nil || b.g == nil {
		return
	}
	b.g.SetMode(ModeDisabled)
}

// Reset returns to normal operation: the circuit is closed, its statistics are
// cleared, any forced mode ends, and the results of calls still in flight are
// discarded. Use it from operational tooling, not the hot path.
func (b *Breaker) Reset() {
	if b == nil || b.g == nil {
		return
	}
	b.g.Reset()
}

// Do runs fn through the breaker. It returns a *RejectedError without calling
// fn when the circuit refuses, and ctx.Err() when ctx is already done.
// A panic in fn is recorded as a failure and then re-raised with the original
// value, so recover returns exactly what fn panicked with.
func Do[T any](ctx context.Context, b *Breaker, fn func(context.Context) (T, error)) (res T, err error) {
	if b == nil || b.g == nil {
		return res, errNotInitialized
	}
	tk, err := b.g.Begin(ctx)
	if err != nil {
		return res, err
	}

	finished := false
	defer func() {
		if finished {
			return
		}
		// fn panicked or called runtime.Goexit: free the slot, then let it continue.
		r := recover()
		b.g.End(tk, errPanicked)
		if r != nil {
			panic(r)
		}
	}()

	res, err = fn(ctx)
	finished = true
	b.g.End(tk, err)
	return res, err
}
