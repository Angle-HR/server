// Package identityhttp is the HTTP surface of the identity service: the product auth routes (/auth/*, org-invite
// acceptance), the admin console auth, users, staff, roles, audit and verification-queue routes (/admin/*), and the
// session handling they rely on. The same handlers are mounted by the monolith (unless IDENTITY_REMOTE=on) and by
// cmd/identity; see Mount.
package identityhttp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	fluvio "github.com/software78/fluvio"

	"github.com/Angle-HR/server/pkg/apperror"
)

const (
	regionSourceExplicit = "explicit"
	// regionSourcePending marks a users_registry row whose region is the global holding sentinel
	// (region.RegionGlobal) rather than a real, user-chosen deployment region.
	regionSourcePending = "pending"
)

// globalDB is the global database as the handlers use it.
type globalDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// jobEnqueuer requests an email inside the caller's transaction (Fluvio job or events outbox).
type jobEnqueuer interface {
	EnqueueTx(
		ctx context.Context,
		tx fluvio.Tx,
		args fluvio.JobArgs,
		opts ...fluvio.EnqueueOption,
	) (*fluvio.JobRow, error)
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

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var errUnavailable = apperror.New(apperror.CodeServiceUnavailable, "service temporarily unavailable, try again")
