package identityhttp

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/internal/userdir"
	"github.com/Angle-HR/server/internal/verifyqueue"
	"github.com/Angle-HR/server/pkg/besteffort"
)

// Admin login gets a longer window and lock than product login: fewer,
// higher-value accounts, so it's worth being stricter once triggered.
const (
	adminPasswordLockoutMaxAttempts = 5
	adminPasswordLockoutWindow      = 15 * time.Minute
	adminPasswordLockoutDuration    = 30 * time.Minute
)

// AdminHandler serves the identity half of /api/v1/admin: auth, users, staff, roles, audit and the KYB
// verification queue.
type AdminHandler struct {
	Store           *admin.Store
	Router          *dbrouter.DBRouter
	GlobalDB        globalDB
	Sessions        *session.Manager
	Enqueuer        jobEnqueuer
	PasswordLockout *auth.LoginLockout
	// TOTPCrypto encrypts the admin authenticator secrets (same keys and rotation as the product); TOTPLockout limits
	// code guesses per admin; TOTPRequired is ADMIN_TOTP_REQUIRED. Set them with UseTOTP.
	TOTPCrypto   *auth.TOTPCrypto
	TOTPLockout  *auth.LoginLockout
	TOTPRequired bool
	// Queue is the review queue (admin.verification_queue in the global database).
	Queue *verifyqueue.Queue
	// Org applies a reviewer's decision to the company's KYB case and reads the case detail. Nil makes the
	// verification routes answer 503.
	Org OrgReview
	// Users is the directory used to activate and deactivate accounts.
	Users *userdir.Directory
	// OrgReads reads the onboarding progress and company name of the user detail from org (ORG_ONBOARDING=grpc).
	// Nil keeps the accounts tables.
	OrgReads *OrgReads
	validate *validator.Validate
}

// NewAdminHandler returns the admin handler. org may be nil (verification routes then answer 503).
func NewAdminHandler(
	store *admin.Store,
	router *dbrouter.DBRouter,
	global globalDB,
	redisClient *goredis.Client,
	sessions *session.Manager,
	enqueuer jobEnqueuer,
	org OrgReview,
) *AdminHandler {
	return &AdminHandler{
		Store:    store,
		Router:   router,
		GlobalDB: global,
		Sessions: sessions,
		Enqueuer: enqueuer,
		PasswordLockout: auth.NewLoginLockout(redisClient, "admin_pwd",
			adminPasswordLockoutMaxAttempts, adminPasswordLockoutWindow, adminPasswordLockoutDuration),
		TOTPLockout: auth.NewLoginLockout(redisClient, "admin_totp",
			adminTOTPLockoutMaxAttempts, adminTOTPLockoutWindow, adminTOTPLockoutDuration),
		Queue:    &verifyqueue.Queue{DB: global},
		Org:      org,
		Users:    &userdir.Directory{Global: global, Router: router},
		validate: validator.New(),
	}
}

// RegisterPublicRoutes mounts unauthenticated admin auth routes.
func (h *AdminHandler) RegisterPublicRoutes(r chi.Router) {
	r.Post("/auth/login", h.login)
	r.Post("/auth/login/totp", h.loginTOTP)
	r.Post("/auth/refresh", h.refresh)
	r.Post("/auth/logout", h.logout)
	r.Get("/auth/invite/{token}", h.getInvite)
	r.Post("/auth/accept-invite", h.acceptInvite)
}

// RegisterProtectedRoutes mounts the authenticated identity admin routes. Caller must apply RequireAdmin.
func (h *AdminHandler) RegisterProtectedRoutes(r chi.Router, mw *auth.AdminMiddleware) {
	r.Get("/auth/me", h.me)
	r.Post("/auth/totp/enroll", h.totpEnroll)
	r.Post("/auth/totp/confirm", h.totpConfirm)
	r.Post("/auth/totp/disable", h.totpDisable)

	r.With(mw.RequirePermission(admin.PermUsersRead)).Get("/users", h.listUsers)
	r.With(mw.RequirePermission(admin.PermUsersRead)).Get("/users/{id}", h.getUser)
	r.With(mw.RequirePermission(admin.PermUsersWrite)).Patch("/users/{id}", h.patchUser)

	r.With(mw.RequirePermission(admin.PermAdminsRead)).Get("/staff", h.listStaff)
	r.With(mw.RequirePermission(admin.PermAdminsWrite)).Post("/staff", h.createStaff)
	r.With(mw.RequirePermission(admin.PermAdminsWrite)).Patch("/staff/{id}", h.patchStaff)
	r.With(mw.RequirePermission(admin.PermAdminsWrite)).Post("/staff/{id}/resend-invite", h.resendInvite)
	r.With(mw.RequirePermission(admin.PermAdminsWrite)).Post("/staff/{id}/totp/reset", h.resetStaffTOTP)

	r.With(mw.RequirePermission(admin.PermAdminsRead)).Get("/roles", h.listRoles)
	r.With(mw.RequirePermission(admin.PermAdminsWrite)).Post("/roles", h.createRole)
	r.With(mw.RequirePermission(admin.PermAdminsWrite)).Patch("/roles/{id}", h.patchRole)
	r.With(mw.RequirePermission(admin.PermAdminsWrite)).Delete("/roles/{id}", h.deleteRole)
	r.With(mw.RequirePermission(admin.PermAdminsRead)).Get("/permissions", h.listPermissions)

	r.With(mw.RequirePermission(admin.PermAuditRead)).Get("/audit-logs", h.listAuditLogs)

	r.With(mw.RequirePermission(admin.PermVerificationReview)).Get("/verification", h.listVerificationQueue)
	r.With(mw.RequirePermission(admin.PermVerificationReview)).Get("/verification/{organizationID}", h.getVerificationItem)
	r.With(mw.RequirePermission(admin.PermVerificationReview)).
		Post("/verification/{organizationID}/review", h.reviewVerification)

	r.With(mw.RequirePermission(admin.PermCompaniesDelete)).Delete("/companies/{id}", h.deleteCompany)
	r.With(mw.RequirePermission(admin.PermCompaniesDelete)).Post("/companies/{id}/reactivate", h.reactivateCompany)
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

// AuditAs writes an audit row for an operator acting outside an HTTP request (gRPC).
func (h *AdminHandler) AuditAs(ctx context.Context, actor uuid.UUID, action, resourceType, resourceID string, meta map[string]any) {
	if h.Store == nil {
		return
	}
	besteffort.Log(ctx, "h.Store.WriteAudit", h.Store.WriteAudit(ctx, actor, action, resourceType, resourceID, meta, ""))
}
