package mailer

import (
	"bytes"
	"context"
	"net/smtp"
	"strings"
	"testing"
)

// renderTemplate renders a mailer template for args and returns the output.
func renderTemplate(t *testing.T, m *Mailer, name string, args EmailArgs) string {
	t.Helper()
	var buf bytes.Buffer
	data := struct {
		EmailArgs
		AppURL string
	}{args, m.cfg.AppURL}
	if err := m.templates.ExecuteTemplate(&buf, name, data); err != nil {
		t.Fatalf("failed to render template: %v", err)
	}
	return buf.String()
}

func assertContains(t *testing.T, content, want string) {
	t.Helper()
	if !strings.Contains(content, want) {
		t.Errorf("expected rendered content to contain %q, got: %s", want, content)
	}
}

func assertNotContains(t *testing.T, content, unwanted string) {
	t.Helper()
	if strings.Contains(content, unwanted) {
		t.Errorf("expected rendered content not to contain %q, got: %s", unwanted, content)
	}
}

func TestTemplatesRendering(t *testing.T) {
	m, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create mailer: %v", err)
	}

	t.Run("waitlist_confirmation", func(t *testing.T) {
		content := renderTemplate(t, m, "waitlist_confirmation.html", EmailArgs{
			Type:      TypeWaitlistConfirmation,
			Recipient: "test@example.com",
		})

		assertNotContains(t, content, "/survey?token=")
		assertNotContains(t, content, "Help shape our product")
		assertContains(t, content, "Hi,")
		assertContains(t, content, "You're on")
		assertContains(t, content, "What happens next?")
	})

	t.Run("more_info_ack", func(t *testing.T) {
		content := renderTemplate(t, m, "more_info_ack.html", EmailArgs{
			Type:      TypeMoreInfoAck,
			Recipient: "test@example.com",
			FullName:  "Jane Smith",
		})

		assertContains(t, content, "Jane Smith")
		assertContains(t, content, "What happens next?")
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
