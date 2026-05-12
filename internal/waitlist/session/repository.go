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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/db/sqlc"
	"github.com/Angle-HR/server/internal/waitlist/catalog"
	"github.com/Angle-HR/server/pkg/apperror"
)

const sessionTTL = 7 * 24 * time.Hour
const sessionTokenBytes = 32

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
	pool    *pgxpool.Pool
	queries *sqlc.Queries
}

// NewRepository returns a PostgreSQL-backed session repository.
func NewRepository(pool *pgxpool.Pool, queries *sqlc.Queries) *Repository {
	return &Repository{pool: pool, queries: queries}
}

// Create inserts a new session and returns the raw token.
func (r *Repository) Create(ctx context.Context) (string, *Record, error) {
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}

	expiresAt := catalog.NowUTC().Add(sessionTTL)
	row, err := r.queries.CreateSubmissionSession(ctx, sqlc.CreateSubmissionSessionParams{
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return "", nil, fmt.Errorf("insert session: %w", err)
	}

	record, err := recordFromSessionRow(row.ID, row.CurrentStep, row.ExpiresAt, row.PartialState, row.CompletedAt)
	if err != nil {
		return "", nil, err
	}

	return token, record, nil
}

// FindByToken loads a session by raw token.
func (r *Repository) FindByToken(ctx context.Context, token string) (*Record, error) {
	row, err := r.queries.GetSubmissionSessionByToken(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperror.New(apperror.CodeSessionNotFound, apperror.MsgSessionNotFound)
		}

		return nil, fmt.Errorf("find session: %w", err)
	}

	return recordFromSessionRow(row.ID, row.CurrentStep, row.ExpiresAt, row.PartialState, row.CompletedAt)
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

	rowsAffected, err := r.queries.UpdateSubmissionSessionStep(ctx, sqlc.UpdateSubmissionSessionStepParams{
		ID:          sessionID,
		Column2:     rawState,
		CurrentStep: currentStep,
	})
	if err != nil {
		return fmt.Errorf("save session step: %w", err)
	}

	if rowsAffected == 0 {
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

	qtx := r.queries.WithTx(tx)

	submission, err := qtx.InsertWaitlistSubmission(ctx, sqlc.InsertWaitlistSubmissionParams{
		Name:             input.Name,
		WantsEarlyAccess: input.WantsEarlyAccess,
		WantsUserTesting: input.WantsUserTesting,
		RoleID:           state.Step4.RoleID,
		TeamSizeID:       state.Step4.TeamSizeID,
	})
	if err != nil {
		return nil, fmt.Errorf("insert submission: %w", err)
	}

	if insertErr := insertIndustryRows(
		ctx,
		qtx,
		submission.ID,
		state.Step1.IndustryIDs,
		state.Step1.OtherIndustry,
	); insertErr != nil {
		return nil, insertErr
	}

	if insertErr := insertHiringToolRows(
		ctx,
		qtx,
		submission.ID,
		state.Step2.ToolIDs,
		state.Step2.OtherTool,
	); insertErr != nil {
		return nil, insertErr
	}

	if insertErr := insertFrustrationRows(
		ctx,
		qtx,
		submission.ID,
		state.Step3.FrustrationIDs,
		state.Step3.OtherFrustration,
	); insertErr != nil {
		return nil, insertErr
	}

	rowsAffected, err := qtx.CompleteSubmissionSession(ctx, sqlc.CompleteSubmissionSessionParams{
		ID:           sessionID,
		SubmissionID: &submission.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("complete session: %w", err)
	}

	if rowsAffected == 0 {
		return nil, apperror.New(apperror.CodeConflict, apperror.MsgSessionAlreadySubmitted)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit submit transaction: %w", err)
	}

	return &SubmitResult{
		PublicID:    submission.Uuid,
		Name:        input.Name,
		SubmittedAt: submission.SubmittedAt,
	}, nil
}

func recordFromSessionRow(
	id int64,
	currentStep int16,
	expiresAt time.Time,
	rawState []byte,
	completedAt pgtype.Timestamptz,
) (*Record, error) {
	var state PartialState
	if err := json.Unmarshal(rawState, &state); err != nil {
		return nil, fmt.Errorf("decode session state: %w", err)
	}

	var completed *time.Time
	if completedAt.Valid {
		value := completedAt.Time
		completed = &value
	}

	return &Record{
		ID:           id,
		CurrentStep:  int(currentStep),
		ExpiresAt:    expiresAt,
		CompletedAt:  completed,
		PartialState: state,
	}, nil
}

func insertIndustryRows(
	ctx context.Context,
	q *sqlc.Queries,
	submissionID int64,
	ids []int64,
	otherText *string,
) error {
	var otherID int64
	if otherText != nil {
		value, err := q.GetOtherIndustryID(ctx)
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

		if err := q.InsertSubmissionIndustry(ctx, sqlc.InsertSubmissionIndustryParams{
			SubmissionID: submissionID,
			IndustryID:   id,
			OtherText:    text,
		}); err != nil {
			return fmt.Errorf("insert submission_industries row: %w", err)
		}
	}

	return nil
}

func insertHiringToolRows(
	ctx context.Context,
	q *sqlc.Queries,
	submissionID int64,
	ids []int64,
	otherText *string,
) error {
	var otherID int64
	if otherText != nil {
		value, err := q.GetOtherHiringToolID(ctx)
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

		if err := q.InsertSubmissionHiringTool(ctx, sqlc.InsertSubmissionHiringToolParams{
			SubmissionID: submissionID,
			HiringToolID: id,
			OtherText:    text,
		}); err != nil {
			return fmt.Errorf("insert submission_hiring_tools row: %w", err)
		}
	}

	return nil
}

func insertFrustrationRows(
	ctx context.Context,
	q *sqlc.Queries,
	submissionID int64,
	ids []int64,
	otherText *string,
) error {
	var otherID int64
	if otherText != nil {
		value, err := q.GetOtherFrustrationID(ctx)
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

		if err := q.InsertSubmissionFrustration(ctx, sqlc.InsertSubmissionFrustrationParams{
			SubmissionID:  submissionID,
			FrustrationID: id,
			OtherText:     text,
		}); err != nil {
			return fmt.Errorf("insert submission_frustrations row: %w", err)
		}
	}

	return nil
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
