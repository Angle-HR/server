// Command org is the org service: company onboarding, KYB, agreements and the OrgService gRPC API, extracted
// from the monolith. See internal/orgsvc.
package main

import (
	"log"
	"os"

	"github.com/Angle-HR/server/internal/orgsvc"
)

func main() {
	log.SetFlags(0)
	if err := orgsvc.Run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
