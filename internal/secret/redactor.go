// Package secret keeps secret values (GitLab tokens, API keys) out of logs,
// errors, and any user-facing output.
package secret

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

const placeholder = "[redacted]"

// Redactor replaces registered secret values with a placeholder wherever they
// appear in strings, errors, or log records. It is safe for concurrent use.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

// NewRedactor returns a Redactor for the given values. Empty values are ignored.
func NewRedactor(values ...string) *Redactor {
	r := &Redactor{}
	for _, v := range values {
		r.Add(v)
	}
	return r
}

// Add registers another secret value to redact. Empty and very short values
// are ignored: redacting them would mangle unrelated text.
func (r *Redactor) Add(value string) {
	if len(value) < 4 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secrets = append(r.secrets, value)
}

// Redact returns s with all registered secrets replaced.
func (r *Redactor) Redact(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, sec := range r.secrets {
		s = strings.ReplaceAll(s, sec, placeholder)
	}
	return s
}

// RedactError returns an error whose message has secrets redacted. The
// original error chain is preserved via %w only when nothing was redacted;
// otherwise the sanitized message replaces the chain so the secret cannot be
// recovered through Unwrap.
func (r *Redactor) RedactError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	clean := r.Redact(msg)
	if clean == msg {
		return err
	}
	return fmt.Errorf("%s", clean)
}

// LogHandler wraps a slog.Handler so every message and attribute value is
// redacted before it reaches the underlying handler.
type LogHandler struct {
	inner    slog.Handler
	redactor *Redactor
}

// NewLogHandler wraps inner with redaction from r.
func NewLogHandler(inner slog.Handler, r *Redactor) *LogHandler {
	return &LogHandler{inner: inner, redactor: r}
}

func (h *LogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *LogHandler) Handle(ctx context.Context, rec slog.Record) error {
	clean := slog.NewRecord(rec.Time, rec.Level, h.redactor.Redact(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(h.redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = h.redactAttr(a)
	}
	return &LogHandler{inner: h.inner.WithAttrs(out), redactor: h.redactor}
}

func (h *LogHandler) WithGroup(name string) slog.Handler {
	return &LogHandler{inner: h.inner.WithGroup(name), redactor: h.redactor}
}

func (h *LogHandler) redactAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(h.redactor.Redact(a.Value.String()))
	case slog.KindGroup:
		grp := a.Value.Group()
		attrs := make([]slog.Attr, len(grp))
		for i, g := range grp {
			attrs[i] = h.redactAttr(g)
		}
		a.Value = slog.GroupValue(attrs...)
	default:
		// Non-string kinds (ints, bools, durations) cannot hold a token.
		if s, ok := a.Value.Any().(fmt.Stringer); ok {
			a.Value = slog.StringValue(h.redactor.Redact(s.String()))
		}
	}
	return a
}
