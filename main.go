package main

import (
	"log"
	"os"

	"github.com/Angle-HR/server/internal/app"
)

func main() {
	os.Exit(run())
}

func run() int {
	return runWith(app.Run)
}

func runWith(appRun func() error) int {
	log.SetFlags(0)
	if err := appRun(); err != nil {
		log.Print(err)
		return 1
	}
	return 0
}
