package applications

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/rbac"
)

type fakeMail struct {
	MessageStore
	tmpl    *Template
	reply   string
	queued  []*NewMessage
	quota   bool
	changes int
}

func (f *fakeMail) Template(_ context.Context, _, id string) (*Template, error) {
	if f.tmpl == nil || f.tmpl.ID != id {
		return nil, ErrNotFound
	}
	return f.tmpl, nil
}
func (f *fakeMail) ReplyTo(context.Context, string) (string, error) { return f.reply, nil }
func (f *fakeMail) QueueMessage(_ context.Context, _ string, m *NewMessage) (*Message, error) {
	if f.quota {
		return nil, ErrQuota
	}
	f.queued = append(f.queued, m)
	return &Message{ID: "m1", Subject: m.Subject, Body: m.Body, Status: m.Status, ScheduledAt: m.ScheduledAt}, nil
}
func (f *fakeMail) CreateTemplate(_ context.Context, _, _, name, subject, body string) (*Template, error) {
	return &Template{ID: "t", Name: name, Subject: subject, Body: body}, nil
}
func (f *fakeMail) Templates(context.Context, string) ([]Template, error)    { return nil, nil }
func (f *fakeMail) SetReplyTo(context.Context, string, string, string) error { return nil }

func mailSvc(t *testing.T) (*Service, *fakeStore, *fakeMail) {
	t.Helper()
	s, st := newSvc(t, "")
	f := &fakeMail{tmpl: &Template{ID: "99999999-9999-4999-8999-999999999999", Subject: "Hello {{candidate_name}}", Body: "Hi {{ candidate_name }},\n\nAbout {{job_title}} at {{company}}.\n\n{{sender_name}}"}, reply: "jobs@acme.test"}
	s.Mail = f
	return s, st, f
}

func TestRenderFillsPlaceholdersInOnePass(t *testing.T) {
	got := Render("Hi {{candidate_name}} / {{ job_title }}", map[string]string{"candidate_name": "{{job_title}}", "job_title": "Engineer"})
	if got != "Hi {{job_title}} / Engineer" {
		t.Errorf("a value must not add a placeholder: %q", got)
	}
}

func TestSendMessageGoesToTheCandidateWithPlaceholdersFilled(t *testing.T) {
	s, _, f := mailSvc(t)
	m, err := s.SendMessage(context.Background(), decider(), job, app, MessageInput{TemplateID: f.tmpl.ID})
	if err != nil {
		t.Fatal(err)
	}
	q := f.queued[0]
	if q.Subject != "Hello Ada" || !strings.Contains(q.Body, "About Engineer at Acme.") || !strings.Contains(q.Body, "the hiring team") {
		t.Errorf("rendered %+v", q)
	}
	if q.ReplyTo != "jobs@acme.test" || q.Company != "Acme" || q.Status != MsgQueued || !q.Enforce || q.AppID != app || q.SenderID != owner {
		t.Errorf("queued %+v", q)
	}
	if m.Status != MsgQueued {
		t.Errorf("status %s", m.Status)
	}
}

