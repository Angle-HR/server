package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	qb "github.com/Software78/sql-go-query-builder"
	"github.com/Software78/sql-go-query-builder/expr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/waitlist/catalog"
	"github.com/Angle-HR/server/pkg/apperror"
)

const sessionTTL = 7 * 24 * time.Hour
const sessionTokenBytes = 32

type dbtx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// PartialState stores resolved internal IDs for each onboarding step.
type PartialState struct {
	Step1 *Step1State `json:"step1,omitempty"`
	Step2 *Step2State `json:"step2,omitempty"`
	Step3 *Step3State `json:"step3,omitempty"`
	Step4 *Step4State `json:"step4,omitempty"`
}

// Step1State stores industry selections.
type Step1State struct {
	IndustryIDs   []int64 `json:"industry_ids"`
	OtherIndustry *string `json:"other_industry,omitempty"`
}

// Step2State stores hiring tool selections.
type Step2State struct {
	ToolIDs   []int64 `json:"tool_ids"`
	OtherTool *string `json:"other_tool,omitempty"`
}

// Step3State stores frustration selections.
type Step3State struct {
	FrustrationIDs   []int64 `json:"frustration_ids"`
	OtherFrustration *string `json:"other_frustration,omitempty"`
}

// Step4State stores role and team size selections.
type Step4State struct {
	RoleID     int64 `json:"role_id"`
	TeamSizeID int64 `json:"team_size_id"`
}

// Record is a persisted onboarding session.
type Record struct {
	ID           int64
	CurrentStep  int
	ExpiresAt    time.Time
	CompletedAt  *time.Time
	PartialState PartialState
}

// Repository persists onboarding sessions and submissions.
type Repository struct {
	pool *pgxpool.Pool
	qb   *qb.QB
}

// NewRepository returns a PostgreSQL-backed session repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, qb: qb.NewPostgres()}
}

// Create inserts a new session and returns the raw token.
func (r *Repository) Create(ctx context.Context) (string, *Record, error) {
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}

	expiresAt := catalog.NowUTC().Add(sessionTTL)
	sql, args, err := r.qb.Insert("submission_sessions").
		Columns("token_hash", "current_step", "expires_at", "partial_state").
		Values(tokenHash, int16(1), expiresAt, []byte("{}")).
		Returning("id", "current_step", "expires_at", "partial_state", "completed_at").
		ToSQL()
	if err != nil {
		return "", nil, fmt.Errorf("insert session: %w", err)
	}

	var (
		id           int64
		currentStep  int16
		expires      time.Time
		rawState     []byte
		completedAt  *time.Time
		completedRaw *time.Time
	)
	scanErr := r.pool.QueryRow(ctx, sql, args...).Scan(
		&id,
		&currentStep,
		&expires,
		&rawState,
		&completedRaw,
	)
	if scanErr != nil {
		return "", nil, fmt.Errorf("insert session: %w", scanErr)
	}
	if completedRaw != nil {
		completedAt = completedRaw
	}

	record, err := recordFromSessionRow(id, currentStep, expires, rawState, completedAt)
	if err != nil {
		return "", nil, err
	}

	return token, record, nil
}

// FindByToken loads a session by raw token.
func (r *Repository) FindByToken(ctx context.Context, token string) (*Record, error) {
	sql, args, err := r.qb.Select("id", "current_step", "expires_at", "partial_state", "completed_at").
		From("submission_sessions").
		Where("token_hash", "=", hashToken(token)).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("find session: %w", err)
	}

	var (
		id           int64
		currentStep  int16
		expires      time.Time
		rawState     []byte
		completedRaw *time.Time
	)
	scanErr := r.pool.QueryRow(ctx, sql, args...).Scan(
		&id,
		&currentStep,
		&expires,
		&rawState,
		&completedRaw,
	)
	if scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, apperror.New(apperror.CodeSessionNotFound, apperror.MsgSessionNotFound)
		}

		return nil, fmt.Errorf("find session: %w", scanErr)
	}

	var completedAt *time.Time
	if completedRaw != nil {
		completedAt = completedRaw
	}

	return recordFromSessionRow(id, currentStep, expires, rawState, completedAt)
}

func currentStepValue(step int) (int16, error) {
	switch step {
	case onboardingStep1:
		return onboardingStep1, nil
	case onboardingStep2:
		return onboardingStep2, nil
	case onboardingStep3:
		return onboardingStep3, nil
	case onboardingStep4:
		return onboardingStep4, nil
	case onboardingStep5:
		return onboardingStep5, nil
	default:
		return 0, apperror.NewWithDetails(
			apperror.CodeValidationError,
			apperror.MsgUnexpectedOnboardingStep,
			map[string]any{
				"step": step,
			},
		)
	}
}

