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

	// Hiring emails. They are sent from the region's outbox by hiring-outbox, so candidate details stay in the
	// region; they are not queued through the global job queue.
	TypeApplicationReceived = "application_received"
	TypeApplicationRejected = "application_rejected"
	TypeApplicationHired    = "application_hired"
	TypeRetentionNotice     = "retention_notice"
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

	// Hiring emails. Subject, when set, replaces the template's default subject.
	Subject   string   `json:"subject,omitempty"`
	JobTitle  string   `json:"job_title,omitempty"`
	JobCode   string   `json:"job_code,omitempty"`
	NoticeURL string   `json:"notice_url,omitempty"` // the job's applicant privacy notice
	Lines     []string `json:"lines,omitempty"`      // retention notice: one line per job
	Count     int      `json:"count,omitempty"`
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
	case TypeApplicationReceived:
		return "application_received.html", "We received your application", baseURL, nil
	case TypeApplicationRejected:
		return "application_rejected.html", "An update on your application", baseURL, nil
	case TypeApplicationHired:
		return "application_hired.html", "Good news about your application", baseURL, nil
	case TypeRetentionNotice:
		return "retention_notice.html", "Applications deleted under your retention policy", baseURL, nil
	default:
		return "", "", "", fmt.Errorf("unknown email type: %s", emailType)
	}
}

// Send renders and delivers the email according to the job arguments.
func (m *Mailer) Send(ctx context.Context, args EmailArgs) error {
	// Hiring emails go to candidates: their address and name stay out of the logs.
	rcpt, name := args.Recipient, args.FullName
	if isHiringType(args.Type) {
		rcpt, name = "(redacted)", "(redacted)"
	}
	m.logger.Info("email send started",
		"type", args.Type,
		"recipient", rcpt,
		"full_name", name,
	)

	templateName, subject, baseURL, err := m.selectTemplate(args.Type)
	if err != nil {
		m.logger.Error("email send failed", "type", args.Type, "recipient", rcpt, "error", err)
		return err
	}

	if sub := cleanHeader(args.Subject); sub != "" {
		subject = sub
	}

	m.logger.Info("email template selected",
		"type", args.Type,
		"recipient", rcpt,
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
			"recipient", rcpt,
			"template", templateName,
			"error", err,
		)
		return err
	}

	m.logger.Info("email template rendered",
		"type", args.Type,
		"recipient", rcpt,
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
		"recipient", rcpt,
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
			"recipient", rcpt,
			"smtp_addr", addr,
			"from", fromHeader,
			"auth_enabled", authEnabled,
			"error", strings.ReplaceAll(err.Error(), args.Recipient, rcpt),
		)
		return err
	}

	m.logger.Info("email sent successfully",
		"type", args.Type,
		"recipient", rcpt,
		"smtp_addr", addr,
		"from", fromHeader,
	)

	return nil
}

func isHiringType(t string) bool {
	switch t {
	case TypeApplicationReceived, TypeApplicationRejected, TypeApplicationHired:
		return true
	}
	return false
}

// cleanHeader makes a value safe to put in a mail header: a line break would let the text add headers of its own.
func cleanHeader(v string) string {
	v = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v)
	return strings.TrimSpace(v)
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
