package mailer

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/smtp"
	"strings"
)

//go:embed templates/*.html
var templatesFS embed.FS

// EmailArgs defines job queue arguments for email notifications.
type EmailArgs struct {
	Type      string `json:"type"` // "waitlist_confirmation" | "more_info_ack"
	Recipient string `json:"recipient"`
	FullName  string `json:"full_name"`
	Token     string `json:"token,omitempty"`
}

// Kind returns the job kind name.
func (EmailArgs) Kind() string { return "email" }

// Config holds the configuration details for SMTP delivery.
type Config struct {
	Host     string
	Port     string
	User     string
	Password string
	From     string
	AppURL   string
	Logger   *slog.Logger
}

// Mailer exposes email template rendering and delivery via SMTP.
type Mailer struct {
	cfg       Config
	templates *template.Template
	sendMail  func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
	logger    *slog.Logger
}

// New returns a Mailer initialized with SMTP configuration.
func New(cfg Config) (*Mailer, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse email templates: %w", err)
	}

	cfg.AppURL = strings.TrimRight(cfg.AppURL, "/")

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Mailer{
		cfg:       cfg,
		templates: tmpl,
		sendMail:  smtp.SendMail,
		logger:    logger,
	}, nil
}

// Send renders and delivers the email according to the job arguments.
func (m *Mailer) Send(ctx context.Context, args EmailArgs) error {
	m.logger.Info("email send started",
		"type", args.Type,
		"recipient", args.Recipient,
		"full_name", args.FullName,
	)

	var templateName string
	var subject string

	switch args.Type {
	case "waitlist_confirmation":
		if m.cfg.AppURL == "" {
			err := fmt.Errorf("APP_URL is required for waitlist_confirmation emails")
			m.logger.Error("email send failed", "type", args.Type, "recipient", args.Recipient, "error", err)
			return err
		}
		templateName = "waitlist_confirmation.html"
		subject = "You're on the Angle HR waitlist"
	case "more_info_ack":
		templateName = "more_info_ack.html"
		subject = "Thanks for sharing more about yourself"
	default:
		err := fmt.Errorf("unknown email type: %s", args.Type)
		m.logger.Error("email send failed", "type", args.Type, "recipient", args.Recipient, "error", err)
		return err
	}

	m.logger.Info("email template selected",
		"type", args.Type,
		"recipient", args.Recipient,
		"template", templateName,
		"subject", subject,
	)

	var body bytes.Buffer
	data := struct {
		EmailArgs
		AppURL string
	}{args, m.cfg.AppURL}
	if err := m.templates.ExecuteTemplate(&body, templateName, data); err != nil {
		err = fmt.Errorf("execute template %s: %w", templateName, err)
		m.logger.Error("email template render failed",
			"type", args.Type,
			"recipient", args.Recipient,
			"template", templateName,
			"error", err,
		)
		return err
	}

	m.logger.Info("email template rendered",
		"type", args.Type,
		"recipient", args.Recipient,
		"template", templateName,
		"body_bytes", body.Len(),
	)

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

	authEnabled := m.cfg.User != "" || m.cfg.Password != ""
	var auth smtp.Auth
	if authEnabled {
		auth = smtp.PlainAuth("", m.cfg.User, m.cfg.Password, m.cfg.Host)
	}

	addr := fmt.Sprintf("%s:%s", m.cfg.Host, m.cfg.Port)
	m.logger.Info("smtp send starting",
		"type", args.Type,
		"recipient", args.Recipient,
		"smtp_addr", addr,
		"from", m.cfg.From,
		"auth_enabled", authEnabled,
		"message_bytes", len(message),
	)

	if err := m.sendMail(addr, auth, m.cfg.From, []string{args.Recipient}, message); err != nil {
		err = fmt.Errorf("smtp send mail to %s: %w", args.Recipient, err)
		m.logger.Error("smtp send failed",
			"type", args.Type,
			"recipient", args.Recipient,
			"smtp_addr", addr,
			"from", m.cfg.From,
			"auth_enabled", authEnabled,
			"error", err,
		)
		return err
	}

	m.logger.Info("email sent successfully",
		"type", args.Type,
		"recipient", args.Recipient,
		"smtp_addr", addr,
		"from", m.cfg.From,
	)

	return nil
}

// SetSendMailForTesting allows setting a custom sendMail implementation for unit tests.
func SetSendMailForTesting(m *Mailer, fn func(addr string, a smtp.Auth, from string, to []string, msg []byte) error) {
	m.sendMail = fn
}