// SaveStep persists a step and advances current_step.
func (r *Repository) SaveStep(ctx context.Context, sessionID int64, step int, state PartialState, nextStep int) error {
	rawState, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode session state: %w", err)
	}

	currentStep, err := currentStepValue(nextStep)
	if err != nil {
		return err
	}

	sql, args, err := r.qb.Update("submission_sessions").
		Set("partial_state", rawState).
		Set("current_step", currentStep).
		SetExpr("updated_at", expr.Raw{SQL: "now()"}).
		Where("id", "=", sessionID).
		WhereNull("completed_at").
		ToSQL()
	if err != nil {
		return fmt.Errorf("save session step: %w", err)
	}

	result, err := r.pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("save session step: %w", err)
	}

	if result.RowsAffected() == 0 {
		return apperror.New(apperror.CodeSessionNotFound, apperror.MsgSessionNotFound)
	}

	return nil
}

// SubmitResult is the persisted submission summary.
type SubmitResult struct {
	PublicID    uuid.UUID
	Name        string
	SubmittedAt time.Time
}

// Submit writes the final submission and completes the session.
func (r *Repository) Submit(
	ctx context.Context,
	sessionID int64,
	state PartialState,
	input SubmitInput,
) (*SubmitResult, error) {
	if state.Step1 == nil || state.Step2 == nil || state.Step3 == nil || state.Step4 == nil {
		return nil, apperror.NewWithDetails(
			apperror.CodeValidationError,
			apperror.MsgOnboardingStepsIncomplete,
			map[string]any{
				"step": "submit",
			},
		)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin submit transaction: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			fmt.Fprintf(os.Stderr, "rollback submit transaction: %v\n", rollbackErr)
		}
	}()

	submission, err := r.insertWaitlistSubmission(
		ctx,
		tx,
		input.Name,
		input.WantsEarlyAccess,
		input.WantsUserTesting,
		state.Step4.RoleID,
		state.Step4.TeamSizeID,
	)
	if err != nil {
		return nil, err
	}

	if insertErr := insertIndustryRows(
		ctx,
		tx,
		r.qb,
		submission.id,
		state.Step1.IndustryIDs,
		state.Step1.OtherIndustry,
	); insertErr != nil {
		return nil, insertErr
	}

	if insertErr := insertHiringToolRows(
		ctx,
		tx,
		r.qb,
		submission.id,
		state.Step2.ToolIDs,
		state.Step2.OtherTool,
	); insertErr != nil {
		return nil, insertErr
	}

	if insertErr := insertFrustrationRows(
		ctx,
		tx,
		r.qb,
		submission.id,
		state.Step3.FrustrationIDs,
		state.Step3.OtherFrustration,
	); insertErr != nil {
		return nil, insertErr
	}

	rowsAffected, err := r.completeSubmissionSession(ctx, tx, sessionID, submission.id)
	if err != nil {
		return nil, err
	}

	if rowsAffected == 0 {
		return nil, apperror.New(apperror.CodeConflict, apperror.MsgSessionAlreadySubmitted)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit submit transaction: %w", err)
	}

	return &SubmitResult{
		PublicID:    submission.uuid,
		Name:        input.Name,
		SubmittedAt: submission.submittedAt,
	}, nil
}

type submissionRow struct {
	id          int64
	uuid        uuid.UUID
	submittedAt time.Time
}

func (r *Repository) insertWaitlistSubmission(
	ctx context.Context,
	tx dbtx,
	name string,
	wantsEarlyAccess bool,
	wantsUserTesting bool,
	roleID int64,
	teamSizeID int64,
) (*submissionRow, error) {
	sql, args, err := r.qb.Insert("waitlist_submissions").
		Columns("name", "wants_early_access", "wants_user_testing", "role_id", "team_size_id").
		Values(name, wantsEarlyAccess, wantsUserTesting, roleID, teamSizeID).
		Returning("id", "uuid", "submitted_at").
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("insert submission: %w", err)
	}

	var row submissionRow
	if scanErr := tx.QueryRow(ctx, sql, args...).Scan(&row.id, &row.uuid, &row.submittedAt); scanErr != nil {
		return nil, fmt.Errorf("insert submission: %w", scanErr)
	}

	return &row, nil
}

func (r *Repository) completeSubmissionSession(
	ctx context.Context,
	tx dbtx,
	sessionID int64,
	submissionID int64,
) (int64, error) {
	sql, args, err := r.qb.Update("submission_sessions").
		SetExpr("completed_at", expr.Raw{SQL: "now()"}).
		Set("submission_id", submissionID).
		SetExpr("updated_at", expr.Raw{SQL: "now()"}).
		Where("id", "=", sessionID).
		WhereNull("completed_at").
		ToSQL()
	if err != nil {
		return 0, fmt.Errorf("complete session: %w", err)
	}

	result, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, fmt.Errorf("complete session: %w", err)
	}

	return result.RowsAffected(), nil
}

