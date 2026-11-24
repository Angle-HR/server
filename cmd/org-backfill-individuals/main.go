// Command org-backfill-individuals gives every individual account that finished onboarding before personal
// organizations existed its personal organization, and puts it in front of the reviewer
// (docs/microservices/individual-accounts.md). It runs once and exits.
//
//	org-backfill-individuals -dry-run          list what would be created (user ids only), change nothing
//	org-backfill-individuals                   create them
//	org-backfill-individuals -region uk        one region only
//
// Idempotent: an account that has an organization (also a deleted one) or is a member of a company is skipped, so
// a rerun after a partial failure only finishes what is missing. Environment: DB_URL_GLOBAL and the regional
// database variables as for cmd/org, plus IDENTITY_QUEUE / IDENTITY_GRPC_ADDR when the review queue is not in the
// global database.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/kyb/kybidentity"
	"github.com/Angle-HR/server/internal/orgstore"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/db"
)

func main() {
	log.SetFlags(0)
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// Pool is the part of a pgx pool the backfill reads with.
type Pool interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// candidate is an individual account without an organization.
type candidate struct {
	orgstore.Personal
}

const candidatesSQL = `
SELECT u.id::text, btrim(coalesce(u.first_name, '') || ' ' || coalesce(u.last_name, '')), u.country_id::text,
       coalesce(u.industry_id::text, ''), coalesce(u.business_type_id::text, ''), coalesce(u.employee_count, 0)
FROM accounts.users u
WHERE u.account_type = 'individual' AND u.onboarding_completed_at IS NOT NULL AND u.deleted_at IS NULL
  AND u.country_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM accounts.organizations o WHERE o.owner_user_id = u.id)
  AND NOT EXISTS (SELECT 1 FROM accounts.organization_members m WHERE m.user_id = u.id)
ORDER BY u.id`

// Candidates lists the accounts of one region that need a personal organization.
func Candidates(ctx context.Context, p Pool) ([]candidate, error) {
	rows, err := p.Query(ctx, candidatesSQL)
	if err != nil {
		return nil, fmt.Errorf("list individual accounts: %w", err)
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.UserID, &c.Name, &c.CountryID, &c.IndustryID, &c.BusinessTypeID, &c.EmployeeCount); err != nil {
			return nil, fmt.Errorf("scan individual account: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Ensurer is orgstore.Store.EnsurePersonal.
type Ensurer interface {
	EnsurePersonal(ctx context.Context, reg region.Region, p orgstore.Personal, q orgstore.CaseQueue) (*orgstore.PersonalOrg, error)
}

// Result counts one region's run.
type Result struct{ Found, Created, Skipped, Errors int }

// Backfill creates the personal organizations of one region. With dryRun it writes nothing and prints the user ids.
func Backfill(
	ctx context.Context, out io.Writer, reg region.Region, cands []candidate, slugs map[string]string, dryRun bool,
	st Ensurer, q orgstore.CaseQueue,
) Result {
	res := Result{Found: len(cands)}
	for _, c := range cands {
		if c.Name == "" {
			fmt.Fprintf(out, "%s user %s: skipped, no name on the profile\n", reg, c.UserID)
			res.Skipped++
			continue
		}
		if dryRun {
			fmt.Fprintf(out, "%s user %s: would create a personal organization\n", reg, c.UserID)
			continue
		}
		c.CountrySlug = slugs[c.CountryID]
		o, err := st.EnsurePersonal(ctx, reg, c.Personal, q)
		if err != nil {
			fmt.Fprintf(out, "%s user %s: error: %v\n", reg, c.UserID, err)
			res.Errors++
			continue
		}
		fmt.Fprintf(out, "%s user %s: organization %s (%s)\n", reg, c.UserID, o.ID, o.Status)
		res.Created++
	}
	return res
}

func run(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("org-backfill-individuals", flag.ContinueOnError)
	dryRun := fl.Bool("dry-run", false, "list the accounts that would get an organization and change nothing")
	only := fl.String("region", "", "only this region (for example uk)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("load .env: %w", err)
	}
	ctx := context.Background()
	dbURL := os.Getenv("DB_URL_GLOBAL")
	if dbURL == "" {
		return errors.New("DB_URL_GLOBAL is required")
	}
	global, err := db.NewGlobalPool(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect to global database: %w", err)
	}
	defer global.Close()
	cfgs, err := dbrouter.LoadConfigsFromEnv()
	if err != nil {
		return fmt.Errorf("load regional database config: %w", err)
	}
	router, err := dbrouter.New(ctx, cfgs)
	if err != nil {
		return fmt.Errorf("connect regional databases: %w", err)
	}
	defer router.Close()

	slugs, err := countrySlugs(ctx, global)
	if err != nil {
		return err
	}
	queueFor, closeQueue, err := kybidentity.NewFactory(global)
	if err != nil {
		return err
	}
	defer closeQueue()
	store := &orgstore.Store{Regional: func(reg region.Region) (orgstore.DB, error) {
		p, perr := router.DB(reg)
		if perr != nil {
			return nil, perr
		}
		return p, nil
	}}

	failed := false
	for _, reg := range dbrouter.Regions() {
		if *only != "" && !strings.EqualFold(*only, string(reg)) {
			continue
		}
		pool, perr := router.DB(reg)
		if perr != nil {
			fmt.Fprintf(out, "%s: skipped: %v\n", reg, perr)
			continue
		}
		cands, cerr := Candidates(ctx, pool)
		if cerr != nil {
			fmt.Fprintf(out, "%s: %v\n", reg, cerr)
			failed = true
			continue
		}
		var q orgstore.CaseQueue
		if cq, ok := queueFor(string(reg)).(kyb.CaseQueue); ok {
			q = cq
		} else {
			return errors.New("the review queue adapter cannot open individual cases")
		}
		res := Backfill(ctx, out, reg, cands, slugs, *dryRun, store, q)
		fmt.Fprintf(out, "%s: found %d, created %d, skipped %d, errors %d (dry-run=%v)\n",
			reg, res.Found, res.Created, res.Skipped, res.Errors, *dryRun)
		failed = failed || res.Errors > 0
	}
	if failed {
		return errors.New("org-backfill-individuals: one or more regions failed")
	}
	return nil
}

func countrySlugs(ctx context.Context, p Pool) (map[string]string, error) {
	rows, err := p.Query(ctx, `SELECT id::text, slug FROM waitlist.countries`)
	if err != nil {
		return nil, fmt.Errorf("read countries: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, slug string
		if err = rows.Scan(&id, &slug); err != nil {
			return nil, err
		}
		out[id] = slug
	}
	return out, rows.Err()
}
