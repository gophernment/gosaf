package circuit_test

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/gophernment/gosaf/internal/circuit"
)

// These tests compare the real code with deliberately naive oracles (plain
// slices, linear scans, exact arithmetic) and check invariants over random
// operation sequences. They are written from the spec, not from the code.

func newRand(seed int) *rand.Rand { return rand.New(rand.NewPCG(uint64(seed), 0x9e3779b9)) }

func pickOutcome(r *rand.Rand) circuit.Outcome {
	return []circuit.Outcome{circuit.Success, circuit.Success, circuit.Failure, circuit.Slow}[r.IntN(4)]
}

// --- naive windows ---------------------------------------------------------

type naiveCount struct {
	n    int
	outs []circuit.Outcome
}

func (w *naiveCount) Record(_ time.Time, o circuit.Outcome) {
	w.outs = append(w.outs, o)
	if len(w.outs) > w.n {
		w.outs = w.outs[len(w.outs)-w.n:]
	}
}

func (w *naiveCount) Counts(time.Time) circuit.Counts { return tally(w.outs) }
func (w *naiveCount) Reset()                          { w.outs = nil }

type naiveRec struct {
	idx int64
	o   circuit.Outcome
}

type naiveTime struct {
	n      int
	width  time.Duration
	origin time.Time
	latest int64
	recs   []naiveRec
}

func (w *naiveTime) index(now time.Time) int64 {
	idx := int64(now.Sub(w.origin) / w.width)
	if idx < w.latest {
		idx = w.latest
	}
	w.latest = idx
	return idx
}

func (w *naiveTime) Record(now time.Time, o circuit.Outcome) {
	w.recs = append(w.recs, naiveRec{w.index(now), o})
}

func (w *naiveTime) Counts(now time.Time) circuit.Counts {
	cur := w.index(now)
	var outs []circuit.Outcome
	for _, r := range w.recs {
		if r.idx > cur-int64(w.n) && r.idx <= cur {
			outs = append(outs, r.o)
		}
	}
	return tally(outs)
}

func (w *naiveTime) Reset() { w.recs = nil }

func tally(outs []circuit.Outcome) circuit.Counts {
	var c circuit.Counts
	for _, o := range outs {
		c.Total++
		switch o {
		case circuit.Failure:
			c.Failures++
		case circuit.Slow:
			c.Slow++
		}
	}
	return c
}

func TestWindowsMatchNaiveModels(t *testing.T) {
	for seed := range 300 {
		r := newRand(seed)
		n := 1 + r.IntN(8)
		period := time.Duration(n) * time.Second
		real := map[string]circuit.Window{
			"count": circuit.NewCountWindow(n),
			"time":  circuit.NewTimeWindow(n, period, t0),
		}
		naive := map[string]interface {
			Record(time.Time, circuit.Outcome)
			Counts(time.Time) circuit.Counts
			Reset()
		}{
			"count": &naiveCount{n: n},
			"time":  &naiveTime{n: n, width: time.Second, origin: t0},
		}

		now := t0
		for step := range 300 {
			switch r.IntN(10) {
			case 0:
				now = now.Add(-time.Duration(r.IntN(5)) * time.Second) // clock steps back
			case 1, 2, 3:
				now = now.Add(time.Duration(r.IntN(3000)) * time.Millisecond)
			}
			switch r.IntN(12) {
			case 0:
				for k := range real {
					real[k].Reset()
					naive[k].Reset()
				}
			case 1, 2:
				for k := range real {
					if got, want := real[k].Counts(now), naive[k].Counts(now); got != want {
						t.Fatalf("seed %d step %d %s window: Counts = %+v, naive = %+v", seed, step, k, got, want)
					}
				}
			default:
				o := pickOutcome(r)
				for k := range real {
					real[k].Record(now, o)
					naive[k].Record(now, o)
				}
			}
		}
	}
}

// --- Closed -> Open trip rule ------------------------------------------------

