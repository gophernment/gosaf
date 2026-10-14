# GoSaf Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the GoSaf v1 core: a dependency-free Go circuit breaker with sliding windows, failure-rate and slow-call tripping, context support, panic safety and a safe Half-Open state.

**Architecture:** Onion + hexagonal. `internal/circuit` is the pure domain (state machine, windows, policy; time is passed in). `internal/guard` is the use case (mutex, permits, outcome classification, listener delivery outside the lock). The root package `gosaf` is the facade: validated `Config`, `Breaker`, generic `Do[T]`, re-exported types.

**Tech Stack:** Go 1.27.2, standard library only. Dev tools (staticcheck, govulncheck) live in a separate `tools/` module so the root `go.mod` has zero dependencies.

**Spec:** `docs/superpowers/specs/2026-10-14-gosaf-core-design.md`

## Global Constraints

- Go `1.27.2` in `go.mod` (latest patch at plan time); module path `github.com/gophernment/gosaf`.
- Standard library only in the root module. No `unsafe`, `reflect`, `init()` side effects, or global mutable state.
- No lock held while running user code (`fn`, `IsFailure`, `Listener`).
- Invalid config fails closed with an error wrapping `ErrInvalidConfig`; no silent defaults.
- Memory bounded: fixed-size windows, `WindowSize <= MaxWindowSize` (100000).
- Errors and events never carry caller payloads, error text or secrets.
- Tests use a fake clock; no `time.Sleep`; hand-written fakes; always run with `-race`.
- Every exported identifier has a doc comment; `gofmt -l .` prints nothing.
- GitHub Actions pinned by commit SHA.

## Review Focus

Failure modes the spec implies that most need a pinned test, most likely first:

1. **Probe never reports back** (fn panics, calls `runtime.Goexit` such as `t.FailNow`, or never returns): Half-Open must not stick. Tests: Task 4 `TestHungProbeIsReclaimed`, Task 5 `TestDoRecordsPanicAndReraises`, `TestDoFreesProbeSlotOnGoexit`.
2. **Caller cancels a probe** (`context.Canceled`): must free the probe slot and not count as a failure, while `DeadlineExceeded` from a slow dependency does count. Tests: Task 3 `TestHalfOpenIgnoredProbeFreesSlot`, Task 4 `TestIgnoredProbeFreesSlot`, `TestCanceledIsIgnoredButDeadlineIsFailure`.
3. **Wall clock steps backwards** (NTP, VM resume): `RetryAfter` stays in `[0, OpenTimeout]`, time windows never rewind. Tests: Task 2 `TestTimeWindowIgnoresClockGoingBackwards`, Task 3 `TestRetryAfterStaysInRangeWhenClockGoesBackwards`.
4. **Misuse**: nil/zero `Breaker`, nil `Permit`, nil context, zero `Config`, absurd `WindowSize`/durations. Must return an error or no-op, never nil-deref or allocate unbounded memory. Tests: Task 4 `TestNilPermitDoneIsSafe`, `TestAllowNilContextFails`; Task 5 `TestZeroValueBreakerFailsClosed`, `TestZeroConfigIsRejected`, `FuzzNew`.
5. **Listener re-enters the breaker or panics**: no deadlock, no crash, breaker state unaffected. Tests: Task 4 `TestListenerMayReenterBreaker`, `TestListenerPanicIsContained`.

---

## File Structure

```
go.mod                          module github.com/gophernment/gosaf, go 1.27.2, no requires
config.go, config_test.go       Config, DefaultConfig, validation, ErrInvalidConfig, MaxWindowSize
gosaf.go, gosaf_test.go         facade: Breaker, New, Do[T], re-exported types/errors
example_test.go                 runnable Example
internal/circuit/               DOMAIN (pure)
  state.go                      State, Outcome, Counts
  window.go                     Window interface, count + time sliding windows
  policy.go                     Policy: when rates mean "unhealthy"
  machine.go                    Machine: Admit / Record / Reset / Snapshot
internal/guard/                 USECASE
  errors.go                     ErrOpen, ErrHalfOpenFull, RejectedError
  guard.go                      Guard, Permit, Listener, Options, Snapshot
tools/go.mod                    separate module pinning staticcheck + govulncheck
.github/workflows/ci.yml        CI
.github/dependabot.yml          weekly updates (gomod root, gomod tools, actions)
SECURITY.md                     vulnerability reporting
```

Dependency direction: `gosaf -> internal/guard -> internal/circuit`. `circuit` imports only `time`.

---

### Task 1: Project scaffolding, tooling, CI

**Files:**
- Create: `go.mod`, `tools/go.mod`, `tools/go.sum`, `.gitignore`, `SECURITY.md`, `.github/workflows/ci.yml`, `.github/dependabot.yml`

**Interfaces:**
- Produces: the check commands every later task uses:
  - `go tool -modfile=tools/go.mod staticcheck ./...`
  - `go tool -modfile=tools/go.mod govulncheck ./...`

- [ ] **Step 1: Initialise the root module**

Run:
```bash
cd /Users/pallat/workspace/github.com/gophernment/gosaf
go mod init github.com/gophernment/gosaf
cat go.mod
```
Expected: `go.mod` contains `module github.com/gophernment/gosaf` and `go 1.27.2` (or a newer 1.27 patch if `go version` is newer). No `require` lines.

- [ ] **Step 2: Create the tools module pinned to the latest versions**

Run:
```bash
mkdir tools && cd tools
go mod init github.com/gophernment/gosaf/tools
go get -tool honnef.co/go/tools/cmd/staticcheck@latest golang.org/x/vuln/cmd/govulncheck@latest
cd ..
go tool -modfile=tools/go.mod staticcheck -version
go tool -modfile=tools/go.mod govulncheck -version
```
Expected: both print a version. `tools/go.mod` has a `tool (...)` block listing both. Root `go.mod` is unchanged.

- [ ] **Step 3: Write `.gitignore`**

```gitignore
*.test
*.out
coverage.*
.DS_Store
```
(Do not ignore `testdata/`; fuzz regression inputs must be committed.)

- [ ] **Step 4: Write `SECURITY.md`**

```markdown
# Security Policy

## Reporting a vulnerability

Please do not open a public issue. Use GitHub's private vulnerability reporting:
the repository's **Security** tab, then **Report a vulnerability**.

Include the affected version, a description, and a minimal reproduction if you have one.

## Supported versions

Only the latest release receives security fixes.

## Supply chain

GoSaf has no runtime dependencies. CI runs `govulncheck`, `staticcheck`, `go vet` and
`go test -race` on every change, and GitHub Actions are pinned by commit SHA.
```

- [ ] **Step 5: Write `.github/workflows/ci.yml`**

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: go.mod
      - name: gofmt
        run: test -z "$(gofmt -l .)"
      - run: go vet ./...
      - run: go tool -modfile=tools/go.mod staticcheck ./...
      - run: go tool -modfile=tools/go.mod govulncheck ./...
      - run: go mod verify
      - run: go test -race -shuffle=on -count=1 ./...
      - name: fuzz smoke
        run: go test -run='^$' -fuzz=FuzzNew -fuzztime=10s .
```

- [ ] **Step 6: Write `.github/dependabot.yml`**

```yaml
version: 2
updates:
  - package-ecosystem: gomod
    directory: /
    schedule:
      interval: weekly
  - package-ecosystem: gomod
    directory: /tools
    schedule:
      interval: weekly
  - package-ecosystem: github-actions
    directory: /
    schedule:
      interval: weekly