func TestSendMessageOverridesAndValidates(t *testing.T) {
	s, _, f := mailSvc(t)
	ctx := context.Background()
	if _, err := s.SendMessage(ctx, decider(), job, app, MessageInput{TemplateID: f.tmpl.ID, Subject: "Custom"}); err != nil || f.queued[0].Subject != "Custom" {
		t.Errorf("subject override: %v %+v", err, f.queued)
	}
	for name, in := range map[string]MessageInput{
		"nothing":             {},
		"unknown template":    {TemplateID: "11111111-1111-4111-8111-111111111112"},
		"unknown placeholder": {Subject: "x", Body: "Hi {{ssn}}"},
		"long subject":        {Subject: strings.Repeat("a", MaxSubject+1), Body: "x"},
	} {
		if _, err := s.SendMessage(ctx, decider(), job, app, in); !isCode(err, apperror.CodeValidationError) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSchedulingWindow(t *testing.T) {
	s, _, f := mailSvc(t)
	now := s.now()
	ok := now.Add(2 * time.Hour)
	if m, err := s.SendMessage(context.Background(), decider(), job, app, MessageInput{Subject: "s", Body: "b", ScheduleAt: &ok}); err != nil || m.Status != MsgScheduled || f.queued[0].Enforce != true {
		t.Fatalf("scheduled: %+v %v", m, err)
	}
	for _, at := range []time.Time{now, now.Add(30 * time.Second), now.AddDate(0, 0, MaxScheduleDays+1)} {
		at := at
		if _, err := s.SendMessage(context.Background(), decider(), job, app, MessageInput{Subject: "s", Body: "b", ScheduleAt: &at}); !isCode(err, apperror.CodeValidationError) {
			t.Errorf("%v: %v", at, err)
		}
	}
}

func TestOnlyDecidersEmailCandidates(t *testing.T) {
	s, _, _ := mailSvc(t)
	if _, err := s.SendMessage(context.Background(), caller(other, rbac.JobViewAssigned), job, app, MessageInput{Subject: "s", Body: "b"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("no access: %v", err)
	}
	s2, _ := newSvc(t, "interviewer")
	s2.Mail = &fakeMail{}
	if _, err := s2.SendMessage(context.Background(), caller(other, rbac.JobViewAssigned), job, app, MessageInput{Subject: "s", Body: "b"}); !isCode(err, apperror.CodeForbidden) {
		t.Errorf("interviewer: %v", err)
	}
}

func TestWithdrawnCandidatesAreNotWrittenTo(t *testing.T) {
	s, st, _ := mailSvc(t)
	st.state.Status = StatusWithdrawn
	if _, err := s.SendMessage(context.Background(), decider(), job, app, MessageInput{Subject: "s", Body: "b"}); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("%v", err)
	}
}

func TestQuotaIsReported(t *testing.T) {
	s, _, f := mailSvc(t)
	f.quota = true
	if _, err := s.SendMessage(context.Background(), decider(), job, app, MessageInput{Subject: "s", Body: "b"}); !errors.Is(err, ErrQuota) {
		t.Errorf("%v", err)
	}
}

func TestTemplatesNeedManageAnyAndKnownPlaceholders(t *testing.T) {
	s, _, _ := mailSvc(t)
	ctx := context.Background()
	if _, err := s.CreateTemplate(ctx, decider(), "Rejection", "s", "b"); !isCode(err, apperror.CodeForbidden) {
		t.Errorf("needs job.manage_any: %v", err)
	}
	admin := caller(owner, rbac.JobManageAny, rbac.JobViewAll)
	if _, err := s.CreateTemplate(ctx, admin, "Rejection", "About {{job_title}}", "Dear {{candidate_name}}"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTemplate(ctx, admin, "Bad", "s", "Dear {{first_name}}"); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("unknown placeholder: %v", err)
	}
	if _, err := s.CreateTemplate(ctx, admin, " ", "s", "b"); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("blank name: %v", err)
	}
}

func TestReplyToValidation(t *testing.T) {
	s, _, _ := mailSvc(t)
	admin := caller(owner, rbac.JobManageAny)
	for _, bad := range []string{"not an email", "Eve <eve@x.test>", "a@b.test\r\nBcc: x@y.test"} {
		if _, err := s.SetReplyTo(context.Background(), admin, bad); !isCode(err, apperror.CodeValidationError) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if got, err := s.SetReplyTo(context.Background(), admin, ""); err != nil || got != "" {
		t.Errorf("clear: %q %v", got, err)
	}
	if _, err := s.SetReplyTo(context.Background(), admin, "jobs@acme.test"); err != nil {
		t.Error(err)
	}
}

func TestDecisionCarriesAMessageInsteadOfTheStandardEmail(t *testing.T) {
	s, st, f := mailSvc(t)
	st.state.Status = StatusInReview
	if _, err := s.SetStatus(context.Background(), decider(), job, app, StatusInput{Status: StatusRejected, Reason: "not_a_fit",
		Message: &MessageInput{Subject: "Update on {{job_title}}", Body: "Dear {{candidate_name}}, no."}}); err != nil {
		t.Fatal(err)
	}
	ch := st.changes[0]
	if ch.Email != nil || ch.Message == nil || ch.Message.Subject != "Update on Engineer" || ch.Message.Enforce {
		t.Errorf("change %+v message %+v", ch, ch.Message)
	}
	_ = f
	// no message: the standard email stays
	if _, err := s.SetStatus(context.Background(), decider(), job, app, StatusInput{Status: StatusRejected}); err != nil {
		t.Fatal(err)
	}
	if last := st.changes[len(st.changes)-1]; last.Email == nil || last.Message != nil {
		t.Errorf("standard email expected: %+v", last)
	}
	// not with a plain move to in_review
	st.state.Status = StatusReceived
	if _, err := s.SetStatus(context.Background(), decider(), job, app, StatusInput{Status: StatusInReview, Message: &MessageInput{Subject: "s", Body: "b"}}); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("message with a non-final status: %v", err)
	}
	// a decision message is sent now
	at := s.now().Add(time.Hour)
	st.state.Status = StatusInReview
	if _, err := s.SetStatus(context.Background(), decider(), job, app, StatusInput{Status: StatusHired, Message: &MessageInput{Subject: "s", Body: "b", ScheduleAt: &at}}); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("scheduled decision message: %v", err)
	}
}
