package handler

import "github.com/Angle-HR/server/internal/apidoc"

// Ensures swag resolves apidoc.ErrorEnvelope for handler package comments.
var _ = apidoc.ErrorEnvelope{}

// CountryResponse is a country option for the waitlist form.
type CountryResponse struct {
	ID      string  `json:"id" example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
	Name    string  `json:"name" example:"United Kingdom"`
	Slug    string  `json:"slug" example:"united-kingdom"`
	Region  string  `json:"region" example:"uk" enums:"uk,us,africa,eu,asia"`
	IconKey *string `json:"icon_key,omitempty" example:"flag-uk"`
}

// CountriesEnvelope is a successful countries list response.
type CountriesEnvelope struct {
	Data []CountryResponse `json:"data"`
	Meta *apidoc.Meta      `json:"meta,omitempty"`
}

// SignupRequest is the waitlist signup request body.
type SignupRequest struct {
	Email string `json:"email" example:"jerry@example.com"`
}

// SignupData is the waitlist signup success payload.
type SignupData struct {
	Message string `json:"message" example:"You're on the list!"`
	Region  string `json:"region" example:"uk"`
}

// SignupEnvelope is a successful waitlist signup response.
type SignupEnvelope struct {
	Data SignupData   `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}