```

- [ ] **Step 7: Verify and commit**

Run: `cat go.mod && go mod verify`
Expected: no `require` in `go.mod`; `go mod verify` prints `all modules verified`.

```bash
git add go.mod tools .gitignore SECURITY.md .github
git commit -m "chore: add module, pinned dev tools, CI and security policy

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

Manual follow-up (cannot be done from code): in the GitHub repo settings enable **Private vulnerability reporting** and **Dependabot alerts**.

---

### Task 2: Domain types and sliding windows

**Files:**
- Create: `internal/circuit/state.go`, `internal/circuit/window.go`
- Test: `internal/circuit/window_test.go`

**Interfaces:**
- Produces (used by Tasks 3-5):
  - `type State int` with `Closed, Open, HalfOpen`; `String()`
  - `type Outcome int` with `Success, Failure, Slow, Ignored`; `String()`
  - `type Counts struct{ Total, Failures, Slow int }`
  - `type Window interface { Record(now time.Time, o Outcome); Counts(now time.Time) Counts; Reset() }`
  - `func NewCountWindow(n int) Window`
  - `func NewTimeWindow(n int, period time.Duration, origin time.Time) Window`
- `window_test.go` declares the package-level `var t0 = time.Unix(1_000_000, 0)` that Task 3's tests reuse.

- [ ] **Step 1: Write the failing test** — `internal/circuit/window_test.go`

```go
package circuit_test

import (
	"testing"
	"time"

	"github.com/gophernment/gosaf/internal/circuit"
)

var t0 = time.Unix(1_000_000, 0)

func TestCountWindowSlides(t *testing.T) {
	w := circuit.NewCountWindow(3)
	for _, o := range []circuit.Outcome{circuit.Failure, circuit.Success, circuit.Slow} {
		w.Record(t0, o)
	}
	if got, want := w.Counts(t0), (circuit.Counts{Total: 3, Failures: 1, Slow: 1}); got != want {
		t.Fatalf("before wrap: got %+v, want %+v", got, want)
	}

	w.Record(t0, circuit.Success) // overwrites the Failure
	if got, want := w.Counts(t0), (circuit.Counts{Total: 3, Slow: 1}); got != want {
		t.Fatalf("after wrap: got %+v, want %+v", got, want)
	}

	w.Reset()
	if got := w.Counts(t0); got != (circuit.Counts{}) {
		t.Fatalf("after reset: got %+v, want zero", got)
	}
}

func TestTimeWindowExpiresOldBuckets(t *testing.T) {
	w := circuit.NewTimeWindow(4, 4*time.Second, t0) // 1s buckets
	w.Record(t0, circuit.Failure)

	if got := w.Counts(t0.Add(3 * time.Second)).Total; got != 1 {
		t.Fatalf("still inside window: got total %d, want 1", got)
	}
	if got := w.Counts(t0.Add(4 * time.Second)).Total; got != 0 {
		t.Fatalf("expired: got total %d, want 0", got)
	}
}

func TestTimeWindowReusesBucketSlot(t *testing.T) {
	w := circuit.NewTimeWindow(2, 2*time.Second, t0)
	w.Record(t0, circuit.Failure)
	w.Record(t0.Add(2*time.Second), circuit.Success) // same slot, newer bucket

	got := w.Counts(t0.Add(2 * time.Second))
	if want := (circuit.Counts{Total: 1}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestTimeWindowIgnoresClockGoingBackwards(t *testing.T) {
	w := circuit.NewTimeWindow(4, 4*time.Second, t0)
	w.Record(t0.Add(10*time.Second), circuit.Failure)
	w.Record(t0.Add(2*time.Second), circuit.Failure) // clock stepped back

	got := w.Counts(t0.Add(10 * time.Second))
	if got.Total != 2 {
		t.Fatalf("got total %d, want 2 (late record lands in newest bucket)", got.Total)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/circuit/`
Expected: FAIL, build error (package has no non-test files / `undefined: circuit.NewCountWindow`).

- [ ] **Step 3: Write `internal/circuit/state.go`**

```go
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
```

- [ ] **Step 4: Write `internal/circuit/window.go`**

```go
package circuit

import "time"

// Window keeps a bounded, sliding record of recent outcomes.
// It is not safe for concurrent use; the caller serialises access.
type Window interface {
	Record(now time.Time, o Outcome)
	Counts(now time.Time) Counts
	Reset()
}

type countWindow struct {
	ring []Outcome
	next int
	size int
	c    Counts
}

// NewCountWindow returns a window over the last n recorded calls.
func NewCountWindow(n int) Window {
	return &countWindow{ring: make([]Outcome, max(n, 1))}
}

func (w *countWindow) Record(_ time.Time, o Outcome) {
	if w.size == len(w.ring) {
		w.c.sub(w.ring[w.next])
	} else {
		w.size++
	}
	w.ring[w.next] = o
	w.c.add(o)
	w.next = (w.next + 1) % len(w.ring)
}

func (w *countWindow) Counts(time.Time) Counts { return w.c }

func (w *countWindow) Reset() {
	clear(w.ring)
	w.next, w.size, w.c = 0, 0, Counts{}
}

type bucket struct {
	idx  int64
	used bool
	c    Counts
}

type timeWindow struct {
	buckets []bucket
	width   time.Duration
	origin  time.Time
	latest  int64
}

// NewTimeWindow returns a window spanning period, split into n buckets.
// origin is the time bucket numbering starts from.
func NewTimeWindow(n int, period time.Duration, origin time.Time) Window {
	n = max(n, 1)
	return &timeWindow{
		buckets: make([]bucket, n),
		width:   max(period/time.Duration(n), 1),
		origin:  origin,
	}
}

// index maps now to a bucket number. It never moves backwards, so a wall-clock
// step back cannot overwrite newer buckets.
func (w *timeWindow) index(now time.Time) int64 {
	idx := int64(now.Sub(w.origin) / w.width)
	if idx < w.latest {
		idx = w.latest
	}
	w.latest = idx
	return idx
}

func (w *timeWindow) Record(now time.Time, o Outcome) {
	idx := w.index(now)
	b := &w.buckets[idx%int64(len(w.buckets))]
	if !b.used || b.idx != idx {
		*b = bucket{idx: idx, used: true}
	}
	b.c.add(o)
}

func (w *timeWindow) Counts(now time.Time) Counts {
	cur := w.index(now)
	oldest := cur - int64(len(w.buckets))
	var c Counts
	for i := range w.buckets {
		b := &w.buckets[i]
		if b.used && b.idx > oldest && b.idx <= cur {
			c.Total += b.c.Total
			c.Failures += b.c.Failures
			c.Slow += b.c.Slow
		}
	}
	return c
}

func (w *timeWindow) Reset() { clear(w.buckets) }
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go vet ./... && go test -race -shuffle=on ./internal/circuit/`
Expected: `ok  github.com/gophernment/gosaf/internal/circuit`

- [ ] **Step 6: Commit**

