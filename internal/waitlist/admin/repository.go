package admin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	qb "github.com/Software78/sql-go-query-builder"
	"github.com/Software78/sql-go-query-builder/builder"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	sqlCreateAdminNote = `
INSERT INTO admin_notes (submission_id, note, created_by)
SELECT ws.id, $2, $3
FROM waitlist_submissions ws
WHERE ws.uuid = $1
  AND ws.deleted_at IS NULL
RETURNING uuid, note, created_by, created_at`

	sqlGetSubmissionStats = `
SELECT
    COUNT(*)::bigint AS total_submissions,
    COALESCE(AVG(CASE WHEN wants_early_access THEN 1.0 ELSE 0.0 END), 0)::float8 AS early_access_opt_in_rate,
    COALESCE(AVG(CASE WHEN wants_user_testing THEN 1.0 ELSE 0.0 END), 0)::float8 AS user_testing_opt_in_rate
FROM waitlist_submissions
WHERE deleted_at IS NULL`

	sqlListTopIndustries = `
SELECT i.uuid, i.name, COUNT(*)::bigint AS count
FROM submission_industries si
INNER JOIN industries i ON i.id = si.industry_id
INNER JOIN waitlist_submissions ws ON ws.id = si.submission_id
WHERE ws.deleted_at IS NULL
GROUP BY i.uuid, i.name
ORDER BY COUNT(*) DESC, i.name
LIMIT 5`

	sqlListTopFrustrations = `
SELECT hf.uuid, hf.description, COUNT(*)::bigint AS count
FROM submission_frustrations sf
INNER JOIN hiring_frustrations hf ON hf.id = sf.frustration_id
INNER JOIN waitlist_submissions ws ON ws.id = sf.submission_id
WHERE ws.deleted_at IS NULL
GROUP BY hf.uuid, hf.description
ORDER BY COUNT(*) DESC, hf.description
LIMIT 5`
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
	pool *pgxpool.Pool
	qb   *qb.QB
}

// NewRepository returns an admin repository backed by PostgreSQL.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, qb: qb.NewPostgres()}
}

func (r *Repository) listSubmissionsQuery(filter *ListFilter) (*builder.SelectBuilder, error) {
	b := r.qb.Select(
		"waitlist_submissions.uuid",
		"waitlist_submissions.name",
		"waitlist_submissions.wants_early_access",
		"waitlist_submissions.wants_user_testing",
		"waitlist_submissions.submitted_at",
		"roles.uuid",
		"roles.name",
		"team_sizes.uuid",
		"team_sizes.label",
	).
		From("waitlist_submissions").
		Join("roles", "roles.id", "waitlist_submissions.role_id").
		Join("team_sizes", "team_sizes.id", "waitlist_submissions.team_size_id").
		WhereNull("waitlist_submissions.deleted_at")

	if filter.IndustryID != nil {
		b = b.WhereRaw(`EXISTS (
			SELECT 1
			FROM submission_industries si
			INNER JOIN industries i ON i.id = si.industry_id
			WHERE si.submission_id = waitlist_submissions.id
			  AND i.uuid = ?
		)`, *filter.IndustryID)
	}
	if filter.RoleID != nil {
		b = b.Where("roles.uuid", "=", *filter.RoleID)
	}
	if filter.TeamSizeID != nil {
		b = b.Where("team_sizes.uuid", "=", *filter.TeamSizeID)
	}
	if filter.WantsEarlyAccess != nil {
		b = b.Where("waitlist_submissions.wants_early_access", "=", *filter.WantsEarlyAccess)
	}
	if filter.WantsUserTesting != nil {
		b = b.Where("waitlist_submissions.wants_user_testing", "=", *filter.WantsUserTesting)
	}
	if filter.SubmittedFrom != nil {
		b = b.Where("waitlist_submissions.submitted_at", ">=", *filter.SubmittedFrom)
	}
	if filter.SubmittedTo != nil {
		b = b.Where("waitlist_submissions.submitted_at", "<=", *filter.SubmittedTo)
	}
	if filter.Cursor != "" {
		cursorTime, cursorID, err := decodeCursor(filter.Cursor)
		if err != nil {
			return nil, err
		}

		b = b.WhereRaw(
			"(waitlist_submissions.submitted_at, waitlist_submissions.id) < (?, ?)",
			cursorTime,
			cursorID,
		)
	}

	return b, nil
}

