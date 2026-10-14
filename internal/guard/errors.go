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

// RejectedError is returned when the circuit refuses a call.
// It carries no caller data.
type RejectedError struct {
	State      circuit.State
	RetryAfter time.Duration // never negative
}

func (e *RejectedError) Error() string {
	base := ErrOpen
	if e.State == circuit.HalfOpen {
		base = ErrHalfOpenFull
	}
	return fmt.Sprintf("%s (retry after %s)", base, e.RetryAfter)
}

// Is makes errors.Is(err, ErrOpen) and errors.Is(err, ErrHalfOpenFull) work.
func (e *RejectedError) Is(target error) bool {
	switch target {
	case ErrOpen:
		return e.State == circuit.Open
	case ErrHalfOpenFull:
		return e.State == circuit.HalfOpen
	}
	return false
}
