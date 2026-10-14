package circuit

// Policy decides when recorded outcomes mean the dependency is unhealthy.
type Policy struct {
	MinCalls    int     // minimum calls in the window before Closed may trip
	FailureRate float64 // trip when failures/total >= FailureRate
	SlowRate    float64 // trip when slow/total >= SlowRate

	// ConsecutiveFailures trips Closed after this many failures in a row,
	// regardless of MinCalls. 0 disables it.
	ConsecutiveFailures int
}

// Unhealthy reports whether the rates in c reach a threshold, ignoring MinCalls.
func (p Policy) Unhealthy(c Counts) bool {
	if c.Total == 0 {
		return false
	}
	total := float64(c.Total)
	return float64(c.Failures)/total >= p.FailureRate ||
		float64(c.Slow)/total >= p.SlowRate
}

// Tripped reports whether a Closed circuit should open.
func (p Policy) Tripped(c Counts) bool {
	return c.Total >= p.MinCalls && p.Unhealthy(c)
}

// ConsecutiveTripped reports whether n failures in a row trip a Closed circuit.
func (p Policy) ConsecutiveTripped(n int) bool {
	return p.ConsecutiveFailures > 0 && n >= p.ConsecutiveFailures
}

// Doomed reports whether a Half-Open circuit cannot recover: even if every one
// of the size probes still to complete succeeds, c would still be unhealthy.
func (p Policy) Doomed(c Counts, size int) bool {
	return p.Unhealthy(Counts{Total: max(size, c.Total), Failures: c.Failures, Slow: c.Slow})
}
