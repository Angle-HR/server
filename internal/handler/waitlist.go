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

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

const (
	regionalWaitlistInsertSQL = `
INSERT INTO waitlist (email, company_name, role, region, region_source, metadata)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (email) DO NOTHING
RETURNING id`

	globalRegistryInsertSQL = `
INSERT INTO users_registry (email, region, region_source)
VALUES ($1, $2, $3)
ON CONFLICT (email) DO NOTHING`
)

// globalRegistryDB executes writes against the global users_registry database.
type globalRegistryDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// WaitlistHandler handles waitlist signup requests.
type WaitlistHandler struct {
	Resolver *region.RegionResolver
	Router   *dbrouter.DBRouter
	GlobalDB globalRegistryDB
	validate *validator.Validate
}

// NewWaitlistHandler returns a waitlist signup handler.
func NewWaitlistHandler(
	resolver *region.RegionResolver,
	router *dbrouter.DBRouter,
	globalDB globalRegistryDB,
) *WaitlistHandler {
	return &WaitlistHandler{
		Resolver: resolver,
		Router:   router,
		GlobalDB: globalDB,
		validate: validator.New(),
	}
}

// RegisterRoutes mounts waitlist routes on r.
func (h *WaitlistHandler) RegisterRoutes(r chi.Router) {
	r.Post("/waitlist", h.handle)
}

type signupRequest struct {
	Email       string         `json:"email" validate:"required,email,max=254"`
	CompanyName string         `json:"company_name" validate:"required,max=120"`
	Role        string         `json:"role" validate:"omitempty,max=80"`
	Region      string         `json:"region" validate:"omitempty,oneof=uk us africa eu"`
	Metadata    map[string]any `json:"metadata"`
}

func (h *WaitlistHandler) handle(w http.ResponseWriter, r *http.Request) {
	reg, source, err := h.Resolver.Resolve(r)
	if err != nil {
		if errors.Is(err, region.ErrInvalidRegion) {
			response.Error(w, r, apperror.NewWithDetails(
				apperror.CodeValidationError,
				apperror.MsgInvalidRegion,
				map[string]any{"field": "region"},
			))
			return
		}

		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgRegionRequired))
		return
	}

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

	role := nullableString(req.Role)
	metadata, err := metadataBytes(req.Metadata)
	if err != nil {
		response.Error(w, r, fmt.Errorf("encode waitlist metadata: %w", err))
		return
	}

	if err := h.signup(r.Context(), reg, source, req.Email, req.CompanyName, role, metadata); err != nil {
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
		"region", reg,
		"region_source", source,
	)

	response.Success(w, r, http.StatusCreated, map[string]string{
		"message": "You're on the list!",
		"region":  string(reg),
	})
}

func (r *signupRequest) trim() {
	r.Email = strings.TrimSpace(r.Email)
	r.CompanyName = strings.TrimSpace(r.CompanyName)
	r.Role = strings.TrimSpace(r.Role)
	r.Region = strings.TrimSpace(r.Region)
}

func (h *WaitlistHandler) signup(
	ctx context.Context,
	reg region.Region,
	source string,
	email, companyName string,
	role *string,
	metadata []byte,
) error {
	pool, err := h.Router.DB(reg)
	if err != nil {
		return fmt.Errorf("regional pool: %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin waitlist transaction: %w", err)
	}
	defer rollbackWaitlistTx(ctx, tx)

	var id int64
	scanErr := tx.QueryRow(
		ctx,
		regionalWaitlistInsertSQL,
		email,
		companyName,
		role,
		string(reg),
		source,
		metadata,
	).Scan(&id)
	if scanErr != nil {
		if isDuplicateWaitlistSignup(scanErr) {
			return apperror.ErrConflict
		}

		return fmt.Errorf("insert regional waitlist: %w", scanErr)
	}

	registrySource := registryRegionSource(source)
	if _, err := h.GlobalDB.Exec(
		ctx,
		globalRegistryInsertSQL,
		email,
		string(reg),
		registrySource,
	); err != nil {
		return fmt.Errorf("insert users registry: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit waitlist transaction: %w", err)
	}

	return nil
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

func registryRegionSource(resolverSource string) string {
	switch resolverSource {
	case "jwt":
		return "jwt"
	case "subdomain":
		return "subdomain"
	case "db":
		return "db"
	case "param":
		return "explicit"
	case "geo":
		return "ip"
	default:
		return "inferred"
	}
}

func metadataBytes(metadata map[string]any) ([]byte, error) {
	if metadata == nil {
		return []byte("{}"), nil
	}

	return json.Marshal(metadata)
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}

	return &s
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
	case "Email":
		return "email"
	case "CompanyName":
		return "company_name"
	case "Role":
		return "role"
	case "Region":
		return "region"
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
	case "oneof":
		return apperror.MsgInvalidRegion
	default:
		return apperror.MsgInvalidRequest
	}
}
