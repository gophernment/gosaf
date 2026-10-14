# Changelog

All notable changes are documented here. Versions follow [Semantic Versioning](https://semver.org/);
while the major version is 0, the API may change between minor versions.

## [Unreleased]

### Added
- `Config.ConsecutiveFailures`: trip after N failures in a row, independent of `MinCalls` (0 disables it).

### Changed
- `OpenTimeout` and `ProbeTimeout` must not exceed `MaxTimeout` (24h); larger values are rejected by `New`.
- Documented `Listener` execution and ordering, lazy `State`/`Snapshot`, and the panic re-raise contract.
- Half-Open re-opens as soon as the probes seen so far make recovery impossible, instead of waiting
  for every probe to finish.

## [0.1.0] - 2026-10-14

First release.

### Added
- Circuit breaker with Closed / Open / Half-Open states.
- Count-based and time-based sliding windows (bounded memory).
- Trip on failure rate and slow-call rate, with `MinCalls`.
- `gosaf.Do[T]` and the two-step `Allow` / `Permit.Done` API (idempotent, panic- and `Goexit`-safe).
- Half-Open probe limit with `ProbeTimeout`; hung probes re-open the circuit.
- `context.Context` support; `context.Canceled` is excluded from statistics by default.
- Validated `Config` (`ErrInvalidConfig`), `DefaultConfig`, injectable `Clock`, `IsFailure`.
- `Listener` for state changes and call results, delivered outside the lock.
- `Snapshot`, `State`, `Reset`.

[0.1.0]: https://github.com/gophernment/gosaf/releases/tag/v0.1.0
