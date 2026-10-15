# GoSaf

**Go System Availability Fuse** — keep your service up when a dependency goes down.

A circuit breaker library for Go. Small, no dependencies, safe to use from many goroutines.

> Status: early (v0.x). The API may still change between minor versions.

## Why

When a dependency (a database, an HTTP API, a queue) becomes slow or starts failing, calling it
again and again makes things worse: requests pile up and your own service fails too.

GoSaf stops the calls early (fail fast), gives the dependency time to recover, and then tests it
carefully before sending full traffic again.

## Install

```sh
go get github.com/gophernment/gosaf
```

Requires Go 1.27.2 or newer.

## How it works

```text
          too many failures or slow calls
  Closed ───────────────────────────────▶ Open
    ▲                                       │
    │ probes succeed                        │ open timeout passes
    │                                       ▼
    └─────────────────────────────────── Half-Open
              probes fail ──────────────▶ back to Open
```

| State     | What happens                                                                     |
|-----------|----------------------------------------------------------------------------------|
| Closed    | Calls go through. GoSaf counts failures and slow calls.                          |
| Open      | Calls are rejected at once with `ErrOpen`. The dependency is not called.         |
| Half-Open | A few test calls (probes) go through. If they succeed the circuit closes; if too many fail it opens again. |

## Usage

```go
cb, err := gosaf.New(gosaf.DefaultConfig())
if err != nil {
    return err // a bad config is rejected, never silently changed
}

res, err := gosaf.Do(ctx, cb, func(ctx context.Context) (string, error) {
    return callPaymentAPI(ctx)
})
switch {
case errors.Is(err, gosaf.ErrOpen), errors.Is(err, gosaf.ErrHalfOpenFull):
    // The circuit refused the call. Fail fast or use a fallback.
    // errors.As to *gosaf.RejectedError tells you when to retry (RetryAfter).
case err != nil:
    // The call itself failed.
}
```

### Configuration

Start from `gosaf.DefaultConfig()` and change what you need. `gosaf.New` checks every value and
returns an error if something is out of range.

- **Window**: judge the last N calls, or the calls in the last period of time.
- **Trip rules**: failure rate, slow-call rate (with `MinCalls`), or N failures in a row (`ConsecutiveFailures`).
- **Recovery**: how long to stay open (`OpenTimeout`), how many probes to allow (`HalfOpenMaxCalls`),
  and how long a probe may run (`ProbeTimeout`).
- **`IsFailure`**: choose which errors count as failures. For example, ignore "not found".
  `context.Canceled` is ignored by default, because it means the caller gave up.
- **`Listener`**: get notified about state changes and call results, for logs or metrics.
- **`Clock`**: replace the clock in tests.

If you need more control than `Do`, use `Allow`, which returns a `Permit`. Call `Permit.Done(err)`
when the call ends. It is safe to call more than once.

### Operating the breaker

- `cb.Snapshot()` returns the state, mode, counts, the number of rejected calls and the time of the last state change.
- `cb.ForceOpen()` rejects every call until `cb.Reset()`. Use it to shed load during an incident.
- `cb.Disable()` turns the breaker into a pass-through until `cb.Reset()`. It removes protection, so use it only if the breaker itself is the problem.
- Implement `gosaf.RejectListener` on your `Listener` to be told about every rejected call.
- `gosafslog.NewListener(logger)` logs state changes, calls and rejections with `log/slog`.

## What is included

- Closed / Open / Half-Open state machine
- Sliding window that uses a fixed amount of memory
- Trip on failure rate, slow-call rate, or consecutive failures
- Probes in Half-Open with a time limit, so a stuck probe cannot block recovery
- Safe against panics in your function
- `context.Context` support
- Injectable clock for tests
- Listener for state changes and call results
- Force open and disable switches for incidents
- Optional log/slog integration (package gosafslog)

## What is not included

GoSaf stays small on purpose. These are not planned for the core:

- Retries, bulkheads, rate limiting
- Metrics exporters and HTTP/gRPC integrations (see the roadmap: they would be separate packages)
- Shared state across several instances

## Development

```sh
gofmt -l .
go vet ./...
go tool -modfile=tools/go.mod staticcheck ./...
go tool -modfile=tools/go.mod govulncheck ./...
go test -race -shuffle=on -count=1 ./...
```

- Design rules and coding guidelines: [AGENTS.md](AGENTS.md)
- Plans for future versions: [ROADMAP.md](ROADMAP.md)
- Changes between versions: [CHANGELOG.md](CHANGELOG.md)
- Report a security problem: [SECURITY.md](SECURITY.md)

## License

[MIT](LICENSE). No third-party code is included, so no NOTICE file is needed.
The design was inspired by [sony/gobreaker](https://github.com/sony/gobreaker) (MIT); no code was copied.
