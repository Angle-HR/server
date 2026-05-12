package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/waitlist/catalog"
	"github.com/Angle-HR/server/pkg/apperror"
)

const (
	onboardingStep1 = 1
	onboardingStep2 = 2
	onboardingStep3 = 3
	onboardingStep4 = 4
	onboardingStep5 = 5
)

type sessionRepository interface {
	Create(ctx context.Context) (string, *Record, error)
	FindByToken(ctx context.Context, token string) (*Record, error)
	SaveStep(ctx context.Context, sessionID int64, step int, state PartialState, nextStep int) error
	Submit(ctx context.Context, sessionID int64, state PartialState, input SubmitInput) (*SubmitResult, error)
}

type catalogLookup interface {
	ResolveIndustryIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]int64, int64, error)
	ResolveHiringToolIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]int64, int64, error)
	ResolveFrustrationIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]int64, int64, error)
	ResolveRoleID(ctx context.Context, id uuid.UUID) (int64, error)
	ResolveTeamSizeID(ctx context.Context, id uuid.UUID) (int64, error)
	UUIDsForIndustryIDs(ctx context.Context, ids []int64) (map[int64]uuid.UUID, error)
	UUIDsForHiringToolIDs(ctx context.Context, ids []int64) (map[int64]uuid.UUID, error)
	UUIDsForFrustrationIDs(ctx context.Context, ids []int64) (map[int64]uuid.UUID, error)
	UUIDForRoleID(ctx context.Context, id int64) (uuid.UUID, error)
	UUIDForTeamSizeID(ctx context.Context, id int64) (uuid.UUID, error)
}

// Service contains onboarding session business logic.
type Service struct {
	sessions sessionRepository
	catalog  catalogLookup
}

// NewService returns a session service.
func NewService(sessions sessionRepository, catalogRepo catalogLookup) *Service {
	return &Service{
		sessions: sessions,
		catalog:  catalogRepo,
	}
}

// CreateSession starts a new anonymous onboarding session.
func (s *Service) CreateSession(ctx context.Context) (token string, step int, expiresAt time.Time, err error) {
	token, record, err := s.sessions.Create(ctx)
	if err != nil {
		return "", 0, time.Time{}, fmt.Errorf("create session: %w", err)
	}

	return token, record.CurrentStep, record.ExpiresAt, nil
}

// PartialResponse is the client-facing saved progress.
type PartialResponse struct {
	IndustryIDs      []uuid.UUID `json:"industry_ids,omitempty"`
	OtherIndustry    *string     `json:"other_industry,omitempty"`
	ToolIDs          []uuid.UUID `json:"tool_ids,omitempty"`
	OtherTool        *string     `json:"other_tool,omitempty"`
	FrustrationIDs   []uuid.UUID `json:"frustration_ids,omitempty"`
	OtherFrustration *string     `json:"other_frustration,omitempty"`
	RoleID           *uuid.UUID  `json:"role_id,omitempty"`
	TeamSizeID       *uuid.UUID  `json:"team_size_id,omitempty"`
}

// GetSession returns the current step and partial progress.
func (s *Service) GetSession(ctx context.Context, token string) (int, PartialResponse, error) {
	record, err := s.loadActiveSession(ctx, token)
	if err != nil {
		return 0, PartialResponse{}, err
	}

	partial, err := s.hydratePartial(ctx, record.PartialState)
	if err != nil {
		return 0, PartialResponse{}, err
	}

	return record.CurrentStep, partial, nil
}

// SaveStep1 stores industry selections.
func (s *Service) SaveStep1(
	ctx context.Context,
	token string,
	industryIDs []uuid.UUID,
	otherIndustry *string,
) (int, error) {
	record, err := s.loadActiveSession(ctx, token)
	if err != nil {
		return 0, err
	}

	if stepErr := s.ensureCurrentStep(record, onboardingStep1); stepErr != nil {
		return 0, stepErr
	}

	resolved, otherID, err := s.catalog.ResolveIndustryIDs(ctx, industryIDs)
	if err != nil {
		return 0, apperror.New(apperror.CodeInvalidReference, apperror.MsgUnknownIndustryReference)
	}

	if err := validateOtherText(otherIndustry, containsID(resolved, otherID)); err != nil {
		return 0, err
	}

	state := record.PartialState
	state.Step1 = &Step1State{
		IndustryIDs:   mapValues(resolved),
		OtherIndustry: normalizeOptionalText(otherIndustry),
	}

	return s.saveStep(ctx, record, state, onboardingStep2)
}

