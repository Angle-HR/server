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

// Email job types.
const (
	TypeWaitlistConfirmation = "waitlist_confirmation"
	TypeMoreInfoAck          = "more_info_ack"
	TypeEmailVerification    = "email_verification"
	TypeOnboardingComplete   = "onboarding_complete"
	TypeAdminInvite          = "admin_invite"
	TypePasswordReset        = "password_reset"
	TypeLoginOTP             = "login_otp"
	TypeOrgInvite            = "org_invite"
	TypeKYBFailed            = "kyb_failed"
	TypeKYBReviewQueued      = "kyb_review_queued"
	TypeKYBNudge1            = "kyb_nudge_1"
	TypeKYBNudge2            = "kyb_nudge_2"
)

// EmailArgs defines job queue arguments for email notifications.
type EmailArgs struct {
	Type             string `json:"type"`
	Recipient        string `json:"recipient"`
	FullName         string `json:"full_name"`
	OrganizationName string `json:"organization_name,omitempty"`
	Token            string `json:"token,omitempty"`
	Code             string `json:"code,omitempty"`
	ExpiresInSeconds int    `json:"expires_in_seconds,omitempty"`
	FailureReason    string `json:"failure_reason,omitempty"` // KYB emails: why verification failed
}

// Kind returns the job kind name.
func (EmailArgs) Kind() string { return "email" }

// Config holds the configuration details for SMTP delivery.
type Config struct {
	Host        string
	Port        string
	User        string
	Password    string
	From        string // envelope and address portion of From
	FromName    string // optional display name shown in inboxes
	AppURL      string // product / waitlist frontend base URL
	AdminAppURL string // admin console base URL (invite links)
	Logger      *slog.Logger
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
	cfg.AdminAppURL = strings.TrimRight(cfg.AdminAppURL, "/")

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

// selectTemplate picks the template file, subject and base URL for an email type,
// and checks that the app URL that type needs is configured.
func (m *Mailer) selectTemplate(emailType string) (templateName, subject, baseURL string, err error) {
	baseURL = m.cfg.AppURL
	needsAppURL := func(name string) error {
		if m.cfg.AppURL == "" {
			return fmt.Errorf("APP_URL is required for %s emails", name)
		}
		return nil
	}

	switch emailType {
	case TypeWaitlistConfirmation:
		return "waitlist_confirmation.html", "You're on the OpenHR waitlist", baseURL, nil
	case TypeMoreInfoAck:
		return "more_info_ack.html", "Thanks for sharing more about yourself", baseURL, nil
	case TypeEmailVerification:
		return "email_verification.html", "Verify your Open HR email", baseURL, nil
	case TypeOnboardingComplete:
		return "onboarding_complete.html", "Welcome to Open HR", baseURL, needsAppURL("onboarding_complete")
	case TypeAdminInvite:
		if m.cfg.AdminAppURL == "" {
			return "", "", "", fmt.Errorf("ADMIN_APP_URL is required for admin_invite emails")
		}
		return "admin_invite.html", "You're invited to Open HR Admin", m.cfg.AdminAppURL, nil
	case TypePasswordReset:
		return "password_reset.html", "Reset your Open HR password", baseURL, needsAppURL("password_reset")
	case TypeLoginOTP:
		return "login_otp.html", "Your Open HR sign-in code", baseURL, nil
	case TypeOrgInvite:
		return "org_invite.html", "You're invited to Open HR", baseURL, needsAppURL("org_invite")
	case TypeKYBFailed:
		return "kyb_failed.html", "We couldn't verify your company yet", baseURL, needsAppURL("kyb_failed")
	case TypeKYBReviewQueued:
		return "kyb_review_queued.html", "Your company verification is in review", baseURL, nil
	case TypeKYBNudge1:
		return "kyb_nudge_1.html", "Finish verifying your company", baseURL, needsAppURL("kyb_nudge_1")
	case TypeKYBNudge2:
		return "kyb_nudge_2.html", "Your company verification still needs attention", baseURL,
			needsAppURL("kyb_nudge_2")
	default:
		return "", "", "", fmt.Errorf("unknown email type: %s", emailType)
	}
}

// Send renders and delivers the email according to the job arguments.
func (m *Mailer) Send(ctx context.Context, args EmailArgs) error {
	m.logger.Info("email send started",
		"type", args.Type,
		"recipient", args.Recipient,
		"full_name", args.FullName,
	)

	templateName, subject, baseURL, err := m.selectTemplate(args.Type)
	if err != nil {
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
	}{args, baseURL}
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

	fromHeader := formatFromHeader(m.cfg.FromName, m.cfg.From)

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
		fromHeader,
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
		"from", fromHeader,
		"auth_enabled", authEnabled,
		"message_bytes", len(message),
	)

	// SMTP envelope uses the bare address; display name is header-only.
	if err := m.sendMail(addr, auth, m.cfg.From, []string{args.Recipient}, message); err != nil {
		err = fmt.Errorf("smtp send mail to %s: %w", args.Recipient, err)
		m.logger.Error("smtp send failed",
			"type", args.Type,
			"recipient", args.Recipient,
			"smtp_addr", addr,
			"from", fromHeader,
			"auth_enabled", authEnabled,
			"error", err,
		)
		return err
	}

	m.logger.Info("email sent successfully",
		"type", args.Type,
		"recipient", args.Recipient,
		"smtp_addr", addr,
		"from", fromHeader,
	)

	return nil
}

// formatFromHeader builds an RFC 5322 From value from optional display name + address.
func formatFromHeader(name, email string) string {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	if name == "" {
		return email
	}
	escaped := strings.ReplaceAll(name, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	//nolint:gocritic // %q would Go-escape non-ASCII names; RFC 5322 needs only \ and " escaped
	return fmt.Sprintf(`"%s" <%s>`, escaped, email)
}

// SetSendMailForTesting allows setting a custom sendMail implementation for unit tests.
func SetSendMailForTesting(m *Mailer, fn func(addr string, a smtp.Auth, from string, to []string, msg []byte) error) {
	m.sendMail = fn
}
