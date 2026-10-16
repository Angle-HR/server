// Package uploads handles the files a candidate attaches to an application. The browser asks for a presigned
// URL, puts the file straight into object storage (a staging key), and names the returned token as the answer
// to the question. When the application is submitted the server claims the token: it checks the file's size
// and type, scans it for malware, copies the bytes it scanned to a final key, removes the staging object and
// only then lets the application use it. The bytes the candidate could still change through the presigned URL
// are never the bytes that are kept.
//
// Uploads fail closed. Without a scanner nothing is accepted, and a scanner that cannot be reached makes the
// application fail with "uploads unavailable" instead of storing an unchecked file.
package uploads

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/internal/hiring/questions"
)

// Limits.
const (
	// DefaultMaxMB is the size limit when a question sets none (the free plan limit).
	DefaultMaxMB = 5
	// HardMaxMB is the largest file any plan accepts.
	HardMaxMB = 10
	// PresignTTL is how long a presigned URL works.
	PresignTTL = 15 * time.Minute
	// PendingTTL is how long an upload waits to be used by an application before the sweeper deletes it.
	PendingTTL = 24 * time.Hour
	// PerIPPerHour caps presigned URLs per address.
	PerIPPerHour = 60

	statusPending  = "pending"
	statusClean    = "clean"
	statusRejected = "rejected"
)

// Storage is object storage as the upload step uses it. Implementations live in the r2 subpackage.
type Storage interface {
	// PresignPut returns a URL that accepts one PUT of the given content type until ttl has passed.
	PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (PutTarget, error)
	// Stat returns the size of an object, or ErrObjectNotFound.
	Stat(ctx context.Context, key string) (int64, error)
	// Open reads an object. The caller closes it.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Put writes an object from r.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// PresignGet returns a short-lived download link. The browser saves the object under filename.
	PresignGet(ctx context.Context, key string, ttl time.Duration, filename, contentType string) (string, error)
	// Remove deletes an object; removing a missing object is not an error.
	Remove(ctx context.Context, key string) error
}

// PutTarget is where and how the browser uploads.
type PutTarget struct {
	URL     string
	Headers map[string]string
}

// ErrObjectNotFound is returned by Storage.Stat and Open for a missing object.
var ErrObjectNotFound = errors.New("uploads: object not found")

// Record is one row of hiring.application_files.
type Record struct {
	ID          string
	TenantID    string
	JobID       string
	QuestionID  string
	Filename    string
	ContentType string
	Declared    int64
	Size        int64
	SHA256      string
	Status      string
	ExpiresAt   *time.Time // nil once an application owns the file

	// Personal-data metadata, required on every row that holds personal data.
	LawfulBasis       string
	CollectionPurpose string
	RetentionUntil    time.Time
}

// Store is one region's database as the upload step uses it.
type Store interface {
	LoadForApply(ctx context.Context, tenantID, publicID string) (*apply.Target, error)
	InsertUpload(ctx context.Context, tenantID string, r *Record) error
	// GetUpload returns hiringtypes.ErrNotFound when the id is unknown for the company.
	GetUpload(ctx context.Context, tenantID, id string) (*Record, error)
	MarkUploadClean(ctx context.Context, tenantID, id string, size int64, sha256hex string) error
	RejectUpload(ctx context.Context, tenantID, id string) error
}

// Directory finds a region's database and bucket.
type Directory interface {
	Store(region string) (Store, error)
	Storage(region string) (Storage, error)
}

// Service issues upload URLs and claims uploads. It implements apply.Uploads.
type Service struct {
	Registry  public.Registry
	Directory Directory
	Scanner   Scanner // required
	Limiter   apply.Limiter
	// PlanMaxMB returns the company's plan limit in MB; nil or 0 means no plan limit beyond the question's.
	// Plans: 5 MB free, 10 MB paid. There is no plan data source yet, so it is unset.
	PlanMaxMB func(ctx context.Context, tenantID string) int
	Now       func() time.Time
	Logger    *slog.Logger
}

var _ apply.Uploads = (*Service)(nil)

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// StagingKey is where the browser puts the file. Nothing is kept here.
func StagingKey(tenantID, jobID, id string) string { return "u/" + tenantID + "/" + jobID + "/" + id }

// FinalKey is where a scanned file is kept.
func FinalKey(tenantID, jobID, id string) string { return "f/" + tenantID + "/" + jobID + "/" + id }

type uploadConfig struct {
	MaxMB  int      `json:"max_mb"`
	Accept []string `json:"accept"`
}

// rules reads a question's size limit and accepted formats.
func (s *Service) rules(ctx context.Context, tenantID string, q *questions.FormQuestion) (int64, []string) {
	var c uploadConfig
	_ = json.Unmarshal(q.Config, &c)
	mb := c.MaxMB
	if mb <= 0 {
		mb = DefaultMaxMB
	}
	if mb > HardMaxMB {
		mb = HardMaxMB
	}
	if s.PlanMaxMB != nil {
		if p := s.PlanMaxMB(ctx, tenantID); p > 0 && p < mb {
			mb = p
		}
	}
	accept := c.Accept
	if len(accept) == 0 {
		accept = defaultAccept(q.Type)
	}
	return int64(mb) << 20, accept
}

