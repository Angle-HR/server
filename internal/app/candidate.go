package app

import (
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/apply/redislimit"
	"github.com/Angle-HR/server/internal/hiring/hiringhttp"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/internal/hiring/uploads"
	"github.com/Angle-HR/server/internal/hiring/uploads/r2"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/config"
)

// newPublicHandler builds what candidates reach without signing in: the job page and sitemap, the JSON for the
// apply page, and the application endpoint. Jobs are found through the global registry and read from the
// company's own region.
//
// PUBLIC_SITE_URL is the origin that serves /j/{public_id}; it falls back to PUBLIC_API_URL. Canonical links,
// share links and the sitemap are built from it.
//
// File uploads (CVs) are on only when CLAMAV_ADDR (host:port of a clamd) is set. Without a scanner the upload
// endpoint answers 503 and forms that ask for a file cannot be answered: an unscanned file is never accepted.
func newPublicHandler(
	cfg config.Config, router *dbrouter.DBRouter, global *pgxpool.Pool, rdb *goredis.Client, log *slog.Logger,
) *hiringhttp.PublicHandler {
	siteURL := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_SITE_URL")), "/")
	if siteURL == "" {
		siteURL = config.NormalizePublicAPIURL(cfg.PublicAPIURL)
	}
	pool := func(reg region.Region) (hiringstore.DB, error) {
		p, err := router.DB(reg)
		if err != nil {
			return nil, err
		}
		return p, nil
	}
	g := &hiringstore.Global{DB: global}
	pub := &public.Service{
		Registry:  g,
		Directory: hiringhttp.PublicDirectory{Pool: pool},
		Catalog:   g,
		SiteURL:   siteURL,
	}
	app := &apply.Service{
		Registry:  g,
		Directory: hiringhttp.ApplyDirectory{Pool: pool},
		Limiter:   redislimit.New(rdb),
		Logger:    log,
	}
	h := hiringhttp.NewPublicHandler(pub, app)

	if addr := strings.TrimSpace(os.Getenv("CLAMAV_ADDR")); addr != "" {
		limiter := redislimit.New(rdb)
		up := &uploads.Service{
			Registry: g,
			Directory: hiringhttp.UploadDirectory{
				Pool: pool,
				Bucket: func(reg string) (uploads.Storage, error) {
					return regionStorage(router, region.Region(reg))
				},
			},
			Scanner: &uploads.ClamAV{Addr: addr},
			Limiter: limiter,
			Logger:  log,
		}
		app.Uploads = up
		h.Uploads = up
	} else {
		log.Warn("file uploads are off: CLAMAV_ADDR is not set")
	}
	return h
}

// regionStorage is a region's bucket for uploads.
func regionStorage(router *dbrouter.DBRouter, reg region.Region) (uploads.Storage, error) {
	client, err := router.R2(reg)
	if err != nil {
		return nil, err
	}
	return r2.New(client, router.Bucket(reg))
}
