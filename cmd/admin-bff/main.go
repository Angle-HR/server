// Command admin-bff is the edge service in front of the API for the admin console (the /api/v1/admin path space).
package main

import (
	"fmt"
	"os"

	"github.com/Angle-HR/server/internal/adminbff"
)

func main() {
	cfg, err := adminbff.LoadConfig(nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := adminbff.Run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
