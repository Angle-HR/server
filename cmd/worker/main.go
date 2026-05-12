package main

import (
	"log"
	"os"

	"github.com/Angle-HR/server/internal/worker"
)

func main() {
	log.SetFlags(0)
	if err := worker.Run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
