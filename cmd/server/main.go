// Package main is the HTTP server entrypoint.
//
//	@title						Angle HR Waitlist API
//	@version					1.0
//	@description				Onboarding waitlist and admin API for Angle HR.
//	@host						localhost:8080
//	@BasePath					/api/v1
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
//	@description				Bearer token containing the admin scope
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
