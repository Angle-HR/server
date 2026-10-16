package mailer

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/smtp"
	"strings"
	"testing"
)

func render(t *testing.T, m *Mailer, name string, a EmailArgs) string {
	t.Helper()
	var b bytes.Buffer
	if err := m.templates.ExecuteTemplate(&b, name, struct {
		EmailArgs
		AppURL string
	}{a, ""}); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return b.String()
}

func TestHiringTemplates(t *testing.T) {
	m, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	base := EmailArgs{FullName: "Ada <b>Lovelace</b>", OrganizationName: "Acme Ltd", JobTitle: "Senior Engineer", JobCode: "JB-12",
		NoticeURL: "https://jobs.test/j/jabc/privacy"}

	got := render(t, m, "application_received.html", base)
	for _, want := range []string{"Senior Engineer", "Acme Ltd", "JB-12", "https://jobs.test/j/jabc/privacy", "Hi Ada &lt;b&gt;Lovelace&lt;/b&gt;,"} {
		if !strings.Contains(got, want) {
			t.Errorf("received: missing %q", want)
		}
	}
	if strings.Contains(got, "<b>Lovelace") {
		t.Error("the candidate's name must be escaped")
	}
	bare := render(t, m, "application_received.html", EmailArgs{JobTitle: "Engineer"})
	if strings.Contains(bare, "href=") || !strings.Contains(bare, "The employer will review") {
		t.Errorf("without a notice link or company: %s", bare)
	}

	rej := render(t, m, "application_rejected.html", base)
	if !strings.Contains(rej, "decided not to take your application further") || !strings.Contains(rej, "made by a person") {
		t.Errorf("rejected: %s", rej)
	}
	if h := render(t, m, "application_hired.html", base); !strings.Contains(h, "move forward with you") {
		t.Errorf("hired: %s", h)
	}

	one := render(t, m, "retention_notice.html", EmailArgs{OrganizationName: "Acme Ltd", Count: 1, Lines: []string{"JB-1 Engineer: 1 application"}})
	many := render(t, m, "retention_notice.html", EmailArgs{Count: 3, Lines: []string{"JB-1 Engineer: 2 applications", "JB-2 <i>Designer</i>: 1 application"}})
	if !strings.Contains(one, "1 application was deleted") || !strings.Contains(many, "3 applications were deleted") ||
		!strings.Contains(many, "JB-2 &lt;i&gt;Designer") {
		t.Errorf("retention notice: %s %s", one, many)
	}
}

func TestSubjectOverrideCannotAddHeaders(t *testing.T) {
	m, _ := New(Config{Host: "smtp.test", Port: "25", From: "noreply@test"})
	var sent []byte
	SetSendMailForTesting(m, func(_ string, _ smtp.Auth, _ string, _ []string, msg []byte) error { sent = msg; return nil })
	err := m.Send(context.Background(), EmailArgs{
		Type: TypeApplicationReceived, Recipient: "ada@example.com", JobTitle: "Engineer",
		Subject: "Hello\r\nBcc: attacker@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	head := string(sent[:bytes.Index(sent, []byte("\r\n\r\n"))])
	if strings.Contains(head, "\r\nBcc:") || !strings.Contains(head, "Subject: HelloBcc: attacker@example.com") {
		t.Errorf("headers = %q", head)
	}
	_ = m.Send(context.Background(), EmailArgs{Type: TypeApplicationReceived, Recipient: "a@b.test"})
	if !strings.Contains(string(sent), "Subject: We received your application") {
		t.Error("the default subject is used when none is given")
	}
}

func TestHiringEmailsKeepCandidatesOutOfTheLogs(t *testing.T) {
	var logs bytes.Buffer
	m, _ := New(Config{Host: "smtp.test", Port: "25", From: "noreply@test", Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	SetSendMailForTesting(m, func(string, smtp.Auth, string, []string, []byte) error {
		return errors.New("smtp send mail to ada@example.com: 550 no such user")
	})
	_ = m.Send(context.Background(), EmailArgs{Type: TypeApplicationReceived, Recipient: "ada@example.com", FullName: "Ada Lovelace", JobTitle: "Engineer"})
	if out := logs.String(); strings.Contains(out, "ada@example.com") || strings.Contains(out, "Lovelace") || !strings.Contains(out, "smtp send failed") {
		t.Errorf("logs leak the candidate or lost the failure: %s", out)
	}
	// Other emails keep logging as before.
	logs.Reset()
	_ = m.Send(context.Background(), EmailArgs{Type: TypeLoginOTP, Recipient: "owner@acme.test", FullName: "Owner"})
	if !strings.Contains(logs.String(), "owner@acme.test") {
		t.Errorf("existing behaviour changed: %s", logs.String())
	}
}
