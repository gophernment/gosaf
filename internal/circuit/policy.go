package circuit

// Policy decides when recorded outcomes mean the dependency is unhealthy.
type Policy struct {
	MinCalls    int     // minimum calls in the window before Closed may trip
	FailureRate float64 // trip when failures/total >= FailureRate
	SlowRate    float64 // trip when slow/total >= SlowRate
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
