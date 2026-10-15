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

// RejectEvent is delivered to RejectListener.OnReject.
type RejectEvent struct {
	Name  string
	State circuit.State // Open or HalfOpen
}

// Listener observes the breaker. Methods run on the goroutine that caused the
// event, after the lock is released: a slow Listener slows that caller, but it
// may safely call back into the breaker. A panic in a method is recovered and
// dropped. Ordering across goroutines is not guaranteed.
type Listener interface {
	OnStateChange(StateChange)
	OnCall(CallEvent)
}

// RejectListener is an optional extension of Listener. If the configured
// Listener also implements it, OnReject is called for every rejected call. The
// reject path costs nothing extra when it is not implemented.
type RejectListener interface {
	OnReject(RejectEvent)
}

// Snapshot is a point-in-time copy of breaker state.
type Snapshot struct {
	Name       string
	State      circuit.State
	Mode       circuit.Mode
	Counts     circuit.Counts
	Rejected   uint64    // calls rejected since the breaker was created
	LastChange time.Time // wall time of the last state change; zero if none
}

// Options are the non-machine settings of a Guard.
type Options struct {
	Name             string
	SlowCallDuration time.Duration
	// IsFailure classifies errors; nil selects DefaultIsFailure.
	IsFailure func(error) bool
	// Clock supplies the time used for all decisions; nil selects MonotonicClock.
	Clock func() time.Time
	// Wall supplies timestamps shown to users (StateChange.At, Snapshot.LastChange);
	// nil selects Clock if it was set, otherwise time.Now.
	Wall     func() time.Time
	Listener Listener
}

// DefaultIsFailure counts every error except context.Canceled, which means
// the caller gave up rather than the dependency failing. A deadline that
// expires while waiting on the dependency is a failure.
func DefaultIsFailure(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled)
}

// MonotonicClock returns a clock that reads only the monotonic clock. It is
// cheaper than time.Now, which also reads the wall clock. The returned times
// are only meant for measuring durations and ordering.
func MonotonicClock() func() time.Time {
	origin := time.Now()
	return func() time.Time { return origin.Add(time.Since(origin)) }
}

// Guard serialises access to a Machine.
type Guard struct {
	mu         sync.Mutex
	m          *circuit.Machine
	opts       Options
	now        func() time.Time
	wall       func() time.Time
	rejectL    RejectListener
	lastRej    *RejectedError
	lastChange time.Time

	// rejected counts rejected calls. fast, when set, describes a period in which
	// every call is rejected identically, so Begin can reject without taking mu.
	rejected atomic.Uint64
	fast     atomic.Pointer[fastReject]
}

// fastReject is an immutable copy of circuit.Machine.RejectWindow plus the
// shared error to return.
type fastReject struct {
	from, until time.Time
	forever     bool
	err         *RejectedError
}

// New returns a Guard around m.
func New(m *circuit.Machine, opts Options) *Guard {
	g := &Guard{m: m, opts: opts, now: opts.Clock, wall: opts.Wall}
	if g.now == nil {
		g.now = MonotonicClock()
		if g.wall == nil {
			g.wall = time.Now
		}
	}
	if g.wall == nil {
		g.wall = g.now
	}
	if g.opts.IsFailure == nil {
		g.opts.IsFailure = DefaultIsFailure
	}
	g.rejectL, _ = opts.Listener.(RejectListener)
	return g
}

// Ticket identifies one admitted call. It is a plain value: the allocation-free
// counterpart of Permit, for callers that report the result exactly once.
type Ticket struct {
	gen   uint64
	start time.Time
}

// Begin asks to make a call. It returns ctx.Err() if ctx is already done,
// without using a probe slot, or a *RejectedError if the circuit refuses.
func (g *Guard) Begin(ctx context.Context) (Ticket, error) {
	if ctx == nil {
		return Ticket{}, errNilContext
	}
	if err := ctx.Err(); err != nil {
		return Ticket{}, err
	}

	now := g.now()
	if fr := g.fast.Load(); fr != nil && (fr.forever || (!now.Before(fr.from) && now.Before(fr.until))) {
		g.rejected.Add(1)
		g.notifyReject(circuit.Open)
		return Ticket{}, fr.err
	}

	g.mu.Lock()
	adm, ts := g.m.Admit(now)
	var rej *RejectedError
	if adm.Reject != circuit.Admitted {
		rej = g.rejection(adm)
		g.rejected.Add(1)
	}
	if len(ts) > 0 {
		g.lastChange = g.wall()
	}
	if len(ts) > 0 || rej != nil {
		g.syncFast()
	}
	g.mu.Unlock()

	g.notifyStates(ts)
	if rej != nil {
		g.notifyReject(rej.state)
		return Ticket{}, rej
	}
	return Ticket{gen: adm.Gen, start: now}, nil
}

