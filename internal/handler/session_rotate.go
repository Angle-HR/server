package handler

import (
	"net/http"

	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/apperror"
)

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
