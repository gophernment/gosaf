package gosaf

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// MaxWindowSize bounds Config.WindowSize so memory use stays fixed and small.
const MaxWindowSize = 100_000

// ErrInvalidConfig is wrapped by every error New returns for a bad Config.
var ErrInvalidConfig = errors.New("gosaf: invalid config")

// Config describes a Breaker. Every field except Name, IsFailure, Clock and
// Listener must be set; New rejects anything out of range instead of
// substituting a default. DefaultConfig returns a ready-to-use starting point.
type Config struct {
	// Name labels Snapshot and Listener events.
	Name string

	// WindowSize is the number of recent calls to judge (count-based, when
	// WindowPeriod is 0) or the number of buckets (time-based). 1..MaxWindowSize.
	WindowSize int
	// WindowPeriod is the total span of a time-based window; 0 selects a
	// count-based window. Must be at least WindowSize nanoseconds.
	WindowPeriod time.Duration

	// MinCalls is how many calls the window needs before Closed may trip.
	// For a count-based window it cannot exceed WindowSize.
	MinCalls int
	// FailureRate in (0,1]: trip when failures/calls reaches it.
	FailureRate float64
	// SlowCallDuration: a successful call taking longer counts as slow.
	SlowCallDuration time.Duration
	// SlowCallRate in (0,1]: trip when slow/calls reaches it.
	SlowCallRate float64

	// OpenTimeout is how long the circuit stays Open before probing.
	OpenTimeout time.Duration
	// HalfOpenMaxCalls is the number of probe calls allowed in Half-Open.
	HalfOpenMaxCalls int
	// ProbeTimeout is how long a probe may run before it counts as hung and
	// the circuit re-opens.
	ProbeTimeout time.Duration

	// IsFailure classifies an error. Return false to exclude it from the
	// statistics. Default: every error except context.Canceled is a failure.
	IsFailure func(error) bool
	// Clock replaces time.Now, mainly for tests.
	Clock func() time.Time
	// Listener receives state changes and call results. Optional.
	Listener Listener
}

// DefaultConfig returns conservative settings: judge the last 100 calls, trip at
// 50% failures after 20 calls, stay open 30s, then allow 5 probes.
func DefaultConfig() Config {
	return Config{
		WindowSize:       100,
		MinCalls:         20,
		FailureRate:      0.5,
		SlowCallDuration: 2 * time.Second,
		SlowCallRate:     1,
		OpenTimeout:      30 * time.Second,
		HalfOpenMaxCalls: 5,
		ProbeTimeout:     10 * time.Second,
	}
}

func (c Config) validate() error {
	switch {
	case c.WindowSize < 1 || c.WindowSize > MaxWindowSize:
		return invalid("WindowSize", "must be in [1, %d]", MaxWindowSize)
	case c.WindowPeriod < 0:
		return invalid("WindowPeriod", "must not be negative")
	case c.WindowPeriod > 0 && c.WindowPeriod < time.Duration(c.WindowSize):
		return invalid("WindowPeriod", "must be at least WindowSize nanoseconds")
	case c.MinCalls < 1:
		return invalid("MinCalls", "must be at least 1")
	case c.WindowPeriod == 0 && c.MinCalls > c.WindowSize:
		return invalid("MinCalls", "must not exceed WindowSize for a count-based window")
	case !validRate(c.FailureRate):
		return invalid("FailureRate", "must be in (0, 1]")
	case !validRate(c.SlowCallRate):
		return invalid("SlowCallRate", "must be in (0, 1]")
	case c.SlowCallDuration <= 0:
		return invalid("SlowCallDuration", "must be positive")
	case c.OpenTimeout <= 0:
		return invalid("OpenTimeout", "must be positive")
	case c.HalfOpenMaxCalls < 1:
		return invalid("HalfOpenMaxCalls", "must be at least 1")
	case c.ProbeTimeout <= 0:
		return invalid("ProbeTimeout", "must be positive")
	}
	return nil
}

func validRate(r float64) bool { return !math.IsNaN(r) && r > 0 && r <= 1 }

func invalid(field, format string, args ...any) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidConfig, field, fmt.Sprintf(format, args...))
}
