package gosafslog_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/gophernment/gosaf"
	"github.com/gophernment/gosaf/gosafslog"
)

func newLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey || a.Key == "at" || a.Key == "duration" {
				return slog.Attr{}
			}
			return a
		},
	}))
}

func TestListenerLogsStateChangesCallsAndRejections(t *testing.T) {
	var buf bytes.Buffer
	cfg := gosaf.DefaultConfig()
	cfg.Name = "db"
	cfg.MinCalls = 1
	cfg.Listener = gosafslog.NewListener(newLogger(&buf))
	b, err := gosaf.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	secret := errors.New("password=hunter2")
	fn := func(context.Context) (int, error) { return 0, secret }
	gosaf.Do(context.Background(), b, fn) // fails, opens the circuit
	gosaf.Do(context.Background(), b, fn) // rejected

	out := buf.String()
	for _, want := range []string{
		`level=WARN msg="circuit state changed" name=db from=closed to=open`,
		`level=DEBUG msg="circuit call" name=db outcome=failure`,
		`level=DEBUG msg="circuit call rejected" name=db state=open`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q\n--- got ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "hunter2") {
		t.Errorf("log output leaked error text:\n%s", out)
	}
}

func TestListenerUsesInfoWhenCircuitRecovers(t *testing.T) {
	var buf bytes.Buffer
	cfg := gosaf.DefaultConfig()
	cfg.MinCalls = 1
	cfg.Listener = gosafslog.NewListener(newLogger(&buf))
	b, err := gosaf.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b.ForceOpen()
	b.Reset()

	if !strings.Contains(buf.String(), `level=INFO msg="circuit state changed"`) ||
		!strings.Contains(buf.String(), "to=closed") {
		t.Fatalf("want an INFO line for the close, got:\n%s", buf.String())
	}
}

func TestNilLoggerFallsBackToDefault(t *testing.T) {
	if gosafslog.NewListener(nil) == nil {
		t.Fatal("NewListener(nil) must return a usable listener")
	}
}
