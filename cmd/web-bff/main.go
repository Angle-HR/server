// Command web-bff is the edge service in front of the API for the public web client.
package main

import (
	"fmt"
	"os"

	"github.com/Angle-HR/server/internal/bff"
)

func main() {
	cfg, err := bff.LoadConfig(bff.Defaults{Name: "web-bff", ListenAddr: ":8090"}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := bff.Run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
