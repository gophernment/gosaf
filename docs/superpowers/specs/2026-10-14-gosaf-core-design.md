# GoSaf Core — Design Spec

Date: 2026-10-14
Status: draft, awaiting review

## 1. Goal

Circuit breaker library for Go that improves on `sony/gobreaker` in safety, modern Go
idiom, and features, while staying small (KISS/YAGNI). v1 = **core only**: no
net/http, gRPC, Redis, or metrics adapters.

Success criteria:
- Correct under concurrency (`-race` clean), no deadlock when callbacks re-enter the breaker.
- No stuck Half-Open state under any caller behavior (panic, skipped/duplicate `Done`, hung call).
- Invalid config is rejected, never silently weakened.
- Zero runtime dependencies (stdlib only). Toolchain and dev tooling always on latest patched versions.

## 2. Non-goals (v1)

HTTP/gRPC adapters, distributed state/`Store`, metrics exporters, retry, bulkhead,
rate limit, `ForceOpen`/`ForceClose`, background goroutines/timers.

## 3. Comparison with gobreaker (drives requirements)

| Topic | gobreaker | GoSaf |
|---|---|---|
| Context | none | `Do(ctx, ...)`; ctx errors ignored for stats |
| Callbacks | run under mutex (re-entrant deadlock) | run after unlock; listener panic recovered |
| Window | tumbling `Interval` / buckets | sliding, count-based or time-based, fixed-size ring |
| Trip policy | consecutive failures (ratio is DIY) | failure rate + `MinCalls`, built in |
| Slow calls | none | slow-call rate threshold |
| Two-step `done` | skippable / repeatable | `Permit.Done` idempotent, generation-bound, probe timeout reclaim |
| Config | zero → silent defaults | validated in `New`; `DefaultConfig()` explicit |
| Errors | sentinels | sentinels + `*RejectedError{State, RetryAfter}` |

## 4. Architecture

Dependencies point inward: `facade → guard → circuit`.

```
gosaf.go              facade/wiring: New, Config, DefaultConfig, Breaker, Do[T],
                      State, Snapshot, Listener, StateChange, CallEvent, errors
internal/circuit/     DOMAIN, pure: State, Outcome, Window, Policy, Machine
                      no mutex, no time.Now, no goroutines; `now` passed as argument
internal/guard/       USECASE (DCI context): Allow/Permit/Done, mutex, generation,
                      probe slots, Clock + Listener ports
```

- Ports (defined by the inner consumer): `Clock func() time.Time`, `Listener` interface.
  Default clock/system adapter wired in the facade only.
- DCI: data = `State`, `Counts`, `Config`; context = guarded call (`guard`);
  roles = `Clock`, `Listener`, `Policy`.
- Public API shape (approach B): non-generic `Breaker`; generic helper
  `Do[T any](ctx, *Breaker, func(context.Context) (T, error)) (T, error)`.
  `Do` = thin wrapper over `Allow` + `Permit.Done` (single primitive, no duplicated logic).

## 5. Behavior

States: Closed, Open, HalfOpen.

- **Closed → Open**: window has ≥ `MinCalls` and (`failureRate ≥ FailureRate` or `slowRate ≥ SlowCallRate`).
- **Open → HalfOpen**: lazily when a call arrives after `OpenTimeout`. No timer.
- **HalfOpen**: admit up to `HalfOpenMaxCalls` probes; extra calls rejected with `ErrHalfOpenFull`.
  When all probes complete: rates below thresholds → Closed (window reset), else → Open.
  A probe not completed within `ProbeTimeout` (or a clock step back past its deadline) is treated as a failed probe: the circuit fails closed and re-opens immediately, slots are reclaimed, and the probe's late result is dropped by generation. Likewise an Open circuit whose clock steps back restarts its wait from the new time.
- **Outcome**: Success, Failure, Slow (success exceeding `SlowCallDuration`), Ignored
  (`context.Canceled` by default, or excluded by `IsFailure`; not recorded). `context.DeadlineExceeded` is a Failure: a dependency that hangs until timeout must still trip the breaker.
- **Generation**: bumped on every transition and window reset; results carrying an old
  generation are discarded.
- **Panic in fn**: record Failure, release slot via `defer`, re-panic.
- **`Do` pre-check**: `ctx.Err() != nil` → return it, no slot consumed.
- **Listener**: events collected under lock, delivered after unlock; panics recovered.

## 6. Config

```go
type Config struct {
    Name             string
    WindowSize       int           // count-based: last N calls; time-based: bucket count
    WindowPeriod     time.Duration // 0 = count-based; >0 = time-based total span
    MinCalls         int
    FailureRate      float64       // (0,1]
    SlowCallDuration time.Duration
    SlowCallRate     float64       // (0,1]
    OpenTimeout      time.Duration
    HalfOpenMaxCalls int
    ProbeTimeout     time.Duration
    IsFailure        func(error) bool // default: err != nil && !errors.Is(err, context.Canceled)
    Clock            func() time.Time // default time.Now
    Listener         Listener
}
func New(Config) (*Breaker, error)
func DefaultConfig() Config
```

Validation (`ErrInvalidConfig`, message names the field): `1 <= WindowSize <= MaxWindowSize` (100000, bounds memory), durations > 0,
rates in (0,1] and not NaN, `MinCalls ≤ WindowSize` (count-based), `HalfOpenMaxCalls ≥ 1`.

## 7. Errors

`ErrOpen`, `ErrHalfOpenFull`, `ErrInvalidConfig` (sentinels, `errors.Is`).
Rejections are `*RejectedError{State, RetryAfter}` (`errors.As`); `RetryAfter ≥ 0`.
No caller payloads or secrets in any error/event.

## 8. Secure coding requirements

- Fixed-size ring buffers only; memory bounded regardless of traffic.
- Counters cannot overflow (clamped / wide types); `RetryAfter` never negative.
- `Snapshot` returns values (no internal pointers/slices).
- No `unsafe`, `reflect`, `init()` side effects, or global mutable state.
- Lock never held while running user code (`fn`, `IsFailure`, `Listener`).
- Fail closed on invalid config or internal inconsistency.

## 9. Testing

- Domain: table-driven; every transition; `MinCalls-1` vs `MinCalls`; rate exactly at threshold; window wrap-around; slow vs failure.
- Guard: `ErrOpen`, probe limit, duplicate/skipped `Done`, stale generation dropped, panic frees slot, probe timeout reclaim, ctx cancel not counted, listener panic, re-entrant listener (no deadlock).
- Concurrency: many goroutines under `-race`.
- `FuzzNew` on config validation (no panic, invalid → error).
- Fake clock everywhere; no `time.Sleep`; hand-written fakes.

## 10. Supply chain / tooling

- `go.mod`: `go 1.27.<latest patch>` (the `go` line sets the minimum toolchain); bump on each patch release. Dev tools are pinned in a nested `tools/` module so the root module has zero dependencies.
- CI: `gofmt -l`, `go vet`, `staticcheck`, `govulncheck ./...`, `go mod verify`,
  `go test -race -shuffle=on ./...`.
- GitHub Actions pinned by commit SHA; Dependabot/Renovate weekly for Go modules and Actions.
- Dev tools pinned to latest at setup, kept current by the bot.
- `SECURITY.md` with vulnerability reporting channel.

## 11. Open items

- License (TBD).
- Module path assumed `github.com/gophernment/gosaf`.