// SaveStep2 stores hiring tool selections.
func (s *Service) SaveStep2(ctx context.Context, token string, toolIDs []uuid.UUID, otherTool *string) (int, error) {
	record, err := s.loadActiveSession(ctx, token)
	if err != nil {
		return 0, err
	}

	if stepErr := s.ensureCurrentStep(record, onboardingStep2); stepErr != nil {
		return 0, stepErr
	}

	resolved, otherID, err := s.catalog.ResolveHiringToolIDs(ctx, toolIDs)
	if err != nil {
		return 0, apperror.New(apperror.CodeInvalidReference, apperror.MsgUnknownHiringToolReference)
	}

	if err := validateOtherText(otherTool, containsID(resolved, otherID)); err != nil {
		return 0, err
	}

	state := record.PartialState
	state.Step2 = &Step2State{
		ToolIDs:   mapValues(resolved),
		OtherTool: normalizeOptionalText(otherTool),
	}

	return s.saveStep(ctx, record, state, onboardingStep3)
}

// SaveStep3 stores frustration selections.
func (s *Service) SaveStep3(
	ctx context.Context,
	token string,
	frustrationIDs []uuid.UUID,
	otherFrustration *string,
) (int, error) {
	record, err := s.loadActiveSession(ctx, token)
	if err != nil {
		return 0, err
	}

	if stepErr := s.ensureCurrentStep(record, onboardingStep3); stepErr != nil {
		return 0, stepErr
	}

	resolved, otherID, err := s.catalog.ResolveFrustrationIDs(ctx, frustrationIDs)
	if err != nil {
		return 0, apperror.New(apperror.CodeInvalidReference, apperror.MsgUnknownFrustrationReference)
	}

	if err := validateOtherText(otherFrustration, containsID(resolved, otherID)); err != nil {
		return 0, err
	}

	state := record.PartialState
	state.Step3 = &Step3State{
		FrustrationIDs:   mapValues(resolved),
		OtherFrustration: normalizeOptionalText(otherFrustration),
	}

	return s.saveStep(ctx, record, state, onboardingStep4)
}

// SaveStep4 stores role and team size selections.
func (s *Service) SaveStep4(ctx context.Context, token string, roleID, teamSizeID uuid.UUID) (int, error) {
	record, err := s.loadActiveSession(ctx, token)
	if err != nil {
		return 0, err
	}

	if stepErr := s.ensureCurrentStep(record, onboardingStep4); stepErr != nil {
		return 0, stepErr
	}

	roleInternalID, err := s.catalog.ResolveRoleID(ctx, roleID)
	if err != nil {
		return 0, apperror.New(apperror.CodeInvalidReference, apperror.MsgUnknownRoleReference)
	}

	teamSizeInternalID, err := s.catalog.ResolveTeamSizeID(ctx, teamSizeID)
	if err != nil {
		return 0, apperror.New(apperror.CodeInvalidReference, apperror.MsgUnknownTeamSizeReference)
	}

	state := record.PartialState
	state.Step4 = &Step4State{
		RoleID:     roleInternalID,
		TeamSizeID: teamSizeInternalID,
	}

	return s.saveStep(ctx, record, state, onboardingStep5)
}

// SubmitInput is the final onboarding payload.
type SubmitInput struct {
	Name             string
	WantsEarlyAccess bool
	WantsUserTesting bool
}

// SubmitResponse is the created submission summary.
type SubmitResponse struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// Submit finalizes the onboarding flow.
func (s *Service) Submit(ctx context.Context, token string, input SubmitInput) (*SubmitResponse, error) {
	record, err := s.loadActiveSession(ctx, token)
	if err != nil {
		return nil, err
	}

	if record.CurrentStep < onboardingStep5 {
		return nil, apperror.NewWithDetails(apperror.CodeValidationError, apperror.MsgOnboardingIncomplete, map[string]any{
			"current_step": record.CurrentStep,
		})
	}

	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, apperror.NewWithDetails(apperror.CodeValidationError, apperror.MsgNameRequired, map[string]any{
			"field": "name",
		})
	}

	result, err := s.sessions.Submit(ctx, record.ID, record.PartialState, SubmitInput{
		Name:             name,
		WantsEarlyAccess: input.WantsEarlyAccess,
		WantsUserTesting: input.WantsUserTesting,
	})
	if err != nil {
		return nil, fmt.Errorf("submit session: %w", err)
	}

	return &SubmitResponse{
		ID:          result.PublicID,
		Name:        result.Name,
		SubmittedAt: result.SubmittedAt,
	}, nil
}

