package jobmodel

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/pkg/formschema"
)

type fixedLister []JobRef

func (l fixedLister) Jobs(context.Context, string) ([]JobRef, error) { return l, nil }

func TestRebuild(t *testing.T) {
	pool := newDB(t)
	fb := &fakeFallback{target: &appjobs.ApplyTarget{
		JobID: tJob, JobCode: "JB-3", Title: "T", CompanyName: "C", Status: "paused", RetentionMonths: 6, FormID: tForm, FormVersion: 2,
		Questions: []formschema.FormQuestion{{ID: "q", Type: "text", Config: []byte(`{}`)}},
	}}
	m := newModel(pool, fb, false)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	refs := fixedLister{{TenantID: tCompany, PublicID: "PUBR"}}

	for i := 0; i < 2; i++ { // idempotent
		reps, err := m.Rebuild(ctx, log, refs, []string{"uk"})
		if err != nil || len(reps) != 1 || reps[0].Listed != 1 || reps[0].Written != 1 {
			t.Fatalf("run %d: %+v %v", i, reps, err)
		}
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM hiring.app_jobs WHERE public_id = 'PUBR' AND status = 'paused' AND source = 'read_through'`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows = %d", n)
	}
	// The model now answers without the fallback.
	before := fb.count("LoadForApply")
	if _, err := regional(t, m).LoadForApply(ctx, tCompany, "PUBR"); err != nil || fb.count("LoadForApply") != before {
		t.Fatalf("not local after rebuild: %v", err)
	}
	// A job the fallback does not find is skipped.
	fb.target = nil
	reps, err := m.Rebuild(ctx, log, fixedLister{{TenantID: tCompany, PublicID: "GONE"}}, []string{"uk"})
	if err != nil || reps[0].Skipped != 1 {
		t.Fatalf("skip: %+v %v", reps, err)
	}
	if _, err := (&Model{Pool: m.Pool}).Rebuild(ctx, log, refs, []string{"uk"}); err != ErrNoFallback {
		t.Fatalf("no fallback: %v", err)
	}
}