```bash
gofmt -l .   # must print nothing
git add internal/circuit
git commit -m "feat(circuit): add state types and sliding windows

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Trip policy and state machine

**Files:**
- Create: `internal/circuit/policy.go`, `internal/circuit/machine.go`
- Test: `internal/circuit/policy_test.go`, `internal/circuit/machine_test.go`

**Interfaces:**
- Consumes (Task 2): `State`, `Outcome`, `Counts`, `Window`, `NewCountWindow`, and `t0` from `window_test.go`.
- Produces (used by Task 4):
  - `type Policy struct{ MinCalls int; FailureRate, SlowRate float64 }` with `Unhealthy(Counts) bool`, `Tripped(Counts) bool`
  - `type MachineConfig struct{ Policy Policy; OpenTimeout time.Duration; HalfOpenMaxCalls int; ProbeTimeout time.Duration }`
  - `type Transition struct{ From, To State }`
  - `type Reject int` with `Admitted, RejectedOpen, RejectedHalfOpenFull`
  - `type Admission struct{ Gen uint64; Reject Reject; RetryAfter time.Duration }`
  - `func NewMachine(MachineConfig, Window) *Machine`
  - `(*Machine).Admit(now time.Time) (Admission, []Transition)`
  - `(*Machine).Record(now time.Time, gen uint64, o Outcome) []Transition` (`Ignored` frees a Half-Open slot; stale `gen` dropped)
  - `(*Machine).Reset(now time.Time) []Transition`
  - `(*Machine).Snapshot(now time.Time) (State, Counts)`

- [ ] **Step 1: Write the failing tests** — `internal/circuit/policy_test.go`

```go
package circuit_test

import (
	"testing"

	"github.com/gophernment/gosaf/internal/circuit"
)

func TestPolicyTripped(t *testing.T) {
	p := circuit.Policy{MinCalls: 4, FailureRate: 0.5, SlowRate: 0.8}
	tests := []struct {
		name string
		c    circuit.Counts
		want bool
	}{
		{"below MinCalls even if all fail", circuit.Counts{Total: 3, Failures: 3}, false},
		{"at MinCalls, below rate", circuit.Counts{Total: 4, Failures: 1}, false},
		{"at MinCalls, rate exactly at threshold", circuit.Counts{Total: 4, Failures: 2}, true},
		{"slow rate trips", circuit.Counts{Total: 5, Slow: 4}, true},
		{"slow rate just below", circuit.Counts{Total: 10, Slow: 7}, false},
		{"empty", circuit.Counts{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.Tripped(tt.c); got != tt.want {
				t.Fatalf("Tripped(%+v) = %v, want %v", tt.c, got, tt.want)
			}
		})
	}
}

func TestPolicyUnhealthyIgnoresMinCalls(t *testing.T) {
	p := circuit.Policy{MinCalls: 100, FailureRate: 0.5, SlowRate: 1}
	if !p.Unhealthy(circuit.Counts{Total: 2, Failures: 1}) {
		t.Fatal("Unhealthy must not apply MinCalls")
	}
}
```

- [ ] **Step 2: Write the failing tests** — `internal/circuit/machine_test.go`

```go
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

func TestOpenRejectsWithRetryAfter(t *testing.T) {
	m := newMachine(2)
	tripOpen(t, m, t0)

	adm, _ := m.Admit(t0.Add(4 * time.Second))
	if adm.Reject != circuit.RejectedOpen || adm.RetryAfter != 6*time.Second {
		t.Fatalf("admission = %+v, want RejectedOpen retry 6s", adm)
	}
}

