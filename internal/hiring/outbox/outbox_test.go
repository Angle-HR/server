package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/outbox"
	"github.com/Angle-HR/server/internal/mailer"
)

var now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

type row struct {
	outbox.Email
	status string
	next   time.Time
	reason string
}

type fakeStore struct{ rows map[string]*row }

func (f *fakeStore) Lease(_ context.Context, tid string, at time.Time, limit int, lease time.Duration) ([]outbox.Email, error) {
	var out []outbox.Email
	for _, r := range f.rows {
		if r.status == "pending" && !r.next.After(at) && len(out) < limit {
			r.Attempts++
			r.next = at.Add(lease)
			out = append(out, r.Email)
		}
	}
	return out, nil
}
func (f *fakeStore) MarkSent(_ context.Context, _, id string, _ time.Time) error {
	f.rows[id].status = "sent"
	return nil
}
func (f *fakeStore) MarkFailed(_ context.Context, _, id, reason string, final bool, next time.Time) error {
	r := f.rows[id]
	r.reason = reason
	if final {
		r.status = "failed"
	} else {
		r.next = next
	}
	return nil
}

type fakeSender struct {
	sent []mailer.EmailArgs
	err  error
}

func (s *fakeSender) Send(_ context.Context, a mailer.EmailArgs) error {
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, a)
	return nil
}

func data(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func newStore(rows ...*row) *fakeStore {
	f := &fakeStore{rows: map[string]*row{}}
	for _, r := range rows {
		r.status, r.next = "pending", now.Add(-time.Minute)
		f.rows[r.ID] = r
	}
	return f
}

func worker(st *fakeStore, s *fakeSender, clock *time.Time) *outbox.Worker {
	return &outbox.Worker{
		Tenants: func(context.Context) ([]string, error) { return []string{"org-1"}, nil },
		Store:   st, Sender: s, Now: func() time.Time { return *clock },
	}
}

func TestSendsAndMarksSent(t *testing.T) {
	st := newStore(&row{Email: outbox.Email{ID: "e1", Kind: "application_received", Recipient: "ada@example.com", Name: "Ada",
		Data: data(map[string]any{"job_title": "Engineer", "job_code": "JB-1", "company": "Acme", "notice_url": "https://x/privacy"})}})
	s := &fakeSender{}
	clock := now
	res, err := worker(st, s, &clock).Run(context.Background())
	if err != nil || res.Sent != 1 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	a := s.sent[0]
	if a.Type != mailer.TypeApplicationReceived || a.Recipient != "ada@example.com" || a.FullName != "Ada" || a.OrganizationName != "Acme" ||
		a.JobTitle != "Engineer" || a.JobCode != "JB-1" || a.NoticeURL != "https://x/privacy" || a.Subject != "We received your application for Engineer" {
		t.Errorf("args = %+v", a)
	}
	if st.rows["e1"].status != "sent" {
		t.Error("not marked sent")
	}
	// A second run finds nothing: the email is not sent twice.
	if res, _ = worker(st, s, &clock).Run(context.Background()); res.Sent != 0 || len(s.sent) != 1 {
		t.Errorf("second run sent again: %+v", res)
	}
}

func TestRetriesWithBackoffThenGivesUp(t *testing.T) {
	st := newStore(&row{Email: outbox.Email{ID: "e1", Kind: "application_rejected", Recipient: "ada@example.com"}})
	s := &fakeSender{err: errors.New("smtp: 451 try later")}
	clock := now
	w := worker(st, s, &clock)
	w.MaxAttempts = 3

	res, _ := w.Run(context.Background())
	if res.Retry != 1 || st.rows["e1"].status != "pending" || !st.rows["e1"].next.Equal(now.Add(time.Minute)) {
		t.Fatalf("first failure: %+v next=%v", res, st.rows["e1"].next)
	}
	// Not due again until the delay has passed.
	if res, _ = w.Run(context.Background()); res.Retry != 0 || res.Sent != 0 {
		t.Errorf("retried too early: %+v", res)
	}
	clock = clock.Add(2 * time.Minute)
	res, _ = w.Run(context.Background())
	if res.Retry != 1 || !st.rows["e1"].next.Equal(clock.Add(5*time.Minute)) {
		t.Errorf("second failure: %+v next=%v", res, st.rows["e1"].next)
	}
	clock = clock.Add(10 * time.Minute)
	res, _ = w.Run(context.Background())
	if res.Failed != 1 || st.rows["e1"].status != "failed" || !strings.Contains(st.rows["e1"].reason, "451") {
		t.Errorf("third failure must give up: %+v %+v", res, st.rows["e1"])
	}
	clock = clock.Add(24 * time.Hour)
	if res, _ = w.Run(context.Background()); res.Retry+res.Failed+res.Sent != 0 {
		t.Errorf("a failed email must not be tried again: %+v", res)
	}
}

func TestArgsKinds(t *testing.T) {
	a, err := outbox.Args(outbox.Email{Kind: "retention_notice", Recipient: "hr@acme.test", Data: data(map[string]any{
		"company": "Acme",
		"items":   []map[string]any{{"job_code": "JB-1", "job_title": "Engineer", "count": 2}, {"job_code": "JB-2", "job_title": "Designer", "count": 1}},
	})})
	if err != nil || a.Type != mailer.TypeRetentionNotice || a.Count != 3 || len(a.Lines) != 2 ||
		a.Lines[0] != "JB-1 Engineer: 2 applications" || a.Lines[1] != "JB-2 Designer: 1 application" {
		t.Errorf("retention notice args = %+v err=%v", a, err)
	}
	if a, _ = outbox.Args(outbox.Email{Kind: "application_hired", Recipient: "x@y.test", Data: data(map[string]any{"job_title": "T"})}); a.Type != mailer.TypeApplicationHired {
		t.Errorf("hired = %+v", a)
	}
	for name, e := range map[string]outbox.Email{
		"unknown kind": {Kind: "marketing", Recipient: "x@y.test"},
		"no recipient": {Kind: "application_received", Recipient: " "},
		"bad data":     {Kind: "application_received", Recipient: "x@y.test", Data: json.RawMessage(`{`)},
	} {
		if _, err := outbox.Args(e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
