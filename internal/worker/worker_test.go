package worker

import (
	"context"
	"errors"
	"net/smtp"
	"testing"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/riverqueue/river"
)

func TestEmailWorker_Work_Success(t *testing.T) {
	t.Parallel()

	m, err := mailer.New(mailer.Config{
		Host: "smtp.example.com",
		Port: "587",
		From: "no-reply@example.com",
	})
	if err != nil {
		t.Fatalf("failed to create mailer: %v", err)
	}

	var calledAddr string
	var calledFrom string
	var calledTo []string

	// Mock out smtp.SendMail.
	mailer.SetSendMailForTesting(m, func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		calledAddr = addr
		calledFrom = from
		calledTo = to
		return nil
	})

	w := &EmailWorker{Mailer: m}
	job := &river.Job[mailer.EmailArgs]{
		Args: mailer.EmailArgs{
			Type:      "waitlist_confirmation",
			Recipient: "recipient@acme.com",
			FullName:  "Jerry",
		},
	}

	err = w.Work(context.Background(), job)
	if err != nil {
		t.Fatalf("expected Work to succeed, got error: %v", err)
	}

	if calledAddr != "smtp.example.com:587" {
		t.Errorf("expected SMTP addr 'smtp.example.com:587', got %q", calledAddr)
	}
	if calledFrom != "no-reply@example.com" {
		t.Errorf("expected From 'no-reply@example.com', got %q", calledFrom)
	}
	if len(calledTo) != 1 || calledTo[0] != "recipient@acme.com" {
		t.Errorf("expected To '[recipient@acme.com]', got %v", calledTo)
	}
}

func TestEmailWorker_Work_SMTPError(t *testing.T) {
	t.Parallel()

	m, err := mailer.New(mailer.Config{})
	if err != nil {
		t.Fatalf("failed to create mailer: %v", err)
	}

	smtpErr := errors.New("connection failed")
	mailer.SetSendMailForTesting(m, func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		return smtpErr
	})

	w := &EmailWorker{Mailer: m}
	job := &river.Job[mailer.EmailArgs]{
		Args: mailer.EmailArgs{
			Type:      "more_info_ack",
			Recipient: "recipient@acme.com",
			FullName:  "Jane",
		},
	}

	err = w.Work(context.Background(), job)
	if !errors.Is(err, smtpErr) {
		t.Fatalf("expected error %v, got %v", smtpErr, err)
	}
}
