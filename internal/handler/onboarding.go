package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

const othersSlug = "others"

// OnboardingHandler handles waitlist onboarding form submission.
type OnboardingHandler struct {
	Router   *dbrouter.DBRouter
	GlobalDB globalDB
	Enqueuer jobEnqueuer
	validate *validator.Validate
}

// NewOnboardingHandler returns an onboarding handler.
func NewOnboardingHandler(router *dbrouter.DBRouter, globalDB globalDB, enqueuer jobEnqueuer) *OnboardingHandler {
	return &OnboardingHandler{
		Router:   router,
		GlobalDB: globalDB,
		Enqueuer: enqueuer,
		validate: validator.New(),
	}
}

// RegisterRoutes mounts onboarding routes on r.
func (h *OnboardingHandler) RegisterRoutes(r chi.Router) {
	r.Post("/waitlist/onboarding", h.submit)
}

type onboardingRequest struct {
	Token            string   `json:"token" validate:"required,uuid"`
	IndustryIDs      []string `json:"industry_ids" validate:"required,min=1,dive,uuid"`
	OtherIndustry    *string  `json:"other_industry"`
	ToolIDs          []string `json:"tool_ids" validate:"required,min=1,dive,uuid"`
	OtherTool        *string  `json:"other_tool"`
	FrustrationIDs   []string `json:"frustration_ids" validate:"required,min=1,dive,uuid"`
	OtherFrustration *string  `json:"other_frustration"`
	RoleID           string   `json:"role_id" validate:"required,uuid"`
	TeamSizeID       string   `json:"team_size_id" validate:"required,uuid"`
	WantsEarlyAccess bool     `json:"wants_early_access"`
	WantsUserTesting bool     `json:"wants_user_testing"`
}

// submit godoc
//
//	@Summary		Submit waitlist onboarding form
//	@Description	Saves the full onboarding form for a waitlist signup token.
//	@Tags			waitlist/onboarding
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.OnboardingRequest	true	"Onboarding payload"
//	@Success		201		{object}	handler.OnboardingEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/waitlist/onboarding [post]
func (h *OnboardingHandler) submit(w http.ResponseWriter, r *http.Request) {
	var req onboardingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	req.trim()

	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	token, err := uuid.Parse(req.Token)
	if err != nil {
		response.Error(w, r, apperror.NewWithDetails(
			apperror.CodeValidationError,
			apperror.MsgInvalidWaitlistToken,
			map[string]any{"field": "token"},
		))
		return
	}

	if err := h.processSubmit(r.Context(), req, token); err != nil {
		var appErr *apperror.AppError
		if errors.As(err, &appErr) {
			response.Error(w, r, err)
			return
		}

		response.Error(w, r, fmt.Errorf("onboarding submit: %w", err))
		return
	}

	response.Success(w, r, http.StatusCreated, map[string]string{
		"message": "Thanks for telling us more!",
	})
}

func (r *onboardingRequest) trim() {
	r.Token = strings.TrimSpace(r.Token)
	r.RoleID = strings.TrimSpace(r.RoleID)
	r.TeamSizeID = strings.TrimSpace(r.TeamSizeID)
	r.OtherIndustry = trimOptionalString(r.OtherIndustry)
	r.OtherTool = trimOptionalString(r.OtherTool)
	r.OtherFrustration = trimOptionalString(r.OtherFrustration)
}

