# GoSaf

**Go System Availability Fuse** — ป้องกันระบบล่มอย่างชาญฉลาด

Circuit breaker library for Go. Small, dependency-free, safe for concurrent use.

> Status: early design. API ยังเปลี่ยนได้.

## Why

เมื่อ dependency (DB, HTTP API, queue) ช้าหรือพัง การเรียกซ้ำๆ ทำให้ระบบเราพังตาม.
GoSaf ตัดวงจรเร็ว (fail fast), ให้ dependency พักฟื้น, แล้วค่อยลองใหม่.

## States

```
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

## Planned API (sketch)

```go
cb := gosaf.New(gosaf.Config{
    FailureThreshold: 5,
    OpenTimeout:      30 * time.Second,
    HalfOpenMaxCalls: 1,
})

err := cb.Do(ctx, func(ctx context.Context) error {
    return callPaymentAPI(ctx)
})
if errors.Is(err, gosaf.ErrOpen) {
    // fail fast / fallback
}
```

## Scope

In:
- Closed / Open / Half-Open state machine
- Failure threshold, open timeout, half-open probes
- `context.Context` support
- Injectable clock (testable)
- State change hook (observability)

Out (YAGNI, จนกว่าจะมีเหตุผลจริง):
- Metrics exporter, bulkhead, retry, rate limit
- Distributed / shared state

## Development

```sh
go test -race ./...
go vet ./...
```

ดูหลักการออกแบบและกฎการเขียนโค้ดที่ [AGENTS.md](AGENTS.md).

## License

TBD
