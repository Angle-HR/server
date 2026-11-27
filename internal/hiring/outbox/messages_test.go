package outbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/mailer"
)

type msgStore struct {
	due    []DueMessage
	sent   []string
	failed map[string]bool
}

func (s *msgStore) LeaseMessages(context.Context, string, time.Time, int, time.Duration) ([]DueMessage, error) {
	d := s.due
	s.due = nil
	return d, nil
}
func (s *msgStore) MarkMessageSent(_ context.Context, _, id string, _ time.Time) error {
	s.sent = append(s.sent, id)
	return nil
}
func (s *msgStore) MarkMessageFailed(_ context.Context, _, id, _ string, final bool, _ time.Time) error {
	if s.failed == nil {
		s.failed = map[string]bool{}
	}
	s.failed[id] = final
	return nil
}

type capture struct {
	args []mailer.EmailArgs
	err  error
}

func (c *capture) Send(_ context.Context, a mailer.EmailArgs) error {
	c.args = append(c.args, a)
	return c.err
}

func TestRunMessagesSendsToTheApplicationsCandidate(t *testing.T) {
	st := &msgStore{due: []DueMessage{{ID: "m1", Recipient: "ada@x.test", Name: "Ada", Company: "Acme", JobTitle: "Engineer", Subject: "Hi", Body: "Body", ReplyTo: "jobs@acme.test", Attempts: 1}}}
	snd := &capture{}
	w := &Worker{Tenants: func(context.Context) ([]string, error) { return []string{"t"}, nil }, Sender: snd}
	res, err := w.RunMessages(context.Background(), st)
	if err != nil || res.Sent != 1 || len(st.sent) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	a := snd.args[0]
	if a.Type != mailer.TypeCandidateMessage || a.Recipient != "ada@x.test" || a.Subject != "Hi" || a.MessageBody != "Body" || a.ReplyTo != "jobs@acme.test" {
		t.Errorf("args %+v", a)
	}
}

func TestRunMessagesRetriesThenGivesUp(t *testing.T) {
	for attempts, final := range map[int]bool{1: false, MaxAttempts: true} {
		st := &msgStore{due: []DueMessage{{ID: "m", Recipient: "a@x.test", Attempts: attempts}}}
		w := &Worker{Tenants: func(context.Context) ([]string, error) { return []string{"t"}, nil }, Sender: &capture{err: errors.New("smtp down")}}
		res, _ := w.RunMessages(context.Background(), st)
		if got, ok := st.failed["m"]; !ok || got != final || (final && res.Failed != 1) || (!final && res.Retry != 1) {
			t.Errorf("attempts %d: %+v %+v", attempts, st.failed, res)
		}
	}
}

func TestMessageWithoutARecipientIsNotSent(t *testing.T) {
	if _, err := MessageArgs(DueMessage{ID: "m"}, ""); err == nil {
		t.Error("no recipient")
	}
}

func TestMessageArgsOfferLink(t *testing.T) {
	m := DueMessage{ID: "m", Recipient: "a@x.test", Subject: "S", Body: "Open it:\n\n{{offer_link}}", Secret: "uk.t.s"}
	a, err := MessageArgs(m, "https://app.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.MessageBody, "https://app.example.test/offers/uk.t.s") || strings.Contains(a.MessageBody, "{{") {
		t.Fatalf("body = %q", a.MessageBody)
	}
	if _, err = MessageArgs(m, ""); err == nil {
		t.Fatal("a link without a site address was sent")
	}
	// a message without a secret never has its text rewritten
	plain, _ := MessageArgs(DueMessage{ID: "m", Body: "{{offer_link}}"}, "https://app.example.test")
	if strings.Contains(plain.MessageBody, "offers/") {
		t.Fatal("link made without a secret")
	}
}
