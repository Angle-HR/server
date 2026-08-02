package logger

import (
	"log/slog"
	"net/http"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// RequestLogger logs method, path, status, bytes, duration, and request ID for every HTTP request.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)

			defer func() {
				log.Info("http request",
					"method", r.Method,
					"path", r.URL.Path,
					"query", r.URL.RawQuery,
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"duration_ms", time.Since(start).Milliseconds(),
					"request_id", chimiddleware.GetReqID(r.Context()),
					"remote", r.RemoteAddr,
					"user_agent", r.UserAgent(),
				)
			}()

			next.ServeHTTP(ww, r)
		})
	}
}
