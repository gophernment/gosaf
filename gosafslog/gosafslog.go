// Package gosafslog connects a gosaf.Breaker to log/slog.
package gosafslog

import (
	"context"
	"log/slog"

	"github.com/gophernment/gosaf"
)

// NewListener returns a gosaf.Listener that logs through l (slog.Default if l is
// nil). State changes are logged at Warn when the circuit opens and at Info
// otherwise; calls and rejections are logged at Debug. It never logs error text
// or payloads.
func NewListener(l *slog.Logger) gosaf.Listener {
	if l == nil {
		l = slog.Default()
	}
	return &listener{log: l}
}

type listener struct {
	log *slog.Logger
}

func (l *listener) OnStateChange(ev gosaf.StateChange) {
	level := slog.LevelInfo
	if ev.To == gosaf.Open {
		level = slog.LevelWarn
	}
	l.emit(level, "circuit state changed",
		slog.String("name", ev.Name),
		slog.String("from", ev.From.String()),
		slog.String("to", ev.To.String()),
		slog.Time("at", ev.At))
}

func (l *listener) OnCall(ev gosaf.CallEvent) {
	l.emit(slog.LevelDebug, "circuit call",
		slog.String("name", ev.Name),
		slog.String("outcome", ev.Outcome.String()),
		slog.Duration("duration", ev.Duration))
}

func (l *listener) OnReject(ev gosaf.RejectEvent) {
	l.emit(slog.LevelDebug, "circuit call rejected",
		slog.String("name", ev.Name),
		slog.String("state", ev.State.String()))
}

func (l *listener) emit(level slog.Level, msg string, attrs ...slog.Attr) {
	ctx := context.Background()
	if l.log.Enabled(ctx, level) {
		l.log.LogAttrs(ctx, level, msg, attrs...)
	}
}
