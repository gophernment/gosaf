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
