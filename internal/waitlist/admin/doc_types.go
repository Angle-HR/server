package admin

import "github.com/Angle-HR/server/internal/apidoc"

// CreateNoteRequest is the admin note creation payload.
type CreateNoteRequest struct {
	Note      string `json:"note"`
	CreatedBy string `json:"created_by"`
}

// SubmissionListEnvelope is a paginated admin submissions response.
type SubmissionListEnvelope struct {
	Data []Summary      `json:"data"`
	Meta SubmissionMeta `json:"meta"`
}

// SubmissionMeta extends list metadata with pagination fields.
type SubmissionMeta struct {
	RequestID  string `json:"request_id,omitempty"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    *bool  `json:"has_more,omitempty"`
}

// SubmissionDetailEnvelope is a single admin submission response.
type SubmissionDetailEnvelope struct {
	Data Detail       `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

// NoteEnvelope is a created admin note response.
type NoteEnvelope struct {
	Data Note         `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

// StatsEnvelope is the admin stats response.
type StatsEnvelope struct {
	Data Stats        `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}
