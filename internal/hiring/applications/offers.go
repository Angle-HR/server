package applications

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/pkg/formschema"
	"github.com/Angle-HR/server/pkg/platform/blob"
	"github.com/Angle-HR/server/pkg/rbac"
)

// Limits of offers.
const (
	MaxOfferTitle     = 200
	MaxOfferBody      = 30000
	MaxOfferCompens   = 200
	MaxOfferTemplates = 30
	DefaultOfferDays  = 7
	MaxOfferDays      = 30
	MaxDeclineReason  = 1000
	MaxSignedName     = 100
)

// Offer statuses.
const (
	OfferDraft     = "draft"
	OfferSent      = "sent"
	OfferViewed    = "viewed"
	OfferSigned    = "signed"
	OfferDeclined  = "declined"
	OfferExpired   = "expired"
	OfferWithdrawn = "withdrawn"
)

// Audit events of an offer.
const (
	OfferEvCreated   = "created"
	OfferEvEdited    = "edited"
	OfferEvSent      = "sent"
	OfferEvViewed    = "viewed"
	OfferEvSigned    = "signed"
	OfferEvDeclined  = "declined"
	OfferEvExpired   = "expired"
	OfferEvWithdrawn = "withdrawn"
)

// ConsentVersion and ConsentText are what a candidate agrees to by signing. The text's hash is in the audit trail.
const (
	ConsentVersion = "2026-11-v1"
	ConsentText    = "I have read this offer. I agree that typing my full name below is my electronic signature and that it has the same effect as a handwritten signature."
)

// Errors of offers.
var (
	// ErrOfferLive: the application already has an offer that is not over.
	ErrOfferLive = errors.New("applications: the application already has a live offer")
	// ErrOfferClosed: the offer was signed, declined, withdrawn or has expired.
	ErrOfferClosed = errors.New("applications: the offer is closed")
	// ErrOfferNotDraft: only a draft can be edited.
	ErrOfferNotDraft = errors.New("applications: only a draft offer can be edited")
	// ErrOfferUnavailable: the candidate can no longer accept (the application was decided or withdrawn meanwhile).
	ErrOfferUnavailable = errors.New("applications: the offer is no longer available")
	// ErrNoOffers: the service has no offer store.
	ErrNoOffers = errors.New("applications: offers are not available")
)

// Offer is an offer to a candidate, as the company sees it.
type Offer struct {
	ID            string     `json:"id"`
	JobID         string     `json:"job_id"`
	ApplicationID string     `json:"application_id"`
	Title         string     `json:"title"`
	Body          string     `json:"body"`
	Compensation  string     `json:"compensation"`
	StartDate     string     `json:"start_date,omitempty"`
	Status        string     `json:"status"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	SentAt        *time.Time `json:"sent_at,omitempty"`
	ViewedAt      *time.Time `json:"viewed_at,omitempty"`
	SignedAt      *time.Time `json:"signed_at,omitempty"`
	DeclinedAt    *time.Time `json:"declined_at,omitempty"`
	DeclineReason string     `json:"decline_reason,omitempty"`
	SignedName    string     `json:"signed_name,omitempty"`
	// HasDocument is true once the signed document exists.
	HasDocument bool      `json:"has_document"`
	CreatedBy   string    `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Internal fields.
	Sequence   int    `json:"-"`
	DocHash    string `json:"-"`
	PDFKey     string `json:"-"`
	TemplateID string `json:"-"`
}

// OfferTemplate is a reusable offer letter.
type OfferTemplate struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OfferEvent is one entry of an offer's audit trail.
type OfferEvent struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	ActorID   string          `json:"actor_id,omitempty"`
	IP        string          `json:"ip,omitempty"`
	UserAgent string          `json:"user_agent,omitempty"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}

// PublicOffer is what the candidate sees through their link.
type PublicOffer struct {
	Status        string     `json:"status"`
	Title         string     `json:"title"`
	Body          string     `json:"body"`
	Company       string     `json:"company"`
	JobTitle      string     `json:"job_title"`
	CandidateName string     `json:"candidate_name"`
	Compensation  string     `json:"compensation,omitempty"`
	StartDate     string     `json:"start_date,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	SignedAt      *time.Time `json:"signed_at,omitempty"`
	SignedName    string     `json:"signed_name,omitempty"`
	// CanRespond is true while the candidate can sign or decline.
	CanRespond  bool   `json:"can_respond"`
	ConsentText string `json:"consent_text"`
	// HasDocument is true once the signed document exists.
	HasDocument bool `json:"has_document"`
}