func (h *OnboardingHandler) processSubmit(ctx context.Context, req onboardingRequest, token uuid.UUID) error {
	registryEmail, reg, err := h.lookupRegistry(ctx, token)
	if err != nil {
		return err
	}

	pool, err := h.Router.DB(reg)
	if err != nil {
		return fmt.Errorf("regional pool: %w", err)
	}

	waitlistID, fullName, waitlistEmail, submitted, err := h.lookupWaitlist(ctx, pool, token)
	if err != nil {
		return err
	}

	if submitted {
		return apperror.New(apperror.CodeConflict, apperror.MsgOnboardingAlreadySubmitted)
	}

	if !strings.EqualFold(strings.TrimSpace(waitlistEmail), strings.TrimSpace(registryEmail)) {
		return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidWaitlistToken)
	}

	industryIDs, err := parseUUIDList(req.IndustryIDs)
	if err != nil {
		return err
	}

	toolIDs, err := parseUUIDList(req.ToolIDs)
	if err != nil {
		return err
	}

	frustrationIDs, err := parseUUIDList(req.FrustrationIDs)
	if err != nil {
		return err
	}

	roleID, err := uuid.Parse(req.RoleID)
	if err != nil {
		return apperror.New(apperror.CodeValidationError, apperror.MsgUnknownRoleReference)
	}

	teamSizeID, err := uuid.Parse(req.TeamSizeID)
	if err != nil {
		return apperror.New(apperror.CodeValidationError, apperror.MsgUnknownTeamSizeReference)
	}

	if err := h.validateCatalogSelections(
		ctx,
		industryIDs,
		toolIDs,
		frustrationIDs,
		roleID,
		teamSizeID,
		req.OtherIndustry,
		req.OtherTool,
		req.OtherFrustration,
	); err != nil {
		return err
	}

	return h.persistOnboarding(ctx, pool, waitlistID, fullName, waitlistEmail, industryIDs, toolIDs, frustrationIDs, roleID, teamSizeID,
		req.OtherIndustry, req.OtherTool, req.OtherFrustration, req.WantsEarlyAccess, req.WantsUserTesting)
}

func (h *OnboardingHandler) lookupRegistry(ctx context.Context, token uuid.UUID) (string, region.Region, error) {
	sql, args, err := query.LookupUsersRegistryByWaitlistToken(token)
	if err != nil {
		return "", "", fmt.Errorf("build registry lookup: %w", err)
	}

	var email string
	var reg string

	scanErr := h.GlobalDB.QueryRow(ctx, sql, args...).Scan(&email, &reg)
	if scanErr != nil {
		if isNotFound(scanErr) {
			return "", "", apperror.New(apperror.CodeNotFound, apperror.MsgInvalidWaitlistToken)
		}

		return "", "", scanErr
	}

	if !region.Valid(region.Region(reg)) {
		return "", "", apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRegion)
	}

	return email, region.Region(reg), nil
}

func (h *OnboardingHandler) lookupWaitlist(ctx context.Context, pool dbrouter.PgxPool, token uuid.UUID) (int64, string, string, bool, error) {
	sql, args, err := query.LookupWaitlistByUUID(token)
	if err != nil {
		return 0, "", "", false, fmt.Errorf("build waitlist lookup: %w", err)
	}

	var id int64
	var fullName string
	var email string
	var submittedAt *time.Time

	scanErr := pool.QueryRow(ctx, sql, args...).Scan(&id, &fullName, &email, &submittedAt)
	if scanErr != nil {
		if isNotFound(scanErr) {
			return 0, "", "", false, apperror.New(apperror.CodeNotFound, apperror.MsgInvalidWaitlistToken)
		}

		return 0, "", "", false, scanErr
	}

	return id, fullName, email, submittedAt != nil, nil
}

