package secret

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	r := NewRedactor("glpat-abc123")
	got := r.Redact("token glpat-abc123 leaked twice: glpat-abc123")
	if strings.Contains(got, "glpat-abc123") {
		t.Errorf("secret survived: %q", got)
	}
	if want := "token [redacted] leaked twice: [redacted]"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestShortAndEmptyValuesIgnored(t *testing.T) {
	r := NewRedactor("", "ab")
	if got := r.Redact("ab abc"); got != "ab abc" {
		t.Errorf("short value must not be redacted: %q", got)
	}
}

func TestRedactError(t *testing.T) {
	r := NewRedactor("glpat-abc123")

	err := errors.New("401 unauthorized using glpat-abc123")
	clean := r.RedactError(err)
	if strings.Contains(clean.Error(), "glpat-abc123") {
		t.Errorf("secret survived: %v", clean)
	}

	if r.RedactError(nil) != nil {
		t.Error("nil must stay nil")
	}

	sentinel := errors.New("no secret here")
	//nolint:errorlint // asserting identity on purpose: clean errors must not be rewrapped
	if got := r.RedactError(sentinel); got != sentinel {
		t.Error("clean errors must be returned unchanged")
	}
}

func TestLogHandlerRedacts(t *testing.T) {
	r := NewRedactor("glpat-abc123")
	var buf bytes.Buffer
	logger := slog.New(NewLogHandler(slog.NewTextHandler(&buf, nil), r))

	logger.Info("auth failed with glpat-abc123", "token", "glpat-abc123", "attempt", 3)
	logger.WithGroup("gitlab").With("secret", "glpat-abc123").Info("grouped")

	out := buf.String()
	if strings.Contains(out, "glpat-abc123") {
		t.Errorf("secret leaked into log: %s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Errorf("expected redaction marker in: %s", out)
	}
	if !strings.Contains(out, "attempt=3") {
		t.Errorf("non-secret attrs must pass through: %s", out)
	}
}
