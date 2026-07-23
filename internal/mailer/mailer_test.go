package mailer

import (
	"bytes"
	"testing"
)

func TestTemplatesRendering(t *testing.T) {
	m, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create mailer: %v", err)
	}

	t.Run("waitlist_confirmation", func(t *testing.T) {
		m, err := New(Config{AppURL: "https://app.anglehr.com"})
		if err != nil {
			t.Fatalf("failed to create mailer: %v", err)
		}

		args := EmailArgs{
			Type:      TypeWaitlistConfirmation,
			Recipient: "test@example.com",
			FullName:  "John Doe",
			Token:     "abc123",
		}

		var buf bytes.Buffer
		data := struct {
			EmailArgs
			AppURL string
		}{args, m.cfg.AppURL}
		err = m.templates.ExecuteTemplate(&buf, "waitlist_confirmation.html", data)
		if err != nil {
			t.Fatalf("failed to render template: %v", err)
		}

		content := buf.String()
		wantHref := `href="https://app.anglehr.com/survey"`
		if !bytes.Contains(buf.Bytes(), []byte(wantHref)) {
			t.Errorf("expected rendered content to contain %q, got: %s", wantHref, content)
		}
		if !bytes.Contains(buf.Bytes(), []byte("John Doe")) {
			t.Errorf("expected rendered content to contain 'John Doe', got: %s", content)
		}
	})

	t.Run("more_info_ack", func(t *testing.T) {
		args := EmailArgs{
			Type:      TypeMoreInfoAck,
			Recipient: "test@example.com",
			FullName:  "Jane Smith",
		}

		var buf bytes.Buffer
		data := struct {
			EmailArgs
			AppURL string
		}{args, m.cfg.AppURL}
		err := m.templates.ExecuteTemplate(&buf, "more_info_ack.html", data)
		if err != nil {
			t.Fatalf("failed to render template: %v", err)
		}

		content := buf.String()
		if !bytes.Contains(buf.Bytes(), []byte("Jane Smith")) {
			t.Errorf("expected rendered content to contain 'Jane Smith', got: %s", content)
		}
		if !bytes.Contains(buf.Bytes(), []byte("What happens next?")) {
			t.Errorf("expected rendered content to contain title, got: %s", content)
		}
	})
}

func TestEmailVerificationTemplate(t *testing.T) {
	m, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	args := EmailArgs{
		Type:             TypeEmailVerification,
		Recipient:        "test@example.com",
		Code:             "224879",
		ExpiresInSeconds: 300,
	}

	var buf bytes.Buffer
	if err := m.templates.ExecuteTemplate(&buf, "email_verification.html", args); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("224879")) {
		t.Fatalf("expected code in template: %s", buf.String())
	}
}
