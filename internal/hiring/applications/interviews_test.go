package applications

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/rbac"
)

type fakeSchedule struct {
	InterviewStore
	created []*Interview
	msgs    []*NewMessage
	saved   []*Interview
	events  []string
	cur     *Interview
	stale   bool
	mine    []Interview
}

func (f *fakeSchedule) CreateInterview(_ context.Context, _, _ string, iv *Interview, m *NewMessage) error {
	f.created = append(f.created, iv)
	if m != nil {
		f.msgs = append(f.msgs, m)
	}
	return nil
}
func (f *fakeSchedule) Interview(context.Context, string, string, string, string) (*Interview, error) {
	if f.cur == nil {
		return nil, ErrNotFound
	}
	c := *f.cur
	return &c, nil
}
func (f *fakeSchedule) SaveInterview(_ context.Context, _, _ string, iv *Interview, _ int, ev string, m *NewMessage) error {
	if f.stale {
		return ErrStale
	}
	f.saved = append(f.saved, iv)
	f.events = append(f.events, ev)
	if m != nil {
		f.msgs = append(f.msgs, m)
	}
	return nil
}
func (f *fakeSchedule) Interviews(context.Context, string, string, string) ([]Interview, error) {
	return []Interview{{ID: "i1"}}, nil
}
func (f *fakeSchedule) MyInterviews(context.Context, string, string, time.Time, time.Time, int) ([]Interview, error) {
	return f.mine, nil
}

func interviewSvc(t *testing.T) (*Service, *fakeStore, *fakeSchedule, *fakeMail) {
	t.Helper()
	s, st, mail := mailSvc(t)
	sc := &fakeSchedule{cur: &Interview{ID: "55555555-1111-4111-8111-111111111111", JobID: job, ApplicationID: app,
		StartsAt: s.now().Add(48 * time.Hour), DurationMin: 60, Timezone: "Europe/London", Kind: KindVideo,
		Location: "https://meet.example/x", Status: InterviewScheduled, InterviewerIDs: []string{}}}
	s.Schedule = sc
	return s, st, sc, mail
}

func goodInterview(s *Service) InterviewInput {
	return InterviewInput{StartsAt: s.now().Add(72 * time.Hour), Timezone: "Europe/London", Kind: KindVideo, Location: "https://meet.example/abc"}
}

func TestScheduleInterviewWithInvite(t *testing.T) {
	s, _, sc, _ := interviewSvc(t)
	in := goodInterview(s)
	in.Invite, in.InterviewerIDs = true, []string{owner, owner}
	iv, err := s.ScheduleInterview(context.Background(), decider(), job, app, in)
	if err != nil {
		t.Fatal(err)
	}
	if iv.DurationMin != DefaultDuration || !iv.EndsAt.Equal(iv.StartsAt.Add(time.Hour)) || len(iv.InterviewerIDs) != 1 || iv.Status != InterviewScheduled {
		t.Errorf("interview %+v", iv)
	}
	m := sc.msgs[0]
	if !strings.Contains(m.ICS, "METHOD:REQUEST") || !strings.Contains(m.ICS, "UID:"+iv.ID+"@openhr") || !strings.Contains(m.ICS, "mailto:jobs@acme.test") ||
		!strings.Contains(m.Body, "Video call: https://meet.example/abc") || !strings.Contains(m.Body, "(Europe/London)") || m.Enforce || m.ReplyTo != "jobs@acme.test" {
		t.Errorf("message %+v\n%s", m, m.ICS)
	}
}

func TestInviteNeedsAReplyTo(t *testing.T) {
	s, _, sc, mail := interviewSvc(t)
	mail.reply = ""
	in := goodInterview(s)
	in.Invite = true
	if _, err := s.ScheduleInterview(context.Background(), decider(), job, app, in); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("invite without reply-to: %v", err)
	}
	if len(sc.created) != 0 {
		t.Error("nothing is saved when the invite cannot be written")
	}
	in.Invite = false
	if _, err := s.ScheduleInterview(context.Background(), decider(), job, app, in); err != nil {
		t.Errorf("no invite: %v", err)
	}
}

