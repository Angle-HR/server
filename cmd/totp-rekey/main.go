// Command totp-rekey re-encrypts stored TOTP secrets under the current TOTP_ENCRYPTION_KEY. Run it once after
// rotating the key: set TOTP_ENCRYPTION_KEY to the new key and TOTP_ENCRYPTION_KEY_PREVIOUS to the old one, deploy,
// run this command, then drop TOTP_ENCRYPTION_KEY_PREVIOUS.
//
//	totp-rekey -dry-run     # report what would change, write nothing
//	totp-rekey              # re-encrypt
//
// It scans accounts.users in every regional database whose ANGLEHR_<REGION>_POSTGRES_DSN is set, and admin.users
// in the global database (DB_URL_GLOBAL) when that is set. Each row is updated
// with a compare-and-swap on the old ciphertext, so a concurrent enrol or disable is never overwritten. A secret no
// configured key can open is reported and left alone; the command then exits non-zero.
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

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/pkg/config"
	"github.com/Angle-HR/server/pkg/platform/secrets"
)

var regions = []string{"UK", "US", "AFRICA", "EU", "ASIA"}

func main() {
	log.SetFlags(0)
	dry := flag.Bool("dry-run", false, "report what would change without writing")
	flag.Parse()
	if err := run(context.Background(), *dry, os.Stdout); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dry bool, out io.Writer) error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("load .env: %w", err)
	}
	var sec secrets.Reader
	current := strings.TrimSpace(sec.Get("TOTP_ENCRYPTION_KEY"))
	if current == "" {
		return errors.New("TOTP_ENCRYPTION_KEY (the new key) is required")
	}
	if config.HardenedEnv(os.Getenv("APP_ENV")) && current == config.InsecureDevTOTPKey {
		return errors.New("refusing to re-encrypt under the insecure development key")
	}
	var previous []string
	for _, p := range strings.Split(sec.Get("TOTP_ENCRYPTION_KEY_PREVIOUS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			previous = append(previous, p)
		}
	}
	if err := sec.Err(); err != nil {
		return err
	}
	crypto, err := auth.NewTOTPCryptoKeys(current, previous)
	if err != nil {
		return err
	}

	failed := 0
	found := false
	for _, r := range regions {
		dsn := strings.TrimSpace(os.Getenv("ANGLEHR_" + r + "_POSTGRES_DSN"))
		if dsn == "" {
			continue
		}
		found = true
		conn, connErr := pgx.Connect(ctx, dsn)
		if connErr != nil {
			return fmt.Errorf("connect %s: %w", r, connErr)
		}
		st, runErr := rekey(ctx, &pgStore{conn: conn, table: "accounts.users"}, crypto, dry)
		_ = conn.Close(ctx)
		if runErr != nil {
			return fmt.Errorf("%s: %w", r, runErr)
		}
		fmt.Fprintf(out, "%s: scanned=%d already-current=%d rekeyed=%d skipped-changed=%d unreadable=%d dry-run=%v\n",
			r, st.Scanned, st.Current, st.Rekeyed, st.Raced, st.Unreadable, dry)
		failed += st.Unreadable
	}
	if dsn := strings.TrimSpace(sec.Get("DB_URL_GLOBAL")); dsn != "" {
		found = true
		conn, connErr := pgx.Connect(ctx, dsn)
		if connErr != nil {
			return fmt.Errorf("connect global: %w", connErr)
		}
		st, runErr := rekey(ctx, &pgStore{conn: conn, table: "admin.users"}, crypto, dry)
		_ = conn.Close(ctx)
		if runErr != nil {
			return fmt.Errorf("global (admin.users): %w", runErr)
		}
		fmt.Fprintf(out, "global admin.users: scanned=%d already-current=%d rekeyed=%d skipped-changed=%d unreadable=%d dry-run=%v\n",
			st.Scanned, st.Current, st.Rekeyed, st.Raced, st.Unreadable, dry)
		failed += st.Unreadable
	}
	if !found {
		return errors.New("no ANGLEHR_<REGION>_POSTGRES_DSN or DB_URL_GLOBAL set")
	}
	if failed > 0 {
		return fmt.Errorf("%d secret(s) could not be decrypted with any configured key", failed)
	}
	return nil
}

// stats counts what one database's pass did.
type stats struct{ Scanned, Current, Rekeyed, Raced, Unreadable int }

type secretRow struct{ ID, Secret string }

// store is the slice of the database rekey needs.
type store interface {
	Secrets(ctx context.Context) ([]secretRow, error)
	// Swap sets the new ciphertext only while the stored value still equals old; it reports whether a row changed.
	Swap(ctx context.Context, id, old, updated string) (bool, error)
}

func rekey(ctx context.Context, s store, crypto *auth.TOTPCrypto, dry bool) (stats, error) {
	var st stats
	rows, err := s.Secrets(ctx)
	if err != nil {
		return st, err
	}
	for _, row := range rows {
		st.Scanned++
		updated, changed, reErr := crypto.Reencrypt(row.Secret)
		switch {
		case reErr != nil:
			st.Unreadable++
			log.Printf("user %s: cannot decrypt TOTP secret: %v", row.ID, reErr)
		case !changed:
			st.Current++
		case dry:
			st.Rekeyed++
		default:
			ok, swapErr := s.Swap(ctx, row.ID, row.Secret, updated)
			if swapErr != nil {
				return st, swapErr
			}
			if ok {
				st.Rekeyed++
			} else {
				st.Raced++
			}
		}
	}
	return st, nil
}

// pgStore reads and swaps TOTP secrets of one users table (accounts.users or admin.users).
type pgStore struct {
	conn  *pgx.Conn
	table string
}

func (p *pgStore) Secrets(ctx context.Context) ([]secretRow, error) {
	rows, err := p.conn.Query(ctx,
		`SELECT id::text, totp_secret FROM `+p.table+` WHERE totp_secret IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []secretRow
	for rows.Next() {
		var r secretRow
		if err := rows.Scan(&r.ID, &r.Secret); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *pgStore) Swap(ctx context.Context, id, old, updated string) (bool, error) {
	tag, err := p.conn.Exec(ctx,
		`UPDATE `+p.table+` SET totp_secret = $3 WHERE id = $1::uuid AND totp_secret = $2`, id, old, updated)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
