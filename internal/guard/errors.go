package guard

import (
	"errors"
	"fmt"
	"time"

	"github.com/gophernment/gosaf/internal/circuit"
)

// Sentinel errors for rejected calls. Match with errors.Is.
var (
	ErrOpen         = errors.New("gosaf: circuit open")
	ErrHalfOpenFull = errors.New("gosaf: half-open probe limit reached")
)

// RejectedError is returned when the circuit refuses a call. It carries no
// caller data. The same value is returned to every caller while the retry time
// is unchanged, so its fields are unexported and it is immutable.
type RejectedError struct {
	state   circuit.State
	retryAt time.Time
}

// State is Open or HalfOpen: the state that caused the rejection.
func (e *RejectedError) State() circuit.State { return e.state }

// RetryAt is when a caller may try again. It is the zero time when no retry is
// scheduled, which is the case while the breaker is forced open.
func (e *RejectedError) RetryAt() time.Time { return e.retryAt }

// RetryAfter is the time left until RetryAt, never negative. It is measured with
// the wall clock, not Config.Clock, and is 0 when no retry is scheduled.
func (e *RejectedError) RetryAfter() time.Duration {
	if e.retryAt.IsZero() {
		return 0
	}
	return max(time.Until(e.retryAt), 0)
}

func (e *RejectedError) Error() string {
	base := ErrOpen
	if e.state == circuit.HalfOpen {
		base = ErrHalfOpenFull
	}
	if e.retryAt.IsZero() {
		return fmt.Sprintf("%s (no retry scheduled)", base)
	}
	return fmt.Sprintf("%s (retry after %s)", base, e.RetryAfter().Round(time.Millisecond))
}

// Is makes errors.Is(err, ErrOpen) and errors.Is(err, ErrHalfOpenFull) work.
func (e *RejectedError) Is(target error) bool {
	switch target {
	case ErrOpen:
		return e.state == circuit.Open
	case ErrHalfOpenFull:
		return e.state == circuit.HalfOpen
	}
	return false
}