func TestClosedTripMatchesNaivePolicy(t *testing.T) {
	type rate struct {
		v        float64
		num, den int
	}
	rates := []rate{{0.1, 1, 10}, {0.25, 1, 4}, {0.5, 1, 2}, {0.75, 3, 4}, {1, 1, 1}}

	for seed := range 400 {
		r := newRand(seed)
		size := 1 + r.IntN(12)
		minCalls := 1 + r.IntN(size)
		fr, sr := rates[r.IntN(len(rates))], rates[r.IntN(len(rates))]
		consecutive := r.IntN(5) // 0 disables
		m := circuit.NewMachine(circuit.MachineConfig{
			Policy: circuit.Policy{
				MinCalls: minCalls, FailureRate: fr.v, SlowRate: sr.v, ConsecutiveFailures: consecutive,
			},
			OpenTimeout: openTimeout, HalfOpenMaxCalls: 1, ProbeTimeout: probeTimeout,
		}, circuit.NewCountWindow(size))

		var hist []circuit.Outcome
		consec := 0
		for step := range 200 {
			o := []circuit.Outcome{circuit.Success, circuit.Failure, circuit.Failure, circuit.Slow, circuit.Ignored}[r.IntN(5)]

			want := false
			if o != circuit.Ignored {
				hist = append(hist, o)
				if o == circuit.Failure {
					consec++
				} else {
					consec = 0
				}
				win := hist
				if len(win) > size {
					win = win[len(win)-size:]
				}
				c := tally(win)
				// exact integer comparison: failures/total >= num/den
				unhealthy := c.Failures*fr.den >= fr.num*c.Total || c.Slow*sr.den >= sr.num*c.Total
				want = (c.Total >= minCalls && unhealthy) || (consecutive > 0 && consec >= consecutive)
			}

			ts := call(t, m, t0, o)
			got := len(ts) == 1 && ts[0].To == circuit.Open
			if got != want {
				t.Fatalf("seed %d step %d (size %d min %d fr %v sr %v consec %d): tripped = %v, naive = %v; history %v",
					seed, step, size, minCalls, fr.v, sr.v, consecutive, got, want, hist)
			}
			if got {
				break
			}
		}
	}
}

// --- Half-Open decision --------------------------------------------------------

func TestHalfOpenDecisionMatchesNaiveRule(t *testing.T) {
	for seed := range 400 {
		r := newRand(seed)
		probes := 1 + r.IntN(6)
		fr := []float64{0.25, 0.5, 0.75, 1}[r.IntN(4)]
		sr := []float64{0.25, 0.5, 0.75, 1}[r.IntN(4)]
		m := circuit.NewMachine(circuit.MachineConfig{
			Policy:      circuit.Policy{MinCalls: 1, FailureRate: fr, SlowRate: sr},
			OpenTimeout: openTimeout, HalfOpenMaxCalls: probes, ProbeTimeout: probeTimeout,
		}, circuit.NewCountWindow(4))
		// MinCalls is 1, so one failure trips the circuit
		if ts := call(t, m, t0, circuit.Failure); len(ts) != 1 {
			t.Fatalf("seed %d: setup failure did not trip (fr %v)", seed, fr)
		}

		now := t0.Add(openTimeout)
		tickets := make([]circuit.Admission, probes)
		for i := range tickets {
			tickets[i], _ = m.Admit(now)
			if tickets[i].Reject != circuit.Admitted {
				t.Fatalf("seed %d: probe %d not admitted: %+v", seed, i, tickets[i])
			}
		}

		fails, slows := 0, 0
		decided := false
		for k, idx := range r.Perm(probes) {
			o := pickOutcome(r)
			ts := m.Record(now, tickets[idx].Gen, o)
			if decided {
				if ts != nil {
					t.Fatalf("seed %d: result after the decision caused %+v", seed, ts)
				}
				continue
			}
			switch o {
			case circuit.Failure:
				fails++
			case circuit.Slow:
				slows++
			}
			// best case: every probe still to finish succeeds
			doomed := float64(fails)/float64(probes) >= fr || float64(slows)/float64(probes) >= sr
			var want circuit.State = -1
			switch {
			case doomed:
				want = circuit.Open
			case k+1 == probes:
				want = circuit.Closed
			}
			switch {
			case want == -1 && ts != nil:
				t.Fatalf("seed %d result %d: transition %+v, naive rule says keep waiting", seed, k, ts)
			case want != -1 && (len(ts) != 1 || ts[0].To != want):
				t.Fatalf("seed %d result %d: transitions %+v, naive rule says %v", seed, k, ts, want)
			}
			decided = want != -1
		}
	}
}

// --- random operation sequences ---------------------------------------------------

const (
	opAdmit = iota
	opRecordOldest
	opRecordNewest
	opAdvance
	opBack
	opReset
	opMode
	opSnapshot
)

