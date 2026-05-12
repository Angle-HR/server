package admin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/db/sqlc"
	"github.com/Angle-HR/server/pkg/db"
)

// ListFilter narrows admin submission queries.
type ListFilter struct {
	Cursor           string
	Limit            int
	IndustryID       *uuid.UUID
	RoleID           *uuid.UUID
	TeamSizeID       *uuid.UUID
	WantsEarlyAccess *bool
	WantsUserTesting *bool
	SubmittedFrom    *time.Time
	SubmittedTo      *time.Time
}

// Summary is a submission row for admin list views.
type Summary struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	WantsEarlyAccess bool      `json:"wants_early_access"`
	WantsUserTesting bool      `json:"wants_user_testing"`
	SubmittedAt      time.Time `json:"submitted_at"`
	Role             NamedRef  `json:"role"`
	TeamSize         LabelRef  `json:"team_size"`
}

// NamedRef is a labeled reference option.
type NamedRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// LabelRef is a team size reference option.
type LabelRef struct {
	ID    uuid.UUID `json:"id"`
	Label string    `json:"label"`
}

// Detail is a full admin submission view.
type Detail struct {
	Summary
	Industries       []NamedRef `json:"industries"`
	Tools            []NamedRef `json:"tools"`
	Frustrations     []NamedRef `json:"frustrations"`
	OtherIndustry    *string    `json:"other_industry,omitempty"`
	OtherTool        *string    `json:"other_tool,omitempty"`
	OtherFrustration *string    `json:"other_frustration,omitempty"`
}