// End reports the result of a call admitted by Begin. Call it exactly once per
// Ticket; use Permit when that cannot be guaranteed.
func (g *Guard) End(tk Ticket, err error) {
	now := g.now()
	elapsed := max(now.Sub(tk.start), 0)
	outcome := g.classify(err, elapsed)

	g.mu.Lock()
	ts := g.m.Record(now, tk.gen, outcome)
	if len(ts) > 0 {
		g.lastChange = g.wall()
		g.syncFast()
	}
	g.mu.Unlock()

	g.notifyStates(ts)
	g.notifyCall(CallEvent{Name: g.opts.Name, Outcome: outcome, Duration: elapsed})
}

// Permit is the right to make one call. Call Done exactly once with the
// call's error; further calls are ignored.
type Permit struct {
	g    *Guard
	tk   Ticket
	done atomic.Bool
}

// Allow is Begin that returns a Permit with an idempotent Done.
func (g *Guard) Allow(ctx context.Context) (*Permit, error) {
	tk, err := g.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &Permit{g: g, tk: tk}, nil
}

// Done reports the call's result. It is idempotent and a no-op on a nil or
// zero-value Permit.
func (p *Permit) Done(err error) {
	if p == nil || p.g == nil || !p.done.CompareAndSwap(false, true) {
		return
	}
	p.g.End(p.tk, err)
}

// Snapshot returns a copy of the current state.
func (g *Guard) Snapshot() Snapshot {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	s, c := g.m.Snapshot(now)
	return Snapshot{
		Name:       g.opts.Name,
		State:      s,
		Mode:       g.m.Mode(),
		Counts:     c,
		Rejected:   g.rejected.Load(),
		LastChange: g.lastChange,
	}
}

// SetMode applies an operator override; circuit.ModeNormal also closes the
// circuit and clears its window (Reset).
func (g *Guard) SetMode(mode circuit.Mode) {
	now := g.now()
	g.mu.Lock()
	ts := g.m.SetMode(now, mode)
	if len(ts) > 0 {
		g.lastChange = g.wall()
	}
	g.syncFast()
	g.mu.Unlock()
	g.notifyStates(ts)
}

// Reset returns to normal operation: closed, empty window, no forced mode.
func (g *Guard) Reset() { g.SetMode(circuit.ModeNormal) }

// syncFast publishes (or clears) the lock-free reject window so that it always
// matches the machine. Callers hold g.mu.
func (g *Guard) syncFast() {
	from, until, forever, ok := g.m.RejectWindow()
	if !ok {
		g.fast.Store(nil)
		return
	}
	if cur := g.fast.Load(); cur != nil && cur.forever == forever && cur.from.Equal(from) && cur.until.Equal(until) {
		return
	}
	retryAt := until
	if forever {
		retryAt = time.Time{}
	}
	g.fast.Store(&fastReject{from: from, until: until, forever: forever, err: g.sharedRejection(circuit.Open, retryAt)})
}

// rejection returns a shared, immutable error while the state and retry time
// are unchanged. Callers hold g.mu.
func (g *Guard) rejection(adm circuit.Admission) *RejectedError {
	st := circuit.Open
	if adm.Reject == circuit.RejectedHalfOpenFull {
		st = circuit.HalfOpen
	}
	return g.sharedRejection(st, adm.RetryAt)
}

// sharedRejection returns the cached error if it matches, else makes a new one.
// Callers hold g.mu.
func (g *Guard) sharedRejection(st circuit.State, retryAt time.Time) *RejectedError {
	if r := g.lastRej; r != nil && r.state == st && r.retryAt.Equal(retryAt) {
		return r
	}
	r := &RejectedError{state: st, retryAt: retryAt}
	g.lastRej = r
	return r
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

func (g *Guard) notifyStates(ts []circuit.Transition) {
	if len(ts) == 0 || g.opts.Listener == nil {
		return
	}
	at := g.wall()
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

func (g *Guard) notifyReject(st circuit.State) {
	if g.rejectL == nil {
		return
	}
	ev := RejectEvent{Name: g.opts.Name, State: st}
	safely(func() { g.rejectL.OnReject(ev) })
}

func safely(f func()) {
	defer func() { _ = recover() }()
	f()
}
