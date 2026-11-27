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

func TestCandidateMessageRendersParagraphsEscapedWithReplyTo(t *testing.T) {
	var got []byte
	m, _ := New(Config{Host: "smtp.test", Port: "25", From: "noreply@test"})
	SetSendMailForTesting(m, func(_ string, _ smtp.Auth, _ string, _ []string, msg []byte) error { got = msg; return nil })
	err := m.Send(context.Background(), EmailArgs{Type: TypeCandidateMessage, Recipient: "ada@x.test", FullName: "Ada",
		OrganizationName: "Acme", JobTitle: "Engineer", Subject: "Hello\r\nBcc: evil@x.test", ReplyTo: "jobs@acme.test",
		MessageBody: "Hi Ada,\n\nSee <b>this</b> & that."})
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{"Reply-To: jobs@acme.test\r\n", "Subject: HelloBcc: evil@x.test\r\n", "Hi Ada,", "&lt;b&gt;this&lt;/b&gt; &amp; that."} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
	if strings.Contains(s, "\r\nBcc:") || strings.Contains(s, "<b>this") {
		t.Errorf("header or markup injection: %s", s)
	}
}

func TestCandidateMessageWithACalendarEventIsMultipart(t *testing.T) {
	var got []byte
	m, _ := New(Config{Host: "smtp.test", Port: "25", From: "noreply@test"})
	SetSendMailForTesting(m, func(_ string, _ smtp.Auth, _ string, _ []string, msg []byte) error { got = msg; return nil })
	ics := "BEGIN:VCALENDAR\nMETHOD:CANCEL\nBEGIN:VEVENT\nUID:x@openhr\nEND:VEVENT\nEND:VCALENDAR\n"
	if err := m.Send(context.Background(), EmailArgs{Type: TypeCandidateMessage, Recipient: "ada@x.test", Subject: "Interview", MessageBody: "Hi", ICS: ics}); err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{"Content-Type: multipart/alternative; boundary=openhr-", "Content-Type: text/html; charset=UTF-8", "Content-Type: text/calendar; charset=UTF-8; method=CANCEL", "METHOD:CANCEL\r\nBEGIN:VEVENT\r\nUID:x@openhr\r\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
	if strings.Count(s, "--openhr-") != 3 {
		t.Errorf("boundaries: %s", s)
	}
}

func TestComposeMessageWithAttachments(t *testing.T) {
	args := EmailArgs{Type: TypeCandidateMessage, Recipient: "a@x.test", ICS: "BEGIN:VCALENDAR\nMETHOD:REQUEST\nEND:VCALENDAR\n",
		Attachments: []Attachment{{Filename: "of\"fer\r\n.pdf", ContentType: "application/pdf", Content: bytes.Repeat([]byte("A"), 200)}}}
	msg := string(composeMessage(args, "From <f@x.test>", "S", "<p>hi</p>"))
	for _, want := range []string{"multipart/mixed", "multipart/alternative", "text/calendar", "Content-Transfer-Encoding: base64", "application/pdf"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(msg, "of\"fer") || strings.Count(msg, "filename=") != 1 {
		t.Errorf("file name not made safe:\n%s", msg)
	}
	for _, line := range strings.Split(msg, "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line of %d characters", len(line))
		}
	}
	// without files nothing changes: a plain candidate message stays single part
	plain := string(composeMessage(EmailArgs{Type: TypeCandidateMessage, Recipient: "a@x.test"}, "F", "S", "<p>hi</p>"))
	if strings.Contains(plain, "multipart") {
		t.Error("plain message is multipart")
	}
	// attachments are ignored on other email types
	other := string(composeMessage(EmailArgs{Type: TypeEmailVerification, Recipient: "a@x.test", Attachments: args.Attachments}, "F", "S", "<p>hi</p>"))
	if strings.Contains(other, "multipart") {
		t.Error("attachment on a system email")
	}
}
