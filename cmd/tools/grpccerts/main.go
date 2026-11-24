// Command grpccerts writes a throwaway development CA and one certificate per service for GRPC_AUTH=mtls.
//
//	go run ./cmd/tools/grpccerts -out deploy/local/certs [-services identity,org,...] [-days 30]
//
// Layout: <out>/ca.crt, ca.key and <out>/<service>/{tls.crt,tls.key,ca.crt}. Each certificate's common name is the
// service name and its DNS names are the service name and localhost. Not for production.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Angle-HR/server/pkg/rpc/devca"
)

func main() {
	out := flag.String("out", "deploy/local/certs", "output directory")
	svcs := flag.String("services", "identity,org,hiring,applications,notifications,server,web-bff,admin-bff,hiring-expire,hiring-outbox,tools", "comma separated service names")
	days := flag.Int("days", 30, "validity in days")
	flag.Parse()
	if err := run(*out, strings.Split(*svcs, ","), time.Duration(*days)*24*time.Hour); err != nil {
		fmt.Fprintln(os.Stderr, "grpccerts:", err)
		os.Exit(1)
	}
}

func write(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, mode)
}

func run(out string, services []string, ttl time.Duration) error {
	ca, err := devca.NewCA("openhr-dev-grpc-ca", ttl)
	if err != nil {
		return err
	}
	if err := write(filepath.Join(out, "ca.crt"), ca.PEM, 0o644); err != nil {
		return err
	}
	if err := write(filepath.Join(out, "ca.key"), ca.KeyPEM(), 0o600); err != nil {
		return err
	}
	for _, s := range services {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		crt, key, err := ca.Issue(s, []string{s, "localhost"}, ttl)
		if err != nil {
			return err
		}
		d := filepath.Join(out, s)
		// Readable by the non-root container user (uid 10001) through a read-only bind mount; the keys are throwaway.
		for name, b := range map[string][]byte{"tls.crt": crt, "tls.key": key, "ca.crt": ca.PEM} {
			if err := write(filepath.Join(d, name), b, 0o644); err != nil {
				return err
			}
		}
	}
	fmt.Printf("wrote CA and %d service certificates to %s (valid %d days)\n", len(services), out, int(ttl.Hours()/24))
	return nil
}
