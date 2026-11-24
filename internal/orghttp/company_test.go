package orghttp

import (
	"testing"

	"github.com/Angle-HR/server/internal/hiring/draft"
)

// The agreements routes exist in hiring and here until ORG_REMOTE is the default; the version must not drift.
func TestDPAVersionMatchesHiring(t *testing.T) {
	if CurrentDPAVersion != draft.CurrentDPAVersion {
		t.Fatalf("org %q != hiring %q", CurrentDPAVersion, draft.CurrentDPAVersion)
	}
}