func (h *OnboardingHandler) validateCatalogSelections(
	ctx context.Context,
	industryIDs, toolIDs, frustrationIDs []uuid.UUID,
	roleID, teamSizeID uuid.UUID,
	otherIndustry, otherTool, otherFrustration *string,
) error {
	industries, err := h.loadSlugMap(ctx, query.ActiveIndustriesByIDs, industryIDs, apperror.MsgUnknownIndustryReference)
	if err != nil {
		return err
	}

	if otherErr := validateOtherText(otherIndustry, slugSelected(industries, othersSlug)); otherErr != nil {
		return otherErr
	}

	tools, err := h.loadSlugMap(ctx, query.ActiveHiringToolsByIDs, toolIDs, apperror.MsgUnknownHiringToolReference)
	if err != nil {
		return err
	}

	if otherErr := validateOtherText(otherTool, slugSelected(tools, othersSlug)); otherErr != nil {
		return otherErr
	}

	frustrations, err := h.loadSlugMap(ctx, query.ActiveHiringFrustrationsByIDs, frustrationIDs, apperror.MsgUnknownFrustrationReference)
	if err != nil {
		return err
	}

	if otherErr := validateOtherText(otherFrustration, slugSelected(frustrations, othersSlug)); otherErr != nil {
		return otherErr
	}

	roleSQL, roleArgs, err := query.ActiveRoleByID(roleID)
	if err != nil {
		return fmt.Errorf("build role lookup: %w", err)
	}

	var resolvedRoleID uuid.UUID
	var roleSlugDiscard string
	if roleErr := h.GlobalDB.QueryRow(ctx, roleSQL, roleArgs...).Scan(&resolvedRoleID, &roleSlugDiscard); roleErr != nil {
		if isNotFound(roleErr) {
			return apperror.New(apperror.CodeInvalidReference, apperror.MsgUnknownRoleReference)
		}

		return roleErr
	}

	teamSQL, teamArgs, err := query.TeamSizeByID(teamSizeID)
	if err != nil {
		return fmt.Errorf("build team size lookup: %w", err)
	}

	var resolvedTeamID uuid.UUID
	if teamErr := h.GlobalDB.QueryRow(ctx, teamSQL, teamArgs...).Scan(&resolvedTeamID); teamErr != nil {
		if isNotFound(teamErr) {
			return apperror.New(apperror.CodeInvalidReference, apperror.MsgUnknownTeamSizeReference)
		}

		return teamErr
	}

	return nil
}

type catalogQueryFn func([]uuid.UUID) (string, []any, error)

func (h *OnboardingHandler) loadSlugMap(
	ctx context.Context,
	build catalogQueryFn,
	ids []uuid.UUID,
	unknownMsg string,
) (map[uuid.UUID]string, error) {
	sql, args, err := build(ids)
	if err != nil {
		return nil, fmt.Errorf("build catalog lookup: %w", err)
	}

	rows, err := h.GlobalDB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	found := make(map[uuid.UUID]string, len(ids))
	for rows.Next() {
		var id uuid.UUID
		var slug string
		if err := rows.Scan(&id, &slug); err != nil {
			return nil, err
		}

		found[id] = slug
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(found) != len(ids) {
		return nil, apperror.New(apperror.CodeInvalidReference, unknownMsg)
	}

	return found, nil
}

func (h *OnboardingHandler) persistOnboarding(
	ctx context.Context,
	pool dbrouter.PgxPool,
	waitlistID int64,
	fullName, email string,
	industryIDs, toolIDs, frustrationIDs []uuid.UUID,
	roleID, teamSizeID uuid.UUID,
	otherIndustry, otherTool, otherFrustration *string,
	wantsEarlyAccess, wantsUserTesting bool,
) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin onboarding transaction: %w", err)
	}
	defer rollbackWaitlistTx(ctx, tx)

	gtx, err := h.GlobalDB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin global transaction: %w", err)
	}
	defer func() {
		_ = gtx.Rollback(ctx)
	}()

	updateSQL, updateArgs, err := query.SubmitWaitlistOnboarding(
		waitlistID, wantsEarlyAccess, wantsUserTesting, roleID, teamSizeID,
	)
	if err != nil {
		return fmt.Errorf("build onboarding update: %w", err)
	}

	var updatedID int64
	scanErr := tx.QueryRow(ctx, updateSQL, updateArgs...).Scan(&updatedID)
	if scanErr != nil {
		if isNotFound(scanErr) {
			return apperror.New(apperror.CodeConflict, apperror.MsgOnboardingAlreadySubmitted)
		}

		return fmt.Errorf("update waitlist onboarding: %w", scanErr)
	}

	if err := h.insertIndustries(ctx, tx, waitlistID, industryIDs, otherIndustry); err != nil {
		return err
	}

	if err := h.insertHiringTools(ctx, tx, waitlistID, toolIDs, otherTool); err != nil {
		return err
	}

	if err := h.insertFrustrations(ctx, tx, waitlistID, frustrationIDs, otherFrustration); err != nil {
		return err
	}

	if h.Enqueuer != nil {
		_, err = h.Enqueuer.EnqueueTx(ctx, gtx, mailer.EmailArgs{
			Type:      "more_info_ack",
			Recipient: email,
			FullName:  fullName,
		}, queue.EmailEnqueueOptions()...)
		if err != nil {
			return fmt.Errorf("enqueue onboarding acknowledgement email: %w", err)
		}
	}

	if err := gtx.Commit(ctx); err != nil {
		return fmt.Errorf("commit global transaction: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit onboarding transaction: %w", err)
	}

	return nil
}

