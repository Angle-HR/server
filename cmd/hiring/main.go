// Command hiring is the hiring service: jobs, drafts, forms, templates, catalog and settings, the publish
// lifecycle, the public careers page and sitemap, and the HiringService gRPC API, extracted from the monolith.
// See internal/hiringsvc and docs/microservices/hiring.md.
package main

import (
	"log"
	"os"

	"github.com/Angle-HR/server/internal/hiringsvc"
)

func main() {
	log.SetFlags(0)
	if err := hiringsvc.Run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
