// Package apidoc holds shared OpenAPI schema types for swag and Scalar.
package apidoc

// Meta is pagination and request metadata in API responses.
type Meta struct {
	RequestID  string `json:"request_id,omitempty" example:"abc123"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    *bool  `json:"has_more,omitempty"`
}

// ErrorBody is the structured error payload.
type ErrorBody struct {
	Code    string         `json:"code" example:"VALIDATION_ERROR"`
	Message string         `json:"message" example:"invalid request body"`
	Details map[string]any `json:"details,omitempty"`
}

// ErrorEnvelope is a failed API response.
type ErrorEnvelope struct {
	Error *ErrorBody `json:"error"`
	Meta  *Meta      `json:"meta,omitempty"`
}
