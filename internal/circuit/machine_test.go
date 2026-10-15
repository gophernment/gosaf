package circuit_test

import (
	"testing"
	"time"

	"github.com/gophernment/gosaf/internal/circuit"
)

const (
	openTimeout  = 10 * time.Second
	probeTimeout = 3 * time.Second
)

func newMachine(halfOpenMax int) *circuit.Machine {
	return circuit.NewMachine(circuit.MachineConfig{
		Policy:           circuit.Policy{MinCalls: 4, FailureRate: 0.5, SlowRate: 0.5},
		OpenTimeout:      openTimeout,
		HalfOpenMaxCalls: halfOpenMax,
		ProbeTimeout:     probeTimeout,
	}, circuit.NewCountWindow(10))
}

// call admits one call at now and records outcome o for it.
func call(t *testing.T, m *circuit.Machine, now time.Time, o circuit.Outcome) []circuit.Transition {
	t.Helper()
	adm, _ := m.Admit(now)
	if adm.Reject != circuit.Admitted {
		t.Fatalf("call rejected: %+v", adm)
	}
	return m.Record(now, adm.Gen, o)
}

func state(m *circuit.Machine, now time.Time) circuit.State {
	s, _ := m.Snapshot(now)
	return s
}

// tripOpen drives m from Closed to Open at time now.
func tripOpen(t *testing.T, m *circuit.Machine, now time.Time) {
	t.Helper()
	for range 4 {
		call(t, m, now, circuit.Failure)
	}
	if got := state(m, now); got != circuit.Open {
		t.Fatalf("state = %v, want open", got)
	}
}

func TestClosedStaysClosedBelowMinCalls(t *testing.T) {
	m := newMachine(2)
	for range 3 {
		call(t, m, t0, circuit.Failure)
	}
	if got := state(m, t0); got != circuit.Closed {
		t.Fatalf("state = %v, want closed (3 < MinCalls)", got)
	}
}

func TestClosedTripsAtMinCallsAndRate(t *testing.T) {
	m := newMachine(2)
	call(t, m, t0, circuit.Success)
	call(t, m, t0, circuit.Failure)
	call(t, m, t0, circuit.Failure)
	ts := call(t, m, t0, circuit.Failure) // 3/4 failures at MinCalls
	if len(ts) != 1 || ts[0] != (circuit.Transition{From: circuit.Closed, To: circuit.Open}) {
		t.Fatalf("transitions = %+v, want closed->open", ts)
	}
}

func TestSlowCallsTrip(t *testing.T) {
	m := newMachine(2)
	for range 2 {
		call(t, m, t0, circuit.Success)
	}
	call(t, m, t0, circuit.Slow)
	call(t, m, t0, circuit.Slow) // 2/4 slow >= 0.5
	if got := state(m, t0); got != circuit.Open {
		t.Fatalf("state = %v, want open", got)
	}
}

func TestIgnoredNeverCounts(t *testing.T) {
	m := newMachine(2)
	for range 10 {
		call(t, m, t0, circuit.Ignored)
	}
	if _, c := m.Snapshot(t0); c.Total != 0 {
		t.Fatalf("total = %d, want 0", c.Total)
	}
}

func TestOpenRejectsWithRetryAt(t *testing.T) {
	m := newMachine(2)
	tripOpen(t, m, t0)

	adm, _ := m.Admit(t0.Add(4 * time.Second))
	if adm.Reject != circuit.RejectedOpen || !adm.RetryAt.Equal(t0.Add(openTimeout)) {
		t.Fatalf("admission = %+v, want RejectedOpen with RetryAt %v", adm, t0.Add(openTimeout))
	}
}

func TestRetryAtIsNeverMoreThanOpenTimeoutAheadWhenClockGoesBackwards(t *testing.T) {
	m := newMachine(2)
	tripOpen(t, m, t0)

	now := t0.Add(-time.Hour)
	adm, _ := m.Admit(now)
	if adm.RetryAt.Before(now) || adm.RetryAt.After(now.Add(openTimeout)) {
		t.Fatalf("RetryAt = %v, want within [%v, %v]", adm.RetryAt, now, now.Add(openTimeout))
	}
}

func TestOpenBecomesHalfOpenAfterTimeout(t *testing.T) {
	m := newMachine(2)
	tripOpen(t, m, t0)

	adm, ts := m.Admit(t0.Add(openTimeout))
	if adm.Reject != circuit.Admitted {
		t.Fatalf("admission = %+v, want admitted probe", adm)
	}
	if len(ts) != 1 || ts[0].To != circuit.HalfOpen {
		t.Fatalf("transitions = %+v, want open->half-open", ts)
	}
}

func TestHalfOpenLimitsProbes(t *testing.T) {
	m := newMachine(2)
	tripOpen(t, m, t0)
	now := t0.Add(openTimeout)

	m.Admit(now)
	m.Admit(now)
	adm, _ := m.Admit(now)
	if adm.Reject != circuit.RejectedHalfOpenFull {
		t.Fatalf("third probe: %+v, want RejectedHalfOpenFull", adm)
	}
}

