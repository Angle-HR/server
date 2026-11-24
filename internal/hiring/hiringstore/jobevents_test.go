package hiringstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobevents"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/pkg/events"
)

// captureArg grabs the envelope argument of the outbox insert.
type captureArg struct{ got *string }

func (c captureArg) Match(v any) bool {
	s, ok := v.(string)
	if ok {
		*c.got = s
	}
	return ok
}

func loader(status string) func() (*jobevents.Input, error) {
	return func() (*jobevents.Input, error) {
		in := &jobevents.Input{Region: "uk", CompanyID: "co-1", Company: jobevents.Company{Name: "Acme"}}
		in.Job = jobs.Job{ID: "job-1", PublicID: "jabc", Status: status, Revision: 4, JobCode: "JB-1"}
		return in, nil
	}
}

func status(to string) *hiringtypes.UpdateResult {
	return &hiringtypes.UpdateResult{Status: &hiringtypes.StatusChange{To: to}}
}

func TestEmitSites(t *testing.T) {
	t.Setenv(jobevents.EnvVar, "on")
	cases := []struct {
		name, from string
		res        *hiringtypes.UpdateResult
		after      string // status the job has after the change
		wantType   string
	}{
		{"publish", jobs.StatusDraft, status(jobs.StatusPublished), jobs.StatusPublished, jobevents.TypePublished},
		{"pause", jobs.StatusPublished, status(jobs.StatusPaused), jobs.StatusPaused, jobevents.TypePaused},
		{"resume", jobs.StatusPaused, status(jobs.StatusPublished), jobs.StatusPublished, jobevents.TypeResumed},
		{"close", jobs.StatusPublished, status(jobs.StatusClosed), jobs.StatusClosed, jobevents.TypeClosed},
		{"expire", jobs.StatusPublished, status(jobs.StatusExpired), jobs.StatusExpired, jobevents.TypeClosed},
		{"closing date edit while live", jobs.StatusPublished, &hiringtypes.UpdateResult{Details: &jobs.Details{}},
			jobs.StatusPublished, jobevents.TypeUpdated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			var env string
			mock.ExpectExec("INSERT INTO events_outbox").
				WithArgs(pgxmock.AnyArg(), tc.wantType, "job-1", captureArg{&env}).
				WillReturnResult(pgxmock.NewResult("INSERT", 1))
			if err = emitJobEvent(context.Background(), mock, "uk", tc.from, tc.res, loader(tc.after)); err != nil {
				t.Fatal(err)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			ev, err := events.Decode([]byte(env))
			if err != nil {
				t.Fatal(err)
			}
			var p jobevents.Payload
			if err = ev.DecodeJSON(&p); err != nil || p.Version != 4 || p.Status != tc.after {
				t.Fatalf("payload: %v %+v", err, p)
			}
			if tc.name == "expire" && p.CloseReason != "expired" {
				t.Fatalf("reason %q", p.CloseReason)
			}
			var raw map[string]any
			_ = json.Unmarshal([]byte(env), &raw)
			if raw["partitionkey"] != "job-1" {
				t.Fatalf("partition key: %v", raw["partitionkey"])
			}
		})
	}
}

