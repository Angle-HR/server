package app

import (
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/software78/fluvio"
	"github.com/software78/fluvio/fluviui"

	"github.com/Angle-HR/server/pkg/config"
)

const defaultFluvioUIOrigin = "http://localhost:5173"

// mountFluvioUI serves the Fluvio queue API at /fluvio/* for the Fluvio dashboard. The API lists job payloads and can
// pause, retry and delete jobs, so it requires basic auth (FLUVIO_BASIC_AUTH_USER / FLUVIO_BASIC_AUTH_PASSWORD).
// Without credentials it is mounted open only in development; anywhere else it is not mounted at all.
func mountFluvioUI(router chi.Router, cfg config.Config, client *fluvio.Client, log *slog.Logger) {
	if handler := fluvioUIHandler(cfg, client, log); handler != nil {
		router.Handle("/fluvio/*", handler)
	}
}

func fluvioUIHandler(cfg config.Config, client *fluvio.Client, log *slog.Logger) http.Handler {
	origin := cfg.FluvioUIOrigin
	if origin == "" {
		origin = defaultFluvioUIOrigin
	}
	opts := []fluviui.Option{fluviui.WithAllowedOrigins(origin)}

	switch {
	case cfg.FluvioBasicAuthUser != "" && cfg.FluvioBasicAuthPass != "":
		opts = append(opts, fluviui.WithMiddleware(basicAuth("fluvio", cfg.FluvioBasicAuthUser, cfg.FluvioBasicAuthPass)))
	case cfg.AppEnv == "development":
		log.Warn("fluvio UI API mounted without authentication (development only)")
	default:
		log.Warn("fluvio UI API not mounted: FLUVIO_BASIC_AUTH_USER and FLUVIO_BASIC_AUTH_PASSWORD are not set")
		return nil
	}
	return fluviui.Handler(client, opts...)
}

// basicAuth rejects requests without the given credentials. Both values are hashed before the constant-time
// comparison so neither their contents nor their lengths leak through timing.
func basicAuth(realm, user, password string) func(http.Handler) http.Handler {
	wantUser := sha256.Sum256([]byte(user))
	wantPass := sha256.Sum256([]byte(password))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUser, gotPass, ok := r.BasicAuth()
			u := sha256.Sum256([]byte(gotUser))
			p := sha256.Sum256([]byte(gotPass))
			match := subtle.ConstantTimeCompare(u[:], wantUser[:]) & subtle.ConstantTimeCompare(p[:], wantPass[:])
			if !ok || match != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`", charset="UTF-8"`)
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