func TestHalfOpenClosesWhenProbesSucceed(t *testing.T) {
	m := newMachine(2)
	tripOpen(t, m, t0)
	now := t0.Add(openTimeout)

	call(t, m, now, circuit.Success)
	ts := call(t, m, now, circuit.Success)
	if len(ts) != 1 || ts[0].To != circuit.Closed {
		t.Fatalf("transitions = %+v, want half-open->closed", ts)
	}
	if _, c := m.Snapshot(now); c.Total != 0 {
		t.Fatalf("window not reset on close: %+v", c)
	}
}

func TestHalfOpenReopensWhenProbesUnhealthy(t *testing.T) {
	m := newMachine(2)
	tripOpen(t, m, t0)
	now := t0.Add(openTimeout)

	call(t, m, now, circuit.Success)
	ts := call(t, m, now, circuit.Failure) // 1/2 >= 0.5
	if len(ts) != 1 || ts[0].To != circuit.Open {
		t.Fatalf("transitions = %+v, want half-open->open", ts)
	}
}

func TestHalfOpenIgnoredProbeFreesSlot(t *testing.T) {
	m := newMachine(1)
	tripOpen(t, m, t0)
	now := t0.Add(openTimeout)

	adm, _ := m.Admit(now)
	m.Record(now, adm.Gen, circuit.Ignored)

	if again, _ := m.Admit(now); again.Reject != circuit.Admitted {
		t.Fatalf("slot not freed: %+v", again)
	}
}

func TestHungProbeReopensAndLateResultIsDropped(t *testing.T) {
	m := newMachine(1)
	tripOpen(t, m, t0)
	now := t0.Add(openTimeout)
	hung, _ := m.Admit(now)

	later := now.Add(probeTimeout)
	adm, ts := m.Admit(later)
	if adm.Reject != circuit.RejectedOpen || len(ts) != 1 || ts[0].To != circuit.Open {
		t.Fatalf("got %+v %+v, want re-opened and rejected", adm, ts)
	}

	if late := m.Record(later, hung.Gen, circuit.Success); late != nil {
		t.Fatalf("stale result caused transition %+v", late)
	}
	if got := state(m, later); got != circuit.Open {
		t.Fatalf("state = %v, want open", got)
	}
}

func TestStaleGenerationIsDropped(t *testing.T) {
	m := newMachine(2)
	old, _ := m.Admit(t0) // admitted while Closed
	tripOpen(t, m, t0)

	if ts := m.Record(t0, old.Gen, circuit.Success); ts != nil {
		t.Fatalf("stale result caused transition %+v", ts)
	}
}

func TestResetClosesAndDropsInFlight(t *testing.T) {
	m := newMachine(2)
	inflight, _ := m.Admit(t0)
	tripOpen(t, m, t0)

	ts := m.Reset(t0)
	if len(ts) != 1 || ts[0] != (circuit.Transition{From: circuit.Open, To: circuit.Closed}) {
		t.Fatalf("transitions = %+v, want open->closed", ts)
	}
	m.Record(t0, inflight.Gen, circuit.Failure)
	if _, c := m.Snapshot(t0); c.Total != 0 {
		t.Fatalf("in-flight result counted after reset: %+v", c)
	}
	if ts := m.Reset(t0); ts != nil {
		t.Fatalf("reset of closed machine reported %+v", ts)
	}
}

func TestHungProbeIsReclaimedWhenClockGoesBackwards(t *testing.T) {
	m := newMachine(1)
	tripOpen(t, m, t0)
	now := t0.Add(openTimeout)
	m.Admit(now) // probe that never reports back

	adm, ts := m.Admit(now.Add(-time.Hour))
	if adm.Reject != circuit.RejectedOpen || len(ts) != 1 || ts[0].To != circuit.Open {
		t.Fatalf("got %+v %+v, want hung probe reclaimed and circuit re-opened", adm, ts)
	}
}

func TestOpenDoesNotStickWhenClockGoesBackwards(t *testing.T) {
	m := newMachine(1)
	tripOpen(t, m, t0)

	back := t0.Add(-time.Hour)
	adm, _ := m.Admit(back)
	if adm.Reject != circuit.RejectedOpen || !adm.RetryAt.Equal(back.Add(openTimeout)) {
		t.Fatalf("admission = %+v, want RejectedOpen with a fresh full wait", adm)
	}
	if adm, _ := m.Admit(back.Add(openTimeout)); adm.Reject != circuit.Admitted {
		t.Fatalf("admission = %+v, want probe admitted OpenTimeout after the step back", adm)
	}
}

func TestHalfOpenReopensEarlyWhenRecoveryIsImpossible(t *testing.T) {
	m := newMachine(4)
	tripOpen(t, m, t0)
	now := t0.Add(openTimeout)
	var probes [4]circuit.Admission
	for i := range probes {
		probes[i], _ = m.Admit(now)
	}

	if ts := m.Record(now, probes[0].Gen, circuit.Failure); ts != nil {
		t.Fatalf("1 failure of 4 can still recover, got %+v", ts)
	}
	ts := m.Record(now, probes[1].Gen, circuit.Failure) // 2/4 >= 0.5 even if the rest succeed
	if len(ts) != 1 || ts[0] != (circuit.Transition{From: circuit.HalfOpen, To: circuit.Open}) {
		t.Fatalf("transitions = %+v, want half-open->open while 2 probes are pending", ts)
	}
	if late := m.Record(now, probes[2].Gen, circuit.Success); late != nil {
		t.Fatalf("late probe result caused %+v", late)
	}
}

