// Package apperror defines application errors and HTTP status mapping.
package apperror

import (
	"errors"
	"net/http"
)

// Client-visible message strings used in API responses.
const (
	MsgNotFound                               = "not found"
	MsgEmailAlreadyRegistered                 = "email already registered"
	MsgInvalidRequest                         = "invalid request"
	MsgUnauthorized                           = "unauthorized"
	MsgForbidden                              = "forbidden"
	MsgInternalServerError                    = "internal server error"
	MsgInvalidRequestBody                     = "invalid request body"
	MsgRegionRequired                         = "region could not be resolved"
	MsgInvalidRegion                          = "invalid region"
	MsgInvalidCountryID                       = "invalid country_id"
	MsgInvalidWaitlistToken                   = "invalid waitlist token"
	MsgOnboardingAlreadySubmitted             = "onboarding already submitted"
	MsgUnknownIndustryReference               = "unknown industry reference"
	MsgUnknownHiringToolReference             = "unknown hiring tool reference"
	MsgUnknownFrustrationReference            = "unknown hiring frustration reference"
	MsgUnknownRoleReference                   = "unknown role reference"
	MsgUnknownTeamSizeReference               = "unknown team size reference"
	MsgOtherTextRequiredWhenOthersSelected    = "other text is required when Others is selected"
	MsgOtherTextOnlyAllowedWhenOthersSelected = "other text is only allowed when Others is selected"
	MsgNotImplemented                         = "endpoint not yet implemented"
)

var (
	// ErrNotFound indicates a missing resource.
	ErrNotFound = errors.New(MsgNotFound)
	// ErrConflict indicates a conflicting resource state.
	ErrConflict = errors.New(MsgEmailAlreadyRegistered)
	// ErrBadRequest indicates invalid client input.
	ErrBadRequest = errors.New(MsgInvalidRequest)
	// ErrInternal indicates an unexpected server failure.
	ErrInternal = errors.New(MsgInternalServerError)
	// ErrUnauthorized indicates missing or invalid credentials.
	ErrUnauthorized = errors.New(MsgUnauthorized)
	// ErrForbidden indicates insufficient permissions.
	ErrForbidden = errors.New(MsgForbidden)
)

// Stable application error codes.
const (
	CodeNotFound         = "NOT_FOUND"
	CodeConflict         = "CONFLICT"
	CodeValidationError  = "VALIDATION_ERROR"
	CodeUnauthorized     = "UNAUTHORIZED"
	CodeForbidden        = "FORBIDDEN"
	CodeInternalError    = "INTERNAL_ERROR"
	CodeInvalidReference = "INVALID_REFERENCE"
	CodeNotImplemented   = "NOT_IMPLEMENTED"
)

var httpStatusByCode = map[string]int{
	CodeNotFound:         http.StatusNotFound,
	CodeConflict:         http.StatusConflict,
	CodeValidationError:  http.StatusBadRequest,
	CodeUnauthorized:     http.StatusUnauthorized,
	CodeForbidden:        http.StatusForbidden,
	CodeInvalidReference: http.StatusBadRequest,
	CodeNotImplemented:   http.StatusNotImplemented,
}

// AppError is a structured application error.
type AppError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *AppError) Error() string {
	if e.Message != "" {
		return e.Message
	}

	return e.Code
}

// New returns an AppError with the given code and message.
func New(code, message string) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
	}
}

// NewWithDetails returns an AppError with field-level details.
func NewWithDetails(code, message string, details map[string]any) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		Details: details,
	}
}

// HTTPStatus maps an error to an HTTP status code.
func HTTPStatus(err error) int {
	var appErr *AppError
	if errors.As(err, &appErr) {
		if status, ok := httpStatusByCode[appErr.Code]; ok {
			return status
		}

		return HTTPStatus(mapAppErrorCode(appErr.Code))
	}

	return httpStatusForError(err)
}

func httpStatusForError(err error) int {
	switch {
	case errors.Is(err, ErrConflict):
		return http.StatusConflict
	case errors.Is(err, ErrBadRequest):
		return http.StatusBadRequest
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

func mapAppErrorCode(code string) error {
	switch code {
	case CodeNotFound:
		return ErrNotFound
	case CodeConflict:
		return ErrConflict
	case CodeValidationError:
		return ErrBadRequest
	case CodeUnauthorized:
		return ErrUnauthorized
	case CodeForbidden:
		return ErrForbidden
	default:
		return ErrInternal
	}
}

// Public maps an error to a stable client-facing code and message.
func Public(err error) (code, message string) {
	var appErr *AppError
	if errors.As(err, &appErr) {
		code = appErr.Code
		if code == "" {
			code = CodeInternalError
		}

		if appErr.Message != "" {
			return code, appErr.Message
		}

		return code, publicMessageForCode(code)
	}

	switch {
	case errors.Is(err, ErrConflict):
		return CodeConflict, MsgEmailAlreadyRegistered
	case errors.Is(err, ErrBadRequest):
		return CodeValidationError, MsgInvalidRequest
	case errors.Is(err, ErrNotFound):
		return CodeNotFound, MsgNotFound
	case errors.Is(err, ErrUnauthorized):
		return CodeUnauthorized, MsgUnauthorized
	case errors.Is(err, ErrForbidden):
		return CodeForbidden, MsgForbidden
	default:
		return CodeInternalError, MsgInternalServerError
	}
}

// PublicDetails returns optional field-level error details.
func PublicDetails(err error) map[string]any {
	var appErr *AppError
	if errors.As(err, &appErr) && len(appErr.Details) > 0 {
		return appErr.Details
	}

	return nil
}

func publicMessageForCode(code string) string {
	switch code {
	case CodeNotFound:
		return MsgNotFound
	case CodeConflict:
		return MsgEmailAlreadyRegistered
	case CodeValidationError:
		return MsgInvalidRequest
	case CodeUnauthorized:
		return MsgUnauthorized
	case CodeForbidden:
		return MsgForbidden
	case CodeNotImplemented:
		return MsgNotImplemented
	default:
		return MsgInternalServerError
	}
}
