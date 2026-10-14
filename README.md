# GoSaf

**Go System Availability Fuse** — ป้องกันระบบล่มอย่างชาญฉลาด

Circuit breaker library for Go. Small, dependency-free, safe for concurrent use.

> Status: early design. API ยังเปลี่ยนได้.

## Why

เมื่อ dependency (DB, HTTP API, queue) ช้าหรือพัง การเรียกซ้ำๆ ทำให้ระบบเราพังตาม.
GoSaf ตัดวงจรเร็ว (fail fast), ให้ dependency พักฟื้น, แล้วค่อยลองใหม่.

## States

```text
        failures >= threshold
 Closed ───────────────────────▶ Open
   ▲                               │
   │ probe success                 │ open timeout elapsed
   │                               ▼
   └───────────────────────── Half-Open
          probe failure ──────▶ back to Open
```

| State     | Behavior                                          |
|-----------|---------------------------------------------------|
| Closed    | เรียกปกติ, นับ failure                              |
| Open      | reject ทันที ด้วย `ErrOpen`, ไม่เรียก dependency      |
| Half-Open | ปล่อย probe จำนวนจำกัด; สำเร็จ → Closed, พลาด → Open |

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

## Scope

In:
- Closed / Open / Half-Open state machine
- Failure-rate and slow-call-rate tripping over a sliding window
- Open timeout, half-open probes with probe timeout
- Panic-safe, idempotent two-step API (`Allow` / `Permit.Done`)
- `context.Context` support
- Injectable clock (testable)
- State change hook (observability)

Out (YAGNI, จนกว่าจะมีเหตุผลจริง):
- Metrics exporter, bulkhead, retry, rate limit
- Distributed / shared state

## Development

```sh
gofmt -l .
go vet ./...
go tool -modfile=tools/go.mod staticcheck ./...
go tool -modfile=tools/go.mod govulncheck ./...
go test -race -shuffle=on -count=1 ./...
```

ดูหลักการออกแบบและกฎการเขียนโค้ดที่ [AGENTS.md](AGENTS.md).

## License

[MIT](LICENSE). No third-party code is included, so no NOTICE file is needed.
Design was inspired by [sony/gobreaker](https://github.com/sony/gobreaker) (MIT); no code was copied.
