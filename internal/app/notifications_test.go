package app

import (
	"testing"

	"github.com/Angle-HR/server/pkg/config"
)

func TestBusNeeded(t *testing.T) {
	cases := []struct {
		transport, hiring string
		want              bool
	}{
		{"fluvio", "", false}, {"fluvio", "off", false}, {"events", "", true}, {"events", "on", true},
	}
	for _, c := range cases {
		// Hermetic: the other gates must not leak in from the developer's environment.
		t.Setenv("APPLICATIONS_EVENTS", "")
		t.Setenv("HIRING_EVENTS", c.hiring)
		if got := busNeeded(config.Config{NotificationsTransport: c.transport}); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}
