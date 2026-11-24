// Command applications is the applications service: public apply, the company-side applications routes and the
// ApplicationsService gRPC API, extracted from the monolith. See internal/applicationsvc.
package main

import (
	"log"
	"os"

	"github.com/Angle-HR/server/internal/applicationsvc"
)

func main() {
	log.SetFlags(0)
	if err := applicationsvc.Run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
