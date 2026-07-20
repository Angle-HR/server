package region

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// Middleware resolves the request region and stores it on the request context.
// It responds with 400 when the region is explicitly invalid or cannot be resolved,
// 413 when the request body exceeds the configured size limit, and 500 when an
// unexpected server-side error (e.g. DB failure) prevents resolution.
func (res *RegionResolver) Middleware() func(http.Handler) http.Handler {
	log := slog.Default()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			region, source, err := res.Resolve(r)
			if err != nil {
				if errors.Is(err, ErrInvalidRegion) {
					response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRegion))
					return
				}

				if errors.Is(err, ErrUnresolved) {
					response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgRegionRequired))
					return
				}

				// DB failures and other unexpected server errors must not surface as 400.
				var maxBytesErr *http.MaxBytesError
				if errors.As(err, &maxBytesErr) {
					response.Error(w, r, apperror.New(apperror.CodePayloadTooLarge, apperror.MsgRequestBodyTooLarge))
					return
				}

				// Client disconnected or timed out — no response needed.
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}

				response.Error(w, r, apperror.ErrInternal)
				return
			}

			log.Debug("region resolved",
				"region", region,
				"source", source,
				"path", r.URL.Path,
			)

			ctx := WithRegion(r.Context(), region, source)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