func TestRetryAfterStaysInRangeWhenClockGoesBackwards(t *testing.T) {
	m := newMachine(2)
	tripOpen(t, m, t0)

	adm, _ := m.Admit(t0.Add(-time.Hour))
	if adm.RetryAfter < 0 || adm.RetryAfter > openTimeout {
		t.Fatalf("RetryAfter = %v, want within [0, %v]", adm.RetryAfter, openTimeout)
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
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/circuit/`
Expected: FAIL, build error `undefined: circuit.Policy` / `circuit.NewMachine`.

- [ ] **Step 4: Write `internal/circuit/policy.go`**

```go
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
```

- [ ] **Step 5: Write `internal/circuit/machine.go`**

```go
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
	if m.state == Open && m.openRemaining(now) == 0 {
		ts = append(ts, m.enter(HalfOpen, now))
	}
	if m.state == HalfOpen && m.half.pending > 0 && !now.Before(m.half.deadline) {
		// A probe never reported back: fail closed and re-open.
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
	rem := m.cfg.OpenTimeout - now.Sub(m.openedAt)
	return min(max(rem, 0), m.cfg.OpenTimeout)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go vet ./... && go test -race -shuffle=on ./internal/circuit/`
Expected: `ok  github.com/gophernment/gosaf/internal/circuit`

- [ ] **Step 7: Commit**

```bash
gofmt -l .   # must print nothing
git add internal/circuit
git commit -m "feat(circuit): add trip policy and state machine

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Guard (use case): permits, classification, listener

**Files:**
- Create: `internal/guard/errors.go`, `internal/guard/guard.go`
- Test: `internal/guard/guard_test.go`

**Interfaces:**
- Consumes (Task 3): `circuit.Machine`, `MachineConfig`, `Policy`, `NewCountWindow`, `Admission`/`Reject` constants, `Transition`, `Outcome`, `State`, `Counts`.
- Produces (used by Task 5):
  - `var ErrOpen, ErrHalfOpenFull error`
  - `type RejectedError struct{ State circuit.State; RetryAfter time.Duration }` (`Error`, `Is`)
  - `type StateChange struct{ Name string; From, To circuit.State; At time.Time }`
  - `type CallEvent struct{ Name string; Outcome circuit.Outcome; Duration time.Duration }`
  - `type Listener interface{ OnStateChange(StateChange); OnCall(CallEvent) }`
  - `type Snapshot struct{ Name string; State circuit.State; Counts circuit.Counts }`
  - `type Options struct{ Name string; SlowCallDuration time.Duration; IsFailure func(error) bool; Clock func() time.Time; Listener Listener }`
  - `func DefaultIsFailure(error) bool`
  - `func New(*circuit.Machine, Options) *Guard`
  - `(*Guard).Allow(ctx) (*Permit, error)`, `(*Guard).Snapshot() Snapshot`, `(*Guard).Reset()`
  - `(*Permit).Done(err error)` (idempotent, nil-safe)

- [ ] **Step 1: Write the failing tests** — `internal/guard/guard_test.go`

```go
package guard_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gophernment/gosaf/internal/circuit"
	"github.com/gophernment/gosaf/internal/guard"
)

const (
	openTimeout  = 10 * time.Second
	probeTimeout = 3 * time.Second
	slow         = time.Second
)

var errBoom = errors.New("boom")

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type recorder struct {
	mu       sync.Mutex
	states   []guard.StateChange
	calls    []guard.CallEvent
	onChange func()
}

func (r *recorder) OnStateChange(ev guard.StateChange) {
	r.mu.Lock()
	r.states = append(r.states, ev)
	r.mu.Unlock()
	if r.onChange != nil {
		r.onChange()
	}
}

func (r *recorder) OnCall(ev guard.CallEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, ev)
}

func (r *recorder) stateChanges() []guard.StateChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]guard.StateChange(nil), r.states...)
}

func newGuard(t *testing.T, halfOpenMax int, mutate func(*guard.Options)) (*guard.Guard, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	m := circuit.NewMachine(circuit.MachineConfig{
		Policy:           circuit.Policy{MinCalls: 2, FailureRate: 0.5, SlowRate: 1},
		OpenTimeout:      openTimeout,
		HalfOpenMaxCalls: halfOpenMax,
		ProbeTimeout:     probeTimeout,
	}, circuit.NewCountWindow(10))
	opts := guard.Options{SlowCallDuration: slow, Clock: clk.Now}
	if mutate != nil {
		mutate(&opts)
	}
	return guard.New(m, opts), clk
}

func mustAllow(t *testing.T, g *guard.Guard) *guard.Permit {
	t.Helper()
	p, err := g.Allow(context.Background())
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	return p
}

func fail(t *testing.T, g *guard.Guard, n int) {
	t.Helper()
	for range n {
		mustAllow(t, g).Done(errBoom)
	}
}

func TestAllowRejectsWhenOpen(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)
	clk.Advance(4 * time.Second)

	_, err := g.Allow(context.Background())
	if !errors.Is(err, guard.ErrOpen) {
		t.Fatalf("err = %v, want ErrOpen", err)
	}
	var rej *guard.RejectedError
	if !errors.As(err, &rej) || rej.RetryAfter != 6*time.Second || rej.State != circuit.Open {
		t.Fatalf("rejected = %+v, want open, retry after 6s", rej)
	}
	if errors.Is(err, guard.ErrHalfOpenFull) {
		t.Fatal("open rejection must not match ErrHalfOpenFull")
	}
}

func TestHalfOpenFullRejection(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)
	clk.Advance(openTimeout)
	mustAllow(t, g) // the only probe, still running

	_, err := g.Allow(context.Background())
	if !errors.Is(err, guard.ErrHalfOpenFull) {
		t.Fatalf("err = %v, want ErrHalfOpenFull", err)
	}
}

func TestDoneIsIdempotent(t *testing.T) {
	g, _ := newGuard(t, 1, nil)
	p := mustAllow(t, g)
	p.Done(errBoom)
	p.Done(errBoom)
	p.Done(nil)

	if got := g.Snapshot().Counts; got.Total != 1 || got.Failures != 1 {
		t.Fatalf("counts = %+v, want exactly one failure", got)
	}
}

func TestNilPermitDoneIsSafe(t *testing.T) {
	var p *guard.Permit
	p.Done(errBoom) // must not panic
}

func TestSlowCallIsCounted(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	p := mustAllow(t, g)
	clk.Advance(slow + time.Millisecond)
	p.Done(nil)

	if got := g.Snapshot().Counts; got.Slow != 1 || got.Failures != 0 {
		t.Fatalf("counts = %+v, want one slow call", got)
	}
}

func TestCallAtSlowThresholdIsNotSlow(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	p := mustAllow(t, g)
	clk.Advance(slow)
	p.Done(nil)

	if got := g.Snapshot().Counts; got.Slow != 0 || got.Total != 1 {
		t.Fatalf("counts = %+v, want one non-slow success", got)
	}
}

func TestCanceledIsIgnoredButDeadlineIsFailure(t *testing.T) {
	g, _ := newGuard(t, 1, nil)
	mustAllow(t, g).Done(context.Canceled)
	if got := g.Snapshot().Counts.Total; got != 0 {
		t.Fatalf("canceled counted: total = %d, want 0", got)
	}
	mustAllow(t, g).Done(context.DeadlineExceeded)
	if got := g.Snapshot().Counts.Failures; got != 1 {
		t.Fatalf("deadline exceeded: failures = %d, want 1", got)
	}
}

func TestAllowChecksContextWithoutUsingASlot(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)
	clk.Advance(openTimeout)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Allow(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	mustAllow(t, g) // the single probe slot must still be free
}

func TestAllowNilContextFails(t *testing.T) {
	g, _ := newGuard(t, 1, nil)
	//lint:ignore SA1012 deliberately passing nil
	if _, err := g.Allow(nil); err == nil {
		t.Fatal("Allow(nil) must return an error")
	}
}

func TestStalePermitAfterResetIsDropped(t *testing.T) {
	g, _ := newGuard(t, 1, nil)
	p := mustAllow(t, g)
	g.Reset()
	p.Done(errBoom)

	if got := g.Snapshot().Counts.Total; got != 0 {
		t.Fatalf("stale result counted: total = %d", got)
	}
}

func TestIgnoredProbeFreesSlot(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)
	clk.Advance(openTimeout)

	mustAllow(t, g).Done(context.Canceled)
	mustAllow(t, g) // slot was released
}

func TestHungProbeIsReclaimed(t *testing.T) {
	g, clk := newGuard(t, 1, nil)
	fail(t, g, 2)
	clk.Advance(openTimeout)
	hung := mustAllow(t, g)

	clk.Advance(probeTimeout)
	if _, err := g.Allow(context.Background()); !errors.Is(err, guard.ErrOpen) {
		t.Fatalf("err = %v, want ErrOpen after probe timeout", err)
	}
	hung.Done(nil) // late result must not close the circuit
	if got := g.Snapshot().State; got != circuit.Open {
		t.Fatalf("state = %v, want open", got)
	}

	clk.Advance(openTimeout)
	mustAllow(t, g) // can probe again
}

func TestListenerSeesTransitionsAndCalls(t *testing.T) {
	rec := &recorder{}
	g, _ := newGuard(t, 1, func(o *guard.Options) { o.Listener = rec; o.Name = "db" })
	fail(t, g, 2)

	states := rec.stateChanges()
	if len(states) != 1 || states[0].Name != "db" ||
		states[0].From != circuit.Closed || states[0].To != circuit.Open {
		t.Fatalf("states = %+v, want one closed->open for db", states)
	}
	if len(rec.calls) != 2 || rec.calls[0].Outcome != circuit.Failure {
		t.Fatalf("calls = %+v, want two failures", rec.calls)
	}
}

func TestListenerMayReenterBreaker(t *testing.T) {
	rec := &recorder{}
	var g *guard.Guard
	rec.onChange = func() { _ = g.Snapshot(); g.Reset() }
	g, _ = newGuard(t, 1, func(o *guard.Options) { o.Listener = rec })

	done := make(chan struct{})
	go func() { defer close(done); fail(t, g, 2) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: listener re-entering the breaker blocked")
	}
}

type panicListener struct{}

func (panicListener) OnStateChange(guard.StateChange) { panic("listener bug") }
func (panicListener) OnCall(guard.CallEvent)          { panic("listener bug") }

func TestListenerPanicIsContained(t *testing.T) {
	g, _ := newGuard(t, 1, func(o *guard.Options) { o.Listener = panicListener{} })
	fail(t, g, 2) // must not panic
	if got := g.Snapshot().State; got != circuit.Open {
		t.Fatalf("state = %v, want open", got)
	}
}

func TestPanickingIsFailureFailsClosed(t *testing.T) {
	g, _ := newGuard(t, 1, func(o *guard.Options) {
		o.IsFailure = func(error) bool { panic("classifier bug") }
	})
	mustAllow(t, g).Done(errBoom)
	if got := g.Snapshot().Counts.Failures; got != 1 {
		t.Fatalf("failures = %d, want 1 (fail closed)", got)
	}
}

func TestCustomIsFailure(t *testing.T) {
	notFound := errors.New("not found")
	g, _ := newGuard(t, 1, func(o *guard.Options) {
		o.IsFailure = func(err error) bool { return !errors.Is(err, notFound) }
	})
	mustAllow(t, g).Done(notFound)
	if got := g.Snapshot().Counts.Total; got != 0 {
		t.Fatalf("excluded error counted: total = %d", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/guard/`
Expected: FAIL, build error `undefined: guard.New` / `guard.Options`.

- [ ] **Step 3: Write `internal/guard/errors.go`**

```go
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
```

- [ ] **Step 4: Write `internal/guard/guard.go`**

```go
// Package guard runs calls through a circuit.Machine. It owns the mutex, turns
// errors and durations into outcomes, and notifies the Listener outside the lock.
package guard

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gophernment/gosaf/internal/circuit"
)

var errNilContext = errors.New("gosaf: nil context")

// StateChange is delivered to Listener.OnStateChange.
type StateChange struct {
	Name     string
	From, To circuit.State
	At       time.Time
}

// CallEvent is delivered to Listener.OnCall. It carries no error text or payload.
type CallEvent struct {
	Name     string
	Outcome  circuit.Outcome
	Duration time.Duration
}

// Listener observes the breaker. Methods run after the lock is released, so
// they may call back into the breaker. A panic in a method is recovered and
// dropped. Ordering across goroutines is not guaranteed.
type Listener interface {
	OnStateChange(StateChange)
	OnCall(CallEvent)
}

// Snapshot is a point-in-time copy of breaker state.
type Snapshot struct {
	Name   string
	State  circuit.State
	Counts circuit.Counts
}

// Options are the non-machine settings of a Guard. Zero Clock and IsFailure
// select time.Now and DefaultIsFailure.
type Options struct {
	Name             string
	SlowCallDuration time.Duration
	IsFailure        func(error) bool
	Clock            func() time.Time
	Listener         Listener
}

// DefaultIsFailure counts every error except context.Canceled, which means
// the caller gave up rather than the dependency failing. A deadline that
// expires while waiting on the dependency is a failure.
func DefaultIsFailure(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled)
}

// Guard serialises access to a Machine.
type Guard struct {
	mu   sync.Mutex
	m    *circuit.Machine
	opts Options
}

// New returns a Guard around m.
func New(m *circuit.Machine, opts Options) *Guard {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.IsFailure == nil {
		opts.IsFailure = DefaultIsFailure
	}
	return &Guard{m: m, opts: opts}
}

// Permit is the right to make one call. Call Done exactly once with the
// call's error; further calls are ignored.
type Permit struct {
	g     *Guard
	gen   uint64
	start time.Time
	done  atomic.Bool
}

// Allow asks to make a call. It returns ctx.Err() if ctx is already done,
// without using a probe slot, or a *RejectedError if the circuit refuses.
func (g *Guard) Allow(ctx context.Context) (*Permit, error) {
	if ctx == nil {
		return nil, errNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	now := g.opts.Clock()
	g.mu.Lock()
	adm, ts := g.m.Admit(now)
	g.mu.Unlock()
	g.notifyStates(ts, now)

	switch adm.Reject {
	case circuit.RejectedOpen:
		return nil, &RejectedError{State: circuit.Open, RetryAfter: adm.RetryAfter}
	case circuit.RejectedHalfOpenFull:
		return nil, &RejectedError{State: circuit.HalfOpen, RetryAfter: adm.RetryAfter}
	}
	return &Permit{g: g, gen: adm.Gen, start: now}, nil
}

// Done reports the call's result. It is safe on a nil Permit and idempotent.
func (p *Permit) Done(err error) {
	if p == nil || !p.done.CompareAndSwap(false, true) {
		return
	}
	g := p.g
	now := g.opts.Clock()
	elapsed := max(now.Sub(p.start), 0)
	outcome := g.classify(err, elapsed)

	g.mu.Lock()
	ts := g.m.Record(now, p.gen, outcome)
	g.mu.Unlock()

	g.notifyStates(ts, now)
	g.notifyCall(CallEvent{Name: g.opts.Name, Outcome: outcome, Duration: elapsed})
}

// Snapshot returns a copy of the current state.
func (g *Guard) Snapshot() Snapshot {
	now := g.opts.Clock()
	g.mu.Lock()
	defer g.mu.Unlock()
	s, c := g.m.Snapshot(now)
	return Snapshot{Name: g.opts.Name, State: s, Counts: c}
}

// Reset closes the circuit, clears its window and drops in-flight results.
func (g *Guard) Reset() {
	now := g.opts.Clock()
	g.mu.Lock()
	ts := g.m.Reset(now)
	g.mu.Unlock()
	g.notifyStates(ts, now)
}

// classify runs user code (IsFailure) outside the lock.
func (g *Guard) classify(err error, elapsed time.Duration) circuit.Outcome {
	if err == nil {
		if elapsed > g.opts.SlowCallDuration {
			return circuit.Slow
		}
		return circuit.Success
	}
	if g.isFailure(err) {
		return circuit.Failure
	}
	return circuit.Ignored
}

// isFailure treats a panicking IsFailure as a failure (fail closed).
func (g *Guard) isFailure(err error) (failed bool) {
	defer func() {
		if recover() != nil {
			failed = true
		}
	}()
	return g.opts.IsFailure(err)
}

func (g *Guard) notifyStates(ts []circuit.Transition, at time.Time) {
	if g.opts.Listener == nil {
		return
	}
	for _, t := range ts {
		ev := StateChange{Name: g.opts.Name, From: t.From, To: t.To, At: at}
		safely(func() { g.opts.Listener.OnStateChange(ev) })
	}
}

func (g *Guard) notifyCall(ev CallEvent) {
	if g.opts.Listener == nil {
		return
	}
	safely(func() { g.opts.Listener.OnCall(ev) })
}

func safely(f func()) {
	defer func() { _ = recover() }()
	f()
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go vet ./... && go test -race -shuffle=on ./internal/...`
Expected: `ok` for `internal/circuit` and `internal/guard`.

- [ ] **Step 6: Commit**

```bash
gofmt -l .   # must print nothing
git add internal/guard
git commit -m "feat(guard): add permits, outcome classification and listener delivery

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Public facade: Config, Breaker, Do[T]

**Files:**
- Create: `config.go`, `gosaf.go`
- Test: `config_test.go`, `gosaf_test.go`, `example_test.go`

**Interfaces:**
- Consumes (Tasks 2-4): `circuit.*`, `guard.*` as listed above.
- Produces (public API):
  - `Config`, `DefaultConfig() Config`, `MaxWindowSize`, `ErrInvalidConfig`
  - `New(Config) (*Breaker, error)`
  - `(*Breaker).Allow(ctx) (*Permit, error)`, `.Snapshot() Snapshot`, `.State() State`, `.Reset()`
  - `Do[T any](ctx, *Breaker, func(context.Context) (T, error)) (T, error)`
  - Re-exports: `State` (+`Closed/Open/HalfOpen`), `Outcome` (+`Success/Failure/Slow/Ignored`), `Counts`, `Snapshot`, `StateChange`, `CallEvent`, `Listener`, `Permit`, `RejectedError`, `ErrOpen`, `ErrHalfOpenFull`

- [ ] **Step 1: Write the failing tests** — `config_test.go`

```go
package gosaf_test

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/gophernment/gosaf"
)

func TestDefaultConfigIsValid(t *testing.T) {
	if _, err := gosaf.New(gosaf.DefaultConfig()); err != nil {
		t.Fatalf("DefaultConfig rejected: %v", err)
	}
}

func TestZeroConfigIsRejected(t *testing.T) {
	_, err := gosaf.New(gosaf.Config{})
	if !errors.Is(err, gosaf.ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		field  string
		mutate func(*gosaf.Config)
	}{
		{"WindowSize", func(c *gosaf.Config) { c.WindowSize = 0 }},
		{"WindowSize", func(c *gosaf.Config) { c.WindowSize = gosaf.MaxWindowSize + 1 }},
		{"WindowPeriod", func(c *gosaf.Config) { c.WindowPeriod = -time.Second }},
		{"WindowPeriod", func(c *gosaf.Config) { c.WindowPeriod = time.Duration(c.WindowSize) - 1 }},
		{"MinCalls", func(c *gosaf.Config) { c.MinCalls = 0 }},
		{"MinCalls", func(c *gosaf.Config) { c.MinCalls = c.WindowSize + 1 }},
		{"FailureRate", func(c *gosaf.Config) { c.FailureRate = 0 }},
		{"FailureRate", func(c *gosaf.Config) { c.FailureRate = 1.01 }},
		{"FailureRate", func(c *gosaf.Config) { c.FailureRate = math.NaN() }},
		{"SlowCallRate", func(c *gosaf.Config) { c.SlowCallRate = -0.1 }},
		{"SlowCallDuration", func(c *gosaf.Config) { c.SlowCallDuration = 0 }},
		{"OpenTimeout", func(c *gosaf.Config) { c.OpenTimeout = 0 }},
		{"HalfOpenMaxCalls", func(c *gosaf.Config) { c.HalfOpenMaxCalls = 0 }},
		{"ProbeTimeout", func(c *gosaf.Config) { c.ProbeTimeout = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			cfg := gosaf.DefaultConfig()
			tt.mutate(&cfg)
			b, err := gosaf.New(cfg)
			if !errors.Is(err, gosaf.ErrInvalidConfig) {
				t.Fatalf("err = %v, want ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("err = %q, want it to name %s", err, tt.field)
			}
			if b != nil {
				t.Fatal("Breaker must be nil on error")
			}
		})
	}
}

func TestMinCallsMayExceedWindowSizeForTimeWindow(t *testing.T) {
	cfg := gosaf.DefaultConfig()
	cfg.WindowPeriod = time.Minute
	cfg.MinCalls = cfg.WindowSize * 10
	if _, err := gosaf.New(cfg); err != nil {
		t.Fatalf("time window rejected: %v", err)
	}
}

func FuzzNew(f *testing.F) {
	f.Add(100, int64(0), 20, 0.5, int64(time.Second), 1.0, int64(time.Second), 5, int64(time.Second))
	f.Add(0, int64(-1), 0, math.NaN(), int64(0), 2.0, int64(-5), -1, int64(math.MaxInt64))
	f.Add(gosaf.MaxWindowSize, int64(math.MaxInt64), 1, 1.0, int64(math.MaxInt64), 1.0, int64(math.MaxInt64), 1, int64(1))

	f.Fuzz(func(t *testing.T, size int, period int64, minCalls int, failRate float64,
		slowDur int64, slowRate float64, openTimeout int64, probes int, probeTimeout int64) {
		b, err := gosaf.New(gosaf.Config{
			WindowSize: size, WindowPeriod: time.Duration(period), MinCalls: minCalls,
			FailureRate: failRate, SlowCallDuration: time.Duration(slowDur), SlowCallRate: slowRate,
			OpenTimeout: time.Duration(openTimeout), HalfOpenMaxCalls: probes,
			ProbeTimeout: time.Duration(probeTimeout),
		})
		if err != nil {
			if !errors.Is(err, gosaf.ErrInvalidConfig) || b != nil {
				t.Fatalf("invalid config must yield ErrInvalidConfig and nil Breaker, got %v, %v", err, b)
			}
			return
		}
		// An accepted config must be usable without panicking.
		for range 3 {
			if p, err := b.Allow(t.Context()); err == nil {
				p.Done(errors.New("x"))
			}
		}
		_ = b.Snapshot()
	})
}
```

- [ ] **Step 2: Write the failing tests** — `gosaf_test.go`

```go
package gosaf_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gophernment/gosaf"
)

var errBoom = errors.New("boom")

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newBreaker(t *testing.T) (*gosaf.Breaker, *clock) {
	t.Helper()
	clk := &clock{t: time.Unix(1_000_000, 0)}
	cfg := gosaf.DefaultConfig()
	cfg.MinCalls = 2
	cfg.HalfOpenMaxCalls = 1
	cfg.Clock = clk.Now
	b, err := gosaf.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b, clk
}

func failOnce(b *gosaf.Breaker) error {
	_, err := gosaf.Do(context.Background(), b, func(context.Context) (int, error) { return 0, errBoom })
	return err
}

func TestDoReturnsResultAndError(t *testing.T) {
	b, _ := newBreaker(t)

	got, err := gosaf.Do(context.Background(), b, func(context.Context) (string, error) { return "ok", nil })
	if err != nil || got != "ok" {
		t.Fatalf("got %q, %v; want ok, nil", got, err)
	}
	if err := failOnce(b); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want errBoom passed through", err)
	}
}

func TestDoFailsFastWhenOpen(t *testing.T) {
	b, _ := newBreaker(t)
	failOnce(b)
	failOnce(b)

	called := false
	_, err := gosaf.Do(context.Background(), b, func(context.Context) (int, error) { called = true; return 0, nil })
	if !errors.Is(err, gosaf.ErrOpen) || called {
		t.Fatalf("err = %v, called = %v; want ErrOpen and fn not called", err, called)
	}
	if b.State() != gosaf.Open {
		t.Fatalf("State = %v, want open", b.State())
	}
}

func TestDoRecoversThroughHalfOpen(t *testing.T) {
	b, clk := newBreaker(t)
	failOnce(b)
	failOnce(b)
	clk.Advance(gosaf.DefaultConfig().OpenTimeout)

	if _, err := gosaf.Do(context.Background(), b, func(context.Context) (int, error) { return 1, nil }); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if b.State() != gosaf.Closed {
		t.Fatalf("State = %v, want closed after successful probe", b.State())
	}
}

func TestDoSkipsFnWhenContextDone(t *testing.T) {
	b, _ := newBreaker(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	_, err := gosaf.Do(ctx, b, func(context.Context) (int, error) { called = true; return 0, nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("err = %v, called = %v; want context.Canceled and fn not called", err, called)
	}
}

func TestDoRecordsPanicAndReraises(t *testing.T) {
	b, _ := newBreaker(t)

	func() {
		defer func() {
			if r := recover(); r != "kaboom" {
				t.Fatalf("recovered %v, want original panic value", r)
			}
		}()
		gosaf.Do(context.Background(), b, func(context.Context) (int, error) { panic("kaboom") })
	}()

	if got := b.Snapshot().Counts.Failures; got != 1 {
		t.Fatalf("failures = %d, want the panic recorded", got)
	}
}

func TestDoFreesProbeSlotOnGoexit(t *testing.T) {
	b, clk := newBreaker(t)
	failOnce(b)
	failOnce(b)
	clk.Advance(gosaf.DefaultConfig().OpenTimeout)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		gosaf.Do(context.Background(), b, func(context.Context) (int, error) {
			runtime.Goexit() // what t.FailNow does inside fn
			return 0, nil
		})
	}()
	wg.Wait()

	// The probe failed (recorded), so the circuit re-opened instead of sticking half-open.
	if got := b.State(); got != gosaf.Open {
		t.Fatalf("State = %v, want open after goexit probe", got)
	}
}

func TestZeroValueBreakerFailsClosed(t *testing.T) {
	var zero gosaf.Breaker
	for name, b := range map[string]*gosaf.Breaker{"nil": nil, "zero": &zero} {
		t.Run(name, func(t *testing.T) {
			called := false
			_, err := gosaf.Do(context.Background(), b, func(context.Context) (int, error) { called = true; return 0, nil })
			if !errors.Is(err, gosaf.ErrInvalidConfig) || called {
				t.Fatalf("err = %v, called = %v; want ErrInvalidConfig and fn not called", err, called)
			}
			b.Reset()
			if s := b.Snapshot(); s != (gosaf.Snapshot{}) {
				t.Fatalf("Snapshot = %+v, want zero", s)
			}
		})
	}
}

func TestAllowAndDoneManually(t *testing.T) {
	b, _ := newBreaker(t)
	p, err := b.Allow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p.Done(nil)
	if got := b.Snapshot().Counts.Total; got != 1 {
		t.Fatalf("total = %d, want 1", got)
	}
}

type countingListener struct{ changes, calls atomic.Int64 }

func (l *countingListener) OnStateChange(gosaf.StateChange) { l.changes.Add(1) }
func (l *countingListener) OnCall(gosaf.CallEvent)          { l.calls.Add(1) }

func TestConcurrentUse(t *testing.T) {
	lis := &countingListener{}
	cfg := gosaf.DefaultConfig()
	cfg.WindowSize, cfg.MinCalls = 50, 10
	cfg.OpenTimeout = time.Millisecond
	cfg.ProbeTimeout = 5 * time.Millisecond
	cfg.Listener = lis
	b, err := gosaf.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for g := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				gosaf.Do(context.Background(), b, func(context.Context) (int, error) {
					if (g+i)%3 == 0 {
						return 0, errBoom
					}
					return 1, nil
				})
				if i%50 == 0 {
					_ = b.Snapshot()
				}
			}
		}()
	}
	wg.Wait()

	if got := b.Snapshot().Counts.Total; got > cfg.WindowSize {
		t.Fatalf("window holds %d calls, bound is %d", got, cfg.WindowSize)
	}
	if lis.calls.Load() == 0 {
		t.Fatal("listener saw no calls")
	}
}
```

- [ ] **Step 3: Write the runnable example** — `example_test.go`

```go
package gosaf_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/gophernment/gosaf"
)

