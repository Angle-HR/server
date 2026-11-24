package nats_test

import (
	"os"
	"testing"

	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/eventstest"
	natsbus "github.com/Angle-HR/server/pkg/events/nats"
)

// TestConformance needs a JetStream enabled server:
//
//	docker run --rm -d --name events-nats -p 24222:4222 nats:2 -js
//	EVENTS_NATS_URL=nats://localhost:24222 go test ./pkg/events/nats/ -count=1
func TestConformance(t *testing.T) {
	url := os.Getenv("EVENTS_NATS_URL")
	if url == "" {
		t.Skip("EVENTS_NATS_URL not set; skipping the NATS conformance suite")
	}
	eventstest.Run(t, func(t *testing.T) events.Bus {
		b, err := natsbus.Connect(natsbus.Config{URL: url, Name: "events-conformance"})
		if err != nil {
			t.Fatal(err)
		}
		return b
	})
}
