package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	goredis "github.com/redis/go-redis/v9"
	fluvio "github.com/software78/fluvio"

	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/besteffort"
)

// Admin login gets a longer window and lock than product login: fewer,
// higher-value accounts, so it's worth being stricter once triggered.
const (
	adminPasswordLockoutMaxAttempts = 5
	adminPasswordLockoutWindow      = 15 * time.Minute
	adminPasswordLockoutDuration    = 30 * time.Minute
)

// AdminHandler serves /api/v1/admin endpoints.
type AdminHandler struct {
	Store           *admin.Store
	Router          *dbrouter.DBRouter
	GlobalDB        globalDB
	Sessions        *session.Manager
	Jobs            *fluvio.Client
	Enqueuer        jobEnqueuer
	KYBNotifierFor  KYBNotifierFactory // optional, emails the owner when a review rejects
	PasswordLockout *auth.LoginLockout
	validate        *validator.Validate
}

// NewAdminHandler returns an admin API handler.
func NewAdminHandler(
	store *admin.Store,
	router *dbrouter.DBRouter,
	globalDB globalDB,
	redisClient *goredis.Client,
	sessions *session.Manager,
	jobs *fluvio.Client,
	enqueuer jobEnqueuer,
) *AdminHandler {
	return &AdminHandler{
		Store:    store,
		Router:   router,
		GlobalDB: globalDB,
		Sessions: sessions,
		Jobs:     jobs,
		Enqueuer: enqueuer,
		PasswordLockout: auth.NewLoginLockout(redisClient, "admin_pwd",
			adminPasswordLockoutMaxAttempts, adminPasswordLockoutWindow, adminPasswordLockoutDuration),
		validate: validator.New(),
	}
}

// RegisterProtectedRoutes mounts the authenticated admin routes this package still serves (catalogs, jobs). The
// identity routes (auth, users, staff, roles, audit, verification) moved to internal/identityhttp. Caller must apply
// RequireAdmin.
func (h *AdminHandler) RegisterProtectedRoutes(r chi.Router, mw *auth.AdminMiddleware) {
	r.With(mw.RequirePermission(admin.PermCatalogsRead)).Get("/catalogs/{type}", h.listCatalog)
	r.With(mw.RequirePermission(admin.PermCatalogsWrite)).Post("/catalogs/{type}", h.createCatalog)
	r.With(mw.RequirePermission(admin.PermCatalogsWrite)).Patch("/catalogs/{type}/{id}", h.patchCatalog)

	r.With(mw.RequirePermission(admin.PermJobsRead)).Get("/jobs", h.listJobs)
	r.With(mw.RequirePermission(admin.PermJobsRead)).Get("/jobs/{id}", h.getJob)
	r.With(mw.RequirePermission(admin.PermJobsWrite)).Post("/jobs/{id}/retry", h.retryJob)
}

func (h *AdminHandler) audit(r *http.Request, action, resourceType, resourceID string, meta map[string]any) {
	if h.Store == nil {
		return
	}
	actorID, _, _, ok := auth.AdminFromContext(r.Context())
	if !ok {
		return
	}
	if meta == nil {
		meta = map[string]any{}
	}
	besteffort.Log(r.Context(), "h.Store.WriteAudit",
		h.Store.WriteAudit(r.Context(), actorID, action, resourceType, resourceID, meta, r.RemoteAddr))
}