func TestInterviewValidation(t *testing.T) {
	s, _, _, _ := interviewSvc(t)
	base := goodInterview(s)
	cases := map[string]func(*InterviewInput){
		"past":              func(i *InterviewInput) { i.StartsAt = s.now().Add(-time.Hour) },
		"too far":           func(i *InterviewInput) { i.StartsAt = s.now().Add(400 * 24 * time.Hour) },
		"zero time":         func(i *InterviewInput) { i.StartsAt = time.Time{} },
		"short":             func(i *InterviewInput) { i.DurationMin = 5 },
		"long":              func(i *InterviewInput) { i.DurationMin = 600 },
		"bad zone":          func(i *InterviewInput) { i.Timezone = "Mars/Base" },
		"no zone":           func(i *InterviewInput) { i.Timezone = "" },
		"bad kind":          func(i *InterviewInput) { i.Kind = "teleport" },
		"video not https":   func(i *InterviewInput) { i.Location = "http://meet.example/x" },
		"video not a link":  func(i *InterviewInput) { i.Location = "my office" },
		"onsite no address": func(i *InterviewInput) { i.Kind, i.Location = KindOnsite, "" },
		"bad interviewer":   func(i *InterviewInput) { i.InterviewerIDs = []string{"nope"} },
		"outsider":          func(i *InterviewInput) { i.InterviewerIDs = []string{other} },
		"too many":          func(i *InterviewInput) { i.InterviewerIDs = make([]string, MaxInterviewers+1) },
		"long notes":        func(i *InterviewInput) { i.Notes = strings.Repeat("a", MaxInterviewNotes+1) },
	}
	for name, mut := range cases {
		in := base
		mut(&in)
		if _, err := s.ScheduleInterview(context.Background(), decider(), job, app, in); !isCode(err, apperror.CodeValidationError) {
			t.Errorf("%s: %v", name, err)
		}
	}
	in := base
	in.Kind, in.Location = KindPhone, ""
	if _, err := s.ScheduleInterview(context.Background(), decider(), job, app, in); err != nil {
		t.Errorf("phone without a number: %v", err)
	}
}

func TestInterviewPermissionsAndFinalApplications(t *testing.T) {
	s, st, _, _ := interviewSvc(t)
	if _, err := s.ScheduleInterview(context.Background(), caller(other, rbac.JobViewAll), job, app, goodInterview(s)); !isCode(err, apperror.CodeForbidden) {
		t.Errorf("viewer: %v", err)
	}
	if _, err := s.Interviews(context.Background(), caller(other, rbac.JobViewAll), job, app); err != nil {
		t.Errorf("a viewer lists: %v", err)
	}
	st.state.Status = StatusRejected
	if _, err := s.ScheduleInterview(context.Background(), decider(), job, app, goodInterview(s)); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("decided application: %v", err)
	}
}

func TestRescheduleBumpsSequenceAndSendsTheUpdate(t *testing.T) {
	s, _, sc, _ := interviewSvc(t)
	at := s.now().Add(96 * time.Hour)
	iv, err := s.UpdateInterview(context.Background(), decider(), job, app, sc.cur.ID, InterviewPatch{StartsAt: &at, Invite: true, InviteNote: "Sorry for the change"})
	if err != nil {
		t.Fatal(err)
	}
	if iv.Sequence != 1 || !iv.StartsAt.Equal(at.UTC()) || sc.events[0] != EventInterviewRescheduled {
		t.Errorf("%+v %v", iv, sc.events)
	}
	m := sc.msgs[0]
	if !strings.Contains(m.ICS, "SEQUENCE:1") || !strings.Contains(m.Body, "have changed") || !strings.Contains(m.Body, "Sorry for the change") {
		t.Errorf("update message %q\n%s", m.Body, m.ICS)
	}
	// unchanged fields stay
	if iv.Location != "https://meet.example/x" || iv.Kind != KindVideo {
		t.Errorf("fields changed: %+v", iv)
	}
}

func TestUpdateRulesAndConcurrency(t *testing.T) {
	s, _, sc, _ := interviewSvc(t)
	// an interview that already started can be edited without moving it to the future
	past := Interview{ID: sc.cur.ID, JobID: job, ApplicationID: app, StartsAt: s.now().Add(-time.Hour), DurationMin: 60, Timezone: "UTC", Kind: KindPhone, Status: InterviewScheduled}
	sc.cur = &past
	n := "new notes"
	if _, err := s.UpdateInterview(context.Background(), decider(), job, app, past.ID, InterviewPatch{Notes: &n}); err != nil {
		t.Errorf("notes of a started interview: %v", err)
	}
	moved := s.now().Add(-time.Minute)
	if _, err := s.UpdateInterview(context.Background(), decider(), job, app, past.ID, InterviewPatch{StartsAt: &moved}); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("moving to the past: %v", err)
	}
	sc.stale = true
	if _, err := s.UpdateInterview(context.Background(), decider(), job, app, past.ID, InterviewPatch{Notes: &n}); !errors.Is(err, ErrStale) {
		t.Errorf("concurrent change: %v", err)
	}
	sc.cur.Status = InterviewCancelled
	if _, err := s.UpdateInterview(context.Background(), decider(), job, app, past.ID, InterviewPatch{Notes: &n}); !errors.Is(err, ErrInterviewClosed) {
		t.Errorf("closed: %v", err)
	}
}

