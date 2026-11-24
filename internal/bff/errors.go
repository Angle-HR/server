package bff

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Angle-HR/server/pkg/response"
)

// writeError emits the repo's error envelope {"error":{code,message,details},"meta":{request_id}}.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string, retryAfterSecs int) {
	if retryAfterSecs > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSecs))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response.Envelope{
		Error: &response.ErrorBody{Code: code, Message: msg},
		Meta:  &response.Meta{RequestID: middleware.GetReqID(r.Context())},
	})
}