// Note is an internal admin note.
type Note struct {
	ID        uuid.UUID `json:"id"`
	Note      string    `json:"note"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Stats is the admin aggregate dashboard.
type Stats struct {
	TotalSubmissions     int64              `json:"total_submissions"`
	EarlyAccessOptInRate float64            `json:"early_access_opt_in_rate"`
	UserTestingOptInRate float64            `json:"user_testing_opt_in_rate"`
	TopIndustries        []CountRef         `json:"top_industries"`
	TopFrustrations      []CountDescription `json:"top_frustrations"`
}

// CountRef is a counted named reference.
type CountRef struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Count int64     `json:"count"`
}

// CountDescription is a counted frustration reference.
type CountDescription struct {
	ID          uuid.UUID `json:"id"`
	Description string    `json:"description"`
	Count       int64     `json:"count"`
}

// ListResult is a paginated admin submission list.
type ListResult struct {
	Items      []Summary
	NextCursor string
	HasMore    bool
}

// Repository loads admin onboarding data.
type Repository struct {
	queries *sqlc.Queries
}

// NewRepository returns an admin repository backed by PostgreSQL.
func NewRepository(queries *sqlc.Queries) *Repository {
	return &Repository{queries: queries}
}

// ListSubmissions returns a cursor page of submissions.
func (r *Repository) ListSubmissions(ctx context.Context, filter *ListFilter) (*ListResult, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	params := sqlc.ListSubmissionsParams{
		IndustryUuid:     db.OptionalUUID(filter.IndustryID),
		RoleUuid:         db.OptionalUUID(filter.RoleID),
		TeamSizeUuid:     db.OptionalUUID(filter.TeamSizeID),
		WantsEarlyAccess: filter.WantsEarlyAccess,
		WantsUserTesting: filter.WantsUserTesting,
		SubmittedFrom:    db.OptionalTime(filter.SubmittedFrom),
		SubmittedTo:      db.OptionalTime(filter.SubmittedTo),
		RowLimit:         int32(limit + 1),
	}

	if filter.Cursor != "" {
		cursorTime, cursorID, err := decodeCursor(filter.Cursor)
		if err != nil {
			return nil, err
		}

		params.CursorSubmittedAt = db.OptionalTime(&cursorTime)
		params.CursorID = &cursorID
	}

	rows, err := r.queries.ListSubmissions(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list submissions: %w", err)
	}

	result := &ListResult{Items: make([]Summary, 0, limit)}
	for i := range rows {
		row := rows[i]
		result.Items = append(result.Items, Summary{
			ID:               row.Uuid,
			Name:             row.Name,
			WantsEarlyAccess: row.WantsEarlyAccess,
			WantsUserTesting: row.WantsUserTesting,
			SubmittedAt:      row.SubmittedAt,
			Role: NamedRef{
				ID:   row.RoleUuid,
				Name: row.RoleName,
			},
			TeamSize: LabelRef{
				ID:    row.TeamSizeUuid,
				Label: row.TeamSizeLabel,
			},
		})
	}

	if len(result.Items) > limit {
		last := result.Items[limit-1]
		lastID, err := r.queries.GetSubmissionInternalID(ctx, last.ID)
		if err != nil {
			return nil, fmt.Errorf("load cursor id: %w", err)
		}

		result.NextCursor = encodeCursor(last.SubmittedAt, lastID)
		result.HasMore = true
		result.Items = result.Items[:limit]
	}

	return result, nil
}

// GetSubmission returns a submission by public UUID.
func (r *Repository) GetSubmission(ctx context.Context, publicID uuid.UUID) (*Detail, error) {
	row, err := r.queries.GetSubmissionDetail(ctx, publicID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("submission not found: %w", err)
		}

		return nil, fmt.Errorf("get submission: %w", err)
	}

	detail := &Detail{
		Summary: Summary{
			ID:               row.Uuid,
			Name:             row.Name,
			WantsEarlyAccess: row.WantsEarlyAccess,
			WantsUserTesting: row.WantsUserTesting,
			SubmittedAt:      row.SubmittedAt,
			Role: NamedRef{
				ID:   row.RoleUuid,
				Name: row.RoleName,
			},
			TeamSize: LabelRef{
				ID:    row.TeamSizeUuid,
				Label: row.TeamSizeLabel,
			},
		},
	}

	industryRows, err := r.queries.ListSubmissionIndustries(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("load industries selections: %w", err)
	}

	industries, otherIndustry, err := loadNamedSelections(
		industryRows,
		func(item sqlc.ListSubmissionIndustriesRow) (uuid.UUID, string, *string, string) {
			return item.Uuid, item.Name, item.OtherText, item.Slug
		},
	)
	if err != nil {
		return nil, err
	}

	detail.Industries = industries
	detail.OtherIndustry = otherIndustry

	toolRows, err := r.queries.ListSubmissionHiringTools(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("load hiring_tools selections: %w", err)
	}

	tools, otherTool, err := loadNamedSelections(
		toolRows,
		func(item sqlc.ListSubmissionHiringToolsRow) (uuid.UUID, string, *string, string) {
			return item.Uuid, item.Name, item.OtherText, item.Slug
		},
	)
	if err != nil {
		return nil, err
	}

	detail.Tools = tools
	detail.OtherTool = otherTool

	frustrationRows, err := r.queries.ListSubmissionFrustrations(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("load hiring_frustrations selections: %w", err)
	}

	frustrations, otherFrustration, err := loadNamedSelections(
		frustrationRows,
		func(item sqlc.ListSubmissionFrustrationsRow) (uuid.UUID, string, *string, string) {
			return item.Uuid, item.Description, item.OtherText, item.Slug
		},
	)
	if err != nil {
		return nil, err
	}

	detail.Frustrations = frustrations
	detail.OtherFrustration = otherFrustration

	return detail, nil
}

// CreateNote stores an admin note for a submission.
func (r *Repository) CreateNote(ctx context.Context, submissionID uuid.UUID, note, createdBy string) (*Note, error) {
	created, err := r.queries.CreateAdminNote(ctx, sqlc.CreateAdminNoteParams{
		Uuid:      submissionID,
		Note:      note,
		CreatedBy: createdBy,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("submission not found: %w", err)
		}

		return nil, fmt.Errorf("create note: %w", err)
	}

	return &Note{
		ID:        created.Uuid,
		Note:      created.Note,
		CreatedBy: created.CreatedBy,
		CreatedAt: created.CreatedAt,
	}, nil
}

// Stats returns aggregate submission metrics.
func (r *Repository) Stats(ctx context.Context) (*Stats, error) {
	aggregate, err := r.queries.GetSubmissionStats(ctx)
	if err != nil {
		return nil, fmt.Errorf("load submission stats: %w", err)
	}

	stats := &Stats{
		TotalSubmissions:     aggregate.TotalSubmissions,
		EarlyAccessOptInRate: aggregate.EarlyAccessOptInRate,
		UserTestingOptInRate: aggregate.UserTestingOptInRate,
	}

	industries, err := r.queries.ListTopIndustries(ctx)
	if err != nil {
		return nil, fmt.Errorf("load top industries: %w", err)
	}

	for _, item := range industries {
		stats.TopIndustries = append(stats.TopIndustries, CountRef{
			ID:    item.Uuid,
			Name:  item.Name,
			Count: item.Count,
		})
	}

	frustrations, err := r.queries.ListTopFrustrations(ctx)
	if err != nil {
		return nil, fmt.Errorf("load top frustrations: %w", err)
	}

	for _, item := range frustrations {
		stats.TopFrustrations = append(stats.TopFrustrations, CountDescription{
			ID:          item.Uuid,
			Description: item.Description,
			Count:       item.Count,
		})
	}

	return stats, nil
}

func loadNamedSelections[T any](
	rows []T,
	accessor func(T) (uuid.UUID, string, *string, string),
) ([]NamedRef, *string, error) {
	var items = make([]NamedRef, 0, len(rows))
	var otherText *string
	for _, row := range rows {
		id, name, text, slug := accessor(row)
		if slug == "other" {
			otherText = text
		}

		items = append(items, NamedRef{ID: id, Name: name})
	}

	return items, otherText, nil
}

func encodeCursor(submittedAt time.Time, id int64) string {
	raw := fmt.Sprintf("%s|%d", submittedAt.UTC().Format(time.RFC3339Nano), id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (time.Time, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("invalid cursor")
	}

	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, 0, fmt.Errorf("invalid cursor")
	}

	submittedAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("invalid cursor")
	}

	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("invalid cursor")
	}

	return submittedAt, id, nil
}