func TestCancelAndComplete(t *testing.T) {
	s, _, sc, _ := interviewSvc(t)
	iv, err := s.CancelInterview(context.Background(), decider(), job, app, sc.cur.ID, true, "")
	if err != nil || iv.Status != InterviewCancelled || sc.events[0] != EventInterviewCancelled {
		t.Fatalf("%+v %v", iv, err)
	}
	m := sc.msgs[0]
	if !strings.Contains(m.ICS, "METHOD:CANCEL") || !strings.Contains(m.ICS, "STATUS:CANCELLED") || strings.Contains(m.Body, "When:") {
		t.Errorf("cancel message %q\n%s", m.Body, m.ICS)
	}
	iv, err = s.CompleteInterview(context.Background(), decider(), job, app, sc.cur.ID)
	if err != nil || iv.Status != InterviewCompleted || sc.events[1] != EventInterviewCompleted {
		t.Fatalf("%+v %v", iv, err)
	}
	if len(sc.msgs) != 1 {
		t.Error("completing sends no email")
	}
	sc.cur.Status = InterviewCompleted
	if _, err = s.CancelInterview(context.Background(), decider(), job, app, sc.cur.ID, false, ""); !errors.Is(err, ErrInterviewClosed) {
		t.Errorf("cancel a held interview: %v", err)
	}
}

type scopedJobs struct {
	fakeJobs
	only string
}

func (j scopedJobs) JobAccess(ctx context.Context, tenant, jobID, user string) (*appjobs.JobAccess, error) {
	if jobID != j.only {
		return nil, ErrNotFound
	}
	return j.fakeJobs.JobAccess(ctx, tenant, jobID, user)
}

func (j scopedJobs) JobLabels(_ context.Context, _ string, ids []string) (map[string]appjobs.JobLabel, error) {
	out := map[string]appjobs.JobLabel{}
	for _, id := range ids {
		out[id] = appjobs.JobLabel{Code: "JB-1", Title: "Engineer"}
	}
	return out, nil
}

func TestMyScheduleKeepsOnlyJobsTheCallerCanSee(t *testing.T) {
	s, st, sc, _ := interviewSvc(t)
	s.Jobs = scopedJobs{fakeJobs{st: st}, job}
	sc.mine = []Interview{{ID: "a", JobID: job}, {ID: "b", JobID: "77777777-7777-4777-8777-777777777700"}}
	got, err := s.MySchedule(context.Background(), caller(owner, rbac.JobViewAssigned), time.Time{}, time.Time{}, 0)
	if err != nil || len(got) != 1 || got[0].ID != "a" || got[0].JobTitle != "Engineer" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err = s.MySchedule(context.Background(), caller(owner), time.Time{}, time.Time{}, 0); !isCode(err, apperror.CodeForbidden) {
		t.Errorf("no permission: %v", err)
	}
	now := s.now()
	if _, err = s.MySchedule(context.Background(), caller(owner, rbac.JobViewAll), now, now.Add(-time.Hour), 0); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("reversed range: %v", err)
	}
}

type compareStore struct{ *fakeStore }

func (c *compareStore) Open(_ context.Context, _ string, in OpenInput) (*Detail, error) {
	c.opened = append(c.opened, in)
	answers := []Answer{{QuestionID: "q1", Section: "About", Type: "short_text", Label: "Why us?", Value: json.RawMessage(`"because"`)}}
	if in.AppID == app {
		answers = append(answers, Answer{QuestionID: "q2", Section: "About", Type: "url", Label: "Portfolio", Value: json.RawMessage(`"https://x.test"`)},
			Answer{QuestionID: "q3", Type: "file", Label: "CV", FileID: "f1"})
	}
	return &Detail{Summary: Summary{ID: in.AppID, Name: "N-" + in.AppID[:2], Status: StatusInReview}, Answers: answers, Flags: json.RawMessage(`[{"x":1}]`), StatusNote: "secret"}, nil
}

func TestCompareAlignsAnswersAndHidesReviewData(t *testing.T) {
	s, st, _, _ := interviewSvc(t)
	s.Store = &compareStore{st}
	other := "44444444-4444-4444-8444-444444444445"
	res, err := s.Compare(context.Background(), decider(), job, []string{app, other, app})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applications) != 2 || len(res.Rows) != 3 {
		t.Fatalf("%d apps %d rows", len(res.Applications), len(res.Rows))
	}
	r := res.Rows[1] // q2: only the first candidate answered
	if r.QuestionID != "q2" || string(r.Values[0]) != `"https://x.test"` || r.Values[1] != nil {
		t.Errorf("row %+v", r)
	}
	if res.Rows[2].FileIDs[0] != "f1" || res.Rows[2].FileIDs[1] != "" {
		t.Errorf("files %+v", res.Rows[2])
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), `"flags"`) {
		t.Errorf("review data in the comparison: %s", b)
	}
	for _, o := range st.opened {
		if o.TrackReview {
			t.Error("a comparison must not make the caller the reviewer")
		}
	}
	for _, bad := range [][]string{{app}, {app, app}, {app, other, "x"}, {app, other, "a", "b", "c"}} {
		if _, err = s.Compare(context.Background(), decider(), job, bad); err == nil {
			t.Errorf("ids %v accepted", bad)
		}
	}
	if _, err = s.Compare(context.Background(), caller("99999999-9999-4999-8999-999999999999"), job, []string{app, other}); !errors.Is(err, ErrNotFound) {
		t.Errorf("no access: %v", err)
	}
}
