// Package apply takes a candidate's application to a published job. It checks the job is open, that the
// candidate's country is one the job is open to, validates every answer against the form that was frozen when
// the job was published, flags (never rejects) answers a screening rule or knockout cares about, stamps the row
// with its retention date and lawful basis, and asks the store to save it.
package apply

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/internal/hiring/questions"
	"github.com/Angle-HR/server/internal/hiring/screening"
	"github.com/Angle-HR/server/internal/sensitive"
)

// Errors the HTTP layer maps to responses.
var (
	// ErrNotFound: no such public job, or it is not public.
	ErrNotFound = hiringtypes.ErrNotFound
	// ErrNotAccepting: the job exists but is paused, closed or past its closing date.
	ErrNotAccepting = errors.New("apply: job is not accepting applications")
	// ErrGone: the job was archived.
	ErrGone = errors.New("apply: job is archived")
	// ErrRateLimited: too many applications from one address or for one email.
	ErrRateLimited = errors.New("apply: too many applications, try again later")
	// ErrFilesUnavailable: the form needs a file, and file uploads are not available (not configured, or the
	// malware scanner cannot be reached). Uploads fail closed: a file nobody scanned is never accepted.
	ErrFilesUnavailable = errors.New("apply: file uploads are not available")
	// ErrFileRejected: the uploaded file cannot be used (unknown, expired, already used, wrong type, too big or
	// infected). The candidate is told to upload it again, without the reason.
	ErrFileRejected = errors.New("apply: file cannot be used")
	// ErrSensitiveUnavailable: the application carries special category answers and the region has no
	// encryption keys configured. They are never written without encryption.
	ErrSensitiveUnavailable = errors.New("apply: special category answers cannot be stored here")
)

// FieldError points at the part of the request that is wrong, e.g. answers.<question id>.
type FieldError = jobs.FieldError

// ValidationError lists what is wrong with an application.
type ValidationError struct{ Fields []FieldError }

func (e *ValidationError) Error() string {
	return fmt.Sprintf("apply: %d fields need attention", len(e.Fields))
}

// Application statuses.
const (
	StatusReceived = "received"
	StatusFlagged  = "flagged" // a screening rule or knockout wants a person to look; nothing is rejected
)

// Flag says why an application was flagged for human review.
type Flag = screening.Flag

// Target is a published job as the apply step needs it, read in one transaction.
type Target struct {
	JobID           string
	JobCode         string
	Title           string
	CompanyName     string
	Declarations    []questions.Declaration // the legal condition and purpose of each special category on the job
	Region          string                  // set by Submit from the registry
	TenantID        string                  // set by Submit from the registry
	Status          string
	ClosingDate     string // last day to apply, YYYY-MM-DD, or ""
	LawfulBasis     string
	RetentionMonths int
	Markets         []string // market codes the job is open to
	FormID          string
	FormVersion     int // newest frozen form version
	Questions       []questions.FormQuestion
	Rules           []screening.Rule
}

// NewApplication is a validated application ready to store.
type NewApplication struct {
	// ID is made by the service, so special category answers can be sealed against it before the row exists.
	ID          string
	JobID       string
	FormID      string
	FormVersion int

	Name    string
	Email   string // lower case
	Country string // ISO 3166-1 alpha-2, as the candidate gave it
	Market  string // the job market that country belongs to

	Answers map[string]json.RawMessage
	// Files are the uploads the answers point at, already scanned. The store attaches them in the same
	// transaction as the application.
	Files []File
	// Sensitive are the sealed special category answers. They never appear in Answers.
	Sensitive []SensitiveAnswer
	// Email is the confirmation email, saved to the region's outbox in the same transaction.
	Confirmation *OutboxEmail
	Flags        []Flag
	Status       string
	Source       string

	IdempotencyKey string

	NoticeVersion        string
	NoticeAcknowledgedAt time.Time
	ConsentAt            *time.Time

	// Personal-data metadata, required on every row that holds personal data.
	LawfulBasis        string
	CollectionPurpose  string
	RetentionUntil     time.Time
	SubmittedAt        time.Time
	DownstreamPurposes []string
}

