package applications

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Angle-HR/server/pkg/formschema"
	"github.com/Angle-HR/server/pkg/rbac"
)

// Limits of candidate email.
const (
	MaxTemplateName    = 60
	MaxSubject         = 200
	MaxTemplateBody    = 10000
	MaxMessageBody     = 12000
	MaxTemplates       = 50
	MaxPerApplication  = 10  // messages to one candidate in 24 hours
	MaxPerCompany      = 500 // messages by one company in 24 hours
	MaxScheduleDays    = 90  // how far ahead a message can be scheduled
	MinScheduleSeconds = 60  // a scheduled message is at least this far in the future
	MaxReplyTo         = 254
)

// Message statuses.
const (
	MsgScheduled = "scheduled"
	MsgQueued    = "queued"
	MsgSent      = "sent"
	MsgFailed    = "failed"
	MsgCancelled = "cancelled"
)

// Errors of candidate email.
var (
	// ErrQuota: the application or the company reached its daily limit of messages.
	ErrQuota = errors.New("applications: message limit reached")
	// ErrNotCancellable: the message was sent, failed or cancelled already.
	ErrNotCancellable = errors.New("applications: message is not waiting to be sent")
	// ErrNoMessages: the service has no message store.
	ErrNoMessages = errors.New("applications: candidate email is not available")
)

// Template is a reusable email.
type Template struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Message is an email to a candidate, as the history shows it.
type Message struct {
	ID          string     `json:"id"`
	SenderID    string     `json:"sender_id,omitempty"`
	TemplateID  string     `json:"template_id,omitempty"`
	Subject     string     `json:"subject"`
	Body        string     `json:"body"`
	Status      string     `json:"status"`
	ScheduledAt time.Time  `json:"scheduled_at"`
	SentAt      *time.Time `json:"sent_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	// Error is a short reason when Status is failed.
	Error       string       `json:"error,omitempty"`
	Attachments []Attachment `json:"attachments"`
}

// NewMessage is a message to store: the text is already rendered.
type NewMessage struct {
	JobID, AppID, SenderID, TemplateID string
	Subject, Body, ReplyTo, Company    string
	// ICS is the calendar event of an interview invite, update or cancellation.
	ICS string
	// Secret is carried to the sender and cleared once the message has gone: the token of an offer link, which the
	// body names as {{offer_link}}.
	Secret string
	// JobTitle is the job\'s title, for the footer of the email.
	JobTitle    string
	Status      string // MsgScheduled or MsgQueued
	ScheduledAt time.Time
	// AttachmentIDs are uploaded files to send with the message.
	AttachmentIDs []string
	// Enforce applies the daily limits. Messages that come with a decision skip them.
	Enforce bool
}

// MessageStore is the part of the store that keeps templates, messages and the reply-to address.
type MessageStore interface {
	Templates(ctx context.Context, tenantID string) ([]Template, error)
	Template(ctx context.Context, tenantID, id string) (*Template, error)
	CreateTemplate(ctx context.Context, tenantID, actorID, name, subject, body string) (*Template, error)
	UpdateTemplate(ctx context.Context, tenantID, id, name, subject, body string) (*Template, error)
	DeleteTemplate(ctx context.Context, tenantID, id string) error
	ReplyTo(ctx context.Context, tenantID string) (string, error)
	SetReplyTo(ctx context.Context, tenantID, actorID, replyTo string) error
	QueueMessage(ctx context.Context, tenantID string, m *NewMessage) (*Message, error)
	Messages(ctx context.Context, tenantID, jobID, appID string) ([]Message, error)
	CancelMessage(ctx context.Context, tenantID, jobID, appID, id string) error
}

func (s *Service) messages() (MessageStore, error) {
	if s.Mail == nil {
		return nil, ErrNoMessages
	}
	return s.Mail, nil
}

// Placeholders a template or a message may use, written {{name}}.
var Placeholders = []string{"candidate_name", "job_title", "company", "sender_name"}

var placeholderRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_]*)\s*\}\}`)

