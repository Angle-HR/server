package app

import (
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/apply/redislimit"
	"github.com/Angle-HR/server/internal/hiring/hiringhttp"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/internal/hiring/reapply"
	"github.com/Angle-HR/server/internal/hiring/uploads"
	"github.com/Angle-HR/server/internal/hiring/uploads/r2"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/sensitive"
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
	siteURL := publicSiteURL(cfg)
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
	hasher := newReapplyHasher(log)
	app := &apply.Service{
		Registry:  g,
		Directory: hiringhttp.ApplyDirectory{Pool: pool},
		Limiter:   redislimit.New(rdb),
		Logger:    log,
		Sealer:    newKeyring(log),
		NoticeURL: noticeURL(siteURL),
	}
	if hasher.Enabled() {
		app.EmailHash = hasher.Hash
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

// newKeyring reads the keys that encrypt special category answers, ANGLEHR_<REGION>_SENSITIVE_KEYS, one variable per
// region. A region without keys cannot take special category answers: forms that ask for one answer 503 instead
// of storing the answer unencrypted. A variable that cannot be read is logged and leaves every region without
// keys, so a typo fails closed.
func newKeyring(log *slog.Logger) *sensitive.Keyring {
	var regions []string
	for _, r := range region.All() {
		regions = append(regions, string(r))
	}
	ring, err := sensitive.LoadFromEnv(regions, os.Getenv)
	if err != nil {
		log.Error("special category answers are off: a SENSITIVE_KEYS variable is invalid", "error", err)
		return sensitive.NewKeyring()
	}
	return ring
}

// newReapplyHasher reads ANGLEHR_REAPPLY_HMAC_KEY (at least 32 characters), the key behind the reapply block's
// email hashes. Without it the block is unavailable: it cannot be turned on and nothing is recorded.
func newReapplyHasher(log *slog.Logger) *reapply.Hasher {
	key := strings.TrimSpace(os.Getenv("ANGLEHR_REAPPLY_HMAC_KEY"))
	if key == "" {
		return nil
	}
	h, err := reapply.New(key)
	if err != nil {
		log.Error("reapply block is off: ANGLEHR_REAPPLY_HMAC_KEY is invalid", "error", err)
		return nil
	}
	return h
}

// publicSiteURL is the origin that serves /j/{public_id}.
func publicSiteURL(cfg config.Config) string {
	siteURL := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_SITE_URL")), "/")
	if siteURL == "" {
		siteURL = config.NormalizePublicAPIURL(cfg.PublicAPIURL)
	}
	return siteURL
}

func noticeURL(siteURL string) func(publicID string) string {
	return func(publicID string) string { return siteURL + "/j/" + publicID + "/privacy" }
}

// newApplicationsService serves the hiring team's applications routes: one service per region, over the region's
// database and bucket. Downloads need the bucket; without it the file route answers with an error and the rest
// still works.
func newApplicationsService(
	cfg config.Config, router *dbrouter.DBRouter, log *slog.Logger,
) hiringhttp.RegionalApplications {
	hasher := newReapplyHasher(log)
	siteURL := publicSiteURL(cfg)
	return func(reg region.Region) (*applications.Service, error) {
		p, err := router.DB(reg)
		if err != nil {
			return nil, err
		}
		svc := &applications.Service{
			Store:     &hiringstore.Store{DB: p},
			Hasher:    hasher,
			NoticeURL: noticeURL(siteURL),
		}
		if files, ferr := regionStorage(router, reg); ferr == nil {
			svc.Files = files
		} else {
			log.Warn("application file downloads are off for a region", "region", string(reg), "error", ferr)
		}
		return svc, nil
	}
}