// SensitiveAnswer is a special category answer as stored: encrypted, bound to its application and question.
type SensitiveAnswer struct {
	QuestionID     string
	Category       string
	LegalCondition string
	Purpose        string
	Sealed         sensitive.Sealed
}

// OutboxEmail is an email to be delivered by the sender command. It holds what the template needs and never an
// answer.
type OutboxEmail struct {
	Kind          string
	Recipient     string
	RecipientName string
	Data          map[string]any
}

// Email kinds written by the application step.
const EmailApplicationReceived = "application_received"

// SealAAD is the additional data a special category answer is sealed with: the company, the application and the
// question. A sealed answer cannot be opened under any other.
func SealAAD(tenantID, applicationID, questionID string) string {
	return tenantID + "|" + applicationID + "|" + questionID
}

// Sealer encrypts special category answers with the keys of a region. *sensitive.Keyring implements it.
type Sealer interface {
	Has(region string) bool
	Seal(region string, plaintext []byte, aad string) (sensitive.Sealed, error)
}

// Store is one region's database as the apply step uses it.
type Store interface {
	// LoadForApply reads a job by public id with its newest frozen form and screening rules. ErrNotFound when
	// the job is not public.
	LoadForApply(ctx context.Context, tenantID, publicID string) (*Target, error)
	// InsertApplication saves the application and raises the job's applicant counters in one transaction. It
	// returns false, with nothing written, when the same email already applied to the job or the idempotency
	// key was already used.
	InsertApplication(ctx context.Context, tenantID string, a *NewApplication) (created bool, err error)
	// ReapplyBlocked reports whether the company has blocked this candidate (a keyed hash of the email) from
	// applying for now.
	ReapplyBlocked(ctx context.Context, tenantID, emailHMAC string) (bool, error)
}

// Directory finds a region's database.
type Directory interface {
	Store(region string) (Store, error)
}

// Limiter counts events in a time window. Allow reports whether the event is within the limit.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// File is an upload that passed every check and is ready to be attached to an application.
type File struct {
	ID         string // the token the candidate gave as the answer
	QuestionID string
	SizeBytes  int64
	SHA256     string
}

// ClaimRequest asks for one upload to be verified for one question of one job.
type ClaimRequest struct {
	Region     string
	TenantID   string
	JobID      string
	QuestionID string
	Token      string
	Question   *questions.FormQuestion
}

// Uploads verifies an upload a candidate refers to: the token was issued for this job and question, is not
// expired or used, the file is there, within its size limit, of the type it claims, and clean.
type Uploads interface {
	// Claim returns ErrFileRejected (possibly wrapped) when the file cannot be used, and ErrFilesUnavailable
	// when it cannot be checked right now.
	Claim(ctx context.Context, req ClaimRequest) (File, error)
}

// FileError is returned by a store when an upload could not be attached because it was already used or
// removed since it was claimed.
type FileError struct{ QuestionID string }

func (e *FileError) Error() string {
	return "apply: upload for " + e.QuestionID + " is no longer available"
}

// Limits of the public endpoint.
const (
	// PerIPPerHour caps applications from one address, which protects the database from floods.
	PerIPPerHour = 30
	// PerEmailPerJobPerHour caps retries for one email on one job.
	PerEmailPerJobPerHour = 5
	maxAnswersBytes       = 200_000
)