func Example() {
	cb, err := gosaf.New(gosaf.DefaultConfig())
	if err != nil {
		panic(err)
	}

	msg, err := gosaf.Do(context.Background(), cb, func(ctx context.Context) (string, error) {
		return "hello", nil // call your dependency here
	})
	if errors.Is(err, gosaf.ErrOpen) {
		fmt.Println("failing fast")
		return
	}
	fmt.Println(msg, err)
	// Output: hello <nil>
}
```

- [ ] **Step 4: Run them to verify they fail**

Run: `go test .`
Expected: FAIL, build error `undefined: gosaf.New` / `gosaf.Config`.

- [ ] **Step 5: Write `config.go`**

```go
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
```

- [ ] **Step 6: Write `gosaf.go`**

```go
// Package gosaf is a circuit breaker: it stops calling a failing or slow
// dependency, lets it recover, then probes before trusting it again.
//
//	cb, err := gosaf.New(gosaf.DefaultConfig())
//	if err != nil { /* bad config */ }
//	res, err := gosaf.Do(ctx, cb, func(ctx context.Context) (string, error) {
//		return callDependency(ctx)
//	})
//	if errors.Is(err, gosaf.ErrOpen) { /* fail fast or fall back */ }
package gosaf

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gophernment/gosaf/internal/circuit"
	"github.com/gophernment/gosaf/internal/guard"
)