func recordFromSessionRow(
	id int64,
	currentStep int16,
	expiresAt time.Time,
	rawState []byte,
	completedAt *time.Time,
) (*Record, error) {
	var state PartialState
	if err := json.Unmarshal(rawState, &state); err != nil {
		return nil, fmt.Errorf("decode session state: %w", err)
	}

	return &Record{
		ID:           id,
		CurrentStep:  int(currentStep),
		ExpiresAt:    expiresAt,
		CompletedAt:  completedAt,
		PartialState: state,
	}, nil
}

func insertIndustryRows(
	ctx context.Context,
	tx dbtx,
	q *qb.QB,
	submissionID int64,
	ids []int64,
	otherText *string,
) error {
	var otherID int64
	if otherText != nil {
		value, err := getOtherID(ctx, tx, q, "industries")
		if err != nil {
			return fmt.Errorf("load other industries id: %w", err)
		}

		otherID = value
	}

	for _, id := range ids {
		var text *string
		if otherText != nil && id == otherID {
			text = otherText
		}

		if err := insertJunctionRow(ctx, tx, q, "submission_industries", "industry_id", submissionID, id, text); err != nil {
			return fmt.Errorf("insert submission_industries row: %w", err)
		}
	}

	return nil
}

func insertHiringToolRows(
	ctx context.Context,
	tx dbtx,
	q *qb.QB,
	submissionID int64,
	ids []int64,
	otherText *string,
) error {
	var otherID int64
	if otherText != nil {
		value, err := getOtherID(ctx, tx, q, "hiring_tools")
		if err != nil {
			return fmt.Errorf("load other hiring_tools id: %w", err)
		}

		otherID = value
	}

	for _, id := range ids {
		var text *string
		if otherText != nil && id == otherID {
			text = otherText
		}

		if err := insertJunctionRow(
			ctx, tx, q, "submission_hiring_tools", "hiring_tool_id", submissionID, id, text,
		); err != nil {
			return fmt.Errorf("insert submission_hiring_tools row: %w", err)
		}
	}

	return nil
}

func insertFrustrationRows(
	ctx context.Context,
	tx dbtx,
	q *qb.QB,
	submissionID int64,
	ids []int64,
	otherText *string,
) error {
	var otherID int64
	if otherText != nil {
		value, err := getOtherID(ctx, tx, q, "hiring_frustrations")
		if err != nil {
			return fmt.Errorf("load other hiring_frustrations id: %w", err)
		}

		otherID = value
	}

	for _, id := range ids {
		var text *string
		if otherText != nil && id == otherID {
			text = otherText
		}

		if err := insertJunctionRow(
			ctx, tx, q, "submission_frustrations", "frustration_id", submissionID, id, text,
		); err != nil {
			return fmt.Errorf("insert submission_frustrations row: %w", err)
		}
	}

	return nil
}

func getOtherID(ctx context.Context, tx dbtx, q *qb.QB, table string) (int64, error) {
	sql, args, err := q.Select("id").
		From(table).
		Where("slug", "=", "other").
		ToSQL()
	if err != nil {
		return 0, err
	}

	var id int64
	if scanErr := tx.QueryRow(ctx, sql, args...).Scan(&id); scanErr != nil {
		return 0, scanErr
	}

	return id, nil
}

func insertJunctionRow(
	ctx context.Context,
	tx dbtx,
	q *qb.QB,
	table string,
	refColumn string,
	submissionID int64,
	refID int64,
	otherText *string,
) error {
	sql, args, err := q.Insert(table).
		Columns("submission_id", refColumn, "other_text").
		Values(submissionID, refID, otherText).
		ToSQL()
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, sql, args...)
	return err
}

func newSessionToken() (token string, hash []byte, err error) {
	buf := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, err
	}

	token = base64.RawURLEncoding.EncodeToString(buf)
	hash = hashToken(token)
	return token, hash, nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// ParseUUIDs parses and deduplicates UUID strings.
func ParseUUIDs(values []string) ([]uuid.UUID, error) {
	if len(values) == 0 {
		return nil, apperror.NewWithDetails(apperror.CodeValidationError, apperror.MsgAtLeastOneOptionRequired, nil)
	}

	seen := make(map[uuid.UUID]struct{}, len(values))
	ids := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		id, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return nil, apperror.NewWithDetails(apperror.CodeValidationError, apperror.MsgInvalidUUID, map[string]any{
				"value": value,
			})
		}

		if _, ok := seen[id]; ok {
			continue
		}

		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	return ids, nil
}