func TestEmitWritesNothingWhenOff(t *testing.T) {
	for _, v := range []string{"", "off"} {
		t.Setenv(jobevents.EnvVar, v)
		mock, _ := pgxmock.NewPool() // no expectations: any Exec fails the call
		called := false
		err := emitJobEvent(context.Background(), mock, "uk", jobs.StatusDraft, status(jobs.StatusPublished),
			func() (*jobevents.Input, error) { called = true; return nil, nil })
		if err != nil || called {
			t.Fatalf("%q: err=%v loaded=%v", v, err, called)
		}
		if err = mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEmitIgnoresChangesThatAreNotEvents(t *testing.T) {
	t.Setenv(jobevents.EnvVar, "on")
	mock, _ := pgxmock.NewPool()
	for _, res := range []*hiringtypes.UpdateResult{status(jobs.StatusPendingApproval), {}, {Confirm: &hiringtypes.GateConfirm{}}} {
		if err := emitJobEvent(context.Background(), mock, "uk", jobs.StatusDraft, res,
			func() (*jobevents.Input, error) { t.Fatal("loaded"); return nil, nil }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEmitFailsClosed(t *testing.T) {
	t.Setenv(jobevents.EnvVar, "on")
	mock, _ := pgxmock.NewPool()
	if err := emitJobEvent(context.Background(), mock, "", jobs.StatusPublished, status(jobs.StatusPaused), loader("paused")); err == nil {
		t.Fatal("no region must fail")
	}
	boom := errors.New("org down")
	if err := emitJobEvent(context.Background(), mock, "uk", jobs.StatusPublished, status(jobs.StatusPaused),
		func() (*jobevents.Input, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("load error must abort: %v", err)
	}
	mock.ExpectExec("INSERT INTO events_outbox").WillReturnError(errors.New("db down"))
	if err := emitJobEvent(context.Background(), mock, "uk", jobs.StatusPublished, status(jobs.StatusPaused), loader("paused")); err == nil {
		t.Fatal("enqueue error must abort")
	}
}

func TestMembersChangedEvent(t *testing.T) {
	t.Setenv(jobevents.EnvVar, "on")
	team := []hiringtypes.Member{{UserID: "u-1", Role: "hiring_manager"}, {UserID: "u-2", Role: "collaborator"}}
	load := func() (*jobevents.MembersPayload, error) {
		p := &jobevents.MembersPayload{Version: 6, JobID: "job-1", CompanyID: "co-1", Region: "uk"}
		for _, m := range team {
			p.Members = append(p.Members, jobevents.Member{UserID: m.UserID, Role: m.Role})
		}
		return p, nil
	}
	mock, _ := pgxmock.NewPool()
	var env string
	mock.ExpectExec("INSERT INTO events_outbox").
		WithArgs(pgxmock.AnyArg(), jobevents.TypeMembersChanged, "job-1", captureArg{&env}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := emitMembersChanged(context.Background(), mock, "uk", &hiringtypes.UpdateResult{Members: &team}, load); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	ev, err := events.Decode([]byte(env))
	if err != nil {
		t.Fatal(err)
	}
	var p jobevents.MembersPayload
	if err = ev.DecodeJSON(&p); err != nil || p.Version != 6 || len(p.Members) != 2 || p.Members[0].Role != "hiring_manager" {
		t.Fatalf("payload %v %+v", err, p)
	}
	if ev.ID != jobevents.EventID(jobevents.TypeMembersChanged, "job-1", 6) {
		t.Errorf("id %s is not deterministic", ev.ID)
	}
	// An emptied team is an empty list, never null.
	empty := []hiringtypes.Member{}
	mock.ExpectExec("INSERT INTO events_outbox").WithArgs(pgxmock.AnyArg(), jobevents.TypeMembersChanged, "job-1", captureArg{&env}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err = emitMembersChanged(context.Background(), mock, "uk", &hiringtypes.UpdateResult{Members: &empty},
		func() (*jobevents.MembersPayload, error) {
			return &jobevents.MembersPayload{Version: 7, JobID: "job-1"}, nil
		}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env, `"members":[]`) {
		t.Errorf("empty team must be []: %s", env)
	}
}

func TestMembersChangedOnlyWhenTeamSavedAndEventsOn(t *testing.T) {
	mock, _ := pgxmock.NewPool() // any Exec fails
	team := []hiringtypes.Member{{UserID: "u-1", Role: "hiring_manager"}}
	fail := func() (*jobevents.MembersPayload, error) { t.Fatal("loaded"); return nil, nil }
	t.Setenv(jobevents.EnvVar, "off")
	if err := emitMembersChanged(context.Background(), mock, "uk", &hiringtypes.UpdateResult{Members: &team}, fail); err != nil {
		t.Fatal(err)
	}
	t.Setenv(jobevents.EnvVar, "on")
	if err := emitMembersChanged(context.Background(), mock, "uk", &hiringtypes.UpdateResult{}, fail); err != nil {
		t.Fatal(err)
	}
	if err := emitMembersChanged(context.Background(), mock, "", &hiringtypes.UpdateResult{Members: &team}, fail); err == nil {
		t.Fatal("no region must fail")
	}
}