// Re-exported types. Their definitions live in internal packages.
type (
	// State is Closed, Open or HalfOpen.
	State = circuit.State
	// Outcome is how a finished call was classified.
	Outcome = circuit.Outcome
	// Counts summarises calls in the current window.
	Counts = circuit.Counts
	// Snapshot is a point-in-time copy of breaker state.
	Snapshot = guard.Snapshot
	// StateChange is delivered to Listener.OnStateChange.
	StateChange = guard.StateChange
	// CallEvent is delivered to Listener.OnCall.
	CallEvent = guard.CallEvent
	// Listener observes a Breaker.
	Listener = guard.Listener
	// Permit is the right to make one call; report the result with Done.
	Permit = guard.Permit
	// RejectedError is returned when the circuit refuses a call.
	RejectedError = guard.RejectedError
)

// Circuit states.
const (
	Closed   = circuit.Closed
	Open     = circuit.Open
	HalfOpen = circuit.HalfOpen
)

// Call outcomes.
const (
	Success = circuit.Success
	Failure = circuit.Failure
	Slow    = circuit.Slow
	Ignored = circuit.Ignored
)

// Rejection errors. Match with errors.Is.
var (
	ErrOpen         = guard.ErrOpen
	ErrHalfOpenFull = guard.ErrHalfOpenFull
)