// runOps decodes ops into Machine calls and checks invariants after each one.
// cfgSel picks the window kind, probe count and consecutive-failure rule.
func runOps(t *testing.T, cfgSel uint8, ops []byte) {
	t.Helper()
	probes := []int{1, 2, 4}[int(cfgSel)%3]
	timeWin := cfgSel&4 != 0
	consecutive := 0
	if cfgSel&8 != 0 {
		consecutive = 3
	}
	const size = 6
	var win circuit.Window = circuit.NewCountWindow(size)
	if timeWin {
		win = circuit.NewTimeWindow(size, 6*time.Second, t0)
	}
	m := circuit.NewMachine(circuit.MachineConfig{
		Policy:           circuit.Policy{MinCalls: 3, FailureRate: 0.5, SlowRate: 0.5, ConsecutiveFailures: consecutive},
		OpenTimeout:      openTimeout,
		HalfOpenMaxCalls: probes,
		ProbeTimeout:     probeTimeout,
	}, win)

	now := t0
	var outstanding []circuit.Admission
	steps := []time.Duration{0, time.Second, probeTimeout, openTimeout, 3 * openTimeout}

	for i, b := range ops {
		prev, _ := m.Snapshot(now)
		arg := int(b >> 3)
		var ts []circuit.Transition

		switch int(b) % 8 {
		case opAdmit:
			adm, got := m.Admit(now)
			ts = got
			switch adm.Reject {
			case circuit.Admitted:
				outstanding = append(outstanding, adm)
			case circuit.RejectedOpen:
				if m.Mode() == circuit.ModeNormal && (adm.RetryAt.Before(now) || adm.RetryAt.After(now.Add(openTimeout))) {
					t.Fatalf("op %d: open RetryAt %v outside [%v, %v]", i, adm.RetryAt, now, now.Add(openTimeout))
				}
			case circuit.RejectedHalfOpenFull:
				if adm.RetryAt.Before(now) || adm.RetryAt.After(now.Add(probeTimeout)) {
					t.Fatalf("op %d: half-open RetryAt %v outside [%v, %v]", i, adm.RetryAt, now, now.Add(probeTimeout))
				}
			}
			if m.Mode() == circuit.ModeDisabled && (adm.Reject != circuit.Admitted || ts != nil) {
				t.Fatalf("op %d: disabled machine must admit everything without transitions", i)
			}
		case opRecordOldest, opRecordNewest:
			if len(outstanding) == 0 {
				continue
			}
			k := 0
			if int(b)%8 == opRecordNewest {
				k = len(outstanding) - 1
			}
			o := []circuit.Outcome{circuit.Success, circuit.Failure, circuit.Slow, circuit.Ignored}[arg%4]
			ts = m.Record(now, outstanding[k].Gen, o)
			outstanding = append(outstanding[:k], outstanding[k+1:]...)
		case opAdvance:
			now = now.Add(steps[arg%len(steps)])
		case opBack:
			now = now.Add(-steps[arg%len(steps)] - time.Second)
		case opReset:
			if arg%8 != 0 { // rare, so sequences usually reach Half-Open
				continue
			}
			ts = m.Reset(now)
		case opMode:
			if arg%12 != 0 { // rare: forced modes would otherwise hide everything else
				continue
			}
			ts = m.SetMode(now, []circuit.Mode{circuit.ModeForcedOpen, circuit.ModeDisabled}[(arg/12)%2])
		case opSnapshot:
		}

		// transitions form a chain from the previous state to the current one
		cur, counts := m.Snapshot(now)
		from := prev
		for _, tr := range ts {
			if tr.From != from || tr.From == tr.To {
				t.Fatalf("op %d: bad transition chain %+v (previous state %v)", i, ts, prev)
			}
			from = tr.To
		}
		if from != cur {
			t.Fatalf("op %d: transitions %+v end in %v but state is %v", i, ts, from, cur)
		}
		if !timeWin && counts.Total > size {
			t.Fatalf("op %d: window holds %d calls, bound is %d", i, counts.Total, size)
		}
		if counts.Failures > counts.Total || counts.Slow > counts.Total || counts.Total < 0 {
			t.Fatalf("op %d: inconsistent counts %+v", i, counts)
		}
	}

	// liveness: unless forced open, the machine admits a call again within a few
	// timeout periods, whatever happened before.
	if m.Mode() == circuit.ModeForcedOpen {
		adm, _ := m.Admit(now.Add(100 * openTimeout))
		if adm.Reject != circuit.RejectedOpen {
			t.Fatalf("forced-open machine admitted a call: %+v", adm)
		}
		return
	}
	admitted := false
	for range 3 {
		now = now.Add(openTimeout + probeTimeout)
		if adm, _ := m.Admit(now); adm.Reject == circuit.Admitted {
			admitted = true
			break
		}
	}
	if !admitted {
		t.Fatalf("machine stuck: no call admitted after 3 timeout periods (ops %v)", ops)
	}
}

func TestRandomOperationsKeepInvariants(t *testing.T) {
	for seed := range 500 {
		r := newRand(seed)
		ops := make([]byte, 20+r.IntN(80))
		for i := range ops {
			ops[i] = byte(r.IntN(256))
		}
		runOps(t, uint8(seed), ops)
	}
}

func FuzzMachine(f *testing.F) {
	f.Add(uint8(0), []byte{0, 1, 1, 0, 1, 3, 0, 9, 0})
	f.Add(uint8(6), []byte{0, 0, 0, 9, 17, 0, 3, 0, 4, 0})
	f.Add(uint8(9), []byte{6, 0, 5, 14, 0, 0})
	f.Fuzz(func(t *testing.T, cfgSel uint8, ops []byte) {
		if len(ops) > 400 {
			ops = ops[:400]
		}
		runOps(t, cfgSel, ops)
	})
}
