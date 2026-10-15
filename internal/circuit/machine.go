package circuit

import "time"

// MachineConfig holds the already-validated rules a Machine follows.
type MachineConfig struct {
	Policy           Policy
	OpenTimeout      time.Duration
	HalfOpenMaxCalls int
	ProbeTimeout     time.Duration
}

// Mode is an operator override of the normal state machine.
type Mode int

// Modes. Anything other than ModeNormal is explicit and lasts until Reset.
const (
	ModeNormal     Mode = iota
	ModeForcedOpen      // every call is rejected; no automatic Half-Open
	ModeDisabled        // every call is admitted and nothing is recorded
)

func (m Mode) String() string {
	switch m {
	case ModeNormal:
		return "normal"
	case ModeForcedOpen:
		return "forced-open"
	case ModeDisabled:
		return "disabled"
	default:
		return "unknown"
	}
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
	Gen    uint64 // pass back to Record; identifies the admitting generation
	Reject Reject
	// RetryAt is when a rejected caller may try again. It is the zero time when
	// no retry is scheduled (ModeForcedOpen). Valid when Reject != Admitted.
	RetryAt time.Time
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
	mode     Mode
	gen      uint64
	openedAt time.Time
	half     probes
	consec   int // failures in a row while Closed; only tracked when enabled
}

// NewMachine returns a Closed machine.
func NewMachine(cfg MachineConfig, win Window) *Machine {
	return &Machine{cfg: cfg, win: win}
}

// Admit decides whether a call may run now.
func (m *Machine) Admit(now time.Time) (Admission, []Transition) {
	switch m.mode {
	case ModeDisabled:
		return Admission{Gen: m.gen}, nil
	case ModeForcedOpen:
		return Admission{Reject: RejectedOpen}, nil
	}

	var ts []Transition
	if m.state == Open && now.Before(m.openedAt) {
		m.openedAt = now // clock stepped back: restart the wait instead of sticking
	}
	if m.state == Open && !now.Before(m.openedAt.Add(m.cfg.OpenTimeout)) {
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
		return Admission{Reject: RejectedOpen, RetryAt: m.openedAt.Add(m.cfg.OpenTimeout)}, ts
	}

	if m.half.admitted >= m.cfg.HalfOpenMaxCalls {
		return Admission{Reject: RejectedHalfOpenFull, RetryAt: m.half.deadline}, ts
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
	if gen != m.gen || m.mode == ModeDisabled {
		return nil
	}
	switch m.state {
	case Closed:
		if o == Ignored {
			return nil
		}
		m.win.Record(now, o)
		if m.cfg.Policy.ConsecutiveFailures > 0 {
			if o == Failure {
				m.consec++
			} else {
				m.consec = 0
			}
		}
		if m.cfg.Policy.Tripped(m.win.Counts(now)) || m.cfg.Policy.ConsecutiveTripped(m.consec) {
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
		// Re-open as soon as the probes seen so far make recovery impossible,
		// without waiting for the rest to finish against a failing dependency.
		if m.cfg.Policy.Doomed(m.half.counts, m.cfg.HalfOpenMaxCalls) {
			return []Transition{m.enter(Open, now)}
		}
		if m.half.completed < m.cfg.HalfOpenMaxCalls {
			return nil
		}
		return []Transition{m.enter(Closed, now)}
	}
	return nil
}

// Reset returns the machine to ModeNormal and Closed with an empty window,
// and drops all in-flight results.
func (m *Machine) Reset(now time.Time) []Transition {
	m.mode = ModeNormal
	from := m.state
	t := m.enter(Closed, now)
	if from == Closed {
		return nil
	}
	return []Transition{t}
}

// SetMode applies an operator override. ModeNormal is the same as Reset.
// ModeForcedOpen moves the machine to Open and keeps it there. ModeDisabled
// keeps the state but stops admitting-with-recording. In every case in-flight
// results are dropped.
func (m *Machine) SetMode(now time.Time, mode Mode) []Transition {
	switch mode {
	case ModeForcedOpen:
		m.mode = mode
		from := m.state
		t := m.enter(Open, now)
		if from == Open {
			return nil
		}
		return []Transition{t}
	case ModeDisabled:
		m.mode = mode
		m.gen++
		m.half = probes{}
		m.consec = 0
		return nil
	default:
		return m.Reset(now)
	}
}

// RejectWindow describes a period in which every call is rejected the same way
// without changing any state, so callers may reject without locking. While the
// machine is forced open, forever is true and the times are zero. While it is
// Open in normal mode, calls with from <= now < until are rejected with
// RetryAt = until. ok is false in every other situation.
func (m *Machine) RejectWindow() (from, until time.Time, forever, ok bool) {
	switch {
	case m.mode == ModeForcedOpen:
		return time.Time{}, time.Time{}, true, true
	case m.mode == ModeNormal && m.state == Open:
		return m.openedAt, m.openedAt.Add(m.cfg.OpenTimeout), false, true
	}
	return time.Time{}, time.Time{}, false, false
}

// Mode returns the current operator override.
func (m *Machine) Mode() Mode { return m.mode }

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
	m.consec = 0
	switch to {
	case Open:
		m.openedAt = now
	case Closed:
		m.win.Reset()
	}
	return t
}
