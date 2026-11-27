package orgsvc

import (
	"context"
	"errors"
	"fmt"
	"github.com/Angle-HR/server/internal/countrycode"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/kyb/kybidentity"
	"github.com/Angle-HR/server/internal/kyb/kybstore"
	"github.com/Angle-HR/server/internal/orgevents"
	"github.com/Angle-HR/server/internal/orghttp"
	"github.com/Angle-HR/server/internal/orgstore"
	"github.com/Angle-HR/server/internal/region"
)

// regionalStore adapts the database router to orgstore.
func regionalStore(router *dbrouter.DBRouter, global *pgxpool.Pool) *orgstore.Store {
	return &orgstore.Store{Slugs: orgstore.GlobalSlugs{DB: global}, Regional: func(reg region.Region) (orgstore.DB, error) {
		p, err := router.DB(reg)
		if err != nil {
			return nil, err
		}
		return p, nil
	}}
}

// kybReviewer applies reviewer decisions through the same kyb.Service the HTTP flow uses, with the review queue
// closing made lenient (identity closes its item before it calls ApplyKybDecision).
type kybReviewer struct {
	Router      *dbrouter.DBRouter
	Queue       kybidentity.Factory
	NotifierFor orghttp.KYBNotifierFactory
	Log         *slog.Logger
}

func (k *kybReviewer) service(reg region.Region) (*kyb.Service, *kybstore.Store, error) {
	pool, err := k.Router.DB(reg)
	if err != nil {
		return nil, nil, err
	}
	store := &kybstore.Store{DB: pool, OnSave: orgevents.KYBSaveHook(string(reg))}
	return &kyb.Service{
		Store:     store,
		Queue:     kybidentity.LenientResolve{Queue: k.Queue(string(reg))},
		Registry:  kyb.NewRegistry(),
		Notifier:  k.NotifierFor(reg),
		Logger:    k.Log,
		Supported: countrycode.SupportedFromEnv(),
	}, store, nil
}

// Review implements KYBReviewer.
func (k *kybReviewer) Review(
	ctx context.Context, reg region.Region, companyID, reviewerID string, approve bool, reason kyb.FailureReason,
) (*kyb.Record, error) {
	svc, _, err := k.service(reg)
	if err != nil {
		return nil, err
	}
	return svc.Review(ctx, companyID, reviewerID, approve, reason)
}

// Status implements KYBReviewer.
func (k *kybReviewer) Status(ctx context.Context, reg region.Region, companyID string) (*kyb.Record, error) {
	_, store, err := k.service(reg)
	if err != nil {
		return nil, err
	}
	return store.Load(ctx, companyID)
}

// globalCatalog reads the onboarding catalogs from the global database.
type globalCatalog struct {
	DB interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	}
}

// Industries implements Catalog.
func (g globalCatalog) Industries(ctx context.Context) ([]Industry, error) {
	rows, err := g.DB.Query(ctx,
		`SELECT id::text, slug, name FROM accounts.onboarding_industries WHERE is_active ORDER BY sort_order, name`)
	if err != nil {
		return nil, fmt.Errorf("orgsvc: industries: %w", err)
	}
	defer rows.Close()
	var out []Industry
	for rows.Next() {
		var it Industry
		if err = rows.Scan(&it.ID, &it.Slug, &it.Name); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// IndustryExists implements Catalog.
func (g globalCatalog) IndustryExists(ctx context.Context, slug string) (bool, error) {
	var ok bool
	err := g.DB.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM accounts.onboarding_industries WHERE slug = $1 AND is_active)`, slug).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("orgsvc: validate industry: %w", err)
	}
	return ok, nil
}

// globalCountries answers which region serves a country (global catalog).
type globalCountries struct {
	DB interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	}
}

// CountryRegion implements CountryResolver. An unknown or inactive country is region.RegionUnknown.
func (g globalCountries) CountryRegion(ctx context.Context, countryID uuid.UUID) (region.Region, error) {
	var reg string
	err := g.DB.QueryRow(ctx, `SELECT region FROM waitlist.countries WHERE id = $1 AND is_active`, countryID).Scan(&reg)
	if errors.Is(err, pgx.ErrNoRows) {
		return region.RegionUnknown, nil
	}
	if err != nil {
		return region.RegionUnknown, fmt.Errorf("orgsvc: country region: %w", err)
	}
	return region.Region(reg), nil
}
