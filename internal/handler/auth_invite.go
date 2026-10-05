package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/onboarding"
	"github.com/Angle-HR/server/internal/org"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/besteffort"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// getInvite godoc
//
//	@Summary		Load product organization invite
//	@Description	Returns invite preview for a raw invite token. Public; no JWT required.
//	@Tags			auth
//	@Produce		json
//	@Param			token	path		string	true	"Invite token"
//	@Success		200		{object}	handler.AuthInviteEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		410		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/invite/{token} [get]
func (h *AuthHandler) getInvite(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	if token == "" {
		response.Error(w, r, apperror.ErrNotFound)
		return
	}

	invite, err := h.loadInviteByToken(r.Context(), token)
	if err != nil {
		writeInviteError(w, r, err)
		return
	}

	response.Success(w, r, http.StatusOK, AuthInviteData{
		Email:            invite.Email,
		OrganizationName: invite.OrganizationName,
		ExpiresAt:        invite.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// acceptInvite godoc
//
//	@Summary		Accept product organization invite
//	@Description	Creates or links a product user, joins the organization, marks onboarding complete, and returns JWTs.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthAcceptInviteRequest	true	"Accept invite payload"
//	@Success		200		{object}	handler.AuthTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Failure		410		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/accept-invite [post]
func (h *AuthHandler) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var req authAcceptInviteBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	ctx := r.Context()
	invite, err := h.loadInviteByToken(ctx, req.Token)
	if err != nil {
		writeInviteError(w, r, err)
		return
	}

	reg := invite.Region
	if !region.Valid(reg) || reg == region.RegionGlobal {
		// An invite always belongs to an already-onboarded organization,
		// which by definition lives in a real region — this should be
		// unreachable. There's no configured default region to fall back to
		// anymore, so treat it as a data-integrity failure instead of
		// guessing at a region.
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	defer rollbackOnError(ctx, tx)

	userID, err := h.upsertInvitedUser(ctx, tx, reg, invite.Email, passwordHash)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	applyInviteeProfile(ctx, tx, userID, &req)

	if err = joinOrganization(ctx, tx, &invite, userID); err != nil {
		response.Error(w, r, err)
		return
	}
	if err = h.commitInvitedUser(ctx, tx, reg, invite.Email, userID); err != nil {
		response.Error(w, r, err)
		return
	}

	user, err := h.loadUserByID(ctx, reg, userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	h.respondAuthTokens(w, r, ctx, reg, user)
}

// upsertInvitedUser sets the password on the invitee's existing unverified account,
// or creates the account if none exists. It returns the user id, or a public API error.
func (h *AuthHandler) upsertInvitedUser(
	ctx context.Context,
	tx pgx.Tx,
	reg region.Region,
	email, passwordHash string,
) (uuid.UUID, error) {
	user, err := h.loadUserByEmail(ctx, reg, email)
	var userID uuid.UUID
	switch {
	case err == nil:
		userID = user.ID
		if user.EmailVerifiedAt != nil && user.PasswordHash != "" {
			return uuid.Nil, apperror.New(apperror.CodeEmailAlreadyRegistered, apperror.MsgEmailAlreadyRegistered)
		}
		sql, args, buildErr := query.UpdateAccountUserPassword(userID, passwordHash)
		if buildErr != nil {
			return uuid.Nil, apperror.ErrInternal
		}
		if scanErr := tx.QueryRow(ctx, sql, args...).Scan(&userID); scanErr != nil {
			return uuid.Nil, apperror.ErrInternal
		}
	case errors.Is(err, pgx.ErrNoRows):
		sql, args, buildErr := query.InsertAccountUser(email, passwordHash)
		if buildErr != nil {
			return uuid.Nil, apperror.ErrInternal
		}
		if scanErr := tx.QueryRow(ctx, sql, args...).Scan(&userID); scanErr != nil {
			return uuid.Nil, apperror.ErrInternal
		}
	default:
		return uuid.Nil, apperror.ErrInternal
	}
	return userID, nil
}

// applyInviteeProfile stores the invitee's name when both parts are given. It is
// best-effort: country is optional for members and failures are only logged.
func applyInviteeProfile(ctx context.Context, tx pgx.Tx, userID uuid.UUID, req *authAcceptInviteBody) {
	if req.FirstName == nil && req.LastName == nil {
		return
	}
	first := stringValue(req.FirstName)
	last := stringValue(req.LastName)
	if first == "" || last == "" {
		return
	}
	_, err := tx.Exec(ctx, `
		UPDATE users
		SET first_name = COALESCE(NULLIF($2, ''), first_name),
		    last_name = COALESCE(NULLIF($3, ''), last_name),
		    account_type = COALESCE(account_type, 'individual')
		WHERE id = $1 AND deleted_at IS NULL
	`, userID, first, last)
	besteffort.Log(ctx, "update invitee profile", err)
}

// joinOrganization completes the invitee's onboarding, adds them as a member and
// marks the invite accepted.
func joinOrganization(ctx context.Context, tx pgx.Tx, invite *orgInviteRow, userID uuid.UUID) error {
	completeSQL, completeArgs, err := query.CompleteAccountUserOnboarding(userID)
	if err != nil {
		return apperror.ErrInternal
	}
	if scanErr := tx.QueryRow(ctx, completeSQL, completeArgs...).Scan(&userID); scanErr != nil {
		return apperror.ErrInternal
	}

	memberSQL, memberArgs, err := query.InsertOrganizationMember(invite.OrganizationID, userID, org.RoleMember)
	if err != nil {
		return apperror.ErrInternal
	}
	var memberID uuid.UUID
	if scanErr := tx.QueryRow(ctx, memberSQL, memberArgs...).Scan(&memberID); scanErr != nil &&
		!errors.Is(scanErr, pgx.ErrNoRows) {
		return apperror.ErrInternal
	}

	acceptSQL, acceptArgs, err := query.AcceptOrganizationInvite(invite.ID)
	if err != nil {
		return apperror.ErrInternal
	}
	if scanErr := tx.QueryRow(ctx, acceptSQL, acceptArgs...).Scan(&invite.ID); scanErr != nil {
		return apperror.ErrInternal
	}

	completed := onboarding.RequiredSteps(onboarding.AccountIndividual)
	progressSQL, progressArgs, err := query.UpsertOnboardingProgress(userID, onboarding.StepComplete, completed)
	if err != nil {
		return apperror.ErrInternal
	}
	if _, execErr := tx.Exec(ctx, progressSQL, progressArgs...); execErr != nil {
		return apperror.ErrInternal
	}
	return nil
}

// commitInvitedUser records the user in the global registry and commits the
// regional and global transactions.
func (h *AuthHandler) commitInvitedUser(
	ctx context.Context,
	tx pgx.Tx,
	reg region.Region,
	email string,
	userID uuid.UUID,
) error {
	globalTx, err := h.GlobalDB.Begin(ctx)
	if err != nil {
		return apperror.ErrInternal
	}
	defer rollbackOnError(ctx, globalTx)

	registrySQL, registryArgs, err := query.UpsertUsersRegistryProductUser(
		email, string(reg), regionSourceExplicit, userID,
	)
	if err != nil {
		return apperror.ErrInternal
	}
	if _, execErr := globalTx.Exec(ctx, registrySQL, registryArgs...); execErr != nil {
		return apperror.ErrInternal
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return apperror.ErrInternal
	}
	if commitErr := globalTx.Commit(ctx); commitErr != nil {
		return apperror.ErrInternal
	}
	return nil
}

// createOrgInvite godoc
//
//	@Summary		Create organization invite
//	@Description	Organization owners invite a member by email. Sends an invite email with a token.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.AuthCreateOrgInviteRequest	true	"Invite payload"
//	@Success		201		{object}	handler.AuthCreateOrgInviteEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/organizations/invites [post]
func (h *AuthHandler) createOrgInvite(w http.ResponseWriter, r *http.Request) {
	var req authCreateOrgInviteBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	ctx := r.Context()
	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	orgSQL, orgArgs, err := query.LookupOrganizationByOwnerID(userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	var orgID uuid.UUID
	var orgName string
	if scanErr := pool.QueryRow(ctx, orgSQL, orgArgs...).Scan(&orgID, &orgName); scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			response.Error(w, r, apperror.New(apperror.CodeForbidden, "only organization owners can invite members"))
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	rawToken, err := org.NewInviteToken()
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	expiresAt := time.Now().UTC().Add(org.InviteTTL)
	tokenHash := org.HashInviteToken(rawToken)

	invSQL, invArgs, err := query.InsertOrganizationInvite(orgID, email, tokenHash, userID, expiresAt)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	var inviteID uuid.UUID
	if err := pool.QueryRow(ctx, invSQL, invArgs...).Scan(&inviteID); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	h.enqueueOrgInviteEmail(ctx, email, orgName, rawToken)

	response.Success(w, r, http.StatusCreated, AuthCreateOrgInviteData{
		Email:     email,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	})
}

type orgInviteRow struct {
	ID               uuid.UUID
	OrganizationID   uuid.UUID
	Email            string
	ExpiresAt        time.Time
	AcceptedAt       *time.Time
	OrganizationName string
	Region           region.Region
}

func (h *AuthHandler) loadInviteByToken(ctx context.Context, rawToken string) (orgInviteRow, error) {
	tokenHash := org.HashInviteToken(rawToken)

	regions := []region.Region{
		region.RegionUK, region.RegionUS, region.RegionAfrica, region.RegionEU, region.RegionAsia,
	}

	var lastErr error
	for _, reg := range regions {
		pool, err := h.Router.DB(reg)
		if err != nil {
			lastErr = err
			continue
		}
		sql, args, err := query.LookupOrganizationInviteByTokenHash(tokenHash)
		if err != nil {
			return orgInviteRow{}, err
		}
		var row orgInviteRow
		err = pool.QueryRow(ctx, sql, args...).Scan(
			&row.ID, &row.OrganizationID, &row.Email, &row.ExpiresAt, &row.AcceptedAt, &row.OrganizationName,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			lastErr = err
			continue
		}
		if err != nil {
			return orgInviteRow{}, err
		}
		row.Region = reg
		if row.AcceptedAt != nil {
			return orgInviteRow{}, errInviteAccepted
		}
		if time.Now().UTC().After(row.ExpiresAt) {
			return orgInviteRow{}, errInviteExpired
		}
		return row, nil
	}
	if lastErr == nil {
		lastErr = pgx.ErrNoRows
	}
	return orgInviteRow{}, lastErr
}

var (
	errInviteExpired  = errors.New("invite expired")
	errInviteAccepted = errors.New("invite already accepted")
)

func writeInviteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errInviteExpired):
		response.Error(w, r, apperror.New(apperror.CodeGone, "invite expired"))
	case errors.Is(err, errInviteAccepted):
		response.Error(w, r, apperror.New(apperror.CodeGone, "invite already accepted"))
	case errors.Is(err, pgx.ErrNoRows):
		response.Error(w, r, apperror.ErrNotFound)
	default:
		response.Error(w, r, apperror.ErrInternal)
	}
}

func (h *AuthHandler) enqueueOrgInviteEmail(ctx context.Context, email, orgName, token string) {
	if h.Enqueuer == nil {
		return
	}
	tx, err := h.GlobalDB.Begin(ctx)
	if err != nil {
		return
	}
	defer rollbackOnError(ctx, tx)

	_, enqueueErr := h.Enqueuer.EnqueueTx(ctx, tx, mailer.EmailArgs{
		Type:             mailer.TypeOrgInvite,
		Recipient:        email,
		OrganizationName: orgName,
		Token:            token,
		ExpiresInSeconds: int(org.InviteTTL.Seconds()),
	}, queue.EmailEnqueueOptions()...)
	besteffort.Log(ctx, "Enqueuer.EnqueueTx", enqueueErr)
	besteffort.Log(ctx, "tx.Commit", tx.Commit(ctx))
}
