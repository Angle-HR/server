package apperror

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

// BodyField is the field name used when a request body is wrong as a whole (not JSON, empty, or not an
// object) and no single field can be named.
const BodyField = "body"

var unknownFieldRE = regexp.MustCompile(`^json: unknown field "(.*)"$`)

// InvalidBody turns the error from decoding a request body into the API's validation error. Like every other
// validation error it names what is wrong in details.fields[].path, so a client can point at the field instead
// of showing a bare "invalid request body". err may be nil when the body decoded but was refused afterwards;
// the error then names the body as a whole.
func InvalidBody(err error) *AppError {
	path, msg := BodyField, "the request body must be a JSON object"

	var typeErr *json.UnmarshalTypeError
	var syntaxErr *json.SyntaxError
	switch {
	case err == nil:
		msg = "the request body is not valid"
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			path = typeErr.Field
		}
		msg = "must be " + jsonTypeName(typeErr.Type.String())
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		msg = "the request body is not valid JSON"
	case errors.Is(err, io.EOF):
		msg = "the request body is empty"
	default:
		if m := unknownFieldRE.FindStringSubmatch(err.Error()); m != nil {
			path, msg = m[1], "unknown field"
		}
	}
	return NewWithDetails(CodeValidationError, MsgInvalidRequestBody,
		map[string]any{"fields": []map[string]string{{"path": path, "message": msg}}})
}

// jsonTypeName says a Go kind the way a JSON client thinks of it.
func jsonTypeName(goType string) string {
	switch {
	case strings.HasPrefix(goType, "[]"):
		return "an array"
	case strings.HasPrefix(goType, "map["), strings.HasPrefix(goType, "struct"):
		return "an object"
	case goType == "bool":
		return "true or false"
	case goType == "string":
		return "a string"
	case strings.HasPrefix(goType, "int"), strings.HasPrefix(goType, "uint"), strings.HasPrefix(goType, "float"):
		return "a number"
	}
	return "a " + goType
}
