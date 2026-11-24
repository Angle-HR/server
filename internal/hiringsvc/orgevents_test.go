package hiringsvc

import (
	"context"
	"testing"

	"github.com/Angle-HR/server/internal/applicationsvc/jobmodel"
	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/inbox"
)

func TestCompanyDeletionRejectsBadEvents(t *testing.T) {
	h := &CompanyDeletion{Pools: func(r string) (inbox.DB, error) {
		if r != "uk" {
			return nil, jobmodel.ErrUnknownRegion
		}
		t.Fatal("database must not be used for a bad event")
		return nil, nil
	}}
	mk := func(typ string, data map[string]any) events.Event {
		e, _ := events.NewJSON("/services/org", typ, data)
		return e
	}
	cases := map[string]events.Event{
		"wrong type": mk("org.company.updated.v1", map[string]any{"company_id": "x"}),
		"bad id":     mk(typeCompanyDeleted, map[string]any{"company_id": "nope", "region": "uk"}),
		"no region":  mk(typeCompanyDeleted, map[string]any{"company_id": "11111111-1111-4111-8111-111111111111"}),
		"unknown region": mk(typeCompanyDeleted, map[string]any{
			"company_id": "11111111-1111-4111-8111-111111111111", "region": "mars"}),
	}
	for name, e := range cases {
		if err := h.Handle(context.Background(), e); err == nil || !events.IsTerminal(err) {
			t.Errorf("%s: want terminal, got %v", name, err)
		}
	}
}