var (
	errNotInitialized = fmt.Errorf("%w: Breaker must be created with New", ErrInvalidConfig)
	errPanicked       = errors.New("gosaf: call panicked")
)

// Breaker is a circuit breaker. Create it with New; it is safe for concurrent use.
type Breaker struct {
	g *guard.Guard
}

// New validates cfg and returns a Breaker in the Closed state.
func New(cfg Config) (*Breaker, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}

	var win circuit.Window
	if cfg.WindowPeriod > 0 {
		win = circuit.NewTimeWindow(cfg.WindowSize, cfg.WindowPeriod, clock())
	} else {
		win = circuit.NewCountWindow(cfg.WindowSize)
	}
	m := circuit.NewMachine(circuit.MachineConfig{
		Policy: circuit.Policy{
			MinCalls:    cfg.MinCalls,
			FailureRate: cfg.FailureRate,
			SlowRate:    cfg.SlowCallRate,
		},
		OpenTimeout:      cfg.OpenTimeout,
		HalfOpenMaxCalls: cfg.HalfOpenMaxCalls,
		ProbeTimeout:     cfg.ProbeTimeout,
	}, win)

	return &Breaker{g: guard.New(m, guard.Options{
		Name:             cfg.Name,
		SlowCallDuration: cfg.SlowCallDuration,
		IsFailure:        cfg.IsFailure,
		Clock:            clock,
		Listener:         cfg.Listener,
	})}, nil
}

// Allow asks to make one call. On success the caller must call Permit.Done
// with the call's error (nil on success). Prefer Do, which does this for you.
// Errors: ctx.Err() if ctx is done, or a *RejectedError (ErrOpen, ErrHalfOpenFull).
func (b *Breaker) Allow(ctx context.Context) (*Permit, error) {
	if b == nil || b.g == nil {
		return nil, errNotInitialized
	}
	return b.g.Allow(ctx)
}

// Snapshot returns a copy of the current state and counts. A zero Breaker
// returns a zero Snapshot.
func (b *Breaker) Snapshot() Snapshot {
	if b == nil || b.g == nil {
		return Snapshot{}
	}
	return b.g.Snapshot()
}