// Meta is who acted and from where, for the audit trail.
type Meta struct{ IP, UserAgent string }

// OfferRef locates an offer by its token without touching it.
type OfferRef struct {
	OfferID, JobID, AppID string
	Status                string
}

// SignRecord is everything the store writes when an offer is signed.
type SignRecord struct {
	TokenHash       string
	Name            string
	Now             time.Time
	Meta            Meta
	ConsentHash     string
	RetentionMonths int
	// Confirmation is the message that tells the candidate their signature was recorded.
	Confirmation *NewMessage
}

// OfferStore keeps offers.
type OfferStore interface {
	OfferTemplates(ctx context.Context, tenantID string) ([]OfferTemplate, error)
	OfferTemplate(ctx context.Context, tenantID, id string) (*OfferTemplate, error)
	CreateOfferTemplate(ctx context.Context, tenantID, actorID, name, body string) (*OfferTemplate, error)
	UpdateOfferTemplate(ctx context.Context, tenantID, id, name, body string) (*OfferTemplate, error)
	DeleteOfferTemplate(ctx context.Context, tenantID, id string) error

	Offers(ctx context.Context, tenantID, jobID, appID string, now time.Time) ([]Offer, error)
	Offer(ctx context.Context, tenantID, jobID, appID, id string, now time.Time) (*Offer, error)
	// CreateOffer saves a draft; ErrOfferLive when the application has a live offer.
	CreateOffer(ctx context.Context, tenantID, actorID string, o *Offer) error
	// SaveDraft replaces a draft that is still at prevSeq (ErrStale otherwise); ErrOfferNotDraft for any other status.
	SaveDraft(ctx context.Context, tenantID, actorID string, o *Offer, prevSeq int) error
	// SendOffer makes the offer sent with a new link (replacing any earlier one), writes the message that carries
	// the link, and the audit entry. Allowed from draft, sent and viewed.
	SendOffer(ctx context.Context, tenantID, actorID string, o *Offer, tokenHash string, msg *NewMessage, now time.Time) error
	WithdrawOffer(ctx context.Context, tenantID, actorID, jobID, appID, id string, now time.Time) error
	OfferEvents(ctx context.Context, tenantID, jobID, appID, id string) ([]OfferEvent, error)

	OfferByToken(ctx context.Context, tenantID, tokenHash string) (*OfferRef, error)
	// ViewOffer returns what the candidate sees and records the first view and every one in the audit trail.
	ViewOffer(ctx context.Context, tenantID, tokenHash string, m Meta, now time.Time) (*PublicOffer, error)
	// SignOffer records the signature and, in the same transaction, hires the candidate.
	SignOffer(ctx context.Context, tenantID string, r *SignRecord) (*Offer, error)
	DeclineOffer(ctx context.Context, tenantID, tokenHash, reason string, m Meta, now time.Time) error
	// DocumentData loads a signed offer with its audit trail, to write the document.
	DocumentData(ctx context.Context, tenantID, offerID string) (*DocumentData, error)
	SetOfferDocument(ctx context.Context, tenantID, offerID, key string) error
}

// DocumentData is what the signed document is made from.
type DocumentData struct {
	Offer         Offer
	Company       string
	JobTitle      string
	CandidateName string
	Events        []OfferEvent
	ConsentHash   string
	SignerIP      string
	SignerAgent   string
}

// DocWriter turns signed offer data into a PDF.
type DocWriter func(d *DocumentData) ([]byte, error)

// OfferDocs stores signed documents and links to them. blob.BlobStore implements it.
type OfferDocs interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	PresignGet(ctx context.Context, key string, opts blob.GetOptions) (string, error)
}

func (s *Service) offers() (OfferStore, error) {
	if s.Contracts == nil {
		return nil, ErrNoOffers
	}
	return s.Contracts, nil
}

// ---- tokens and hashing ----

var tokenRe = regexp.MustCompile(`^([a-z]{2,8})\.([0-9a-f-]{36})\.([A-Za-z0-9_-]{43})$`)

