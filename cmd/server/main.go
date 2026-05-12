package main

import (
	"log"
	"os"

	"github.com/Angle-HR/server/internal/app"
)

func main() {
	log.SetFlags(0)
	if err := app.Run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
