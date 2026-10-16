// Package applications is what the hiring team does with the applications a job received: list them, open one,
// download its files, move it through the pipeline and decide the ones a screening rule flagged.
//
// Rules this package enforces:
//   - Who can see an application is who can see its job: people with job.view.all, or, with job.view.assigned,
//     the job's creator and its team members.
//   - Only the job's creator, its hiring managers and recruiters, and people with job.manage_any change a status.
//     Interviewers and viewers read.
//   - A flagged application leaves "flagged" only through a review: the reviewer opens the application, then
//     decides (proceed or reject). There is no bulk action and no direct status change for a flagged candidate.
//   - Special category answers are never read here. They stay sealed in their own schema.
//   - Every opening of an application, every file download and every change is written to the job's audit log.
package applications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/reapply"
	"github.com/Angle-HR/server/internal/hiring/uploads"
	"github.com/Angle-HR/server/internal/rbac"
)

// Caller is the signed-in member and what they may do.
type Caller = draft.Caller

// Application statuses.
const (
	StatusReceived  = "received"
	StatusInReview  = "in_review"
	StatusFlagged   = "flagged"
	StatusRejected  = "rejected"
	StatusWithdrawn = "withdrawn"
	StatusHired     = "hired"
)

// AllStatuses lists every status, in pipeline order.
var AllStatuses = []string{StatusReceived, StatusFlagged, StatusInReview, StatusRejected, StatusHired, StatusWithdrawn}

// Final reports whether a status ends the application. A final application is decided: its retention clock
// starts and nothing changes it any more.
func Final(status string) bool {
	return status == StatusRejected || status == StatusHired || status == StatusWithdrawn
}

// Review decisions.
const (
	DecisionProceed = "proceed"
	DecisionReject  = "reject"
)

// MaxNote is the longest note on a decision.
const MaxNote = 2000

// Errors.
var (
	// ErrNotFound: no such job or application, or the caller may not see it.
	ErrNotFound = hiringtypes.ErrNotFound
	// ErrInvalidTransition: the application cannot move from its status to the one asked for.
	ErrInvalidTransition = errors.New("applications: that status change is not allowed")
	// ErrReviewRequired: a flagged application is decided through its review, not by changing its status.
	ErrReviewRequired = errors.New("applications: review a flagged application before deciding it")
	// ErrNoReview: the application has no open review.
	ErrNoReview = errors.New("applications: this application has no open review")
	// ErrNotOpened: the person deciding has not opened the application since it was flagged.
	ErrNotOpened = errors.New("applications: open the application before deciding")
	// ErrStale: the application changed while the caller was deciding.
	ErrStale = errors.New("applications: the application was changed by someone else")
)

// JobAccess is a job as access checks need it: who created it and the caller's role on it.
type JobAccess struct {
	JobID           string
	Code            string
	Title           string
	Status          string
	CreatedBy       string
	MemberRole      string // the caller's role in the job's team, "" when not a member
	RetentionMonths int
}

// Summary is one row of the applications list. It holds no answers.
type Summary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Email       string    `json:"email"`
	Country     string    `json:"country"`
	Market      string    `json:"market"`
	Status      string    `json:"status"`
	Source      string    `json:"source"`
	FlagCount   int       `json:"flag_count"`
	Opened      bool      `json:"opened"`
	HasFiles    bool      `json:"has_files"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// StoreListQuery is a list request after validation.
type StoreListQuery struct {
	Statuses []string
	Source   string
	Search   string
	NewOnly  bool
	// After is the keyset position of the last row of the previous page; the zero value starts at the top.
	AfterTime time.Time
	AfterID   string
	Limit     int // the store returns up to Limit rows; the service asks for one more to see if there is a next page
}

// ListQuery is what the client sends.
type ListQuery struct {
	Statuses []string
	Source   string
	Query    string
	NewOnly  bool
	Cursor   string
	Limit    int
}

// Page is one page of the list with the per-status counts of the whole job.
type Page struct {
	Items      []Summary      `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
	Counts     map[string]int `json:"counts"`
}

// Answer is one answer of an application, with the question as it was when the job was published.
type Answer struct {
	QuestionID string          `json:"question_id"`
	Section    string          `json:"section"`
	Type       string          `json:"type"`
	Label      string          `json:"label"`
	Value      json.RawMessage `json:"value,omitempty"`
	// FileID is set for upload questions; the file is fetched through the file endpoint.
	FileID string `json:"file_id,omitempty"`
}