// PresignInput is what the browser sends before uploading.
type PresignInput struct {
	PublicID    string
	QuestionID  string
	Filename    string
	ContentType string
	SizeBytes   int64
	ClientIP    string
}

// Presigned is what the browser needs to upload.
type Presigned struct {
	// Token is the answer to the question once the file is uploaded.
	Token     string
	Method    string
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
	MaxBytes  int64
}

func fieldErr(path, msg string) error {
	return &apply.ValidationError{Fields: []apply.FieldError{{Path: path, Message: msg}}}
}

// Presign checks the request against the job's frozen form and returns a one-time upload URL.
func (s *Service) Presign(ctx context.Context, in PresignInput) (*Presigned, error) {
	if s.Scanner == nil {
		return nil, apply.ErrFilesUnavailable
	}
	if s.Limiter != nil {
		ok, err := s.Limiter.Allow(ctx, "upload:ip:"+in.ClientIP, PerIPPerHour, time.Hour)
		if err == nil && !ok {
			return nil, apply.ErrRateLimited
		}
		if err != nil && s.Logger != nil {
			s.Logger.Warn("uploads: rate limiter unavailable", "error", err)
		}
	}
	entry, err := s.Registry.LookupJob(ctx, in.PublicID)
	if err != nil {
		return nil, err
	}
	store, err := s.Directory.Store(entry.Region)
	if err != nil {
		return nil, fmt.Errorf("uploads: region %q: %w", entry.Region, err)
	}
	t, err := store.LoadForApply(ctx, entry.OrganizationID, in.PublicID)
	if err != nil {
		return nil, err
	}
	state, ok := public.StateOf(t.Status, t.ClosingDate, s.now())
	switch {
	case t.Status == jobs.StatusArchived:
		return nil, apply.ErrGone
	case !ok:
		return nil, apply.ErrNotFound
	case state != public.StateOpen:
		return nil, apply.ErrNotAccepting
	}

	var q *questions.FormQuestion
	for i := range t.Questions {
		if t.Questions[i].ID == in.QuestionID {
			q = &t.Questions[i]
		}
	}
	if q == nil || !apply.IsUpload(q.Type) {
		return nil, fieldErr("question_id", "this question does not take a file")
	}
	if q.SpecialCategory != "" {
		return nil, apply.ErrSensitiveUnavailable
	}
	maxBytes, accept := s.rules(ctx, entry.OrganizationID, q)
	name := cleanFilename(in.Filename)
	if name == "" {
		return nil, fieldErr("filename", "the file needs a name")
	}
	f, ok := match(accept, in.ContentType, name)
	if !ok {
		return nil, fieldErr("content_type", "this file type is not accepted: use "+strings.ToUpper(strings.Join(accept, ", ")))
	}
	if in.SizeBytes <= 0 {
		return nil, fieldErr("size_bytes", "the file is empty")
	}
	if in.SizeBytes > maxBytes {
		return nil, fieldErr("size_bytes", fmt.Sprintf("the file is larger than %d MB", maxBytes>>20))
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	exp := now.Add(PendingTTL)
	basis := t.LawfulBasis
	if basis == "" {
		basis = "legitimate_interests"
	}
	rec := &Record{
		ID: id, TenantID: entry.OrganizationID, JobID: t.JobID, QuestionID: q.ID,
		Filename: name, ContentType: f.contentTypes[0], Declared: in.SizeBytes, Status: statusPending,
		ExpiresAt: &exp, LawfulBasis: basis,
		CollectionPurpose: "Assessing your application for this role and contacting you about it",
		// Until an application uses the file, its retention is the time it waits. The application's own
		// retention date replaces it when the file is attached.
		RetentionUntil: exp,
	}
	if err = store.InsertUpload(ctx, entry.OrganizationID, rec); err != nil {
		return nil, fmt.Errorf("uploads: record upload: %w", err)
	}
	st, err := s.Directory.Storage(entry.Region)
	if err != nil {
		return nil, fmt.Errorf("uploads: storage for %q: %w", entry.Region, err)
	}
	put, err := st.PresignPut(ctx, StagingKey(rec.TenantID, rec.JobID, id), rec.ContentType, PresignTTL)
	if err != nil {
		return nil, fmt.Errorf("uploads: presign: %w", err)
	}
	return &Presigned{
		Token: id, Method: "PUT", URL: put.URL, Headers: put.Headers,
		ExpiresAt: now.Add(PresignTTL), MaxBytes: maxBytes,
	}, nil
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("uploads: random id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

func rejected(why string) error { return fmt.Errorf("%w: %s", apply.ErrFileRejected, why) }

// Claim verifies the upload a candidate named as an answer. It implements apply.Uploads. A file that passes is
// marked clean and its scanned bytes are stored under the final key; calling Claim again for the same token
// (a retried submit) does not scan again.
func (s *Service) Claim(ctx context.Context, req apply.ClaimRequest) (apply.File, error) {
	if s.Scanner == nil {
		return apply.File{}, apply.ErrFilesUnavailable
	}
	if !uuidRE.MatchString(req.Token) {
		return apply.File{}, rejected("unknown token")
	}
	store, err := s.Directory.Store(req.Region)
	if err != nil {
		return apply.File{}, fmt.Errorf("uploads: region %q: %w", req.Region, err)
	}
	rec, err := store.GetUpload(ctx, req.TenantID, req.Token)
	if errors.Is(err, hiringtypes.ErrNotFound) {
		return apply.File{}, rejected("unknown token")
	}
	if err != nil {
		return apply.File{}, fmt.Errorf("uploads: load upload: %w", err)
	}
	if rec.JobID != req.JobID || rec.QuestionID != req.QuestionID {
		return apply.File{}, rejected("token belongs to another question")
	}
	if rec.Status == statusRejected || rec.ExpiresAt == nil {
		return apply.File{}, rejected("token cannot be used")
	}
	if !s.now().Before(*rec.ExpiresAt) {
		return apply.File{}, rejected("token expired")
	}
	ok := apply.File{ID: rec.ID, QuestionID: rec.QuestionID, SizeBytes: rec.Size, SHA256: rec.SHA256}
	if rec.Status == statusClean {
		return ok, nil
	}

	st, err := s.Directory.Storage(req.Region)
	if err != nil {
		return apply.File{}, fmt.Errorf("uploads: storage for %q: %w", req.Region, err)
	}
	maxBytes, accept := s.rules(ctx, req.TenantID, req.Question)
	stage := StagingKey(rec.TenantID, rec.JobID, rec.ID)

	reject := func(why string) (apply.File, error) {
		if err := st.Remove(ctx, stage); err != nil && s.Logger != nil {
			s.Logger.Warn("uploads: remove rejected object", "error", err)
		}
		if err := store.RejectUpload(ctx, rec.TenantID, rec.ID); err != nil && s.Logger != nil {
			s.Logger.Warn("uploads: mark upload rejected", "error", err)
		}
		return apply.File{}, rejected(why)
	}

	size, err := st.Stat(ctx, stage)
	if errors.Is(err, ErrObjectNotFound) {
		return apply.File{}, rejected("nothing was uploaded") // the candidate may still upload to the same URL
	}
	if err != nil {
		return apply.File{}, fmt.Errorf("uploads: stat upload: %w", err)
	}
	if size <= 0 || size > maxBytes {
		return reject("size out of range")
	}
	rc, err := st.Open(ctx, stage)
	if err != nil {
		return apply.File{}, fmt.Errorf("uploads: open upload: %w", err)
	}
	// The whole file is read once. Everything after this works on these bytes, so the object cannot change
	// between the scan and the copy.
	data, err := io.ReadAll(io.LimitReader(rc, maxBytes+1))
	_ = rc.Close()
	if err != nil {
		return apply.File{}, fmt.Errorf("uploads: read upload: %w", err)
	}
	if int64(len(data)) > maxBytes || int64(len(data)) != size {
		return reject("size changed")
	}

	f, matched := match(accept, rec.ContentType, rec.Filename)
	if !matched || !f.magic(data) {
		return reject("content does not match its type")
	}
	if declared := rec.Declared; declared != 0 && declared != int64(len(data)) {
		return reject("size differs from the declared size")
	}

	verdict, err := s.Scanner.Scan(ctx, bytes.NewReader(data))
	if err != nil {
		if s.Logger != nil {
			s.Logger.Error("uploads: scan failed", "error", err)
		}
		return apply.File{}, apply.ErrFilesUnavailable
	}
	if !verdict.Clean {
		if s.Logger != nil {
			s.Logger.Warn("uploads: malware found", "job", rec.JobID, "signature", verdict.Signature)
		}
		return reject("malware")
	}

	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	if err = st.Put(ctx, FinalKey(rec.TenantID, rec.JobID, rec.ID), bytes.NewReader(data), int64(len(data)), rec.ContentType); err != nil {
		return apply.File{}, fmt.Errorf("uploads: store scanned file: %w", err)
	}
	if err = store.MarkUploadClean(ctx, rec.TenantID, rec.ID, int64(len(data)), hexSum); err != nil {
		return apply.File{}, fmt.Errorf("uploads: mark clean: %w", err)
	}
	if err = st.Remove(ctx, stage); err != nil && s.Logger != nil {
		s.Logger.Warn("uploads: remove staging object", "error", err)
	}
	return apply.File{ID: rec.ID, QuestionID: rec.QuestionID, SizeBytes: int64(len(data)), SHA256: hexSum}, nil
}