// checkPlaceholders returns the first unknown placeholder, or "".
func checkPlaceholders(text string) string {
	for _, m := range placeholderRe.FindAllStringSubmatch(text, -1) {
		if !contains(Placeholders, m[1]) {
			return "{{" + m[1] + "}}"
		}
	}
	return ""
}

// Render fills the placeholders. A value cannot add a placeholder of its own: the text is replaced in one pass.
func Render(text string, vals map[string]string) string {
	return placeholderRe.ReplaceAllStringFunc(text, func(m string) string {
		return vals[placeholderRe.FindStringSubmatch(m)[1]]
	})
}

func cleanText(field, v string, max int) (string, error) {
	v = strings.ReplaceAll(strings.TrimSpace(v), "\r\n", "\n")
	if v == "" {
		return "", invalid(field, "this cannot be empty")
	}
	if utf8.RuneCountInString(v) > max {
		return "", invalid(field, fmt.Sprintf("keep this under %d characters", max+1))
	}
	if bad := checkPlaceholders(v); bad != "" {
		return "", invalid(field, bad+" is not a placeholder; use "+strings.Join(Placeholders, ", "))
	}
	return v, nil
}

func cleanSubject(v string) (string, error) {
	v = strings.Join(strings.Fields(v), " ") // one line
	return cleanText("subject", v, MaxSubject)
}

func readers(c Caller) bool { return has(c, rbac.JobViewAll) || has(c, rbac.JobViewAssigned) }

// Templates lists the company's email templates.
func (s *Service) Templates(ctx context.Context, c Caller) ([]Template, error) {
	m, err := s.messages()
	if err != nil {
		return nil, err
	}
	if !readers(c) {
		return nil, forbidden("you cannot view email templates")
	}
	return m.Templates(ctx, c.OrgID)
}

func (s *Service) cleanTemplate(name, subject, body string) (n, sub, b string, err error) {
	if n, err = cleanName(name, MaxTemplateName, "name"); err != nil {
		return
	}
	if sub, err = cleanSubject(subject); err != nil {
		return
	}
	b, err = cleanText("body", body, MaxTemplateBody)
	return
}

// CreateTemplate adds a template. Needs job.manage_any.
func (s *Service) CreateTemplate(ctx context.Context, c Caller, name, subject, body string) (*Template, error) {
	m, err := s.messages()
	if err != nil {
		return nil, err
	}
	if !has(c, rbac.JobManageAny) {
		return nil, forbidden("you cannot manage email templates")
	}
	n, sub, b, err := s.cleanTemplate(name, subject, body)
	if err != nil {
		return nil, err
	}
	if list, lerr := m.Templates(ctx, c.OrgID); lerr == nil && len(list) >= MaxTemplates {
		return nil, invalid("name", fmt.Sprintf("a company can have at most %d templates", MaxTemplates))
	}
	return m.CreateTemplate(ctx, c.OrgID, c.UserID, n, sub, b)
}

// UpdateTemplate replaces a template's name, subject and body.
func (s *Service) UpdateTemplate(ctx context.Context, c Caller, id, name, subject, body string) (*Template, error) {
	m, err := s.messages()
	if err != nil {
		return nil, err
	}
	if !has(c, rbac.JobManageAny) {
		return nil, forbidden("you cannot manage email templates")
	}
	if !formschema.IsUUID(id) {
		return nil, ErrNotFound
	}
	n, sub, b, err := s.cleanTemplate(name, subject, body)
	if err != nil {
		return nil, err
	}
	return m.UpdateTemplate(ctx, c.OrgID, id, n, sub, b)
}

// DeleteTemplate removes a template; messages already sent from it keep their text.
func (s *Service) DeleteTemplate(ctx context.Context, c Caller, id string) error {
	m, err := s.messages()
	if err != nil {
		return err
	}
	if !has(c, rbac.JobManageAny) {
		return forbidden("you cannot manage email templates")
	}
	if !formschema.IsUUID(id) {
		return ErrNotFound
	}
	return m.DeleteTemplate(ctx, c.OrgID, id)
}

