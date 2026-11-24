package notifications

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	fluvio "github.com/software78/fluvio"

	"github.com/Angle-HR/server/internal/mailer"
)

type fakeTx struct {
	sql  string
	args []any
}

func (f *fakeTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.sql, f.args = sql, args
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func TestEnqueueTxWritesEmailRequestedToOutbox(t *testing.T) {
	t.Parallel()
	tx := &fakeTx{}
	_, err := NewEventEnqueuer().EnqueueTx(t.Context(), tx, mailer.EmailArgs{Type: mailer.TypeEmailVerification, Recipient: "a@b.co", Code: "123456"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.args) != 4 || tx.args[1] != TypeEmailRequested {
		t.Fatalf("outbox row = %v", tx.args)
	}
	var env struct{ Data mailer.EmailArgs }
	if err := json.Unmarshal([]byte(tx.args[3].(string)), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Recipient != "a@b.co" || env.Data.Code != "123456" {
		t.Fatalf("payload = %+v", env.Data)
	}
}

func TestEnqueueTxRejectsOtherJobsAndTransactions(t *testing.T) {
	t.Parallel()
	e := NewEventEnqueuer()
	if _, err := e.EnqueueTx(t.Context(), &fakeTx{}, fluvio.JobArgs(nil)); err == nil {
		t.Fatal("nil args accepted")
	}
	if _, err := e.EnqueueTx(t.Context(), "not a tx", mailer.EmailArgs{Recipient: "a@b.co"}); err == nil {
		t.Fatal("non-tx accepted")
	}
}

func TestStreamMaxAgeFromEnv(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"": StreamMaxAge, "2h": 2 * time.Hour, "30m": 30 * time.Minute, "168h": 168 * time.Hour,
		"bogus": StreamMaxAge, "1s": StreamMaxAge, "9999h": StreamMaxAge, "-1h": StreamMaxAge,
	} {
		t.Setenv("NOTIFICATIONS_STREAM_MAX_AGE", raw)
		if got := StreamMaxAgeFromEnv(); got != want {
			t.Errorf("%q: got %v want %v", raw, got, want)
		}
	}
	t.Setenv("NOTIFICATIONS_STREAM_MAX_AGE", "1h")
	if got := BusConfig("nats://x", "n").DomainMaxAge["notifications"]; got != time.Hour {
		t.Errorf("BusConfig max age = %v", got)
	}
}
