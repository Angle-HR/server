package applicationsvc

import (
	"log/slog"
	"os"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/apphttp"
	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/apply/redislimit"
	"github.com/Angle-HR/server/internal/hiring/appstore"
	"github.com/Angle-HR/server/internal/hiring/reapply"
	"github.com/Angle-HR/server/internal/hiring/uploads"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/sensitive"
	"github.com/Angle-HR/server/pkg/config"
)

// Services are the application services of the process, built once and shared by the HTTP and gRPC servers.
type Services struct {
	// Applications returns a region's company-side service.
	Applications apphttp.RegionalApplications
	// Apply takes public applications; Uploads issues upload URLs (nil without a scanner).
	Apply   *apply.Service
	Uploads *uploads.Service
	// Lookup answers the questions the gRPC API asks that the HTTP routes never do.
	Lookup *StoreLookup
}

// ServicesDeps are the shared resources the services are built from.
type ServicesDeps struct {
	Config config.Config
	Router *dbrouter.DBRouter
	Jobs   appjobs.JobSource
	Redis  *goredis.Client
	Log    *slog.Logger
}

// NewServices builds the services the way the monolith does (internal/app/candidate.go), over d.Jobs.
func NewServices(d ServicesDeps) *Services {
	hasher := newReapplyHasher(d.Log)
	pool := func(reg string) (appstore.DB, error) {
		p, err := d.Router.DB(region.Region(reg))
		if err != nil {
			return nil, err
		}
		return p, nil
	}
	apps := &apply.Service{
		Jobs:      d.Jobs,
		Directory: applyDirectory{pool: pool},
		Limiter:   redislimit.New(d.Redis),
		Logger:    d.Log,
		Sealer:    newKeyring(d.Log),
		NoticeURL: noticeURL(config.NormalizePublicAPIURL(d.Config.PublicAPIURL)),
	}
	if hasher.Enabled() {
		apps.EmailHash = hasher.Hash
	}
	s := &Services{Apply: apps, Lookup: &StoreLookup{Pool: pool}}

	if scanners := regionScanners(); len(scanners) > 0 {
		up := &uploads.Service{
			Jobs:      d.Jobs,
			Directory: uploadDirectory{pool: pool, router: d.Router},
			Scanners:  scanners,
			Limiter:   redislimit.New(d.Redis),
			Logger:    d.Log,
		}
		apps.Uploads = up
		s.Uploads = up
	} else {
		d.Log.Warn("file uploads are off: neither CLAMAV_ADDR nor any ANGLEHR_<REGION>_CLAMAV_ADDR is set")
	}

	s.Applications = func(reg region.Region) (*applications.Service, error) {
		p, err := d.Router.DB(reg)
		if err != nil {
			return nil, err
		}
		jobs, err := d.Jobs.Regional(string(reg))
		if err != nil {
			return nil, err
		}
		store := &appstore.Store{DB: p, Jobs: jobs, Region: string(reg)}
		svc := &applications.Service{
			Store:     store,
			Pipeline:  store,
			Mail:      store,
			Schedule:  store,
			Jobs:      jobs,
			Hasher:    hasher,
			NoticeURL: noticeURL(config.NormalizePublicAPIURL(d.Config.PublicAPIURL)),
		}
		if files, ferr := d.Router.Blob(reg); ferr == nil {
			svc.Files = files
		} else {
			d.Log.Warn("application file downloads are off for a region", "region", string(reg), "error", ferr)
		}
		return svc, nil
	}
	return s
}

// applyDirectory finds the store of a region for the application service.
type applyDirectory struct {
	pool func(reg string) (appstore.DB, error)
}

func (d applyDirectory) Store(reg string) (apply.Store, error) {
	p, err := d.pool(reg)
	if err != nil {
		return nil, err
	}
	return &appstore.Store{DB: p, Region: reg}, nil
}

// uploadDirectory finds the database and the bucket of a region for the upload step.
type uploadDirectory struct {
	pool   func(reg string) (appstore.DB, error)
	router *dbrouter.DBRouter
}

func (d uploadDirectory) Store(reg string) (uploads.Store, error) {
	p, err := d.pool(reg)
	if err != nil {
		return nil, err
	}
	return &appstore.Store{DB: p, Region: reg}, nil
}

func (d uploadDirectory) Storage(reg string) (uploads.Storage, error) {
	return d.router.Blob(region.Region(reg))
}

var (
	_ apply.Directory   = applyDirectory{}
	_ uploads.Directory = uploadDirectory{}
)

// regionScanners is one clamd per region that has an address: ANGLEHR_<REGION>_CLAMAV_ADDR, else CLAMAV_ADDR.
func regionScanners() map[string]uploads.Scanner {
	fallback := strings.TrimSpace(os.Getenv("CLAMAV_ADDR"))
	scanners := map[string]uploads.Scanner{}
	for _, reg := range dbrouter.Regions() {
		addr := strings.TrimSpace(os.Getenv("ANGLEHR_" + strings.ToUpper(string(reg)) + "_CLAMAV_ADDR"))
		if addr == "" {
			addr = fallback
		}
		if addr != "" {
			scanners[string(reg)] = &uploads.ClamAV{Addr: addr}
		}
	}
	return scanners
}

// newKeyring reads ANGLEHR_<REGION>_SENSITIVE_KEYS, as the monolith does. A variable that cannot be read leaves
// every region without keys, so a typo fails closed.
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

// newReapplyHasher reads ANGLEHR_REAPPLY_HMAC_KEY (at least 32 characters). Without it the reapply block is
// unavailable.
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

// publicSiteURL is the origin that serves /j/{public_id}: PUBLIC_SITE_URL, else PUBLIC_API_URL.
func publicSiteURL(cfg config.Config) string {
	siteURL := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_SITE_URL")), "/")
	if siteURL == "" {
		siteURL = config.NormalizePublicAPIURL(cfg.PublicAPIURL)
	}
	return siteURL
}

// noticeURL points at the API origin: the applicant privacy notice is rendered by the server (versioned legal
// text), not by the frontend.
func noticeURL(apiURL string) func(publicID string) string {
	return func(publicID string) string { return apiURL + "/j/" + publicID + "/privacy" }
}
