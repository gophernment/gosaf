// Package circuit holds the pure circuit-breaker rules: states, sliding
// windows, the trip policy and the state machine. It never reads the clock,
// starts goroutines or locks; callers pass the current time in.
package circuit

// State is the circuit state.
type State int

// Circuit states.
const (
	Closed State = iota
	Open
	HalfOpen
)

func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	case HalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Outcome classifies a finished call.
type Outcome int

// Call outcomes. Ignored calls are never counted.
const (
	Success Outcome = iota
	Failure
	Slow // succeeded, but took longer than the slow-call threshold
	Ignored
)

func (o Outcome) String() string {
	switch o {
	case Success:
		return "success"
	case Failure:
		return "failure"
	case Slow:
		return "slow"
	case Ignored:
		return "ignored"
	default:
		return "unknown"
	}
}

// Counts is a summary of recorded outcomes.
type Counts struct {
	Total    int
	Failures int
	Slow     int
}

func (c *Counts) add(o Outcome) {
	c.Total++
	switch o {
	case Failure:
		c.Failures++
	case Slow:
		c.Slow++
	}
}

func (c *Counts) sub(o Outcome) {
	c.Total--
	switch o {
	case Failure:
		c.Failures--
	case Slow:
		c.Slow--
	}
}
