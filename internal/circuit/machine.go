package circuit

import "time"

// MachineConfig holds the already-validated rules a Machine follows.
type MachineConfig struct {
	Policy           Policy
	OpenTimeout      time.Duration
	HalfOpenMaxCalls int
	ProbeTimeout     time.Duration
}

// Transition describes a state change.
type Transition struct {
	From, To State
}

// Reject says why a call was not admitted.
type Reject int

// Admission results.
const (
	Admitted Reject = iota
	RejectedOpen
	RejectedHalfOpenFull
)

// Admission is the result of Machine.Admit.
type Admission struct {
	Gen        uint64 // pass back to Record; identifies the admitting generation
	Reject     Reject
	RetryAfter time.Duration // valid when Reject != Admitted; always >= 0
}

type probes struct {
	admitted  int
	completed int
	pending   int
	counts    Counts
	deadline  time.Time
}

// Machine is the circuit state machine. It is not safe for concurrent use;
// the caller serialises access and supplies the current time.
type Machine struct {
	cfg      MachineConfig
	win      Window
	state    State
	gen      uint64
	openedAt time.Time
	half     probes
}

// NewMachine returns a Closed machine.
func NewMachine(cfg MachineConfig, win Window) *Machine {
	return &Machine{cfg: cfg, win: win}
}

// Admit decides whether a call may run now.
func (m *Machine) Admit(now time.Time) (Admission, []Transition) {
	var ts []Transition
	if m.state == Open && now.Before(m.openedAt) {
		m.openedAt = now // clock stepped back: restart the wait instead of sticking
	}
	if m.state == Open && m.openRemaining(now) == 0 {
		ts = append(ts, m.enter(HalfOpen, now))
	}
	if m.state == HalfOpen && m.half.pending > 0 &&
		(!now.Before(m.half.deadline) || m.half.deadline.Sub(now) > m.cfg.ProbeTimeout) {
		// A probe never reported back (or the clock stepped back past its
		// deadline): fail closed and re-open.
		ts = append(ts, m.enter(Open, now))
	}

	switch m.state {
	case Closed:
		return Admission{Gen: m.gen}, ts
	case Open:
		return Admission{Reject: RejectedOpen, RetryAfter: m.openRemaining(now)}, ts
	}

	if m.half.admitted >= m.cfg.HalfOpenMaxCalls {
		wait := min(max(m.half.deadline.Sub(now), 0), m.cfg.ProbeTimeout)
		return Admission{Reject: RejectedHalfOpenFull, RetryAfter: wait}, ts
	}
	if m.half.pending == 0 {
		m.half.deadline = now.Add(m.cfg.ProbeTimeout)
	}
	m.half.admitted++
	m.half.pending++
	return Admission{Gen: m.gen}, ts
}

// Record reports the outcome of a call admitted under generation gen.
// Results from an older generation are dropped.
func (m *Machine) Record(now time.Time, gen uint64, o Outcome) []Transition {
	if gen != m.gen {
		return nil
	}
	switch m.state {
	case Closed:
		if o == Ignored {
			return nil
		}
		m.win.Record(now, o)
		if m.cfg.Policy.Tripped(m.win.Counts(now)) {
			return []Transition{m.enter(Open, now)}
		}
	case HalfOpen:
		m.half.pending = max(m.half.pending-1, 0)
		if o == Ignored {
			m.half.admitted = max(m.half.admitted-1, 0) // free the probe slot
			return nil
		}
		m.half.completed++
		m.half.counts.add(o)
		if m.half.completed < m.cfg.HalfOpenMaxCalls {
			return nil
		}
		if m.cfg.Policy.Unhealthy(m.half.counts) {
			return []Transition{m.enter(Open, now)}
		}
		return []Transition{m.enter(Closed, now)}
	}
	return nil
}

// Reset forces the machine to Closed with an empty window and drops all
// in-flight results.
func (m *Machine) Reset(now time.Time) []Transition {
	from := m.state
	t := m.enter(Closed, now)
	if from == Closed {
		return nil
	}
	return []Transition{t}
}

// Snapshot returns the stored state and the counts that drive it.
// An Open machine moves to HalfOpen lazily, on the next Admit.
func (m *Machine) Snapshot(now time.Time) (State, Counts) {
	if m.state == HalfOpen {
		return m.state, m.half.counts
	}
	return m.state, m.win.Counts(now)
}

func (m *Machine) enter(to State, now time.Time) Transition {
	t := Transition{From: m.state, To: to}
	m.state = to
	m.gen++
	m.half = probes{}
	switch to {
	case Open:
		m.openedAt = now
	case Closed:
		m.win.Reset()
	}
	return t
}

// openRemaining is how long until an Open circuit may probe, within
// [0, OpenTimeout] even if the clock steps backwards.
func (m *Machine) openRemaining(now time.Time) time.Duration {
	elapsed := now.Sub(m.openedAt)
	if elapsed < 0 {
		return m.cfg.OpenTimeout // avoids Duration overflow in the subtraction below
	}
	return max(m.cfg.OpenTimeout-elapsed, 0)
}
