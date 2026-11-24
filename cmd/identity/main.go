// Command identity is the identity service: product auth and sessions, admin console auth, staff, roles, audit,
// users and the KYB verification queue, extracted from the monolith. See internal/identitysvc.
package main

import (
	"log"
	"os"

	"github.com/Angle-HR/server/internal/identitysvc"
)

func main() {
	log.SetFlags(0)
	if err := identitysvc.Run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
