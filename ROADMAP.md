# GoSaf Roadmap

Goal: be a better circuit breaker than [sony/gobreaker](https://github.com/sony/gobreaker) —
safer, more correct under failure, more observable, and no harder to use.

This is a plan, not a promise. Every item must pass the YAGNI gate in
[AGENTS.md](AGENTS.md): it enters a release only with a concrete need, a test that fails first,
and a place in the layering (domain / usecase / facade). Anything that needs a third-party
dependency ships as a **separate module** so the core stays dependency-free.

## What "better" means (measurable)

| Axis | gobreaker today | GoSaf target | Status |
|---|---|---|---|
| Context support | none in `Execute` | `Do(ctx, ...)`, cancel excluded from stats | done (v0.1) |
| Callbacks | run under the mutex (re-entrancy deadlock) | run after unlock, panics contained | done (v0.1) |
| Window | tumbling `Interval`, optional buckets | sliding count/time window, bounded memory | done (v0.1) |
| Trip policy | consecutive failures; ratios are DIY | failure rate + slow-call rate + `MinCalls` (v0.1) + consecutive failures | v0.1.1 |
| Stuck Half-Open | skipped/duplicate `done` can stall it | idempotent `Done`, probe timeout, panic/`Goexit` safe (v0.1); early re-open when recovery is impossible | v0.1.1 |
| Config | zero values silently defaulted | validated, fail closed | done (v0.1) |
| Correctness proof | example tests | model-based + property tests, race stress, fuzz | v0.2 |
| Performance | baseline | `ns/op` and `allocs/op` no worse on the hot path | v0.2 (not yet measured) |
| Recovery behaviour | fixed open timeout | backoff with jitter, gradual ramp-up | v0.3 |
| Observability | `OnStateChange` only | rejects, events, `slog`, OpenTelemetry/Prometheus | v0.2–v0.3 |
| Integrations | none in core | `net/http`, gRPC, as adapters | v0.3 |
| Shared state | separate distributed type | `Store` port + reference adapter | v0.4 |
| Supply chain | — | signed tags, provenance, vuln-checked releases, OpenSSF Scorecard | v0.2–v1.0 |

Do not claim "faster" or "better" anywhere in the docs until the matching gate below is met
with reproducible numbers.

## v0.1.1 — finish v0.1 (patch)

- Publish the repo, push `main` and `v0.1.0`, confirm CI is green on Linux; enable private
  vulnerability reporting and Dependabot alerts.
- (done, unreleased) Half-Open reacts faster: re-open as soon as the probes already seen make a healthy outcome
  impossible (best case: every remaining probe succeeds), instead of waiting for all probes while
  still sending traffic to a failing dependency. gobreaker re-opens on the first probe failure.
- (done, unreleased) `Config.ConsecutiveFailures`: trip after N failures in a row (independent of `MinCalls`),
  covering the common "5 in a row" rule that gobreaker's default `ReadyToTrip` offers.
- Cap `OpenTimeout` in `Config.validate` (Duration overflow guard).
- Document: `Listener` runs on the caller goroutine and may block it; event ordering across
  goroutines; `State()`/`Snapshot()` are lazy (Open→Half-Open happens on the next `Allow`);
  `Do` re-panics with the original value.
- Re-panic keeps the original stack where possible (wrap in a type that carries it).

## v0.2 — prove it, make it operable (minor)

- **Evidence**
  - Benchmarks (`ns/op`, `allocs/op`, parallel) for `Do` on Closed/Open paths; a comparison
    harness against gobreaker kept in a separate `bench/` module so the core has no dependency.
  - Model-based test: a tiny reference implementation vs `Machine` over random operation
    sequences (admit / record / advance time / reset). Property: no sequence leaves Half-Open stuck.
  - Use `testing/synctest` for time-driven concurrency tests where it removes fake-clock plumbing.
  - Fuzz `Machine` operation sequences, not only `Config`.
  - Gate: hot path no slower and no more allocations than gobreaker's `Execute`; if the
    `Permit` allocation is the cost, add a non-allocating path before shipping.
- **Operations**
  - `ForceOpen()` / `ForceClose()` / `Disable()` for incidents (explicit, logged via `Listener`).
  - `Listener.OnReject` (or a `Rejected` call event) so shed load is visible.
  - `Snapshot` additions: time of last transition, `RetryAfter`, rejected count.
  - `log/slog` helper that implements `Listener` (stdlib only).
- **Supply chain**: signed tags, release workflow with provenance attestation, Scorecard in CI.

## v0.3 — smarter recovery, integrations (minor)

- **Recovery**
  - Exponential backoff with jitter for `OpenTimeout` on repeated re-opens (bounded by a max),
    so a still-broken dependency is probed less often and herds don't synchronise.
  - Gradual Half-Open ramp-up (admit a growing share of traffic) as an opt-in policy.
  - Helpers for error classification (for example "5xx and timeouts are failures, 4xx are not").
- **Adapters**, each a subpackage when stdlib-only, otherwise a separate module:
  - `gosaf/http`: `http.RoundTripper` wrapper that classifies by status code and honours `RetryAfter`.
  - `gosaf/grpc` (separate module): client interceptors with status-code classification.
  - `gosaf/otel` and `gosaf/prom` (separate modules): metrics via `Listener`.

## v0.4 — shared state (minor, only if there is demand)

- `Store` port (domain-defined interface) so several instances can share window/state, with an
  explicit consistency contract (what happens on store failure: default fail open vs fail closed
  must be a deliberate, documented choice).
- Reference adapter in a separate module (Redis first). Local breaker remains the default.
- Decision gate: build only after at least one real user needs it. gobreaker's distributed
  variant shows the demand exists in the ecosystem, not that we need it.

## v1.0 — stability

Release criteria, all required:
- API reviewed and frozen; no breaking change in the previous two minor releases.
- Benchmarks and model-based/fuzz suites in CI; zero known races, zero open Important issues.
- `govulncheck`, `staticcheck`, `go vet`, `-race` green on Linux, macOS and Windows.
- Docs: usage guide, tuning guide (how to choose rates/windows), failure-mode guide, migration
  notes from gobreaker.
- Security: documented threat model (what a misbehaving `fn`, clock or `Listener` can and cannot do).

## Explicitly not planned

Retry, bulkhead, rate limiting, hedging, caching. They are different resilience patterns;
compose them with GoSaf rather than growing the core. Revisit only with a design that keeps the
core small.

## How items are chosen

1. A user-visible failure mode or need, written as a test that fails first.
2. Smallest design that fits the layering; new public API is a promise, so prefer fewer methods.
3. Measured, not asserted: performance and safety claims need a benchmark or a failing-then-passing test.
