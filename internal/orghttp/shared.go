package orghttp

// Helpers shared with the identity half of the former internal/handler package. They are copies (the monolith's
// internal/handler keeps its own) so the two halves can be split and merged independently.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	fluvio "github.com/software78/fluvio"

	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/besteffort"
)

const regionSourceExplicit = "explicit"

type jobEnqueuer interface {
	EnqueueTx(
		ctx context.Context,
		tx fluvio.Tx,
		args fluvio.JobArgs,
		opts ...fluvio.EnqueueOption,
	) (*fluvio.JobRow, error)
}

type fieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func validationError(err error) error {
	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) || len(verrs) == 0 {
		return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequest)
	}

	fields := make([]fieldError, 0, len(verrs))
	for _, ve := range verrs {
		fields = append(fields, fieldError{
			Path:    jsonFieldName(ve.Field()),
			Message: validationMessage(ve),
		})
	}

	return apperror.NewWithDetails(
		apperror.CodeValidationError,
		apperror.MsgInvalidRequest,
		map[string]any{"fields": fields},
	)
}

// fieldEmail is both the JSON field name and the validator tag for email.
const fieldEmail = "email"

func jsonFieldName(structField string) string {
	switch structField {
	case "FullName":
		return "full_name"
	case "Email":
		return fieldEmail
	case "CountryID":
		return "country_id"
	default:
		return strings.ToLower(structField)
	}
}

func validationMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required", "required_if":
		return jsonFieldName(fe.Field()) + " is required"
	case fieldEmail:
		return "invalid email format"
	case "max":
		return fmt.Sprintf("%s exceeds maximum length", jsonFieldName(fe.Field()))
	case "uuid":
		return apperror.MsgInvalidCountryID
	default:
		return apperror.MsgInvalidRequest
	}
}

type accountUser struct {
	ID                    uuid.UUID
	Email                 string
	PasswordHash          string
	EmailVerifiedAt       *time.Time
	OnboardingCompletedAt *time.Time
	AccountType           *string
	FirstName             *string
	LastName              *string
	LegalFullName         *string
	CountryID             *uuid.UUID
	TOTPSecret            *string
	TOTPEnabledAt         *time.Time
}

func scanAccountUser(row pgx.Row) (accountUser, error) {
	var user accountUser
	err := row.Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.EmailVerifiedAt,
		&user.OnboardingCompletedAt,
		&user.AccountType,
		&user.FirstName,
		&user.LastName,
		&user.LegalFullName,
		&user.CountryID,
		&user.TOTPSecret,
		&user.TOTPEnabledAt,
	)
	return user, err
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func rollbackOnError(ctx context.Context, tx pgx.Tx) {
	besteffort.Log(ctx, "tx.Rollback", tx.Rollback(ctx))
}

// rotateIfMarked finishes a session rotation that a save step marked by setting marker. The marker carries the
// new region; rotating swaps the session id and CSRF token in the same response (new Set-Cookie).
func rotateIfMarked(w http.ResponseWriter, r *http.Request, mgr *session.Manager, marker **RegionReissue) error {
	if marker == nil || *marker == nil {
		return nil
	}
	if mgr == nil {
		return apperror.ErrInternal
	}
	ident, err := mgr.Rotate(w, r, (*marker).Region)
	if err != nil {
		return apperror.ErrInternal
	}
	(*marker).CSRFToken = ident.CSRFToken
	return nil
}
