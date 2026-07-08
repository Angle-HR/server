package main

import (
	"log"
	"os"

	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/upload"
	"github.com/Angle-HR/server/internal/worker/runtime"
	"github.com/Angle-HR/server/pkg/logger"
	fluvio "github.com/software78/fluvio"
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "development"
	}
	slogLogger := logger.New(appEnv)

	return runtime.Run("upload-worker", queue.UploadWorkerQueues(), func(workers *fluvio.Workers) {
		fluvio.AddWorker(workers, &upload.UploadWorker{Logger: slogLogger})
	})
}