func (s *Service) loadActiveSession(ctx context.Context, token string) (*Record, error) {
	record, err := s.sessions.FindByToken(ctx, token)
	if err != nil {
		return nil, err
	}

	if record.CompletedAt != nil {
		return nil, apperror.New(apperror.CodeSessionNotFound, apperror.MsgSessionNotFound)
	}

	if catalog.NowUTC().After(record.ExpiresAt) {
		return nil, apperror.New(apperror.CodeSessionExpired, apperror.MsgSessionExpired)
	}

	return record, nil
}

func (s *Service) ensureCurrentStep(record *Record, step int) error {
	if record.CurrentStep != step {
		return apperror.NewWithDetails(apperror.CodeValidationError, apperror.MsgUnexpectedOnboardingStep, map[string]any{
			"expected_step": record.CurrentStep,
			"received_step": step,
		})
	}

	return nil
}

func (s *Service) saveStep(ctx context.Context, record *Record, state PartialState, nextStep int) (int, error) {
	if err := s.sessions.SaveStep(ctx, record.ID, record.CurrentStep, state, nextStep); err != nil {
		return 0, fmt.Errorf("save step: %w", err)
	}

	return nextStep, nil
}

func (s *Service) hydratePartial(ctx context.Context, state PartialState) (PartialResponse, error) {
	var partial PartialResponse

	if state.Step1 != nil {
		uuids, err := s.catalog.UUIDsForIndustryIDs(ctx, state.Step1.IndustryIDs)
		if err != nil {
			return PartialResponse{}, fmt.Errorf("hydrate industries: %w", err)
		}

		partial.IndustryIDs = orderedUUIDs(state.Step1.IndustryIDs, uuids)
		partial.OtherIndustry = state.Step1.OtherIndustry
	}

	if state.Step2 != nil {
		uuids, err := s.catalog.UUIDsForHiringToolIDs(ctx, state.Step2.ToolIDs)
		if err != nil {
			return PartialResponse{}, fmt.Errorf("hydrate hiring tools: %w", err)
		}

		partial.ToolIDs = orderedUUIDs(state.Step2.ToolIDs, uuids)
		partial.OtherTool = state.Step2.OtherTool
	}

	if state.Step3 != nil {
		uuids, err := s.catalog.UUIDsForFrustrationIDs(ctx, state.Step3.FrustrationIDs)
		if err != nil {
			return PartialResponse{}, fmt.Errorf("hydrate frustrations: %w", err)
		}

		partial.FrustrationIDs = orderedUUIDs(state.Step3.FrustrationIDs, uuids)
		partial.OtherFrustration = state.Step3.OtherFrustration
	}

	if state.Step4 != nil {
		roleUUID, err := s.catalog.UUIDForRoleID(ctx, state.Step4.RoleID)
		if err != nil {
			return PartialResponse{}, fmt.Errorf("hydrate role: %w", err)
		}

		teamSizeUUID, err := s.catalog.UUIDForTeamSizeID(ctx, state.Step4.TeamSizeID)
		if err != nil {
			return PartialResponse{}, fmt.Errorf("hydrate team size: %w", err)
		}

		partial.RoleID = &roleUUID
		partial.TeamSizeID = &teamSizeUUID
	}

	return partial, nil
}

func validateOtherText(value *string, includesOther bool) error {
	trimmed := strings.TrimSpace(stringValue(value))
	if includesOther && trimmed == "" {
		return apperror.NewWithDetails(
			apperror.CodeValidationError,
			apperror.MsgOtherTextRequiredWhenOthersSelected,
			map[string]any{
				"field": "other",
			},
		)
	}

	if !includesOther && trimmed != "" {
		return apperror.NewWithDetails(
			apperror.CodeValidationError,
			apperror.MsgOtherTextOnlyAllowedWhenOthersSelected,
			map[string]any{
				"field": "other",
			},
		)
	}

	return nil
}

func containsID(resolved map[uuid.UUID]int64, otherID int64) bool {
	if otherID == 0 {
		return false
	}

	for _, internalID := range resolved {
		if internalID == otherID {
			return true
		}
	}

	return false
}

func mapValues(resolved map[uuid.UUID]int64) []int64 {
	values := make([]int64, 0, len(resolved))
	for _, value := range resolved {
		values = append(values, value)
	}

	return values
}

func orderedUUIDs(ids []int64, lookup map[int64]uuid.UUID) []uuid.UUID {
	uuids := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		uuids = append(uuids, lookup[id])
	}

	return uuids
}

func normalizeOptionalText(value *string) *string {
	if value == nil {
		return nil
	}

	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}

	return &trimmed
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}