// File is an uploaded file of an application.
type File struct {
	ID          string `json:"id"`
	QuestionID  string `json:"question_id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

// Review is the review task of a flagged application.
type Review struct {
	Status    string          `json:"status"` // open or done
	Reasons   json.RawMessage `json:"reasons"`
	Decision  string          `json:"decision,omitempty"`
	DecidedAt *time.Time      `json:"decided_at,omitempty"`
	Note      string          `json:"note,omitempty"`
	// OpenedByMe is true when the caller opened the application since it was flagged, so they may decide it.
	OpenedByMe bool `json:"opened_by_me"`
}

// Detail is one application as a reviewer sees it.
type Detail struct {
	Summary
	JobID          string          `json:"job_id"`
	FormVersion    int             `json:"form_version"`
	Flags          json.RawMessage `json:"flags"`
	Answers        []Answer        `json:"answers"`
	Files          []File          `json:"files"`
	Review         *Review         `json:"review,omitempty"`
	StatusNote     string          `json:"status_note,omitempty"`
	DecidedAt      *time.Time      `json:"decided_at,omitempty"`
	ConsentGivenAt *time.Time      `json:"consent_given_at,omitempty"`
	NoticeVersion  string          `json:"privacy_notice_version"`
	RetentionUntil time.Time       `json:"retention_until"`
	// CanDecide tells the client whether to offer status changes and review decisions.
	CanDecide bool `json:"can_decide"`
}

// OpenInput asks the store to load an application and record that someone opened it.
type OpenInput struct {
	JobID, AppID, ActorID string
	// TrackReview marks the open review task as opened by the actor. Only people who may decide do that.
	TrackReview bool
}

// FileInfo is a file of an application, with where it is kept.
type FileInfo struct {
	ID, Filename, ContentType string
	SizeBytes                 int64
	JobID                     string
}

// ReviewState is the review task as the status logic needs it.
type ReviewState struct {
	Open     bool
	OpenedBy string // the last person who opened the application, "" when nobody did
}

// ApplicantSettings are the company's candidate settings.
type ApplicantSettings struct {
	// ReapplyBlockMonths is 0 (off) or 6: a rejected candidate cannot apply to the company again for that long.
	ReapplyBlockMonths int `json:"reapply_block_months"`
	// SendStatusEmails sends the candidate an email when they are rejected or hired.
	SendStatusEmails bool `json:"send_status_emails"`
	// ReapplyBlockAvailable is false when the server has no key for it, so it cannot be turned on.
	ReapplyBlockAvailable bool `json:"reapply_block_available"`
}

// ChangeState is what the service needs to decide a status change.
type ChangeState struct {
	Status             string
	Name, Email        string
	JobTitle, JobCode  string
	Company            string
	PublicID           string
	RetentionMonths    int
	Review             *ReviewState
	ReapplyBlockMonths int
	SendStatusEmails   bool
}

// Block is a reapply block to record with a rejection.
type Block struct {
	EmailHMAC string
	Until     time.Time
}

// Change is a decided status change, applied in one transaction.
type Change struct {
	AppID, JobID, ActorID string
	From, To              string
	Note                  string
	Now                   time.Time
	// RetentionMonths is the job's; used when To is final, to start the retention clock.
	RetentionMonths int
	// Decision is the review decision, "" for a plain status change.
	Decision string
	Block    *Block
	Email    *apply.OutboxEmail
}

// Store is one region's database as this package uses it.
type Store interface {
	// JobAccess returns hiringtypes.ErrNotFound for an unknown job.
	JobAccess(ctx context.Context, tenantID, jobID, userID string) (*JobAccess, error)
	List(ctx context.Context, tenantID, jobID string, q StoreListQuery) ([]Summary, map[string]int, error)
	// Open loads one application, records that it was opened (counters, review task) and writes the audit entry,
	// in one transaction. It returns hiringtypes.ErrNotFound when the application is not on the job.
	Open(ctx context.Context, tenantID string, in OpenInput) (*Detail, error)
	// FileInfo finds a file of an application and writes the audit entry for the download.
	FileInfo(ctx context.Context, tenantID, jobID, appID, fileID, actorID string) (*FileInfo, error)
	LoadChange(ctx context.Context, tenantID, jobID, appID string) (*ChangeState, error)
	// ApplyChange makes the change if the application is still in ch.From (ErrStale otherwise) and, for a review
	// decision, still awaiting the actor's decision (ErrNotOpened otherwise).
	ApplyChange(ctx context.Context, tenantID string, ch *Change) error
	GetApplicantSettings(ctx context.Context, tenantID string) (reapplyMonths int, statusEmails bool, err error)
	SaveApplicantSettings(ctx context.Context, tenantID string, reapplyMonths int, statusEmails bool) error
}

// Files makes short-lived download links. uploads.Storage implements it.
type Files interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration, filename, contentType string) (string, error)
}

var _ Files = uploads.Storage(nil)

// FileLinkTTL is how long a download link works.
const FileLinkTTL = 5 * time.Minute

// Service serves the hiring team of one region.
type Service struct {
	Store  Store
	Files  Files
	Hasher *reapply.Hasher
	// NoticeURL is the link to a job's applicant privacy notice, put in status emails. Optional.
	NoticeURL func(publicID string) string
	Now       func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func forbidden(msg string) error { return &draft.ForbiddenError{Msg: msg} }

func invalid(path, msg string) error {
	return &draft.ValidationError{Fields: []jobs.FieldError{{Path: path, Message: msg}}}
}

func has(c Caller, p rbac.Permission) bool { return c.Perms.Has(p) }

// canSee follows the job: everyone with job.view.all, and with job.view.assigned the creator and the team.
func canSee(c Caller, a *JobAccess) bool {
	if has(c, rbac.JobViewAll) {
		return true
	}
	if !has(c, rbac.JobViewAssigned) {
		return false
	}
	return a.CreatedBy == c.UserID || a.MemberRole != ""
}

// canDecide: whoever manages any job, the job's creator, and its hiring managers and recruiters.
func canDecide(c Caller, a *JobAccess) bool {
	if !canSee(c, a) {
		return false
	}
	return has(c, rbac.JobManageAny) || a.CreatedBy == c.UserID ||
		a.MemberRole == hiringtypes.MemberHiringManager || a.MemberRole == hiringtypes.MemberRecruiter
}

// access loads the job and refuses with ErrNotFound when the caller may not see it, so the existence of a job is
// not revealed.
func (s *Service) access(ctx context.Context, c Caller, jobID string) (*JobAccess, error) {
	if !jobs.IsUUID(jobID) {
		return nil, ErrNotFound
	}
	a, err := s.Store.JobAccess(ctx, c.OrgID, jobID, c.UserID)
	if err != nil {
		return nil, err
	}
	if !canSee(c, a) {
		return nil, ErrNotFound
	}
	return a, nil
}

// List returns one page of a job's applications, newest first.
func (s *Service) List(ctx context.Context, c Caller, jobID string, q ListQuery) (*Page, error) {
	if _, err := s.access(ctx, c, jobID); err != nil {
		return nil, err
	}
	sq := StoreListQuery{Source: strings.ToLower(strings.TrimSpace(q.Source)), NewOnly: q.NewOnly}
	for _, st := range q.Statuses {
		if !contains(AllStatuses, st) {
			return nil, invalid("status", "status must be one of "+strings.Join(AllStatuses, ", "))
		}
		sq.Statuses = append(sq.Statuses, st)
	}
	if sq.Source != "" && len(sq.Source) > 20 {
		return nil, invalid("source", "unknown source")
	}
	sq.Search = strings.TrimSpace(q.Query)
	if len([]rune(sq.Search)) > 100 {
		return nil, invalid("q", "search text is too long")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	if q.Cursor != "" {
		ts, id, err := hiringtypes.DecodeCursor(q.Cursor)
		if err != nil {
			return nil, invalid("cursor", "invalid cursor")
		}
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, invalid("cursor", "invalid cursor")
		}
		sq.AfterTime, sq.AfterID = t, id
	}
	sq.Limit = limit + 1
	items, counts, err := s.Store.List(ctx, c.OrgID, jobID, sq)
	if err != nil {
		return nil, err
	}
	page := &Page{Items: items, Counts: counts}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.NextCursor = hiringtypes.EncodeCursor(last.SubmittedAt.UTC().Format(time.RFC3339Nano), last.ID)
	}
	if page.Items == nil {
		page.Items = []Summary{}
	}
	if page.Counts == nil {
		page.Counts = map[string]int{}
	}
	return page, nil
}

// Get opens one application. Opening is recorded: the first opening takes the application out of the job's
// "new" count, and the audit log gets an entry. A person who may decide a flagged application and opens it
// becomes the reviewer who can decide it.
func (s *Service) Get(ctx context.Context, c Caller, jobID, appID string) (*Detail, error) {
	a, err := s.access(ctx, c, jobID)
	if err != nil {
		return nil, err
	}
	if !jobs.IsUUID(appID) {
		return nil, ErrNotFound
	}
	decide := canDecide(c, a)
	d, err := s.Store.Open(ctx, c.OrgID, OpenInput{JobID: jobID, AppID: appID, ActorID: c.UserID, TrackReview: decide})
	if err != nil {
		return nil, err
	}
	d.CanDecide = decide
	return d, nil
}

// FileLink is a short-lived download link.
type FileLink struct {
	URL       string    `json:"url"`
	Filename  string    `json:"filename"`
	ExpiresAt time.Time `json:"expires_at"`
}

// File returns a download link for a file of an application. The download is audited.
func (s *Service) File(ctx context.Context, c Caller, jobID, appID, fileID string) (*FileLink, error) {
	if _, err := s.access(ctx, c, jobID); err != nil {
		return nil, err
	}
	if !jobs.IsUUID(appID) || !jobs.IsUUID(fileID) {
		return nil, ErrNotFound
	}
	if s.Files == nil {
		return nil, errors.New("applications: file storage is not configured")
	}
	f, err := s.Store.FileInfo(ctx, c.OrgID, jobID, appID, fileID, c.UserID)
	if err != nil {
		return nil, err
	}
	url, err := s.Files.PresignGet(ctx, uploads.FinalKey(c.OrgID, f.JobID, f.ID), FileLinkTTL, f.Filename, f.ContentType)
	if err != nil {
		return nil, fmt.Errorf("applications: download link: %w", err)
	}
	return &FileLink{URL: url, Filename: f.Filename, ExpiresAt: s.now().Add(FileLinkTTL)}, nil
}

// transitions are the plain status changes. A flagged application leaves "flagged" only through Review, and a
// final status never changes.
var transitions = map[string][]string{
	StatusReceived: {StatusInReview, StatusWithdrawn},
	StatusFlagged:  {StatusWithdrawn},
	StatusInReview: {StatusRejected, StatusHired, StatusWithdrawn},
}

// StatusInput is a plain status change.
type StatusInput struct {
	Status string
	Note   string
}

// Result is the outcome of a status change.
type Result struct {
	Status    string     `json:"status"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
}

