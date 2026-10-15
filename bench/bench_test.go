// Package bench compares GoSaf with sony/gobreaker. It is a separate module so
// that gobreaker never becomes a dependency of GoSaf itself.
package bench

import (
	"context"
	"errors"
	"testing"

	"github.com/gophernment/gosaf"
	"github.com/sony/gobreaker/v2"
)

var errFail = errors.New("fail")

func newGoSaf(tb testing.TB, minCalls int) *gosaf.Breaker {
	tb.Helper()
	cfg := gosaf.DefaultConfig()
	cfg.MinCalls = minCalls
	b, err := gosaf.New(cfg)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

func newGobreaker(alwaysTrip bool) *gobreaker.CircuitBreaker[int] {
	st := gobreaker.Settings{Name: "bench"}
	if alwaysTrip {
		st.ReadyToTrip = func(gobreaker.Counts) bool { return true }
	}
	return gobreaker.NewCircuitBreaker[int](st)
}

func okGoSaf(context.Context) (int, error) { return 1, nil }
func okGobreaker() (int, error)            { return 1, nil }
func failGoSaf(context.Context) (int, error) {
	return 0, errFail
}
func failGobreaker() (int, error) { return 0, errFail }

// --- gate benchmarks (serial): ClosedSuccess and OpenReject -----------------

func BenchmarkGoSafClosedSuccess(b *testing.B) {
	cb, ctx := newGoSaf(b, 20), context.Background()
	b.ReportAllocs()
	for b.Loop() {
		gosaf.Do(ctx, cb, okGoSaf)
	}
}

func BenchmarkGobreakerClosedSuccess(b *testing.B) {
	cb := newGobreaker(false)
	b.ReportAllocs()
	for b.Loop() {
		cb.Execute(okGobreaker)
	}
}

func BenchmarkGoSafOpenReject(b *testing.B) {
	cb, ctx := newGoSaf(b, 1), context.Background()
	gosaf.Do(ctx, cb, failGoSaf) // trips the circuit
	b.ReportAllocs()
	for b.Loop() {
		gosaf.Do(ctx, cb, okGoSaf)
	}
}

func BenchmarkGobreakerOpenReject(b *testing.B) {
	cb := newGobreaker(true)
	cb.Execute(failGobreaker) // trips the circuit
	b.ReportAllocs()
	for b.Loop() {
		cb.Execute(okGobreaker)
	}
}

// --- informational benchmarks ------------------------------------------------

func BenchmarkGoSafClosedFailure(b *testing.B) {
	cb, ctx := newGoSaf(b, 1_000_000), context.Background() // never trips
	b.ReportAllocs()
	for b.Loop() {
		gosaf.Do(ctx, cb, failGoSaf)
	}
}

func BenchmarkGobreakerClosedFailure(b *testing.B) {
	cb := gobreaker.NewCircuitBreaker[int](gobreaker.Settings{ReadyToTrip: func(gobreaker.Counts) bool { return false }})
	b.ReportAllocs()
	for b.Loop() {
		cb.Execute(failGobreaker)
	}
}

func BenchmarkGoSafClosedSuccessParallel(b *testing.B) {
	cb, ctx := newGoSaf(b, 20), context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			gosaf.Do(ctx, cb, okGoSaf)
		}
	})
}

func BenchmarkGobreakerClosedSuccessParallel(b *testing.B) {
	cb := newGobreaker(false)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cb.Execute(okGobreaker)
		}
	})
}

func BenchmarkGoSafOpenRejectParallel(b *testing.B) {
	cb, ctx := newGoSaf(b, 1), context.Background()
	gosaf.Do(ctx, cb, failGoSaf) // trips the circuit
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			gosaf.Do(ctx, cb, okGoSaf)
		}
	})
}

func BenchmarkGobreakerOpenRejectParallel(b *testing.B) {
	cb := newGobreaker(true)
	cb.Execute(failGobreaker) // trips the circuit
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cb.Execute(okGobreaker)
		}
	})
}
