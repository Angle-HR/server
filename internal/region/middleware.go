package region

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// Middleware resolves the request region and stores it on the request context.
// It responds with 400 when region cannot be resolved or is explicitly invalid.
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

				response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgRegionRequired))
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
