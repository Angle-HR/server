package memory_test

import (
	"testing"

	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/eventstest"
	"github.com/Angle-HR/server/pkg/events/memory"
)

func TestConformance(t *testing.T) {
	eventstest.Run(t, func(*testing.T) events.Bus { return memory.New() })
}
