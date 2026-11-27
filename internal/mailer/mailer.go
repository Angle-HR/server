package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"log/slog"
	"net/smtp"
	"regexp"
	"strings"
)

//go:embed templates/*.html
var templatesFS embed.FS

// Email job types.
const (
	TypeMoreInfoAck        = "more_info_ack"
	TypeEmailVerification  = "email_verification"
	TypeOnboardingComplete = "onboarding_complete"
	TypeAdminInvite        = "admin_invite"
	TypePasswordReset      = "password_reset"
	TypeLoginOTP           = "login_otp"
	TypeOrgInvite          = "org_invite"
	TypeKYBFailed          = "kyb_failed"
	TypeKYBReviewQueued    = "kyb_review_queued"
	TypeKYBNudge1          = "kyb_nudge_1"
	TypeKYBNudge2          = "kyb_nudge_2"

	// Hiring emails. They are sent from the region's outbox by hiring-outbox, so candidate details stay in the
	// region; they are not queued through the global job queue.
	TypeApplicationReceived = "application_received"
	TypeApplicationRejected = "application_rejected"
	TypeApplicationHired    = "application_hired"
	TypeRetentionNotice     = "retention_notice"
	// TypeCandidateMessage is an email a person at a company wrote to a candidate (Subject, MessageBody, ReplyTo).
	TypeCandidateMessage = "candidate_message"
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
	// MessageBody is the plain text of a candidate message; the template turns it into paragraphs.
	MessageBody string `json:"message_body,omitempty"`
	// ReplyTo, when set, is where a reply goes (a candidate message only).
	ReplyTo string `json:"reply_to,omitempty"`
	// ICS, when set on a candidate message, is a calendar event (an interview invite, update or cancellation)
	// sent as a second part of the email.
	ICS string `json:"ics,omitempty"`
	// Attachments are files sent with a candidate message.
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment is a file sent with an email. Content is base64 in JSON.
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Content     []byte `json:"content"`
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
	case TypeCandidateMessage:
		return "candidate_message.html", "A message about your application", baseURL, nil
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
	message := composeMessage(args, fromHeader, subject, body.String())

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
	case TypeApplicationReceived, TypeApplicationRejected, TypeApplicationHired, TypeCandidateMessage:
		return true
	}
	return false
}

// replyToHeader is the Reply-To line of a candidate message, or nothing.
func replyToHeader(args EmailArgs) string {
	if args.Type != TypeCandidateMessage {
		return ""
	}
	if r := cleanHeader(args.ReplyTo); r != "" {
		return "Reply-To: " + r + "\r\n"
	}
	return ""
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

// Paragraphs splits a candidate message into paragraphs at blank lines; the template escapes each one.
func (e EmailArgs) Paragraphs() []string {
	var out []string
	for _, p := range strings.Split(strings.ReplaceAll(e.MessageBody, "\r\n", "\n"), "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var icsMethodRe = regexp.MustCompile(`(?m)^METHOD:([A-Z]+)\r?$`)

// composeMessage builds the RFC 822 message: single-part HTML, or HTML with a calendar part when the email carries
// an event (so mail clients offer to add or update it).
func composeMessage(args EmailArgs, from, subject, html string) []byte {
	head := fmt.Sprintf("To: %s\r\nFrom: %s\r\n%sSubject: %s\r\nMIME-Version: 1.0\r\n",
		args.Recipient, from, replyToHeader(args), subject)
	candidate := args.Type == TypeCandidateMessage
	var inner string // the content headers, a blank line and the content
	if !candidate || strings.TrimSpace(args.ICS) == "" {
		inner = "Content-Type: text/html; charset=UTF-8\r\n\r\n" + html + "\r\n"
	} else {
		method := "REQUEST"
		if m := icsMethodRe.FindStringSubmatch(args.ICS); m != nil {
			method = m[1]
		}
		boundary := newBoundary()
		ics := strings.ReplaceAll(strings.ReplaceAll(args.ICS, "\r\n", "\n"), "\n", "\r\n")
		inner = "Content-Type: multipart/alternative; boundary=" + boundary + "\r\n\r\n" +
			"--" + boundary + "\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n" + html + "\r\n" +
			"--" + boundary + "\r\nContent-Type: text/calendar; charset=UTF-8; method=" + method + "\r\n\r\n" + ics +
			"--" + boundary + "--\r\n"
	}
	if !candidate || len(args.Attachments) == 0 {
		return []byte(head + inner)
	}
	boundary := newBoundary()
	var b strings.Builder
	b.WriteString(head + "Content-Type: multipart/mixed; boundary=" + boundary + "\r\n\r\n")
	b.WriteString("--" + boundary + "\r\n" + inner)
	for _, a := range args.Attachments {
		name := attachmentName(a.Filename)
		ct := a.ContentType
		if ct == "" || strings.ContainsAny(ct, "\r\n;") {
			ct = "application/octet-stream"
		}
		b.WriteString("--" + boundary + "\r\nContent-Type: " + ct + "; name=\"" + name + "\"\r\n" +
			"Content-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"" + name + "\"\r\n\r\n")
		enc := base64.StdEncoding.EncodeToString(a.Content)
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String())
}

func newBoundary() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "openhr-" + hex.EncodeToString(b[:])
}

// attachmentName keeps a file name safe inside a header: no quotes, control characters or path separators.
func attachmentName(n string) string {
	n = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r == '/' || r > 0x7e {
			return '_'
		}
		return r
	}, n)
	if strings.TrimSpace(n) == "" {
		return "attachment"
	}
	return n
}
