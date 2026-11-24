package admin

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
)

func TestNormalizeIP(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "",
		"192.168.65.1:64455":     "192.168.65.1",
		"192.168.65.1":           "192.168.65.1",
		"[2001:db8::1]:443":      "2001:db8::1",
		"2001:db8::1":            "2001:db8::1",
		"[::1]":                  "::1",
		"::ffff:10.0.0.7":        "10.0.0.7",
		"[::ffff:10.0.0.7]:5000": "10.0.0.7",
		"fe80::1%eth0":           "fe80::1",
		"not-an-ip":              "",
		"unix:":                  "",
	} {
		if got := NormalizeIP(in); got != want {
			t.Errorf("NormalizeIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWaitlistPermissionsRetired(t *testing.T) {
	b, err := os.ReadFile("../../db/migrations/global_registry/000015_remove_waitlist_permissions.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"waitlist:read", "waitlist:write", "DELETE FROM admin.permissions", "DELETE FROM admin.role_permissions"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("migration lacks %q", want)
		}
	}
}

func TestAuditIPMigrationShapes(t *testing.T) {
	b, err := os.ReadFile("../../db/migrations/global_registry/000016_audit_ip_bare.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "regexp_replace") {
		t.Error("migration must strip ports with anchored regexps")
	}
}

func TestWriteAuditStoresBareIP(t *testing.T) {
	m, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	actor := uuid.New()
	m.ExpectExec("INSERT INTO admin.audit_logs").
		WithArgs(actor, "x.y", "staff", pgxmock.AnyArg(), pgxmock.AnyArg(), ptr("192.168.65.1")).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("INSERT INTO admin.audit_logs").
		WithArgs(actor, "x.y", "staff", pgxmock.AnyArg(), pgxmock.AnyArg(), ptr("2001:db8::1")).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("INSERT INTO admin.audit_logs").
		WithArgs(actor, "x.y", "staff", pgxmock.AnyArg(), pgxmock.AnyArg(), (*string)(nil)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	s := NewStore(m)
	for _, ip := range []string{"192.168.65.1:64455", "[2001:db8::1]:443", "not-an-ip"} {
		if err = s.WriteAudit(context.Background(), actor, "x.y", "staff", "", nil, ip); err != nil {
			t.Fatal(err)
		}
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func ptr(s string) *string { return &s }