// State returns the current state. An Open breaker reports Open until the next
// call after OpenTimeout moves it to HalfOpen.
func (b *Breaker) State() State { return b.Snapshot().State }

// Reset closes the circuit, clears its statistics and discards the results of
// calls still in flight. Use it from operational tooling, not the hot path.
func (b *Breaker) Reset() {
	if b == nil || b.g == nil {
		return
	}
	b.g.Reset()
}

// Do runs fn through the breaker. It returns a *RejectedError without calling
// fn when the circuit refuses, and ctx.Err() when ctx is already done.
// A panic in fn is recorded as a failure and then re-raised.
func Do[T any](ctx context.Context, b *Breaker, fn func(context.Context) (T, error)) (res T, err error) {
	p, err := b.Allow(ctx)
	if err != nil {
		return res, err
	}

	finished := false
	defer func() {
		if finished {
			return
		}
		// fn panicked or called runtime.Goexit: free the slot, then let it continue.
		r := recover()
		p.Done(errPanicked)
		if r != nil {
			panic(r)
		}
	}()

	res, err = fn(ctx)
	finished = true
	p.Done(err)
	return res, err
}
```

- [ ] **Step 7: Run all tests to verify they pass**

Run: `go vet ./... && go test -race -shuffle=on -count=1 ./...`
Expected: `ok` for `gosaf`, `internal/circuit`, `internal/guard`.

- [ ] **Step 8: Run the fuzz target briefly**

Run: `go test -run='^$' -fuzz=FuzzNew -fuzztime=10s .`
Expected: `PASS`. If it finds a crasher, fix `validate`/`New`, keep the generated `testdata/fuzz/FuzzNew/*` file, and re-run.

- [ ] **Step 9: Commit**

```bash
gofmt -l .   # must print nothing
git add config.go config_test.go gosaf.go gosaf_test.go example_test.go
git commit -m "feat: add public Breaker, validated Config and generic Do

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Docs alignment and full verification

**Files:**
- Modify: `README.md` (sections "Planned API (sketch)" and "Scope"), `AGENTS.md` (layout block, port list, check commands), `docs/superpowers/specs/2026-10-14-gosaf-core-design.md` (only if the executor finds a divergence not already patched)

**Interfaces:**
- Consumes: the finished public API from Task 5.

- [ ] **Step 1: Replace README "Planned API (sketch)" with real usage**

Replace the whole section (heading through the end of its code block) with:

````markdown
## Usage

```go
cb, err := gosaf.New(gosaf.DefaultConfig())
if err != nil {
    return err // invalid config is rejected, never silently fixed
}

res, err := gosaf.Do(ctx, cb, func(ctx context.Context) (string, error) {
    return callPaymentAPI(ctx)
})
switch {
case errors.Is(err, gosaf.ErrOpen), errors.Is(err, gosaf.ErrHalfOpenFull):
    // fail fast or fall back; errors.As to *gosaf.RejectedError gives RetryAfter
case err != nil:
    // the call itself failed
}
```

Tune behaviour through `gosaf.Config`: sliding window (count- or time-based), failure rate,
slow-call rate, open timeout, half-open probes and probe timeout. `IsFailure` excludes errors
(for example "not found") from the statistics; `context.Canceled` is excluded by default.
A `Listener` receives state changes and call results.
````

In "Scope: In" add: `Failure-rate and slow-call-rate tripping over a sliding window` and `Panic-safe, idempotent two-step API (Allow/Done)`. Keep "Out" as is.

- [ ] **Step 2: Update AGENTS.md to match what was built**

Replace the "Suggested layout" code block with:

```
gosaf/
  gosaf.go           # facade: New, Breaker, Do[T], re-exported types and errors
  config.go          # Config, DefaultConfig, validation
  internal/
    circuit/         # domain: State, Outcome, Window, Policy, Machine
    guard/           # usecase: Allow/Permit/Done, mutex, classification, Listener
  tools/             # separate module pinning staticcheck + govulncheck
```

Change the `Clock` port bullet to: ``- `Clock` is a plain `func() time.Time` (injected via `Config.Clock`; fake in tests)``, and delete the `adapter/` bullet from the Onion list (there are no adapters in v1; the root facade does the wiring).

Replace the "Before finishing any change" command block with:

```sh
gofmt -l .        # must print nothing
go vet ./...
go tool -modfile=tools/go.mod staticcheck ./...
go tool -modfile=tools/go.mod govulncheck ./...
go test -race -shuffle=on -count=1 ./...
```

- [ ] **Step 3: Full verification (evidence before claiming done)**

Run:
```bash
gofmt -l .
go vet ./...
go tool -modfile=tools/go.mod staticcheck ./...
go tool -modfile=tools/go.mod govulncheck ./...
go mod verify
go test -race -shuffle=on -count=1 -cover ./...
go test -run='^$' -fuzz=FuzzNew -fuzztime=10s .
```
Expected: `gofmt` prints nothing; vet and staticcheck print nothing; govulncheck prints `No vulnerabilities found.`; `go mod verify` prints `all modules verified`; tests `ok` with coverage roughly: `gosaf` ~98%, `internal/circuit` ~86%, `internal/guard` ~92%; fuzz `PASS`. Report any deviation instead of hiding it.

- [ ] **Step 4: Confirm zero dependencies**

Run: `grep -c '^require' go.mod`
Expected: `0`

- [ ] **Step 5: Commit**

```bash
git add README.md AGENTS.md docs
git commit -m "docs: align README and AGENTS with the implemented core

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage**

| Spec section | Task |
|---|---|
| §4 architecture, approach B (`Do[T]` over `Allow`/`Done`) | 3, 4, 5 |
| §5 state transitions, probes, generation, panic, ctx pre-check, listener after unlock | 3 (`Machine`), 4 (`Guard`), 5 (`Do`) |
| §6 Config + validation + `DefaultConfig` | 5 |
| §7 errors, `RejectedError` with `RetryAfter >= 0` | 3 (clamp), 4 (types), 5 (re-export) |
| §8 secure coding (bounded memory, fail closed, copy-by-value snapshot, no lock around user code) | 2 (ring buffers), 4, 5 (`MaxWindowSize`) |
| §9 testing (table, guard, race, `FuzzNew`, fake clock) | 2-5 |
| §10 tooling / supply chain | 1, 6 |

**Deliberate refinements of the spec (already reflected in code; the spec is patched to match):**
- Default `IsFailure` excludes only `context.Canceled`. `context.DeadlineExceeded` counts as a failure, otherwise a dependency that hangs until timeout would never trip the breaker.
- `Config.WindowSize` is capped at `MaxWindowSize` (100000) to keep memory bounded.
- `go.mod` uses `go 1.27.2` (the `go` line sets the minimum toolchain); no separate `toolchain` line.
- Dev tools are pinned in a nested `tools/` module so the root module stays dependency-free.
- Event types are named `StateChange` and `CallEvent` (spec said `Event`).

**Placeholder scan:** none. Task 1 uses concrete action SHAs resolved on 2026-10-14.

**Type consistency:** names match across tasks (`Machine.Admit/Record/Reset/Snapshot`, `guard.Options`, `Permit.Done`, `Config` fields). All code in Tasks 2-5 was compiled and tested together before inclusion.
