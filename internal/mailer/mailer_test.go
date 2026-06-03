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
			Type:      "waitlist_confirmation",
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
		wantHref := `href="https://app.anglehr.com/onboarding?token=abc123"`
		if !bytes.Contains(buf.Bytes(), []byte(wantHref)) {
			t.Errorf("expected rendered content to contain %q, got: %s", wantHref, content)
		}
		if !bytes.Contains(buf.Bytes(), []byte("John Doe")) {
			t.Errorf("expected rendered content to contain 'John Doe', got: %s", content)
		}
	})

	t.Run("more_info_ack", func(t *testing.T) {
		args := EmailArgs{
			Type:      "more_info_ack",
			Recipient: "test@example.com",
			FullName:  "Jane Smith",
		}

		var buf bytes.Buffer
		err := m.templates.ExecuteTemplate(&buf, "more_info_ack.html", args)
		if err != nil {
			t.Fatalf("failed to render template: %v", err)
		}

		content := buf.String()
		if !bytes.Contains(buf.Bytes(), []byte("Jane Smith")) {
			t.Errorf("expected rendered content to contain 'Jane Smith', got: %s", content)
		}
		if !bytes.Contains(buf.Bytes(), []byte("Thanks for sharing more!")) {
			t.Errorf("expected rendered content to contain title, got: %s", content)
		}
	})
}