// ListSubmissions returns a cursor page of submissions.
func (r *Repository) ListSubmissions(ctx context.Context, filter *ListFilter) (*ListResult, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	b, err := r.listSubmissionsQuery(filter)
	if err != nil {
		return nil, err
	}

	sql, args, err := b.
		OrderBy("waitlist_submissions.submitted_at", builder.DESC).
		OrderBy("waitlist_submissions.id", builder.DESC).
		Limit(int64(limit + 1)).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("list submissions: %w", err)
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list submissions: %w", err)
	}
	defer rows.Close()

	result := &ListResult{Items: make([]Summary, 0, limit)}
	for rows.Next() {
		var row Summary
		if scanErr := rows.Scan(
			&row.ID,
			&row.Name,
			&row.WantsEarlyAccess,
			&row.WantsUserTesting,
			&row.SubmittedAt,
			&row.Role.ID,
			&row.Role.Name,
			&row.TeamSize.ID,
			&row.TeamSize.Label,
		); scanErr != nil {
			return nil, fmt.Errorf("list submissions: %w", scanErr)
		}
		result.Items = append(result.Items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list submissions: %w", err)
	}

	if len(result.Items) > limit {
		last := result.Items[limit-1]
		lastID, err := r.getSubmissionInternalID(ctx, last.ID)
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
	sql, args, err := r.qb.Select(
		"waitlist_submissions.id",
		"waitlist_submissions.uuid",
		"waitlist_submissions.name",
		"waitlist_submissions.wants_early_access",
		"waitlist_submissions.wants_user_testing",
		"waitlist_submissions.submitted_at",
		"roles.uuid",
		"roles.name",
		"team_sizes.uuid",
		"team_sizes.label",
	).
		From("waitlist_submissions").
		Join("roles", "roles.id", "waitlist_submissions.role_id").
		Join("team_sizes", "team_sizes.id", "waitlist_submissions.team_size_id").
		Where("waitlist_submissions.uuid", "=", publicID).
		WhereNull("waitlist_submissions.deleted_at").
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("get submission: %w", err)
	}

	var (
		internalID int64
		detail     Detail
	)
	scanErr := r.pool.QueryRow(ctx, sql, args...).Scan(
		&internalID,
		&detail.ID,
		&detail.Name,
		&detail.WantsEarlyAccess,
		&detail.WantsUserTesting,
		&detail.SubmittedAt,
		&detail.Role.ID,
		&detail.Role.Name,
		&detail.TeamSize.ID,
		&detail.TeamSize.Label,
	)
	if scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, fmt.Errorf("submission not found: %w", scanErr)
		}

		return nil, fmt.Errorf("get submission: %w", scanErr)
	}

	industryRows, err := r.listSubmissionIndustries(ctx, internalID)
	if err != nil {
		return nil, fmt.Errorf("load industries selections: %w", err)
	}

	industries, otherIndustry, err := loadNamedSelections(
		industryRows,
		func(item namedSelectionRow) (uuid.UUID, string, *string, string) {
			return item.id, item.name, item.otherText, item.slug
		},
	)
	if err != nil {
		return nil, err
	}

	detail.Industries = industries
	detail.OtherIndustry = otherIndustry

	toolRows, err := r.listSubmissionHiringTools(ctx, internalID)
	if err != nil {
		return nil, fmt.Errorf("load hiring_tools selections: %w", err)
	}

	tools, otherTool, err := loadNamedSelections(
		toolRows,
		func(item namedSelectionRow) (uuid.UUID, string, *string, string) {
			return item.id, item.name, item.otherText, item.slug
		},
	)
	if err != nil {
		return nil, err
	}

	detail.Tools = tools
	detail.OtherTool = otherTool

	frustrationRows, err := r.listSubmissionFrustrations(ctx, internalID)
	if err != nil {
		return nil, fmt.Errorf("load hiring_frustrations selections: %w", err)
	}

	frustrations, otherFrustration, err := loadNamedSelections(
		frustrationRows,
		func(item namedSelectionRow) (uuid.UUID, string, *string, string) {
			return item.id, item.description, item.otherText, item.slug
		},
	)
	if err != nil {
		return nil, err
	}

	detail.Frustrations = frustrations
	detail.OtherFrustration = otherFrustration

	return &detail, nil
}

type namedSelectionRow struct {
	id          uuid.UUID
	name        string
	description string
	otherText   *string
	slug        string
}

func (r *Repository) listSubmissionIndustries(ctx context.Context, submissionID int64) ([]namedSelectionRow, error) {
	sql, args, err := r.qb.Select(
		"industries.uuid",
		"industries.name",
		"submission_industries.other_text",
		"industries.slug",
	).
		From("submission_industries").
		Join("industries", "industries.id", "submission_industries.industry_id").
		Where("submission_industries.submission_id", "=", submissionID).
		OrderBy("industries.sort_order", builder.ASC).
		OrderBy("industries.name", builder.ASC).
		ToSQL()
	if err != nil {
		return nil, err
	}

	return r.scanNamedSelections(ctx, sql, args, false)
}

