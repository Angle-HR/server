// Package main is the HTTP server entrypoint.
//
//	@title						Angle HR API
//	@version					1.0
//	@description				Waitlist and product onboarding API for Open HR.
//	@description				Interactive docs (Scalar) are served at `/` in non-production environments.
//	@description				Product flow: signup → verify email (6-digit OTP, 5 min expiry) → profile → address → [business] → complete.
//	@description				Onboarding step endpoints require `Authorization: Bearer <access_token>` after email verification.
//	@host						localhost:8080
//	@BasePath					/api/v1
//
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
//	@description				Bearer JWT access token
//
// Runtime serving overrides host and schemes from PUBLIC_API_URL (see internal/docs).
package main

import (
	"log"
	"os"

	"github.com/Angle-HR/server/internal/app"
)

//go:generate swag init -g main.go -o ../../internal/docs/spec --parseDependency --parseInternal

func main() {
	log.SetFlags(0)
	if err := app.Run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
