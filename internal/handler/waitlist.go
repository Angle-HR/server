// Package handler exposes HTTP handlers for the API.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	fluvio "github.com/software78/fluvio"
)

var _ = apidoc.ErrorEnvelope{}

const regionSourceExplicit = "explicit"

type jobEnqueuer interface {
	EnqueueTx(ctx context.Context, tx fluvio.Tx, args fluvio.JobArgs, opts ...fluvio.EnqueueOption) (*fluvio.JobRow, error)
}

// WaitlistHandler handles waitlist signup requests.
type WaitlistHandler struct {
	Router   *dbrouter.DBRouter
	GlobalDB globalDB
	Enqueuer jobEnqueuer

	validate *validator.Validate
}

// NewWaitlistHandler returns a waitlist signup handler.
func NewWaitlistHandler(router *dbrouter.DBRouter, globalDB globalDB, enqueuer jobEnqueuer) *WaitlistHandler {
	return &WaitlistHandler{
		Router:   router,
		GlobalDB: globalDB,
		Enqueuer: enqueuer,
		validate: validator.New(),
	}
}

// RegisterRoutes mounts waitlist routes on r.
func (h *WaitlistHandler) RegisterRoutes(r chi.Router) {
	r.Post("/waitlist", h.handle)
}

type signupRequest struct {
	FullName  string `json:"full_name" validate:"required,max=120"`
	Email     string `json:"email" validate:"required,email,max=254"`
	CountryID string `json:"country_id" validate:"required,uuid"`
}

// handle godoc
//
//	@Summary		Join waitlist
//	@Description	Registers a user for the regional waitlist and global users registry.
//	@Tags			waitlist/signup
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.SignupRequest	true	"Signup payload"
//	@Success		201		{object}	handler.SignupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/waitlist [post]
func (h *WaitlistHandler) handle(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	req.trim()

	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	countryID, err := uuid.Parse(req.CountryID)
	if err != nil {
		response.Error(w, r, apperror.NewWithDetails(
			apperror.CodeValidationError,
			apperror.MsgInvalidCountryID,
			map[string]any{"field": "country_id"},
		))
		return
	}

	country, err := lookupCountry(r.Context(), h.GlobalDB, countryID)
	if err != nil {
		var appErr *apperror.AppError
		if errors.As(err, &appErr) {
			response.Error(w, r, err)
			return
		}

		response.Error(w, r, fmt.Errorf("lookup country: %w", err))
		return
	}

	waitlistToken, err := h.signup(r.Context(), country, req.FullName, req.Email)
	if err != nil {
		if errors.Is(err, apperror.ErrConflict) {
			response.Error(w, r, err)
			return
		}

		var appErr *apperror.AppError
		if errors.As(err, &appErr) {
			response.Error(w, r, err)
			return
		}

		response.Error(w, r, fmt.Errorf("waitlist signup: %w", err))
		return
	}

	slog.Info("waitlist signup",
		"email", maskEmail(req.Email),
		"region", country.Region,
		"country_id", country.ID,
	)

	response.Success(w, r, http.StatusCreated, map[string]string{
		"message": "You're on the list!",
		"region":  string(country.Region),
		"token":   waitlistToken.String(),
	})
}

func (r *signupRequest) trim() {
	r.FullName = strings.TrimSpace(r.FullName)
	r.Email = strings.TrimSpace(r.Email)
	r.CountryID = strings.TrimSpace(r.CountryID)
}

func (h *WaitlistHandler) signup(
	ctx context.Context,
	country Country,
	fullName, email string) (uuid.UUID, error) {
	pool, err := h.Router.DB(country.Region)
	if err != nil {
		return uuid.Nil, fmt.Errorf("regional pool: %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin waitlist transaction: %w", err)
	}
	defer rollbackWaitlistTx(ctx, tx)

	gtx, err := h.GlobalDB.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin global transaction: %w", err)
	}
	defer func() {
		_ = gtx.Rollback(ctx)
	}()

	var waitlistToken uuid.UUID
	insertSQL, insertArgs, err := query.InsertWaitlistSignup(
		fullName,
		email,
		country.ID,
		string(country.Region),
		regionSourceExplicit,
		[]byte("{}"),
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("build waitlist insert: %w", err)
	}

	scanErr := tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&waitlistToken)
	if scanErr != nil {
		if isDuplicateWaitlistSignup(scanErr) {
			return uuid.Nil, apperror.ErrConflict
		}

		return uuid.Nil, fmt.Errorf("insert regional waitlist: %w", scanErr)
	}

	registrySQL, registryArgs, err := query.InsertUsersRegistry(
		email,
		string(country.Region),
		regionSourceExplicit,
		waitlistToken,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("build users registry insert: %w", err)
	}

	if _, err := gtx.Exec(ctx, registrySQL, registryArgs...); err != nil {
		return uuid.Nil, fmt.Errorf("insert users registry: %w", err)
	}

	if h.Enqueuer != nil {
		_, err = h.Enqueuer.EnqueueTx(ctx, gtx, mailer.EmailArgs{
			Type:      "waitlist_confirmation",
			Recipient: email,
			FullName:  fullName,
			Token:     waitlistToken.String(),
		}, queue.DefaultEnqueueOptions()...)
		if err != nil {
			return uuid.Nil, fmt.Errorf("enqueue waitlist confirmation email: %w", err)
		}
	}

	if err := gtx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("commit global transaction: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("commit waitlist transaction: %w", err)
	}

	return waitlistToken, nil
}

func isDuplicateWaitlistSignup(err error) bool {
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

func rollbackWaitlistTx(ctx context.Context, tx pgx.Tx) {
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		slog.Warn("rollback waitlist transaction", "error", err)
	}
}

func maskEmail(email string) string {
	email = strings.TrimSpace(email)
	at := strings.LastIndex(email, "@")
	if at <= 0 || at >= len(email)-1 {
		return "***"
	}

	local := email[:at]
	domain := email[at+1:]

	first, _ := utf8.DecodeRuneInString(local)
	if first == utf8.RuneError {
		return "***@" + domain
	}

	return string(first) + "***@" + domain
}

func validationError(err error) error {
	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) || len(verrs) == 0 {
		return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequest)
	}

	ve := verrs[0]
	field := ve.Field()
	jsonField := jsonFieldName(field)

	return apperror.NewWithDetails(
		apperror.CodeValidationError,
		validationMessage(ve),
		map[string]any{"field": jsonField},
	)
}

func jsonFieldName(structField string) string {
	switch structField {
	case "FullName":
		return "full_name"
	case "Email":
		return "email"
	case "CountryID":
		return "country_id"
	default:
		return strings.ToLower(structField)
	}
}

func validationMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return jsonFieldName(fe.Field()) + " is required"
	case "email":
		return "invalid email format"
	case "max":
		return fmt.Sprintf("%s exceeds maximum length", jsonFieldName(fe.Field()))
	case "uuid":
		return apperror.MsgInvalidCountryID
	default:
		return apperror.MsgInvalidRequest
	}
}
