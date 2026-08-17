package mailer

import (
	"bytes"
	"context"
	"net/smtp"
	"testing"
)

func TestTemplatesRendering(t *testing.T) {
	m, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create mailer: %v", err)
	}

	t.Run("waitlist_confirmation", func(t *testing.T) {
		m, err := New(Config{})
		if err != nil {
			t.Fatalf("failed to create mailer: %v", err)
		}

		args := EmailArgs{
			Type:      TypeWaitlistConfirmation,
			Recipient: "test@example.com",
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
		if bytes.Contains(buf.Bytes(), []byte("/survey?token=")) {
			t.Errorf("expected no survey CTA, got: %s", content)
		}
		if bytes.Contains(buf.Bytes(), []byte("Help shape our product")) {
			t.Errorf("expected no survey button, got: %s", content)
		}
		if !bytes.Contains(buf.Bytes(), []byte("Hi,")) {
			t.Errorf("expected generic greeting, got: %s", content)
		}
		if !bytes.Contains(buf.Bytes(), []byte("You're on")) {
			t.Errorf("expected confirmation copy, got: %s", content)
		}
		if !bytes.Contains(buf.Bytes(), []byte("What happens next?")) {
			t.Errorf("expected next-steps copy, got: %s", content)
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

func TestAdminInviteTemplate(t *testing.T) {
	m, err := New(Config{AdminAppURL: "https://admin.example.com"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	args := EmailArgs{
		Type:             TypeAdminInvite,
		Recipient:        "ops@example.com",
		FullName:         "Ops User",
		Token:            "invite-token",
		ExpiresInSeconds: 259200,
	}
	var buf bytes.Buffer
	data := struct {
		EmailArgs
		AppURL string
	}{args, m.cfg.AdminAppURL}
	if err := m.templates.ExecuteTemplate(&buf, "admin_invite.html", data); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("https://admin.example.com/admin/accept-invite?token=invite-token")) {
		t.Fatalf("expected invite link, got: %s", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte("Ops User")) {
		t.Fatalf("expected name, got: %s", buf.String())
	}
}

func TestAdminInviteRequiresAdminAppURL(t *testing.T) {
	m, err := New(Config{AppURL: "https://app.example.com"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	SetSendMailForTesting(m, func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		t.Fatal("sendMail should not be called")
		return nil
	})
	err = m.Send(context.Background(), EmailArgs{
		Type:      TypeAdminInvite,
		Recipient: "ops@example.com",
		Token:     "tok",
	})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("ADMIN_APP_URL")) {
		t.Fatalf("expected ADMIN_APP_URL error, got %v", err)
	}
}

func TestFormatFromHeader(t *testing.T) {
	tests := []struct {
		name  string
		email string
		want  string
	}{
		{name: "", email: "hello@example.com", want: "hello@example.com"},
		{name: "Angle HR", email: "hello@example.com", want: `"Angle HR" <hello@example.com>`},
		{name: `Foo "Bar"`, email: "a@b.com", want: `"Foo \"Bar\"" <a@b.com>`},
	}
	for _, tt := range tests {
		got := formatFromHeader(tt.name, tt.email)
		if got != tt.want {
			t.Errorf("formatFromHeader(%q, %q) = %q, want %q", tt.name, tt.email, got, tt.want)
		}
	}
}
