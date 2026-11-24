// Command jobmodel-verify is a manual verification tool for the applications job read model against a running
// stack (see docs/microservices/applications-jobmodel.md). It is not part of any deployment.
//
//	jobmodel-verify consume             subscribe the Projector (consumer applications-jobs) until interrupted
//	jobmodel-verify replay <region>     feed every event of the region's events_outbox through the Projector again and
//	                                    report whether any app_jobs row changed (it must not)
//	jobmodel-verify compare <public id> <company id> <region>
//	                                    print LoadForApply from the model and from jobsource and whether they are equal
//	jobmodel-verify readthrough <public id> <company id> <region>
//	                                    LoadForApply through a Model with ReadThrough on, then again, reporting the row
//
// Needs NATS_URL, DB_URL_GLOBAL and the ANGLEHR_<REGION>_POSTGRES_DSN variables.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Angle-HR/server/internal/applicationsvc/jobmodel"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/jobsource"
	"github.com/Angle-HR/server/internal/notifications"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/db"
	"github.com/Angle-HR/server/pkg/events"
	natsbus "github.com/Angle-HR/server/pkg/events/nats"
)

func main() {
	log.SetFlags(log.Ltime)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: jobmodel-verify consume | compare|readthrough <public id> <company id> <region>")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfgs, err := dbrouter.LoadConfigsFromEnv()
	if err != nil {
		return err
	}
	router, err := dbrouter.New(ctx, cfgs)
	if err != nil {
		return err
	}
	defer router.Close()

	if os.Args[1] == "consume" {
		bus, err := natsbus.Connect(notifications.BusConfig(os.Getenv("NATS_URL"), "applications-jobmodel-verify"))
		if err != nil {
			return err
		}
		defer func() { _ = bus.Close() }()
		sub, err := jobmodel.Subscribe(ctx, bus, router, events.WithConcurrency(2))
		if err != nil {
			return err
		}
		log.Printf("projector subscribed: consumer=%s subject=%s", jobmodel.Consumer, jobmodel.SubjectFilter)
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return sub.Stop(sctx)
	}

	if os.Args[1] == "replay" && len(os.Args) == 3 {
		return replay(ctx, router, os.Args[2])
	}
	if len(os.Args) != 5 {
		return fmt.Errorf("usage: jobmodel-verify %s <public id> <company id> <region>", os.Args[1])
	}
	publicID, company, reg := os.Args[2], os.Args[3], os.Args[4]
	global, err := db.NewGlobalPool(ctx, os.Getenv("DB_URL_GLOBAL"))
	if err != nil {
		return err
	}
	defer global.Close()
	hs := &jobsource.Source{Global: &hiringstore.Global{DB: global},
		Pool: func(r string) (hiringstore.DB, error) { return router.DB(region.Region(r)) }}
	pool := func(r string) (jobmodel.DB, error) { return router.DB(region.Region(r)) }

	switch os.Args[1] {
	case "compare":
		m := &jobmodel.Model{Pool: pool, Fallback: hs}
		return compare(ctx, hs, m, publicID, company, reg)
	case "readthrough":
		m := &jobmodel.Model{Pool: pool, Fallback: hs, ReadThrough: true}
		r, _ := m.Regional(reg)
		for i := 1; i <= 2; i++ {
			t, err := r.LoadForApply(ctx, company, publicID)
			fmt.Printf("call %d: err=%v status=%v\n", i, err, status(t))
			p, _ := router.DB(region.Region(reg))
			var v int64
			var src string
			if err := p.QueryRow(ctx, `SELECT version, source FROM hiring.app_jobs WHERE public_id = $1`, publicID).Scan(&v, &src); err != nil {
				fmt.Printf("  row: %v\n", err)
			} else {
				fmt.Printf("  row: version=%d source=%s\n", v, src)
			}
		}
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
	return nil
}

func status(t *appjobs.ApplyTarget) string {
	if t == nil {
		return "-"
	}
	return t.Status
}

func compare(ctx context.Context, hs, m appjobs.JobSource, publicID, company, reg string) error {
	hr, err := hs.Regional(reg)
	if err != nil {
		return err
	}
	mr, err := m.Regional(reg)
	if err != nil {
		return err
	}
	a, ea := hr.LoadForApply(ctx, company, publicID)
	b, eb := mr.LoadForApply(ctx, company, publicID)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	fmt.Printf("jobsource: err=%v status=%s\nmodel:     err=%v status=%s\n", ea, status(a), eb, status(b))
	if (ea == nil) != (eb == nil) || string(ja) != string(jb) {
		fmt.Printf("DIFFERENT\n  jobsource: %s\n  model:     %s\n", ja, jb)
		return fmt.Errorf("model and jobsource differ")
	}
	fmt.Println("EQUAL (LoadForApply json identical)")
	return nil
}

func replay(ctx context.Context, router *dbrouter.DBRouter, reg string) error {
	pool, err := router.DB(region.Region(reg))
	if err != nil {
		return err
	}
	snapshot := func() string {
		var s string
		_ = pool.QueryRow(ctx, `SELECT COALESCE(string_agg(job_id::text || ':' || version || ':' || applied_at::text, ',' ORDER BY job_id), '') FROM hiring.app_jobs`).Scan(&s)
		return s
	}
	var inboxBefore int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM events_inbox WHERE consumer = $1`, jobmodel.Consumer).Scan(&inboxBefore)
	before := snapshot()
	rows, err := pool.Query(ctx, `SELECT envelope FROM events_outbox WHERE type LIKE 'hiring.job.%' ORDER BY seq`)
	if err != nil {
		return err
	}
	var evs []events.Event
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		e, err := events.Decode(raw)
		if err != nil {
			return err
		}
		evs = append(evs, e)
	}
	rows.Close()
	p := jobmodel.NewProjector(jobmodel.RouterPools(router))
	for _, e := range evs {
		if err := p.Handle(ctx, e); err != nil {
			return fmt.Errorf("replay %s: %w", e.ID, err)
		}
	}
	var inboxAfter int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM events_inbox WHERE consumer = $1`, jobmodel.Consumer).Scan(&inboxAfter)
	fmt.Printf("replayed %d events; inbox rows %d -> %d; app_jobs unchanged: %v\n", len(evs), inboxBefore, inboxAfter, before == snapshot())
	if before != snapshot() || inboxBefore != inboxAfter {
		return fmt.Errorf("replay changed state")
	}
	return nil
}
