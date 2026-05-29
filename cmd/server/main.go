// Package main is the HTTP server entrypoint.
//
//	@title						Angle HR Waitlist API
//	@version					1.0
//	@description				Waitlist signup API for Open HR.
//	@host						localhost:8080
//	@BasePath					/api/v1
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
