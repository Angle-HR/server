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
	Tokens          *auth.TokenService
	Jobs            *fluvio.Client
	Enqueuer        jobEnqueuer
	PasswordLockout *auth.LoginLockout
	validate        *validator.Validate
}

// NewAdminHandler returns an admin API handler.
func NewAdminHandler(
	store *admin.Store,
	router *dbrouter.DBRouter,
	globalDB globalDB,
	redisClient *goredis.Client,
	tokens *auth.TokenService,
	jobs *fluvio.Client,
	enqueuer jobEnqueuer,
) *AdminHandler {
	return &AdminHandler{
		Store:    store,
		Router:   router,
		GlobalDB: globalDB,
		Tokens:   tokens,
		Jobs:     jobs,
		Enqueuer: enqueuer,
		PasswordLockout: auth.NewLoginLockout(redisClient, "admin_pwd",
			adminPasswordLockoutMaxAttempts, adminPasswordLockoutWindow, adminPasswordLockoutDuration),
		validate: validator.New(),
	}
}

// RegisterPublicRoutes mounts unauthenticated admin auth routes.
func (h *AdminHandler) RegisterPublicRoutes(r chi.Router) {
	r.Post("/auth/login", h.login)
	r.Post("/auth/refresh", h.refresh)
	r.Get("/auth/invite/{token}", h.getInvite)
	r.Post("/auth/accept-invite", h.acceptInvite)
}

// RegisterProtectedRoutes mounts authenticated admin routes. Caller must apply RequireAdmin.
func (h *AdminHandler) RegisterProtectedRoutes(r chi.Router, mw *auth.AdminMiddleware) {
	r.Get("/auth/me", h.me)

	r.With(mw.RequirePermission(admin.PermWaitlistRead)).Get("/waitlist", h.listWaitlist)
	r.With(mw.RequirePermission(admin.PermWaitlistRead)).Get("/waitlist/{uuid}", h.getWaitlist)
	r.With(mw.RequirePermission(admin.PermWaitlistWrite)).Patch("/waitlist/{uuid}", h.patchWaitlist)
	r.With(mw.RequirePermission(admin.PermWaitlistWrite)).Delete("/waitlist/{uuid}", h.deleteWaitlist)
	r.With(mw.RequirePermission(admin.PermWaitlistWrite)).Post("/waitlist/{uuid}/restore", h.restoreWaitlist)

	r.With(mw.RequirePermission(admin.PermUsersRead)).Get("/users", h.listUsers)
	r.With(mw.RequirePermission(admin.PermUsersRead)).Get("/users/{id}", h.getUser)
	r.With(mw.RequirePermission(admin.PermUsersWrite)).Patch("/users/{id}", h.patchUser)

	r.With(mw.RequirePermission(admin.PermCatalogsRead)).Get("/catalogs/{type}", h.listCatalog)
	r.With(mw.RequirePermission(admin.PermCatalogsWrite)).Post("/catalogs/{type}", h.createCatalog)
	r.With(mw.RequirePermission(admin.PermCatalogsWrite)).Patch("/catalogs/{type}/{id}", h.patchCatalog)

	r.With(mw.RequirePermission(admin.PermJobsRead)).Get("/jobs", h.listJobs)
	r.With(mw.RequirePermission(admin.PermJobsRead)).Get("/jobs/{id}", h.getJob)
	r.With(mw.RequirePermission(admin.PermJobsWrite)).Post("/jobs/{id}/retry", h.retryJob)

	r.With(mw.RequirePermission(admin.PermAdminsRead)).Get("/staff", h.listStaff)
	r.With(mw.RequirePermission(admin.PermAdminsWrite)).Post("/staff", h.createStaff)
	r.With(mw.RequirePermission(admin.PermAdminsWrite)).Patch("/staff/{id}", h.patchStaff)
	r.With(mw.RequirePermission(admin.PermAdminsWrite)).Post("/staff/{id}/resend-invite", h.resendInvite)

	r.With(mw.RequirePermission(admin.PermAdminsRead)).Get("/roles", h.listRoles)
	r.With(mw.RequirePermission(admin.PermAdminsWrite)).Post("/roles", h.createRole)
	r.With(mw.RequirePermission(admin.PermAdminsWrite)).Patch("/roles/{id}", h.patchRole)
	r.With(mw.RequirePermission(admin.PermAdminsWrite)).Delete("/roles/{id}", h.deleteRole)
	r.With(mw.RequirePermission(admin.PermAdminsRead)).Get("/permissions", h.listPermissions)

	r.With(mw.RequirePermission(admin.PermAuditRead)).Get("/audit-logs", h.listAuditLogs)
}

func (h *AdminHandler) audit(r *http.Request, action, resourceType, resourceID string, meta map[string]any) {
	actorID, _, _, ok := auth.AdminFromContext(r.Context())
	if !ok {
		return
	}
	if meta == nil {
		meta = map[string]any{}
	}
	_ = h.Store.WriteAudit(r.Context(), actorID, action, resourceType, resourceID, meta, r.RemoteAddr)
}
