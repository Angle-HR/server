package besteffort

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestLogNilAndClosedTxAreSilent(t *testing.T) {
	buf := capture(t)
	Log(context.Background(), "x", nil)
	Log(context.Background(), "x", pgx.ErrTxClosed)
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
}

func TestLogRecordsError(t *testing.T) {
	buf := capture(t)
	Log(context.Background(), "close redis", errors.New("boom"))
	if out := buf.String(); !strings.Contains(out, "close redis") || !strings.Contains(out, "boom") {
		t.Fatalf("unexpected output %q", out)
	}
}
