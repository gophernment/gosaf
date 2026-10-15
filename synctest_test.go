package gosaf_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gophernment/gosaf"
)

// These tests use the real default clock inside a synctest bubble, where time
// is fake and only advances when every goroutine is blocked. No Clock override.

func TestHungProbeReopensTheCircuit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := gosaf.DefaultConfig()
		cfg.MinCalls = 2
		cfg.HalfOpenMaxCalls = 1
		cfg.OpenTimeout = 10 * time.Second
		cfg.ProbeTimeout = 3 * time.Second
		b, err := gosaf.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		failOnce(b)
		failOnce(b)

		time.Sleep(10 * time.Second)
		probe, err := b.Allow(context.Background())
		if err != nil {
			t.Fatalf("probe after OpenTimeout: %v", err)
		}

		time.Sleep(3 * time.Second) // the probe never reports back
		if _, err := b.Allow(context.Background()); !errors.Is(err, gosaf.ErrOpen) {
			t.Fatalf("err = %v, want ErrOpen once the probe timed out", err)
		}

		probe.Done(nil) // late result must not close the circuit
		if got := b.State(); got != gosaf.Open {
			t.Fatalf("State = %v, want open", got)
		}
	})
}

func TestTimeWindowForgetsOldFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := gosaf.DefaultConfig()
		cfg.WindowSize = 6
		cfg.WindowPeriod = time.Minute
		cfg.MinCalls = 2
		b, err := gosaf.New(cfg)
		if err != nil {
			t.Fatal(err)
		}

		failOnce(b)
		time.Sleep(2 * time.Minute) // the first failure leaves the window
		failOnce(b)

		if got := b.State(); got != gosaf.Closed {
			t.Fatalf("State = %v, want closed: only one failure is inside the window", got)
		}
	})
}