// Service takes applications.
type Service struct {
	Registry  public.Registry
	Directory Directory
	Limiter   Limiter // nil disables rate limiting
	Uploads   Uploads // nil: forms that ask for a file cannot be answered yet
	// Sealer encrypts special category answers. Nil, or a region without keys: forms that ask for one cannot be
	// answered.
	Sealer Sealer
	// EmailHash makes the keyed hash of a candidate's email that reapply blocks use. Nil turns the check off.
	EmailHash func(tenantID, email string) string
	// NoticeURL is the link to a job's applicant privacy notice, put in the confirmation email. Optional.
	NoticeURL func(publicID string) string
	Now       func() time.Time
	Logger    *slog.Logger
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Input is what a candidate sends.
type Input struct {
	PublicID           string
	CandidateCountry   string
	Answers            map[string]json.RawMessage
	NoticeAcknowledged bool
	Consent            bool
	Source             string
	IdempotencyKey     string
	ClientIP           string
}

// Receipt is the same whether the application was new or a repeat, so nobody can use the endpoint to find out
// whether an email has applied to a job.
type Receipt struct{ Received bool }

var (
	countryRE     = regexp.MustCompile(`^[A-Z]{2}$`)
	idempotencyRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,64}$`)
)

// Submit validates and stores an application.
func (s *Service) Submit(ctx context.Context, in Input) (*Receipt, error) {
	if !s.allow(ctx, "ip:"+in.ClientIP, PerIPPerHour) {
		return nil, ErrRateLimited
	}
	entry, err := s.Registry.LookupJob(ctx, in.PublicID)
	if err != nil {
		return nil, err
	}
	st, err := s.Directory.Store(entry.Region)
	if err != nil {
		return nil, fmt.Errorf("apply: region %q: %w", entry.Region, err)
	}
	t, err := st.LoadForApply(ctx, entry.OrganizationID, in.PublicID)
	if err != nil {
		return nil, err
	}
	t.Region, t.TenantID = entry.Region, entry.OrganizationID
	state, ok := public.StateOf(t.Status, t.ClosingDate, s.now())
	switch {
	case t.Status == jobs.StatusArchived:
		return nil, ErrGone
	case !ok:
		return nil, ErrNotFound
	case state != public.StateOpen:
		return nil, ErrNotAccepting
	}

	app, err := s.build(ctx, in, t)
	if err != nil {
		return nil, err
	}
	app.Confirmation = s.confirmation(in.PublicID, t, app)
	if !s.allow(ctx, "email:"+in.PublicID+":"+hash(app.Email), PerEmailPerJobPerHour) {
		return nil, ErrRateLimited
	}
	if s.EmailHash != nil {
		blocked, berr := st.ReapplyBlocked(ctx, entry.OrganizationID, s.EmailHash(entry.OrganizationID, app.Email))
		if berr != nil {
			return nil, fmt.Errorf("apply: check reapply block: %w", berr)
		}
		if blocked {
			// The answer is the same as for a new application: telling a candidate (or someone who typed their
			// address) that the employer turned them down before would leak that decision.
			if s.Logger != nil {
				s.Logger.Info("apply: application from a blocked candidate ignored", "job", t.JobID)
			}
			return &Receipt{Received: true}, nil
		}
	}
	created, err := st.InsertApplication(ctx, entry.OrganizationID, app)
	var fe *FileError
	if errors.As(err, &fe) {
		return nil, &ValidationError{Fields: []FieldError{{
			Path: "answers." + fe.QuestionID, Message: "this file could not be used; upload it again"}}}
	}
	if err != nil {
		return nil, fmt.Errorf("apply: save application: %w", err)
	}
	if !created && s.Logger != nil {
		s.Logger.Info("apply: repeat application ignored", "job", t.JobID)
	}
	return &Receipt{Received: true}, nil
}

// allow counts one event against a limit. A limiter outage lets the request through: losing applications
// because Redis is down is worse than a short stretch without throttling.
func (s *Service) allow(ctx context.Context, key string, limit int) bool {
	if s.Limiter == nil {
		return true
	}
	ok, err := s.Limiter.Allow(ctx, "apply:"+key, limit, time.Hour)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("apply: rate limiter unavailable", "error", err)
		}
		return true
	}
	return ok
}

// build validates the candidate's input against the frozen form and returns the row to store.
func (s *Service) build(ctx context.Context, in Input, t *Target) (*NewApplication, error) {
	var errs []FieldError
	add := func(path, msg string) { errs = append(errs, FieldError{Path: path, Message: msg}) }

	size := 0
	for _, raw := range in.Answers {
		size += len(raw)
	}
	if size > maxAnswersBytes {
		return nil, &ValidationError{Fields: []FieldError{{Path: "answers", Message: "the answers are too long"}}}
	}

	country := strings.ToUpper(strings.TrimSpace(in.CandidateCountry))
	market := ""
	switch {
	case !countryRE.MatchString(country):
		add("candidate_country", "choose your country")
	default:
		m, known := MarketFor(country)
		if !known || !contains(t.Markets, m) {
			add("candidate_country", "this role is not open to candidates in that country")
		} else {
			market = m
		}
	}

	if !in.NoticeAcknowledged {
		add("privacy_notice_acknowledged", "confirm you have read the privacy notice")
	}
	if t.LawfulBasis == "consent" && !in.Consent {
		add("consent", "this employer needs your consent to handle your application")
	}

	byID := map[string]*questions.FormQuestion{}
	for i := range t.Questions {
		byID[t.Questions[i].ID] = &t.Questions[i]
	}
	var unknown []string
	for id := range in.Answers {
		if byID[id] == nil {
			unknown = append(unknown, id)
		}
	}
	sort.Strings(unknown)
	for _, id := range unknown {
		add("answers."+truncate(id, 40), "this question is not part of the application form")
	}

	answers := map[string]json.RawMessage{}
	var files []File
	var sealed []SensitiveAnswer
	var name, email string
	appID, err := newID()
	if err != nil {
		return nil, err
	}
	for i := range t.Questions {
		q := &t.Questions[i]
		path := "answers." + q.ID
		raw, present := in.Answers[q.ID]
		present = present && !isEmpty(raw)

		if q.SpecialCategory != "" {
			// Special category answers are sealed and kept apart from the other answers. Without keys for the
			// region they are refused: failing closed beats writing them unprotected.
			if !present && !q.Required {
				continue
			}
			if s.Sealer == nil || !s.Sealer.Has(t.Region) {
				return nil, ErrSensitiveUnavailable
			}
			if !present {
				add(path, "this question is required")
				continue
			}
			if err := questions.ValidateAnswer(q.Type, q.Config, raw); err != nil {
				add(path, err.Error())
				continue
			}
			decl, ok := declaration(t.Declarations, q.SpecialCategory)
			if !ok {
				return nil, fmt.Errorf("apply: no declaration for special category %q on job %s", q.SpecialCategory, t.JobID)
			}
			box, err := s.Sealer.Seal(t.Region, raw, SealAAD(t.TenantID, appID, q.ID))
			if err != nil {
				if errors.Is(err, sensitive.ErrNoKey) {
					return nil, ErrSensitiveUnavailable
				}
				return nil, fmt.Errorf("apply: seal answer: %w", err)
			}
			sealed = append(sealed, SensitiveAnswer{
				QuestionID: q.ID, Category: q.SpecialCategory, LegalCondition: decl.LegalCondition,
				Purpose: decl.Purpose, Sealed: box,
			})
			continue
		}
		if isUpload(q.Type) && (present || q.Required) {
			if s.Uploads == nil {
				return nil, ErrFilesUnavailable
			}
		}
		if !present {
			if q.Required {
				add(path, "this question is required")
			}
			continue
		}
		if err := questions.ValidateAnswer(q.Type, q.Config, raw); err != nil {
			add(path, err.Error())
			continue
		}
		if isUpload(q.Type) {
			var token string
			_ = json.Unmarshal(raw, &token)
			f, err := s.Uploads.Claim(ctx, ClaimRequest{
				Region: t.Region, TenantID: t.TenantID, JobID: t.JobID, QuestionID: q.ID,
				Token: strings.TrimSpace(token), Question: q,
			})
			switch {
			case errors.Is(err, ErrFileRejected):
				add(path, "this file could not be used; upload it again")
				continue
			case err != nil:
				if errors.Is(err, ErrFilesUnavailable) {
					return nil, ErrFilesUnavailable
				}
				return nil, fmt.Errorf("apply: check upload: %w", err)
			}
			files = append(files, f)
		}
		answers[q.ID] = raw
		switch q.SystemKey {
		case "full_name":
			name = stringAnswer(raw)
		case "email":
			email = strings.ToLower(strings.TrimSpace(stringAnswer(raw)))
		}
	}
	if name == "" && !hasErr(errs, "answers.") {
		add("answers", "the form must include your full name")
	}
	if email == "" && !hasErr(errs, "answers.") {
		add("answers", "the form must include your email")
	}

	key := strings.TrimSpace(in.IdempotencyKey)
	if key != "" && !idempotencyRE.MatchString(key) {
		add("idempotency_key", "the idempotency key must be 8 to 64 letters, digits or - _ . :")
	}
	if len(errs) > 0 {
		return nil, &ValidationError{Fields: errs}
	}

	flags := knockoutFlags(t.Questions, answers)
	flags = append(flags, screening.Evaluate(t.Rules, answers)...)
	status := StatusReceived
	if len(flags) > 0 {
		status = StatusFlagged
	}

	now := s.now().UTC()
	app := &NewApplication{
		ID:    appID,
		JobID: t.JobID, FormID: t.FormID, FormVersion: t.FormVersion,
		Name: name, Email: email, Country: country, Market: market,
		Answers: answers, Files: files, Sensitive: sealed, Flags: flags, Status: status, Source: source(in.Source),
		IdempotencyKey: key,
		NoticeVersion:  public.NoticeVersion, NoticeAcknowledgedAt: now,
		LawfulBasis:        t.LawfulBasis,
		CollectionPurpose:  "Assessing your application for this role and contacting you about it",
		RetentionUntil:     RetentionUntil(now, t.ClosingDate, t.RetentionMonths),
		SubmittedAt:        now,
		DownstreamPurposes: []string{},
	}
	if in.Consent {
		app.ConsentAt = &now
	}
	if app.LawfulBasis == "" {
		app.LawfulBasis = "legitimate_interests"
	}
	return app, nil
}

// RetentionUntil is when an application's data must be deleted. The clock really starts at the rejection date,
// or at the job's close date for candidates never reviewed; neither is known when the application arrives, so
// this uses the later of "now" and the closing date as a safe starting point. The retention task recomputes it
// when the application is decided or the job closes. The months come from the job, within the market's limits.
func RetentionUntil(now time.Time, closingDate string, months int) time.Time {
	if months <= 0 {
		months = jobs.DefaultRetentionMonths
	}
	base := now.UTC()
	if closingDate != "" {
		if d, err := time.Parse("2006-01-02", closingDate); err == nil {
			if end := d.AddDate(0, 0, 1); end.After(base) {
				base = end
			}
		}
	}
	return base.AddDate(0, months, 0)
}

// source keeps only a known share channel; anything else is "direct".
func source(src string) string {
	src = strings.ToLower(strings.TrimSpace(src))
	if public.ValidSource(src) {
		return src
	}
	return "direct"
}

func declaration(list []questions.Declaration, category string) (questions.Declaration, bool) {
	for _, d := range list {
		if d.Category == category {
			return d, true
		}
	}
	return questions.Declaration{}, false
}

// confirmation is the "we received your application" email.
func (s *Service) confirmation(publicID string, t *Target, a *NewApplication) *OutboxEmail {
	data := map[string]any{"job_title": t.Title, "job_code": t.JobCode, "company": t.CompanyName}
	if s.NoticeURL != nil {
		data["notice_url"] = s.NoticeURL(publicID)
	}
	return &OutboxEmail{Kind: EmailApplicationReceived, Recipient: a.Email, RecipientName: a.Name, Data: data}
}

// newID makes a random version 4 UUID.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("apply: random id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// IsUpload reports whether a question type takes a file.
func IsUpload(t string) bool { return isUpload(t) }

func isUpload(t string) bool {
	return t == "image_upload" || t == "file_upload" || t == "autofill_resume"
}

func isEmpty(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == `""` || s == "[]" {
		return true
	}
	var str string
	if json.Unmarshal(raw, &str) == nil && strings.TrimSpace(str) == "" {
		return true
	}
	return false
}

func stringAnswer(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func hasErr(errs []FieldError, prefix string) bool {
	for _, e := range errs {
		if strings.HasPrefix(e.Path, prefix) {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