// NewOfferToken makes a link token: the region and company (so the public link finds its database without a
// lookup) and a 256 bit secret. Only the hash of the secret is ever stored.
func NewOfferToken(region, tenantID string) (token, secretHash string, err error) {
	var b [32]byte
	if _, err = rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("applications: offer token: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(b[:])
	return region + "." + tenantID + "." + secret, HashSecret(secret), nil
}

// HashSecret is the SHA-256 of a token's secret, in hex.
func HashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// ParseOfferToken splits a link token. ok is false for anything that is not shaped like one.
func ParseOfferToken(token string) (region, tenantID, secretHash string, ok bool) {
	m := tokenRe.FindStringSubmatch(token)
	if m == nil || !formschema.IsUUID(m[2]) {
		return "", "", "", false
	}
	return m[1], m[2], HashSecret(m[3]), true
}

// OfferDocHash is the SHA-256 of the terms of an offer. A signature is over this hash.
func OfferDocHash(title, body, compensation, startDate string) string {
	h := sha256.New()
	for _, part := range []string{title, body, compensation, startDate} {
		_, _ = fmt.Fprintf(h, "%d:%s\n", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ConsentHash is the SHA-256 of the consent text with its version.
func ConsentHash() string {
	h := sha256.Sum256([]byte(ConsentVersion + "\n" + ConsentText))
	return hex.EncodeToString(h[:])
}

// ---- placeholders ----

// OfferPlaceholders may be used in an offer letter.
var OfferPlaceholders = []string{"candidate_name", "job_title", "company", "sender_name", "start_date", "compensation"}

func checkOfferPlaceholders(field, text string) error {
	for _, m := range placeholderRe.FindAllStringSubmatch(text, -1) {
		if !contains(OfferPlaceholders, m[1]) {
			return invalid(field, "{{"+m[1]+"}} is not a placeholder; use "+strings.Join(OfferPlaceholders, ", "))
		}
	}
	return nil
}

func cleanOfferText(field, v string, max int, required bool) (string, error) {
	v = strings.ReplaceAll(strings.TrimSpace(v), "\r\n", "\n")
	if v == "" && required {
		return "", invalid(field, "this cannot be empty")
	}
	if utf8.RuneCountInString(v) > max {
		return "", invalid(field, fmt.Sprintf("keep this under %d characters", max+1))
	}
	return v, checkOfferPlaceholders(field, v)
}

// ---- templates ----

func (s *Service) offerTemplatesAllowed(c Caller, write bool) error {
	if write && !has(c, rbac.JobManageAny) {
		return forbidden("you cannot manage offer templates")
	}
	if !write && !readers(c) {
		return forbidden("you cannot view offer templates")
	}
	return nil
}

// OfferTemplates lists the company's offer templates.
func (s *Service) OfferTemplates(ctx context.Context, c Caller) ([]OfferTemplate, error) {
	o, err := s.offers()
	if err != nil {
		return nil, err
	}
	if err = s.offerTemplatesAllowed(c, false); err != nil {
		return nil, err
	}
	return o.OfferTemplates(ctx, c.OrgID)
}

// CreateOfferTemplate adds a template. Needs job.manage_any.
func (s *Service) CreateOfferTemplate(ctx context.Context, c Caller, name, body string) (*OfferTemplate, error) {
	o, err := s.offers()
	if err != nil {
		return nil, err
	}
	if err = s.offerTemplatesAllowed(c, true); err != nil {
		return nil, err
	}
	n, err := cleanName(name, MaxTemplateName, "name")
	if err != nil {
		return nil, err
	}
	b, err := cleanOfferText("body", body, MaxOfferBody, true)
	if err != nil {
		return nil, err
	}
	if list, lerr := o.OfferTemplates(ctx, c.OrgID); lerr == nil && len(list) >= MaxOfferTemplates {
		return nil, invalid("name", fmt.Sprintf("a company can have at most %d offer templates", MaxOfferTemplates))
	}
	return o.CreateOfferTemplate(ctx, c.OrgID, c.UserID, n, b)
}

// UpdateOfferTemplate replaces a template.
func (s *Service) UpdateOfferTemplate(ctx context.Context, c Caller, id, name, body string) (*OfferTemplate, error) {
	o, err := s.offers()
	if err != nil {
		return nil, err
	}
	if err = s.offerTemplatesAllowed(c, true); err != nil {
		return nil, err
	}
	if !formschema.IsUUID(id) {
		return nil, ErrNotFound
	}
	n, err := cleanName(name, MaxTemplateName, "name")
	if err != nil {
		return nil, err
	}
	b, err := cleanOfferText("body", body, MaxOfferBody, true)
	if err != nil {
		return nil, err
	}
	return o.UpdateOfferTemplate(ctx, c.OrgID, id, n, b)
}

// DeleteOfferTemplate removes a template; offers already made keep their text.
func (s *Service) DeleteOfferTemplate(ctx context.Context, c Caller, id string) error {
	o, err := s.offers()
	if err != nil {
		return err
	}
	if err = s.offerTemplatesAllowed(c, true); err != nil {
		return err
	}
	if !formschema.IsUUID(id) {
		return ErrNotFound
	}
	return o.DeleteOfferTemplate(ctx, c.OrgID, id)
}

// ---- offers (company side) ----

// OfferInput creates an offer.
type OfferInput struct {
	TemplateID   string
	Title        string // default: "Offer of employment: {job_title}"
	Body         string // the letter; overrides the template's
	Compensation string
	StartDate    string // YYYY-MM-DD
	// Send sends the offer to the candidate right away, valid for ExpiresInDays (7 by default, 30 at most).
	Send          bool
	ExpiresInDays int
}

// OfferPatch changes a draft; nil leaves a field.
type OfferPatch struct {
	Title        *string
	Body         *string
	Compensation *string
	StartDate    *string
}

func (s *Service) offerDecider(ctx context.Context, c Caller, jobID, appID string) (OfferStore, error) {
	o, err := s.offers()
	if err != nil {
		return nil, err
	}
	a, err := s.access(ctx, c, jobID)
	if err != nil {
		return nil, err
	}
	if !canDecide(c, a) {
		return nil, forbidden("you cannot make offers on this job")
	}
	if !formschema.IsUUID(appID) {
		return nil, ErrNotFound
	}
	return o, nil
}

func (s *Service) senderName(ctx context.Context, c Caller) string {
	if s.Names != nil && c.UserID != "" {
		if n, err := s.Names.Names(ctx, c.Region, []string{c.UserID}); err == nil && strings.TrimSpace(n[c.UserID]) != "" {
			return n[c.UserID]
		}
	}
	return "the hiring team"
}

func cleanStartDate(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if _, err := time.Parse("2006-01-02", v); err != nil {
		return "", invalid("start_date", "enter a date as YYYY-MM-DD")
	}
	return v, nil
}

// renderOffer fills the placeholders of an offer's text.
func (s *Service) renderOffer(ctx context.Context, c Caller, st *ChangeState, text, compensation, startDate string) string {
	when := ""
	if startDate != "" {
		if t, err := time.Parse("2006-01-02", startDate); err == nil {
			when = t.Format("2 January 2006")
		}
	}
	return Render(text, map[string]string{"candidate_name": st.Name, "job_title": st.JobTitle, "company": st.Company,
		"sender_name": s.senderName(ctx, c), "start_date": when, "compensation": compensation})
}

// CreateOffer makes a draft offer for an application that is in review, and sends it when asked.
func (s *Service) CreateOffer(ctx context.Context, c Caller, jobID, appID string, in OfferInput) (*Offer, error) {
	os, err := s.offerDecider(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	st, err := s.loadChange(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	if st.Status != StatusInReview {
		return nil, ErrInvalidTransition // an offer is made to a candidate in review: not received, flagged or decided
	}
	comp, err := cleanOfferText("compensation", in.Compensation, MaxOfferCompens, false)
	if err != nil {
		return nil, err
	}
	start, err := cleanStartDate(in.StartDate)
	if err != nil {
		return nil, err
	}
	body := in.Body
	if strings.TrimSpace(body) == "" && in.TemplateID != "" {
		if !formschema.IsUUID(in.TemplateID) {
			return nil, invalid("template_id", "unknown template")
		}
		t, terr := os.OfferTemplate(ctx, c.OrgID, in.TemplateID)
		if errors.Is(terr, ErrNotFound) {
			return nil, invalid("template_id", "unknown template")
		}
		if terr != nil {
			return nil, terr
		}
		body = t.Body
	}
	body, err = cleanOfferText("body", body, MaxOfferBody, true)
	if err != nil {
		return nil, err
	}
	title := strings.Join(strings.Fields(in.Title), " ")
	if title == "" {
		title = "Offer of employment: {{job_title}}"
	}
	if title, err = cleanOfferText("title", title, MaxOfferTitle, true); err != nil {
		return nil, err
	}
	o := &Offer{ID: uuid.NewString(), JobID: jobID, ApplicationID: appID, Status: OfferDraft, CreatedBy: c.UserID, TemplateID: in.TemplateID,
		Title: strings.Join(strings.Fields(s.renderOffer(ctx, c, st, title, comp, start)), " "),
		Body:  s.renderOffer(ctx, c, st, body, comp, start), Compensation: comp, StartDate: start}
	if err = os.CreateOffer(ctx, c.OrgID, c.UserID, o); err != nil {
		return nil, err
	}
	if in.Send {
		return s.sendOffer(ctx, c, os, st, o, in.ExpiresInDays)
	}
	return o, nil
}

// UpdateOffer changes a draft.
func (s *Service) UpdateOffer(ctx context.Context, c Caller, jobID, appID, id string, p OfferPatch) (*Offer, error) {
	os, err := s.offerDecider(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	if !formschema.IsUUID(id) {
		return nil, ErrNotFound
	}
	o, err := os.Offer(ctx, c.OrgID, jobID, appID, id, s.now())
	if err != nil {
		return nil, err
	}
	if o.Status != OfferDraft {
		return nil, ErrOfferNotDraft
	}
	st, err := s.loadChange(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	prev := o.Sequence
	if p.Compensation != nil {
		if o.Compensation, err = cleanOfferText("compensation", *p.Compensation, MaxOfferCompens, false); err != nil {
			return nil, err
		}
	}
	if p.StartDate != nil {
		if o.StartDate, err = cleanStartDate(*p.StartDate); err != nil {
			return nil, err
		}
	}
	if p.Title != nil {
		t, terr := cleanOfferText("title", strings.Join(strings.Fields(*p.Title), " "), MaxOfferTitle, true)
		if terr != nil {
			return nil, terr
		}
		o.Title = strings.Join(strings.Fields(s.renderOffer(ctx, c, st, t, o.Compensation, o.StartDate)), " ")
	}
	if p.Body != nil {
		b, berr := cleanOfferText("body", *p.Body, MaxOfferBody, true)
		if berr != nil {
			return nil, berr
		}
		o.Body = s.renderOffer(ctx, c, st, b, o.Compensation, o.StartDate)
	}
	o.Sequence++
	if err = os.SaveDraft(ctx, c.OrgID, c.UserID, o, prev); err != nil {
		return nil, err
	}
	return o, nil
}

// SendOffer sends a draft, or sends a sent offer again with a new link (the earlier link stops working).
func (s *Service) SendOffer(ctx context.Context, c Caller, jobID, appID, id string, expiresInDays int) (*Offer, error) {
	os, err := s.offerDecider(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	if !formschema.IsUUID(id) {
		return nil, ErrNotFound
	}
	o, err := os.Offer(ctx, c.OrgID, jobID, appID, id, s.now())
	if err != nil {
		return nil, err
	}
	st, err := s.loadChange(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	if st.Status != StatusInReview {
		return nil, ErrInvalidTransition
	}
	return s.sendOffer(ctx, c, os, st, o, expiresInDays)
}

func (s *Service) sendOffer(ctx context.Context, c Caller, os OfferStore, st *ChangeState, o *Offer, days int) (*Offer, error) {
	if o.Status != OfferDraft && o.Status != OfferSent && o.Status != OfferViewed {
		return nil, ErrOfferClosed
	}
	if days == 0 {
		days = DefaultOfferDays
	}
	if days < 1 || days > MaxOfferDays {
		return nil, invalid("expires_in_days", fmt.Sprintf("choose between 1 and %d days", MaxOfferDays))
	}
	now := s.now().UTC()
	exp := now.AddDate(0, 0, days)
	token, hash, err := NewOfferToken(c.Region, c.OrgID)
	if err != nil {
		return nil, err
	}
	o.ExpiresAt, o.DocHash = &exp, OfferDocHash(o.Title, o.Body, o.Compensation, o.StartDate)
	body := fmt.Sprintf("Hi %s,\n\nGood news: %s would like to offer you the %s role. You can read the offer and answer it here until %s:\n\n{{offer_link}}\n\nIf you were not expecting this email, you can ignore it.",
		st.Name, st.Company, st.JobTitle, exp.Format("2 January 2006"))
	msg := &NewMessage{JobID: o.JobID, AppID: o.ApplicationID, SenderID: c.UserID, Subject: "Your offer for " + st.JobTitle, Body: body,
		Company: st.Company, JobTitle: st.JobTitle, Secret: token, Status: MsgQueued, ScheduledAt: now}
	if m, merr := s.messages(); merr == nil {
		msg.ReplyTo, _ = m.ReplyTo(ctx, c.OrgID)
	}
	if err = os.SendOffer(ctx, c.OrgID, c.UserID, o, hash, msg, now); err != nil {
		return nil, err
	}
	o.Status, o.SentAt = OfferSent, &now
	return o, nil
}

// WithdrawOffer takes back an offer that was not answered.
func (s *Service) WithdrawOffer(ctx context.Context, c Caller, jobID, appID, id string) error {
	os, err := s.offerDecider(ctx, c, jobID, appID)
	if err != nil {
		return err
	}
	if !formschema.IsUUID(id) {
		return ErrNotFound
	}
	return os.WithdrawOffer(ctx, c.OrgID, c.UserID, jobID, appID, id, s.now().UTC())
}

// Offers lists an application's offers, newest first. Anyone who can see the job can.
func (s *Service) Offers(ctx context.Context, c Caller, jobID, appID string) ([]Offer, error) {
	os, err := s.offers()
	if err != nil {
		return nil, err
	}
	if _, err = s.access(ctx, c, jobID); err != nil {
		return nil, err
	}
	if !formschema.IsUUID(appID) {
		return nil, ErrNotFound
	}
	return os.Offers(ctx, c.OrgID, jobID, appID, s.now())
}

// OfferAudit returns an offer's audit trail, oldest first.
func (s *Service) OfferAudit(ctx context.Context, c Caller, jobID, appID, id string) ([]OfferEvent, error) {
	os, err := s.offers()
	if err != nil {
		return nil, err
	}
	if _, err = s.access(ctx, c, jobID); err != nil {
		return nil, err
	}
	if !formschema.IsUUID(appID) || !formschema.IsUUID(id) {
		return nil, ErrNotFound
	}
	return os.OfferEvents(ctx, c.OrgID, jobID, appID, id)
}

// OfferDocument returns a download link for the signed document, writing it first if it is not there yet.
func (s *Service) OfferDocument(ctx context.Context, c Caller, jobID, appID, id string) (*FileLink, error) {
	os, err := s.offers()
	if err != nil {
		return nil, err
	}
	if _, err = s.access(ctx, c, jobID); err != nil {
		return nil, err
	}
	if !formschema.IsUUID(appID) || !formschema.IsUUID(id) {
		return nil, ErrNotFound
	}
	o, err := os.Offer(ctx, c.OrgID, jobID, appID, id, s.now())
	if err != nil {
		return nil, err
	}
	if o.Status != OfferSigned {
		return nil, ErrNotFound
	}
	return s.documentLink(ctx, os, c.OrgID, o)
}

func (s *Service) documentLink(ctx context.Context, os OfferStore, tenantID string, o *Offer) (*FileLink, error) {
	if s.Docs == nil || s.DocWriter == nil {
		return nil, ErrNoOffers
	}
	key := o.PDFKey
	if key == "" {
		var err error
		if key, err = s.writeDocument(ctx, os, tenantID, o.ID); err != nil {
			return nil, err
		}
	}
	name := "offer-signed.pdf"
	url, err := s.Docs.PresignGet(ctx, key, blob.GetOptions{TTL: FileLinkTTL, Filename: name, ContentType: "application/pdf"})
	if err != nil {
		return nil, fmt.Errorf("applications: link to the signed offer: %w", err)
	}
	return &FileLink{URL: url, Filename: name, ExpiresAt: s.now().Add(FileLinkTTL)}, nil
}

// writeDocument makes the signed document of an offer and stores it.
func (s *Service) writeDocument(ctx context.Context, os OfferStore, tenantID, offerID string) (string, error) {
	d, err := os.DocumentData(ctx, tenantID, offerID)
	if err != nil {
		return "", err
	}
	pdf, err := s.DocWriter(d)
	if err != nil {
		return "", fmt.Errorf("applications: write the signed offer: %w", err)
	}
	key := fmt.Sprintf("offers/%s/%s/signed.pdf", tenantID, offerID)
	if err = s.Docs.Put(ctx, key, bytes.NewReader(pdf), int64(len(pdf)), "application/pdf"); err != nil {
		return "", fmt.Errorf("applications: store the signed offer: %w", err)
	}
	if err = os.SetOfferDocument(ctx, tenantID, offerID, key); err != nil {
		return "", err
	}
	return key, nil
}

// ---- the candidate's side ----

// ViewOffer shows the offer behind a link and records the view.
func (s *Service) ViewOffer(ctx context.Context, tenantID, tokenHash string, m Meta) (*PublicOffer, error) {
	os, err := s.offers()
	if err != nil {
		return nil, err
	}
	return os.ViewOffer(ctx, tenantID, tokenHash, m, s.now().UTC())
}

// SignOffer records the candidate's signature. Signing hires them: the application moves to Hired in the same
// transaction, the retention clock starts and the candidate gets a confirmation. The signed document is written
// afterwards, and again on demand if that fails.
func (s *Service) SignOffer(ctx context.Context, tenantID, tokenHash, fullName string, consent bool, m Meta) (*PublicOffer, error) {
	os, err := s.offers()
	if err != nil {
		return nil, err
	}
	name := strings.Join(strings.Fields(fullName), " ")
	if utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > MaxSignedName || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil, invalid("full_name", "type your full name")
	}
	if !consent {
		return nil, invalid("consent", "you need to agree to sign electronically")
	}
	ref, err := os.OfferByToken(ctx, tenantID, tokenHash)
	if err != nil {
		return nil, err
	}
	st, err := s.Store.LoadChange(ctx, tenantID, ref.JobID, ref.AppID)
	if err != nil {
		return nil, err
	}
	j, err := s.Jobs.JobSummary(ctx, tenantID, ref.JobID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	rec := &SignRecord{TokenHash: tokenHash, Name: name, Now: now, Meta: m, ConsentHash: ConsentHash(), RetentionMonths: j.RetentionMonths}
	rec.Confirmation = &NewMessage{JobID: ref.JobID, AppID: ref.AppID, Company: j.Company, JobTitle: j.Title, Status: MsgQueued, ScheduledAt: now,
		Subject: "We recorded your signature for " + j.Title,
		Body: fmt.Sprintf("Hi %s,\n\nThank you. %s has your signature on the offer for %s, recorded on %s. They will be in touch with the next steps.",
			st.Name, j.Company, j.Title, now.Format("2 January 2006"))}
	if mm, merr := s.messages(); merr == nil {
		rec.Confirmation.ReplyTo, _ = mm.ReplyTo(ctx, tenantID)
	}
	o, err := os.SignOffer(ctx, tenantID, rec)
	if err != nil {
		return nil, err
	}
	if s.Docs != nil && s.DocWriter != nil {
		_, _ = s.writeDocument(ctx, os, tenantID, o.ID) // best effort: written again on demand
	}
	return &PublicOffer{Status: OfferSigned, Title: o.Title, Body: o.Body, Company: j.Company, JobTitle: j.Title, CandidateName: st.Name,
		Compensation: o.Compensation, StartDate: o.StartDate, SignedAt: o.SignedAt, SignedName: o.SignedName, ConsentText: ConsentText,
		HasDocument: s.Docs != nil && s.DocWriter != nil}, nil
}

// PublicOfferDocument returns a link to the signed document to the candidate who signed.
func (s *Service) PublicOfferDocument(ctx context.Context, tenantID, tokenHash string) (*FileLink, error) {
	os, err := s.offers()
	if err != nil {
		return nil, err
	}
	ref, err := os.OfferByToken(ctx, tenantID, tokenHash)
	if err != nil {
		return nil, err
	}
	o, err := os.Offer(ctx, tenantID, ref.JobID, ref.AppID, ref.OfferID, s.now())
	if err != nil {
		return nil, err
	}
	if o.Status != OfferSigned {
		return nil, ErrNotFound
	}
	return s.documentLink(ctx, os, tenantID, o)
}

// DeclineOffer records that the candidate said no.
func (s *Service) DeclineOffer(ctx context.Context, tenantID, tokenHash, reason string, m Meta) error {
	os, err := s.offers()
	if err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > MaxDeclineReason {
		return invalid("reason", fmt.Sprintf("keep this under %d characters", MaxDeclineReason+1))
	}
	return os.DeclineOffer(ctx, tenantID, tokenHash, reason, m, s.now().UTC())
}
