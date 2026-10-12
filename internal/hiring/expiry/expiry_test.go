package expiry_test

import (
	"context"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/drafttest"
	"github.com/Angle-HR/server/internal/hiring/expiry"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
)

const (
	org  = "00000000-0000-4000-8000-0000000000a1"
	user = "00000000-0000-4000-8000-0000000000b1"
)

func seed(t *testing.T, st *drafttest.Store, status, closing string) string {
	t.Helper()
	rec, err := st.CreateJob(context.Background(), &hiringtypes.CreateInput{
		TenantID: org, UserID: user, Details: jobs.NewDetails(), Step: jobs.StepDetails,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := st.Jobs[drafttest.Key(org, rec.Job.ID)]
	r.Job.Status, r.Job.Details.ClosingDate, r.Job.PublicID = status, closing, "pub-"+rec.Job.ID[:8]
	return rec.Job.ID
}

func TestSweeperExpiresOnlyPublishedJobsPastTheirClosingDate(t *testing.T) {
	st := drafttest.NewStore()
	ref := &drafttest.Ref{Skills: map[string]string{}}
	svc := &draft.Service{Store: st, Ref: ref, Now: func() time.Time { return time.Now() }}

	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	due := seed(t, st, jobs.StatusPublished, yesterday)
	open := seed(t, st, jobs.StatusPublished, tomorrow)
	noDate := seed(t, st, jobs.StatusPublished, "")
	paused := seed(t, st, jobs.StatusPaused, yesterday)
	closed := seed(t, st, jobs.StatusClosed, yesterday)

	sw := &expiry.Sweeper{
		Tenants: func(context.Context) ([]string, error) { return []string{org}, nil },
		Expirer: svc,
	}
	res, err := sw.Run(context.Background())
	if err != nil || res.Expired != 1 || res.Companies != 1 || res.Errors != 0 {
		t.Fatalf("run: %+v %v", res, err)
	}
	want := map[string]string{
		due: jobs.StatusExpired, open: jobs.StatusPublished, noDate: jobs.StatusPublished,
		paused: jobs.StatusPaused, closed: jobs.StatusClosed,
	}
	for id, status := range want {
		if got := st.Jobs[drafttest.Key(org, id)].Job.Status; got != status {
			t.Errorf("job %s: status %q, want %q", id, got, status)
		}
	}

	// A second run finds nothing: expiring is idempotent.
	if res, err = sw.Run(context.Background()); err != nil || res.Expired != 0 {
		t.Fatalf("second run: %+v %v", res, err)
	}
}
