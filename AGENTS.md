# AGENTS.md

Guide for AI agents and humans working on **GoSaf** (Go circuit breaker library).
Read [README.md](README.md) first for scope.

## Principles

### KISS
- Simplest code that works. Prefer plain functions and structs over abstractions.
- Standard library only. No third-party dependency without a written reason.
- Small API surface. Every exported identifier is a promise.

### YAGNI
- Build only what README "Scope: In" lists. No speculative options, hooks, or interfaces.
- No interface until there are 2 real implementations or a test boundary needs one.
- Delete dead code. Do not leave "for later" code.

### Clean Architecture (Onion + Hexagonal + Screaming + DCI)

**Screaming** — package names say the domain (circuit breaking), not the framework.
Top-level layout names the use cases, not `utils/`, `models/`, `helpers/`.

**Onion** — dependencies point inward only:

```
facade    ──▶  usecase  ──▶  domain
(outer)                      (core, no imports from this repo)
```

- `domain`: pure circuit/state rules (states, transitions, counters). No I/O, no `time.Now()`, no goroutines.
- `usecase` (application): orchestrates a call through the breaker (`Do`). Depends on domain and on ports.
- `facade` (root package): validates `Config`, wires usecase + ports, re-exports the public API. No adapters in v1.

**Hexagonal** — ports are small interfaces defined by the *consumer* (inner layer), implemented by the caller or the facade:
- `Clock` port: a plain `func() time.Time` (injected via `Config.Clock`; fake in tests)
- `Listener` port for state-change events (optional)

**DCI** (Data, Context, Interaction):
- *Data*: dumb domain types (`State`, `Counts`, `Config`).
- *Context*: the use case that binds roles for one scenario (e.g. "guarded call").
- *Interaction*: roles as small interfaces/methods the context calls (`Clock`, `Listener`, `Policy`). Behavior lives in the context, not scattered in data types.

Suggested layout (adjust only with reason; keep the root package as the public facade):

```
gosaf/
  gosaf.go           # facade: New, Breaker, Do[T], re-exported types and errors
  config.go          # Config, DefaultConfig, validation
  internal/
    circuit/         # domain: State, Outcome, Window, Policy, Machine
    guard/           # usecase: Allow/Permit/Done, mutex, classification, Listener
  tools/             # separate module pinning staticcheck + govulncheck
```

Rules:
- `internal/` for everything not intentionally public.
- Domain must not import usecase or facade. Usecase must not import facade.
- Wiring happens in the root facade only.

## Testing

Cover what matters, no more. Target behavior, not lines.

- Table-driven tests, `package x_test` where possible.
- Domain: every state transition (Closed→Open, Open→Half-Open, Half-Open→Closed, Half-Open→Open), threshold edge (N-1 vs N), counter reset.
- Usecase: success, failure, `ErrOpen` fast-fail, ctx cancel/deadline, panic in fn, probe limit in Half-Open.
- Time: always use a fake `Clock`. No `time.Sleep` in tests.
- Concurrency: run with `-race`; one test with many goroutines hammering `Do`.
- No mocks libraries. Hand-written fakes.
- Do not test getters, trivial constructors, or the standard library.

Before finishing any change:

```sh
gofmt -l .        # must print nothing
go vet ./...
go tool -modfile=tools/go.mod staticcheck ./...
go tool -modfile=tools/go.mod govulncheck ./...
go test -race -shuffle=on -count=1 ./...
```

Work test-first when adding behavior or fixing a bug (red → green → refactor).

## Secure Coding

- **Concurrency safety**: all shared state guarded (mutex or atomics). No data races (`-race` clean). Never hold a lock while calling user code (`fn`, `Listener`) — deadlock and re-entrancy risk.
- **Fail safe**: invalid state or config → fail closed (reject / return error), never silently allow.
- **Validate input**: `Config` validated in `New`; reject zero/negative thresholds and timeouts with explicit errors. No hidden magic defaults that weaken protection.
- **Panics**: user `fn` may panic. Record as failure, release probe slot (`defer`), then re-panic. Never leave the breaker stuck in Half-Open.
- **Context**: honor `ctx`; never block forever. No goroutine leaks.
- **Resource bounds**: no unbounded growth (queues, maps, history). Fixed-size counters.
- **No sensitive data** in errors, logs, or listener events (no payloads, credentials, tokens). Errors wrap with `%w`, expose sentinels (`ErrOpen`) via `errors.Is`.
- **No `unsafe`, no `reflect`, no `init()` side effects, no global mutable state.**
- **Overflow**: guard counter arithmetic; use fixed-width types deliberately.
- **Dependencies**: none by default. If added: pin version, run `govulncheck ./...`.
- Run `govulncheck ./...` and `go vet ./...` before release.

## Go Style

- Idiomatic Go, `gofmt`, doc comment on every exported identifier.
- Errors are values: sentinel + `errors.Is/As`. No panics for control flow.
- Accept interfaces, return structs. Keep interfaces 1–3 methods.
- Pass `context.Context` first; never store it in a struct.
- Zero value / constructor must be safe and obvious. Functional options only if >3 optional settings (YAGNI until then).
- Keep functions short; comment *why*, not *what*.

## Agent Workflow

1. Check change fits README scope. If not, ask before adding.
2. Pick the layer first (domain / usecase / facade), then write the test, then code.
3. Smallest diff that passes. No drive-by refactors.
4. Run the checks in **Testing** and report real output.
5. Update README when public API or scope changes.
