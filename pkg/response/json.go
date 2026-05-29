// Package response writes JSON HTTP responses.
package response

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Angle-HR/server/pkg/apperror"
)

// Envelope is the top-level JSON shape for every API response.
type Envelope struct {
	Data  any        `json:"data,omitempty"`
	Error *ErrorBody `json:"error,omitempty"`
	Meta  *Meta      `json:"meta,omitempty"`
}

// ErrorBody is the structured error payload returned to clients.
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Meta carries response metadata such as the request identifier.
type Meta struct {
	RequestID  string `json:"request_id,omitempty"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    *bool  `json:"has_more,omitempty"`
}

// Success writes a JSON success response with the given status code.
func Success(w http.ResponseWriter, r *http.Request, status int, data any) {
	SuccessWithMeta(w, r, status, data, nil)
}

// SuccessWithMeta writes a JSON success response with optional pagination metadata.
func SuccessWithMeta(w http.ResponseWriter, r *http.Request, status int, data any, extra *Meta) {
	if data == nil && extra == nil {
		write(w, status, envelopeFromRequest(r))
		return
	}

	meta := mergeMeta(metaFromRequest(r), extra)
	write(w, status, Envelope{
		Data: data,
		Meta: meta,
	})
}

// Error writes a JSON error response for err.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	code, message := apperror.Public(err)

	write(w, apperror.HTTPStatus(err), Envelope{
		Error: &ErrorBody{
			Code:    code,
			Message: message,
			Details: apperror.PublicDetails(err),
		},
		Meta: metaFromRequest(r),
	})
}

func write(w http.ResponseWriter, status int, envelope Envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(envelope); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func envelopeFromRequest(r *http.Request) Envelope {
	return Envelope{
		Meta: metaFromRequest(r),
	}
}

func metaFromRequest(r *http.Request) *Meta {
	if r == nil {
		return nil
	}

	requestID := middleware.GetReqID(r.Context())
	if requestID == "" {
		return nil
	}

	return &Meta{RequestID: requestID}
}

func mergeMeta(base, extra *Meta) *Meta {
	if base == nil && extra == nil {
		return nil
	}

	merged := Meta{}
	if base != nil {
		merged = *base
	}

	if extra != nil {
		if extra.RequestID != "" {
			merged.RequestID = extra.RequestID
		}

		if extra.NextCursor != "" {
			merged.NextCursor = extra.NextCursor
		}

		if extra.HasMore != nil {
			merged.HasMore = extra.HasMore
		}
	}

	if merged.RequestID == "" && merged.NextCursor == "" && merged.HasMore == nil {
		return nil
	}

	return &merged
}