func cleanNote(note string) (string, error) {
	note = strings.TrimSpace(note)
	if len([]rune(note)) > MaxNote {
		return "", invalid("note", fmt.Sprintf("the note can be at most %d characters", MaxNote))
	}
	return note, nil
}

// SetStatus moves an application along the pipeline.
func (s *Service) SetStatus(ctx context.Context, c Caller, jobID, appID string, in StatusInput) (*Result, error) {
	a, err := s.access(ctx, c, jobID)
	if err != nil {
		return nil, err
	}
	if !canDecide(c, a) {
		return nil, forbidden("you cannot change the status of applications on this job")
	}
	if !jobs.IsUUID(appID) {
		return nil, ErrNotFound
	}
	if !contains(AllStatuses, in.Status) {
		return nil, invalid("status", "status must be one of "+strings.Join(AllStatuses, ", "))
	}
	note, err := cleanNote(in.Note)
	if err != nil {
		return nil, err
	}
	st, err := s.Store.LoadChange(ctx, c.OrgID, jobID, appID)
	if err != nil {
		return nil, err
	}
	if st.Status == StatusFlagged && (in.Status == StatusInReview || in.Status == StatusRejected || in.Status == StatusHired) {
		return nil, ErrReviewRequired
	}
	if !contains(transitions[st.Status], in.Status) {
		return nil, ErrInvalidTransition
	}
	return s.apply(ctx, c, a, st, appID, in.Status, note, "")
}