func newConsecutiveMachine() *circuit.Machine {
	return circuit.NewMachine(circuit.MachineConfig{
		Policy:           circuit.Policy{MinCalls: 100, FailureRate: 1, SlowRate: 1, ConsecutiveFailures: 3},
		OpenTimeout:      openTimeout,
		HalfOpenMaxCalls: 1,
		ProbeTimeout:     probeTimeout,
	}, circuit.NewCountWindow(100))
}

func TestConsecutiveFailuresTripBelowMinCalls(t *testing.T) {
	tests := []struct {
		name     string
		outcomes []circuit.Outcome
		want     circuit.State
	}{
		{"two in a row", []circuit.Outcome{circuit.Failure, circuit.Failure}, circuit.Closed},
		{"three in a row", []circuit.Outcome{circuit.Failure, circuit.Failure, circuit.Failure}, circuit.Open},
		{"success resets", []circuit.Outcome{circuit.Failure, circuit.Failure, circuit.Success, circuit.Failure}, circuit.Closed},
		{"slow resets", []circuit.Outcome{circuit.Failure, circuit.Failure, circuit.Slow, circuit.Failure}, circuit.Closed},
		{"ignored does not reset", []circuit.Outcome{circuit.Failure, circuit.Failure, circuit.Ignored, circuit.Failure}, circuit.Open},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newConsecutiveMachine()
			for _, o := range tt.outcomes {
				call(t, m, t0, o)
			}
			if got := state(m, t0); got != tt.want {
				t.Fatalf("state = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConsecutiveCounterRestartsAfterReset(t *testing.T) {
	m := newConsecutiveMachine()
	call(t, m, t0, circuit.Failure)
	call(t, m, t0, circuit.Failure)
	m.Reset(t0)
	call(t, m, t0, circuit.Failure)
	call(t, m, t0, circuit.Failure)
	if got := state(m, t0); got != circuit.Closed {
		t.Fatalf("state = %v, want closed: failures before Reset must not count", got)
	}
}

func TestForceOpenRejectsWithoutRetryTimeAndNeverRecovers(t *testing.T) {
	m := newMachine(2)
	ts := m.SetMode(t0, circuit.ModeForcedOpen)
	if len(ts) != 1 || ts[0] != (circuit.Transition{From: circuit.Closed, To: circuit.Open}) {
		t.Fatalf("transitions = %+v, want closed->open", ts)
	}

	adm, ts := m.Admit(t0.Add(100 * openTimeout))
	if adm.Reject != circuit.RejectedOpen || !adm.RetryAt.IsZero() || ts != nil {
		t.Fatalf("got %+v %+v, want rejected with no retry time and no transition", adm, ts)
	}
	if got := state(m, t0); got != circuit.Open || m.Mode() != circuit.ModeForcedOpen {
		t.Fatalf("state = %v, mode = %v, want open, forced-open", got, m.Mode())
	}
}

func TestForceOpenWhenAlreadyOpenReportsNoTransition(t *testing.T) {
	m := newMachine(2)
	tripOpen(t, m, t0)
	if ts := m.SetMode(t0, circuit.ModeForcedOpen); ts != nil {
		t.Fatalf("transitions = %+v, want none", ts)
	}
}

func TestDisabledAdmitsEverythingAndRecordsNothing(t *testing.T) {
	m := newMachine(2)
	m.SetMode(t0, circuit.ModeDisabled)
	for range 20 {
		call(t, m, t0, circuit.Failure)
	}
	if s, c := m.Snapshot(t0); s != circuit.Closed || c.Total != 0 {
		t.Fatalf("state = %v, counts = %+v, want closed and nothing recorded", s, c)
	}
}

func TestResetEndsForcedModes(t *testing.T) {
	for _, mode := range []circuit.Mode{circuit.ModeForcedOpen, circuit.ModeDisabled} {
		t.Run(mode.String(), func(t *testing.T) {
			m := newMachine(2)
			m.SetMode(t0, mode)
			m.Reset(t0)
			if m.Mode() != circuit.ModeNormal || state(m, t0) != circuit.Closed {
				t.Fatalf("mode = %v, state = %v, want normal, closed", m.Mode(), state(m, t0))
			}
			call(t, m, t0, circuit.Success)
		})
	}
}

func TestModeChangeDropsInFlightResults(t *testing.T) {
	m := newMachine(2)
	inflight, _ := m.Admit(t0)
	m.SetMode(t0, circuit.ModeDisabled)
	m.SetMode(t0, circuit.ModeNormal)

	m.Record(t0, inflight.Gen, circuit.Failure)
	if _, c := m.Snapshot(t0); c.Total != 0 {
		t.Fatalf("in-flight result counted after mode change: %+v", c)
	}
}
