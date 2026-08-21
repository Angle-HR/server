package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// Country is a waitlist country option.
type Country struct {
	ID      uuid.UUID     `json:"id"`
	Name    string        `json:"name"`
	Slug    string        `json:"slug"`
	Region  region.Region `json:"region"`
	IconKey *string       `json:"icon_key,omitempty"`
}

// globalDB reads and writes against the global registry database.
type globalDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// CountriesHandler serves country reference data.
type CountriesHandler struct {
	GlobalDB globalDB
}

// NewCountriesHandler returns a countries handler.
func NewCountriesHandler(globalDB globalDB) *CountriesHandler {
	return &CountriesHandler{GlobalDB: globalDB}
}

// RegisterRoutes mounts country routes on r.
func (h *CountriesHandler) RegisterRoutes(r chi.Router) {
	r.Get("/countries", h.list)
}

// list godoc
//
//	@Summary		List countries
//	@Description	Returns active countries for product onboarding.
//	@Tags			waitlist/reference
//	@Produce		json
//	@Success		200	{object}	handler.CountriesEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/countries [get]
func (h *CountriesHandler) list(w http.ResponseWriter, r *http.Request) {
	countries, err := h.loadCountries(r.Context())
	if err != nil {
		response.Error(w, r, err)
		return
	}

	response.Success(w, r, http.StatusOK, countries)
}

func (h *CountriesHandler) loadCountries(ctx context.Context) ([]Country, error) {
	sql, args, err := query.ListActiveCountries()
	if err != nil {
		return nil, fmt.Errorf("build list countries query: %w", err)
	}

	rows, err := h.GlobalDB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	countries := make([]Country, 0, 8)
	for rows.Next() {
		country, err := scanCountry(rows)
		if err != nil {
			return nil, err
		}

		countries = append(countries, country)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return countries, nil
}

type countryScanner interface {
	Scan(dest ...any) error
}

func scanCountry(row countryScanner) (Country, error) {
	var country Country
	var iconKey *string

	if err := row.Scan(&country.ID, &country.Name, &country.Slug, &country.Region, &iconKey); err != nil {
		return Country{}, err
	}

	country.IconKey = iconKey

	return country, nil
}