func (h *OnboardingHandler) insertIndustries(
	ctx context.Context,
	tx pgx.Tx,
	waitlistID int64,
	ids []uuid.UUID,
	otherText *string,
) error {
	slugs, err := h.loadSlugMap(ctx, query.ActiveIndustriesByIDs, ids, apperror.MsgUnknownIndustryReference)
	if err != nil {
		return err
	}

	for _, id := range ids {
		var text *string
		if slugs[id] == othersSlug {
			text = otherText
		}

		sql, args, err := query.InsertWaitlistIndustry(waitlistID, id, text)
		if err != nil {
			return fmt.Errorf("build industry insert: %w", err)
		}

		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("insert waitlist industry: %w", err)
		}
	}

	return nil
}

func (h *OnboardingHandler) insertHiringTools(
	ctx context.Context,
	tx pgx.Tx,
	waitlistID int64,
	ids []uuid.UUID,
	otherText *string,
) error {
	slugs, err := h.loadSlugMap(ctx, query.ActiveHiringToolsByIDs, ids, apperror.MsgUnknownHiringToolReference)
	if err != nil {
		return err
	}

	for _, id := range ids {
		var text *string
		if slugs[id] == othersSlug {
			text = otherText
		}

		sql, args, err := query.InsertWaitlistHiringTool(waitlistID, id, text)
		if err != nil {
			return fmt.Errorf("build hiring tool insert: %w", err)
		}

		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("insert waitlist hiring tool: %w", err)
		}
	}

	return nil
}

func (h *OnboardingHandler) insertFrustrations(
	ctx context.Context,
	tx pgx.Tx,
	waitlistID int64,
	ids []uuid.UUID,
	otherText *string,
) error {
	slugs, err := h.loadSlugMap(ctx, query.ActiveHiringFrustrationsByIDs, ids, apperror.MsgUnknownFrustrationReference)
	if err != nil {
		return err
	}

	for _, id := range ids {
		var text *string
		if slugs[id] == othersSlug {
			text = otherText
		}

		sql, args, err := query.InsertWaitlistFrustration(waitlistID, id, text)
		if err != nil {
			return fmt.Errorf("build frustration insert: %w", err)
		}

		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("insert waitlist frustration: %w", err)
		}
	}

	return nil
}

func parseUUIDList(values []string) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		id, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return nil, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequest)
		}

		ids = append(ids, id)
	}

	return ids, nil
}

func validateOtherText(value *string, includesOther bool) error {
	trimmed := stringValue(value)
	if includesOther && trimmed == "" {
		return apperror.NewWithDetails(
			apperror.CodeValidationError,
			apperror.MsgOtherTextRequiredWhenOthersSelected,
			map[string]any{"field": "other"},
		)
	}

	if !includesOther && trimmed != "" {
		return apperror.NewWithDetails(
			apperror.CodeValidationError,
			apperror.MsgOtherTextOnlyAllowedWhenOthersSelected,
			map[string]any{"field": "other"},
		)
	}

	return nil
}

func slugSelected(slugs map[uuid.UUID]string, slug string) bool {
	for _, s := range slugs {
		if s == slug {
			return true
		}
	}

	return false
}

func trimOptionalString(value *string) *string {
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

func isNotFound(err error) bool {
	for err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return true
		}

		if strings.Contains(strings.ToLower(err.Error()), "no rows") {
			return true
		}

		err = errors.Unwrap(err)
	}

	return false
}
