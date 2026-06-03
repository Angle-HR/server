package mailer

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/smtp"
)

//go:embed templates/*.html
var templatesFS embed.FS

// EmailArgs defines the River job arguments for email notifications.
type EmailArgs struct {
	Type      string `json:"type"` // "waitlist_confirmation" | "more_info_ack"
	Recipient string `json:"recipient"`
	FullName  string `json:"full_name"`
	Token     string `json:"token,omitempty"`
}

// Kind returns the job kind name for River.
func (EmailArgs) Kind() string { return "email" }

// Config holds the configuration details for SMTP delivery.
type Config struct {
	Host     string
	Port     string
	User     string
	Password string
	From     string
	AppURL   string
}

// Mailer exposes email template rendering and delivery via SMTP.
type Mailer struct {
	cfg       Config
	templates *template.Template
	sendMail  func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// New returns a Mailer initialized with SMTP configuration.
func New(cfg Config) (*Mailer, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse email templates: %w", err)
	}

	return &Mailer{
		cfg:       cfg,
		templates: tmpl,
		sendMail:  smtp.SendMail,
	}, nil
}

// Send renders and delivers the email according to the job arguments.
func (m *Mailer) Send(ctx context.Context, args EmailArgs) error {
	var templateName string
	var subject string

	switch args.Type {
	case "waitlist_confirmation":
		templateName = "waitlist_confirmation.html"
		subject = "You're on the Angle HR waitlist"
	case "more_info_ack":
		templateName = "more_info_ack.html"
		subject = "Thanks for sharing more about yourself"
	default:
		return fmt.Errorf("unknown email type: %s", args.Type)
	}

	var body bytes.Buffer
	data := struct {
		EmailArgs
		AppURL string
	}{args, m.cfg.AppURL}
	if err := m.templates.ExecuteTemplate(&body, templateName, data); err != nil {
		return fmt.Errorf("execute template %s: %w", templateName, err)
	}

	// Compose the RFC 822 email message.
	message := []byte(fmt.Sprintf(
		"To: %s\r\n"+
			"From: %s\r\n"+
			"Subject: %s\r\n"+
			"MIME-Version: 1.0\r\n"+
			"Content-Type: text/html; charset=UTF-8\r\n"+
			"\r\n"+
			"%s\r\n",
		args.Recipient,
		m.cfg.From,
		subject,
		body.String(),
	))

	var auth smtp.Auth
	if m.cfg.User != "" || m.cfg.Password != "" {
		auth = smtp.PlainAuth("", m.cfg.User, m.cfg.Password, m.cfg.Host)
	}

	addr := fmt.Sprintf("%s:%s", m.cfg.Host, m.cfg.Port)
	if err := m.sendMail(addr, auth, m.cfg.From, []string{args.Recipient}, message); err != nil {
		return fmt.Errorf("smtp send mail to %s: %w", args.Recipient, err)
	}

	return nil
}

// SetSendMailForTesting allows setting a custom sendMail implementation for unit tests.
func SetSendMailForTesting(m *Mailer, fn func(addr string, a smtp.Auth, from string, to []string, msg []byte) error) {
	m.sendMail = fn
}