// ReplyTo returns the address replies to candidate emails go to ("" when none is set).
func (s *Service) ReplyTo(ctx context.Context, c Caller) (string, error) {
	m, err := s.messages()
	if err != nil {
		return "", err
	}
	if !readers(c) {
		return "", forbidden("you cannot view email settings")
	}
	return m.ReplyTo(ctx, c.OrgID)
}

// SetReplyTo sets (or, when empty, clears) the reply-to address. Needs job.manage_any.
func (s *Service) SetReplyTo(ctx context.Context, c Caller, addr string) (string, error) {
	m, err := s.messages()
	if err != nil {
		return "", err
	}
	if !has(c, rbac.JobManageAny) {
		return "", forbidden("you cannot change email settings")
	}
	addr = strings.TrimSpace(addr)
	if addr != "" {
		a, perr := mail.ParseAddress(addr)
		if perr != nil || a.Name != "" || a.Address != addr || len(addr) > MaxReplyTo || strings.ContainsAny(addr, "\r\n") {
			return "", invalid("reply_to", "enter an email address")
		}
	}
	return addr, m.SetReplyTo(ctx, c.OrgID, c.UserID, addr)
}

// MessageInput is what a person writes to a candidate: a template, or a subject and body, and optionally when.
type MessageInput struct {
	TemplateID string
	Subject    string
	Body       string
	// ScheduleAt, when set, sends the message then instead of now.
	ScheduleAt *time.Time
	// AttachmentIDs are files uploaded to the application (at most 3), sent with the message.
	AttachmentIDs []string
}

// Preview is a message as the candidate would get it.
type Preview struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// compose resolves the template, validates and renders the text for this candidate.
func (s *Service) compose(ctx context.Context, c Caller, st *ChangeState, in MessageInput) (*Preview, string, error) {
	m, err := s.messages()
	if err != nil {
		return nil, "", err
	}
	subject, body := in.Subject, in.Body
	if in.TemplateID != "" {
		if !formschema.IsUUID(in.TemplateID) {
			return nil, "", invalid("template_id", "unknown template")
		}
		t, terr := m.Template(ctx, c.OrgID, in.TemplateID)
		if errors.Is(terr, ErrNotFound) {
			return nil, "", invalid("template_id", "unknown template")
		}
		if terr != nil {
			return nil, "", terr
		}
		if strings.TrimSpace(subject) == "" {
			subject = t.Subject
		}
		if strings.TrimSpace(body) == "" {
			body = t.Body
		}
	}
	sub, err := cleanSubject(subject)
	if err != nil {
		return nil, "", err
	}
	b, err := cleanText("body", body, MaxMessageBody)
	if err != nil {
		return nil, "", err
	}
	sender := "the hiring team"
	if s.Names != nil && c.UserID != "" {
		if n, nerr := s.Names.Names(ctx, c.Region, []string{c.UserID}); nerr == nil && strings.TrimSpace(n[c.UserID]) != "" {
			sender = n[c.UserID]
		}
	}
	vals := map[string]string{"candidate_name": st.Name, "job_title": st.JobTitle, "company": st.Company, "sender_name": sender}
	out := &Preview{Subject: cleanHeaderText(Render(sub, vals)), Body: Render(b, vals)}
	if out.Subject == "" || strings.TrimSpace(out.Body) == "" {
		return nil, "", invalid("body", "the message is empty once the placeholders are filled")
	}
	if utf8.RuneCountInString(out.Subject) > MaxSubject {
		return nil, "", invalid("subject", fmt.Sprintf("keep the subject under %d characters once filled", MaxSubject+1))
	}
	reply, err := m.ReplyTo(ctx, c.OrgID)
	return out, reply, err
}

// cleanHeaderText keeps a subject on one line.
func cleanHeaderText(v string) string { return strings.Join(strings.Fields(v), " ") }