// ReviewInput is the decision on a flagged application.
type ReviewInput struct {
	Decision string
	Note     string
}

// Review decides a flagged application. The reviewer must have opened the application since it was flagged: the
// last person to open it is the only one who can decide it. Proceed moves it to in_review; reject rejects it.
// Neither is automatic and neither can be done in bulk.
func (s *Service) Review(ctx context.Context, c Caller, jobID, appID string, in ReviewInput) (*Result, error) {
	a, err := s.access(ctx, c, jobID)
	if err != nil {
		return nil, err
	}
	if !canDecide(c, a) {
		return nil, forbidden("you cannot review applications on this job")
	}
	if !jobs.IsUUID(appID) {
		return nil, ErrNotFound
	}
	if in.Decision != DecisionProceed && in.Decision != DecisionReject {
		return nil, invalid("decision", "decision must be proceed or reject")
	}
	note, err := cleanNote(in.Note)
	if err != nil {
		return nil, err
	}
	st, err := s.Store.LoadChange(ctx, c.OrgID, jobID, appID)
	if err != nil {
		return nil, err
	}
	if st.Status != StatusFlagged || st.Review == nil || !st.Review.Open {
		return nil, ErrNoReview
	}
	if st.Review.OpenedBy != c.UserID {
		return nil, ErrNotOpened
	}
	to := StatusInReview
	if in.Decision == DecisionReject {
		to = StatusRejected
	}
	return s.apply(ctx, c, a, st, appID, to, note, in.Decision)
}

