package orghttp

import (
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/kyb/kybidentity"
	"github.com/Angle-HR/server/internal/kyb/kybnotify"
	"github.com/Angle-HR/server/internal/onboarding"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/config"
)

// EnvRemote, set to "on", stops the monolith serving the org routes (countries, onboarding, company verification,
// agreements and privacy contact). They are then served by cmd/org, and the edge (web-bff, UPSTREAM_ORG_URL) sends
// them there. Off by default: the monolith serves everything.
const EnvRemote = "ORG_REMOTE"

// Remote reports whether the org routes are served elsewhere.
func Remote() bool { return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvRemote)), "on") }

// Deps are what the org routes are built from. The monolith and cmd/org build the same handlers from them.
type Deps struct {
	Router   *dbrouter.DBRouter
	Global   *pgxpool.Pool
	Sessions *session.Manager
	// Enqueuer requests emails inside the handler's transaction (Fluvio client or the events enqueuer).
	Enqueuer jobEnqueuer
	Config   config.Config
	// Queue builds the KYB review queue of a region; nil uses the global database table (see kybidentity).
	Queue kybidentity.Factory
	// Company, when set, mounts the agreements and privacy-contact routes. The monolith leaves it nil: hiring
	// serves those paths there until ORG_REMOTE is on.
	Company  CompanyStore
	Identify Identify
}

// Handlers are the org HTTP handlers.
type Handlers struct {
	Countries  *CountriesHandler
	Product    *ProductOnboardingHandler
	Individual *IndividualOnboardingHandler
	Business   *BusinessOnboardingHandler
	KYB        *KYBHandler
	Company    *CompanyHandler
}

// New builds the handlers.
func New(d Deps) *Handlers {
	product := NewProductOnboardingHandler(d.Router, d.Global, d.Enqueuer, d.Sessions)
	WireAddressProviders(product, d.Config)
	product.QueueFor = d.Queue
	kybHandler := NewKYBHandler(d.Router, d.Global, NewKYBRegistry(d.Config))
	kybHandler.NotifierFor = NewKYBNotifierFactory(d.Router, d.Global, d.Enqueuer)
	kybHandler.Formats = onboarding.RegistrationNumberFormatOK
	kybHandler.QueueFor = d.Queue
	h := &Handlers{
		Countries:  NewCountriesHandler(d.Global),
		Product:    product,
		Individual: NewIndividualOnboardingHandler(d.Router, d.Global, d.Sessions),
		Business:   NewBusinessOnboardingHandler(d.Router, d.Global, d.Sessions),
		KYB:        kybHandler,
	}
	if d.Company != nil {
		h.Company = &CompanyHandler{Store: d.Company, Identify: d.Identify}
	}
	return h
}

// RegisterPublicRoutes mounts the routes that need no session.
func (h *Handlers) RegisterPublicRoutes(r chi.Router) {
	h.Countries.RegisterRoutes(r)
	h.Product.RegisterRoutes(r)
}

// RegisterProtectedRoutes mounts the routes that need a signed-in user; the caller applies authentication.
func (h *Handlers) RegisterProtectedRoutes(r chi.Router) {
	h.Product.RegisterProtectedRoutes(r)
	h.Individual.RegisterProtectedRoutes(r)
	h.Business.RegisterProtectedRoutes(r)
	h.KYB.RegisterProtectedRoutes(r)
	if h.Company != nil {
		h.Company.RegisterProtectedRoutes(r)
	}
}

// Mount mounts public routes and, behind requireAuth, the protected ones.
func (h *Handlers) Mount(r chi.Router, requireAuth func(http.Handler) http.Handler) {
	h.RegisterPublicRoutes(r)
	r.Group(func(r chi.Router) {
		if requireAuth != nil {
			r.Use(requireAuth)
		}
		h.RegisterProtectedRoutes(r)
	})
}

// NewKYBRegistry installs the automated company verifiers that are configured. A country without one is verified
// by a person (Tier 2), so the UK stays a manual review until COMPANIES_HOUSE_API_KEY is set. Czechia (ARES) and
// Finland (PRH) need no key and are always installed.
func NewKYBRegistry(cfg config.Config) *kyb.Registry {
	reg := kyb.NewRegistry()
	if cfg.CompaniesHouseAPIKey != "" {
		reg.Register("GB", &kyb.CompaniesHouse{APIKey: cfg.CompaniesHouseAPIKey})
	}
	reg.Register("CZ", &kyb.ARES{})
	reg.Register("FI", &kyb.PRH{})
	return reg
}

// NewKYBNotifierFactory returns the factory that builds a region's KYB email notifier. A region with no database
// yields no notifier, so verification still works without emails.
func NewKYBNotifierFactory(router *dbrouter.DBRouter, global *pgxpool.Pool, enqueuer kybnotify.Enqueuer) KYBNotifierFactory {
	return func(reg region.Region) kyb.Notifier {
		pool, err := router.DB(reg)
		if err != nil {
			return nil
		}
		return &kybnotify.Notifier{Regional: pool, Global: global, Enqueuer: enqueuer}
	}
}

// WireAddressProviders picks the address search and verification providers from config.
func WireAddressProviders(h *ProductOnboardingHandler, cfg config.Config) {
	google := onboarding.NewGoogleAddressProvider(cfg.GoogleMapsAPIKey)
	switch cfg.AddressVerifyMode {
	case "passthrough":
		h.AddressProvider = onboarding.PassthroughAddressVerifier{}
	case "google":
		h.AddressProvider = google
	}
	switch cfg.AddressSearchMode {
	case "passthrough":
		h.AddressSearcher = onboarding.PassthroughAddressSearcher{}
	case "google":
		h.AddressSearcher = google
	}
}
