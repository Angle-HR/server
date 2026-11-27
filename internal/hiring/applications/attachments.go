package applications

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/pkg/formschema"
)

// Limits of attachments on candidate emails.
const (
	MaxAttachmentBytes   = 5 << 20
	MaxAttachmentsPerMsg = 3
	// MaxUnsentAttachments is how many uploaded files may wait on one application without being sent.
	MaxUnsentAttachments = 10
)

// Errors of attachments.
var (
	// ErrInfected: the scan found malware.
	ErrInfected = errors.New("applications: the file did not pass the malware scan")
	// ErrTooManyAttachments: the application already has MaxUnsentAttachments files waiting.
	ErrTooManyAttachments = errors.New("applications: too many files waiting")
	// ErrNoAttachments: attachments are not set up (no store, bucket or scanner).
	ErrNoAttachments = errors.New("applications: attachments are not available")
	// ErrUnknownAttachment: a file named in attachment_ids is not an unsent file of this application.
	ErrUnknownAttachment = errors.New("applications: unknown attachment")
)

// attachmentTypes are the file types a company may send: the extension and the type it is checked against.
var attachmentTypes = map[string][]string{
	".pdf":  {"application/pdf"},
	".docx": {"application/zip", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
	".png":  {"image/png"},
	".jpg":  {"image/jpeg"},
	".jpeg": {"image/jpeg"},
	".txt":  {"text/plain; charset=utf-8"},
}

var attachmentContentTypes = map[string]string{
	".pdf": "application/pdf", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".txt": "text/plain; charset=utf-8",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
}

// Attachment is a file on an email to a candidate.
type Attachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	// ObjectKey is where the file is kept; it is not part of the API.
	ObjectKey string `json:"-"`
	SHA256    string `json:"-"`
}

// AttachmentStore keeps the rows of attachments.
type AttachmentStore interface {
	// AddAttachment saves a row for an uploaded file that no message uses yet. ErrTooManyAttachments when the application
	// already has MaxUnsentAttachments waiting (ErrTooManyAttachments).
	AddAttachment(ctx context.Context, tenantID, jobID, appID, actorID string, a *Attachment) error
	// UnsentAttachments lists the files uploaded to the application that no message uses.
	UnsentAttachments(ctx context.Context, tenantID, jobID, appID string) ([]Attachment, error)
	// DeleteAttachment removes an unsent file's row and returns its object key.
	DeleteAttachment(ctx context.Context, tenantID, jobID, appID, id string) (string, error)
}

func cleanAttachmentIDs(ids []string) ([]string, error) {
	if len(ids) > MaxAttachmentsPerMsg {
		return nil, invalid("attachment_ids", fmt.Sprintf("attach at most %d files", MaxAttachmentsPerMsg))
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !formschema.IsUUID(id) {
			return nil, invalid("attachment_ids", "unknown file")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func (s *Service) attachments() (AttachmentStore, error) {
	if s.Attach == nil || s.Docs == nil || s.Scanner == nil {
		return nil, ErrNoAttachments
	}
	return s.Attach, nil
}

// UploadAttachment checks, scans and stores a file a person wants to send to a candidate. The file waits on the
// application until a message uses it (attachment_ids). A file that cannot be scanned is refused.
func (s *Service) UploadAttachment(ctx context.Context, c Caller, jobID, appID, filename string, data []byte) (*Attachment, error) {
	as, err := s.attachments()
	if err != nil {
		return nil, err
	}
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
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/"))
	if name == "." || name == "/" || name == "" || utf8.RuneCountInString(name) > 120 || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil, invalid("file", "give the file a shorter name without special characters")
	}
	ext := strings.ToLower(path.Ext(name))
	want, ok := attachmentTypes[ext]
	if !ok {
		return nil, invalid("file", "send a PDF, Word (.docx), PNG, JPEG or text file")
	}
	if len(data) == 0 || len(data) > MaxAttachmentBytes {
		return nil, invalid("file", fmt.Sprintf("the file must be between 1 byte and %d MB", MaxAttachmentBytes>>20))
	}
	// The content must be what the name says: a program renamed to .pdf is refused.
	sniffed := http.DetectContentType(data)
	match := false
	for _, w := range want {
		if sniffed == w {
			match = true
		}
	}
	if !match {
		return nil, invalid("file", "the file is not the type its name says")
	}
	v, err := s.Scanner.Scan(ctx, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("applications: scan attachment: %w", err)
	}
	if !v.Clean {
		return nil, ErrInfected
	}
	sum := sha256.Sum256(data)
	att := &Attachment{ID: uuid.NewString(), Filename: name, ContentType: attachmentContentTypes[ext], Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	att.ObjectKey = AttachmentKey(c.OrgID, appID, att.ID)
	if err = s.Docs.Put(ctx, att.ObjectKey, bytes.NewReader(data), int64(len(data)), att.ContentType); err != nil {
		return nil, fmt.Errorf("applications: store attachment: %w", err)
	}
	if err = as.AddAttachment(ctx, c.OrgID, jobID, appID, c.UserID, att); err != nil {
		_ = s.Docs.Delete(ctx, att.ObjectKey)
		return nil, err
	}
	return att, nil
}

// AttachmentKey is where an attachment is kept in the regional bucket.
func AttachmentKey(tenantID, appID, id string) string {
	return "m/" + tenantID + "/" + appID + "/" + id
}

// UnsentAttachments lists the uploaded files that no message uses yet.
func (s *Service) UnsentAttachments(ctx context.Context, c Caller, jobID, appID string) ([]Attachment, error) {
	as, err := s.attachments()
	if err != nil {
		return nil, err
	}
	if _, err = s.access(ctx, c, jobID); err != nil {
		return nil, err
	}
	if !formschema.IsUUID(appID) {
		return nil, ErrNotFound
	}
	return as.UnsentAttachments(ctx, c.OrgID, jobID, appID)
}

// DeleteAttachment removes an uploaded file that no message uses.
func (s *Service) DeleteAttachment(ctx context.Context, c Caller, jobID, appID, id string) error {
	as, err := s.attachments()
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
	key, err := as.DeleteAttachment(ctx, c.OrgID, jobID, appID, id)
	if err != nil {
		return err
	}
	return s.Docs.Delete(ctx, key)
}