func (r *Repository) listSubmissionHiringTools(ctx context.Context, submissionID int64) ([]namedSelectionRow, error) {
	sql, args, err := r.qb.Select(
		"hiring_tools.uuid",
		"hiring_tools.name",
		"submission_hiring_tools.other_text",
		"hiring_tools.slug",
	).
		From("submission_hiring_tools").
		Join("hiring_tools", "hiring_tools.id", "submission_hiring_tools.hiring_tool_id").
		Where("submission_hiring_tools.submission_id", "=", submissionID).
		OrderBy("hiring_tools.sort_order", builder.ASC).
		OrderBy("hiring_tools.name", builder.ASC).
		ToSQL()
	if err != nil {
		return nil, err
	}

	return r.scanNamedSelections(ctx, sql, args, false)
}

func (r *Repository) listSubmissionFrustrations(ctx context.Context, submissionID int64) ([]namedSelectionRow, error) {
	sql, args, err := r.qb.Select(
		"hiring_frustrations.uuid",
		"hiring_frustrations.description",
		"submission_frustrations.other_text",
		"hiring_frustrations.slug",
	).
		From("submission_frustrations").
		Join("hiring_frustrations", "hiring_frustrations.id", "submission_frustrations.frustration_id").
		Where("submission_frustrations.submission_id", "=", submissionID).
		OrderBy("hiring_frustrations.sort_order", builder.ASC).
		OrderBy("hiring_frustrations.description", builder.ASC).
		ToSQL()
	if err != nil {
		return nil, err
	}

	return r.scanNamedSelections(ctx, sql, args, true)
}

func (r *Repository) scanNamedSelections(
	ctx context.Context,
	sql string,
	args []any,
	useDescription bool,
) ([]namedSelectionRow, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]namedSelectionRow, 0)
	for rows.Next() {
		var item namedSelectionRow
		var scanErr error
		if useDescription {
			scanErr = rows.Scan(&item.id, &item.description, &item.otherText, &item.slug)
			item.name = item.description
		} else {
			scanErr = rows.Scan(&item.id, &item.name, &item.otherText, &item.slug)
		}
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}

	return items, rows.Err()
}

func (r *Repository) getSubmissionInternalID(ctx context.Context, publicID uuid.UUID) (int64, error) {
	sql, args, err := r.qb.Select("id").
		From("waitlist_submissions").
		Where("uuid", "=", publicID).
		ToSQL()
	if err != nil {
		return 0, err
	}

	var id int64
	if scanErr := r.pool.QueryRow(ctx, sql, args...).Scan(&id); scanErr != nil {
		return 0, scanErr
	}

	return id, nil
}

// CreateNote stores an admin note for a submission.
func (r *Repository) CreateNote(ctx context.Context, submissionID uuid.UUID, note, createdBy string) (*Note, error) {
	var created Note
	err := r.pool.QueryRow(ctx, sqlCreateAdminNote, submissionID, note, createdBy).Scan(
		&created.ID,
		&created.Note,
		&created.CreatedBy,
		&created.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("submission not found: %w", err)
		}

		return nil, fmt.Errorf("create note: %w", err)
	}

	return &created, nil
}

// Stats returns aggregate submission metrics.
func (r *Repository) Stats(ctx context.Context) (*Stats, error) {
	var stats Stats
	if err := r.pool.QueryRow(ctx, sqlGetSubmissionStats).Scan(
		&stats.TotalSubmissions,
		&stats.EarlyAccessOptInRate,
		&stats.UserTestingOptInRate,
	); err != nil {
		return nil, fmt.Errorf("load submission stats: %w", err)
	}

	industryRows, err := r.pool.Query(ctx, sqlListTopIndustries)
	if err != nil {
		return nil, fmt.Errorf("load top industries: %w", err)
	}
	defer industryRows.Close()

	for industryRows.Next() {
		var item CountRef
		if scanErr := industryRows.Scan(&item.ID, &item.Name, &item.Count); scanErr != nil {
			return nil, fmt.Errorf("load top industries: %w", scanErr)
		}
		stats.TopIndustries = append(stats.TopIndustries, item)
	}
	if rowsErr := industryRows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("load top industries: %w", rowsErr)
	}

	frustrationRows, err := r.pool.Query(ctx, sqlListTopFrustrations)
	if err != nil {
		return nil, fmt.Errorf("load top frustrations: %w", err)
	}
	defer frustrationRows.Close()

	for frustrationRows.Next() {
		var item CountDescription
		if scanErr := frustrationRows.Scan(&item.ID, &item.Description, &item.Count); scanErr != nil {
			return nil, fmt.Errorf("load top frustrations: %w", scanErr)
		}
		stats.TopFrustrations = append(stats.TopFrustrations, item)
	}
	if err := frustrationRows.Err(); err != nil {
		return nil, fmt.Errorf("load top frustrations: %w", err)
	}

	return &stats, nil
}

func loadNamedSelections[T any](
	rows []T,
	accessor func(T) (uuid.UUID, string, *string, string),
) ([]NamedRef, *string, error) {
	items := make([]NamedRef, 0, len(rows))
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