// messageTarget finds the application's candidate and checks the caller may write to them: the people who decide
// on the job. Candidates who withdrew are not written to.
func (s *Service) messageTarget(ctx context.Context, c Caller, jobID, appID string) (*ChangeState, error) {
	a, err := s.access(ctx, c, jobID)
	if err != nil {
		return nil, err
	}
	if !canDecide(c, a) {
		return nil, forbidden("you cannot email candidates of this job")
	}
	if !formschema.IsUUID(appID) {
		return nil, ErrNotFound
	}
	st, err := s.loadChange(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	if st.Status == StatusWithdrawn {
		return nil, ErrInvalidTransition
	}
	return st, nil
}

// PreviewMessage renders a message for the application's candidate without sending it.
func (s *Service) PreviewMessage(ctx context.Context, c Caller, jobID, appID string, in MessageInput) (*Preview, error) {
	st, err := s.messageTarget(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	p, _, err := s.compose(ctx, c, st, in)
	return p, err
}

// SendMessage emails the application's candidate now, or at in.ScheduleAt. The recipient is always the
// application's candidate.
func (s *Service) SendMessage(ctx context.Context, c Caller, jobID, appID string, in MessageInput) (*Message, error) {
	st, err := s.messageTarget(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	p, reply, err := s.compose(ctx, c, st, in)
	if err != nil {
		return nil, err
	}
	ids, err := cleanAttachmentIDs(in.AttachmentIDs)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	nm := &NewMessage{JobID: jobID, AppID: appID, SenderID: c.UserID, TemplateID: in.TemplateID, Subject: p.Subject, Body: p.Body,
		ReplyTo: reply, Company: st.Company, JobTitle: st.JobTitle, Status: MsgQueued, ScheduledAt: now, Enforce: true, AttachmentIDs: ids}
	if in.ScheduleAt != nil {
		at := in.ScheduleAt.UTC()
		if at.Before(now.Add(MinScheduleSeconds*time.Second)) || at.After(now.AddDate(0, 0, MaxScheduleDays)) {
			return nil, invalid("schedule_at", fmt.Sprintf("choose a time between a minute and %d days from now", MaxScheduleDays))
		}
		nm.Status, nm.ScheduledAt = MsgScheduled, at
	}
	m, _ := s.messages()
	return m.QueueMessage(ctx, c.OrgID, nm)
}

// Messages lists the emails sent to the application's candidate, newest first. Anyone who can see the job can.
func (s *Service) Messages(ctx context.Context, c Caller, jobID, appID string) ([]Message, error) {
	m, err := s.messages()
	if err != nil {
		return nil, err
	}
	if _, err = s.access(ctx, c, jobID); err != nil {
		return nil, err
	}
	if !formschema.IsUUID(appID) {
		return nil, ErrNotFound
	}
	return m.Messages(ctx, c.OrgID, jobID, appID)
}

// CancelMessage stops a scheduled or queued message that has not gone out.
func (s *Service) CancelMessage(ctx context.Context, c Caller, jobID, appID, id string) error {
	m, err := s.messages()
	if err != nil {
		return err
	}
	a, err := s.access(ctx, c, jobID)
	if err != nil {
		return err
	}
	if !canDecide(c, a) {
		return forbidden("you cannot email candidates of this job")
	}
	if !formschema.IsUUID(appID) || !formschema.IsUUID(id) {
		return ErrNotFound
	}
	return m.CancelMessage(ctx, c.OrgID, jobID, appID, id)
}

// decisionMessage renders the message that goes with a decision. It replaces the standard decision email, so the
// candidate gets one email, written by a person.
func (s *Service) decisionMessage(ctx context.Context, c Caller, st *ChangeState, jobID, appID string, in *MessageInput) (*NewMessage, error) {
	if in == nil {
		return nil, nil
	}
	if in.ScheduleAt != nil {
		return nil, invalid("message.schedule_at", "a message sent with a decision goes out now")
	}
	p, reply, err := s.compose(ctx, c, st, *in)
	if err != nil {
		return nil, err
	}
	ids, err := cleanAttachmentIDs(in.AttachmentIDs)
	if err != nil {
		return nil, err
	}
	return &NewMessage{JobID: jobID, AppID: appID, SenderID: c.UserID, TemplateID: in.TemplateID, Subject: p.Subject, Body: p.Body,
		ReplyTo: reply, Company: st.Company, JobTitle: st.JobTitle, Status: MsgQueued, ScheduledAt: s.now().UTC(), AttachmentIDs: ids}, nil
}
