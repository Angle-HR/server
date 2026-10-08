package kyb

import (
	"context"
	"errors"
	"testing"
	"time"
)

type sweepStore struct {
	memStore
	recs    map[string]*Record
	due     []string
	notices []string
	flagged map[string]bool
	err     error
}

func (s *sweepStore) Load(_ context.Context, id string) (*Record, error) {
	if r, ok := s.recs[id]; ok {
		c := *r
		return &c, nil
	}
	return nil, nil
}

func (s *sweepStore) DueOrganizations(context.Context, time.Time) ([]string, error) {
	return s.due, s.err
}

func (s *sweepStore) RecordNotice(_ context.Context, id, notice string, _ time.Time) error {
	s.notices = append(s.notices, id+":"+notice)
	s.recs[id].NoticesSent++
	return nil
}

func (s *sweepStore) FlagDeletion(_ context.Context, id string, _ time.Time) (bool, error) {
	if s.flagged[id] {
		return false, nil
	}
	s.flagged[id] = true
	return true, nil
}

var sweepNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func failedAgo(d time.Duration, sent int) *Record {
	at := sweepNow.Add(-d)
	return &Record{Status: StatusFailed, FailureReason: ReasonNumberNotFound, CheckedAt: &at, NoticesSent: sent}
}

func newSweeper(recs map[string]*Record, due ...string) (*Sweeper, *sweepStore, *memNotifier) {
	st := &sweepStore{recs: recs, due: due, flagged: map[string]bool{}}
	n := &memNotifier{}
	return &Sweeper{Store: st, Notifier: n, Now: func() time.Time { return sweepNow }}, st, n
}

func TestSweep_sendsFirstAndSecondNudge(t *testing.T) {
	sw, st, n := newSweeper(map[string]*Record{
		"a": failedAgo(3*24*time.Hour, 0),
		"b": failedAgo(8*24*time.Hour, 1),
		"c": failedAgo(24*time.Hour, 0), // too early
	}, "a", "b", "c")

	res, err := sw.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Nudged != 2 || res.Flagged != 0 || res.Errors != 0 {
		t.Fatalf("result: %+v", res)
	}
	if len(n.sent) != 2 || n.sent[0] != NoticeNudge1 || n.sent[1] != NoticeNudge2 {
		t.Fatalf("sent: %v", n.sent)
	}
	if len(st.notices) != 2 {
		t.Fatalf("recorded: %v", st.notices)
	}
}

func TestSweep_thirtyDaysOnlyFlags(t *testing.T) {
	sw, st, n := newSweeper(map[string]*Record{"a": failedAgo(31*24*time.Hour, 2)}, "a")

	res, err := sw.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Flagged != 1 || res.Nudged != 0 || len(n.sent) != 0 {
		t.Fatalf("result: %+v sent: %v", res, n.sent)
	}
	if !st.flagged["a"] {
		t.Fatal("account was not flagged")
	}
	// A second sweep must not report the same account as newly flagged.
	res, err = sw.Run(context.Background())
	if err != nil || res.Flagged != 0 {
		t.Fatalf("second sweep: %+v, %v", res, err)
	}
}

func TestSweep_failedEmailIsNotCountedAndSweepContinues(t *testing.T) {
	sw, st, n := newSweeper(map[string]*Record{
		"a": failedAgo(3*24*time.Hour, 0),
		"b": failedAgo(3*24*time.Hour, 0),
	}, "a", "b")
	n.err = errors.New("smtp down")

	res, err := sw.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors != 2 || res.Nudged != 0 || len(st.notices) != 0 {
		t.Fatalf("result: %+v recorded: %v", res, st.notices)
	}
}

func TestSweep_listError(t *testing.T) {
	sw, st, _ := newSweeper(map[string]*Record{})
	st.err = errors.New("db down")
	if _, err := sw.Run(context.Background()); err == nil {
		t.Fatal("want error")
	}
}