// apply works out everything a change brings with it and has the store do it in one transaction: the retention
// clock for a final status, the reapply block and the email to the candidate for a rejection, the email for a
// hire.
func (s *Service) apply(ctx context.Context, c Caller, a *JobAccess, st *ChangeState, appID, to, note, decision string) (*Result, error) {
	now := s.now().UTC()
	ch := &Change{
		AppID: appID, JobID: a.JobID, ActorID: c.UserID, From: st.Status, To: to, Note: note, Now: now,
		RetentionMonths: a.RetentionMonths, Decision: decision,
	}
	if Final(to) {
		if to == StatusRejected && st.ReapplyBlockMonths > 0 && s.Hasher.Enabled() {
			ch.Block = &Block{
				EmailHMAC: s.Hasher.Hash(c.OrgID, st.Email),
				Until:     now.AddDate(0, st.ReapplyBlockMonths, 0),
			}
		}
		if st.SendStatusEmails && (to == StatusRejected || to == StatusHired) {
			kind := "application_rejected"
			if to == StatusHired {
				kind = "application_hired"
			}
			data := map[string]any{"job_title": st.JobTitle, "job_code": st.JobCode, "company": st.Company}
			if s.NoticeURL != nil && st.PublicID != "" {
				data["notice_url"] = s.NoticeURL(st.PublicID)
			}
			ch.Email = &apply.OutboxEmail{Kind: kind, Recipient: st.Email, RecipientName: st.Name, Data: data}
		}
	}
	if err := s.Store.ApplyChange(ctx, c.OrgID, ch); err != nil {
		return nil, err
	}
	res := &Result{Status: to}
	if Final(to) {
		res.DecidedAt = &now
	}
	return res, nil
}

// Settings returns the company's candidate settings.
func (s *Service) Settings(ctx context.Context, c Caller) (ApplicantSettings, error) {
	if !has(c, rbac.JobCreate) && !has(c, rbac.JobRetentionSet) && !has(c, rbac.JobViewAll) {
		return ApplicantSettings{}, forbidden("you cannot view these settings")
	}
	return s.settings(ctx, c)
}

func (s *Service) settings(ctx context.Context, c Caller) (ApplicantSettings, error) {
	months, emails, err := s.Store.GetApplicantSettings(ctx, c.OrgID)
	if err != nil {
		return ApplicantSettings{}, err
	}
	return ApplicantSettings{ReapplyBlockMonths: months, SendStatusEmails: emails, ReapplyBlockAvailable: s.Hasher.Enabled()}, nil
}

// SettingsInput changes settings; a nil field is left as it is.
type SettingsInput struct {
	ReapplyBlockMonths *int
	SendStatusEmails   *bool
}

// SaveSettings changes the company's candidate settings. It needs job.retention.set (the permission that governs
// how long candidate data is kept). The reapply block cannot be turned on while the server has no key for it,
// instead of silently not blocking.
func (s *Service) SaveSettings(ctx context.Context, c Caller, in SettingsInput) (ApplicantSettings, error) {
	if !has(c, rbac.JobRetentionSet) {
		return ApplicantSettings{}, forbidden("you cannot change these settings")
	}
	cur, err := s.settings(ctx, c)
	if err != nil {
		return ApplicantSettings{}, err
	}
	if in.ReapplyBlockMonths != nil {
		m := *in.ReapplyBlockMonths
		if m != 0 && m != 6 {
			return ApplicantSettings{}, invalid("reapply_block_months", "must be 0 (off) or 6")
		}
		if m != 0 && !cur.ReapplyBlockAvailable {
			return ApplicantSettings{}, invalid("reapply_block_months", "blocking reapplications is not available yet")
		}
		cur.ReapplyBlockMonths = m
	}
	if in.SendStatusEmails != nil {
		cur.SendStatusEmails = *in.SendStatusEmails
	}
	if err = s.Store.SaveApplicantSettings(ctx, c.OrgID, cur.ReapplyBlockMonths, cur.SendStatusEmails); err != nil {
		return ApplicantSettings{}, err
	}
	return cur, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
