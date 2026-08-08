package query

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestListActiveCountriesSQL(t *testing.T) {
	t.Parallel()

	sql, args, err := ListActiveCountries()
	if err != nil {
		t.Fatalf("ListActiveCountries: %v", err)
	}

	if len(args) != 1 || args[0] != true {
		t.Fatalf("args: got %v", args)
	}

	if !strings.Contains(sql, `SELECT "id", "name", "slug", "region", "icon_key" FROM "countries"`) {
		t.Fatalf("sql: %q", sql)
	}
	if !strings.Contains(sql, `"is_active" = $1`) {
		t.Fatalf("sql: %q", sql)
	}
}

func TestLookupCountryByIDSQL(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("a1b2c3d4-e5f6-4789-a012-3456789abcde")
	sql, args, err := LookupCountryByID(id)
	if err != nil {
		t.Fatalf("LookupCountryByID: %v", err)
	}

	if len(args) != 2 || args[0] != id || args[1] != true {
		t.Fatalf("args: got %v", args)
	}

	if !strings.Contains(sql, `"id" = $1 AND "is_active" = $2`) {
		t.Fatalf("sql: %q", sql)
	}
}

func TestInsertWaitlistSignupSQL(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("a1b2c3d4-e5f6-4789-a012-3456789abcde")
	sql, args, err := InsertWaitlistSignup("Jerry", "jane@acme.com", id, "uk", "explicit", []byte("{}"))
	if err != nil {
		t.Fatalf("InsertWaitlistSignup: %v", err)
	}

	if len(args) != 6 {
		t.Fatalf("args: got %v", args)
	}

	if !strings.Contains(sql, `INSERT INTO "waitlist"`) {
		t.Fatalf("sql: %q", sql)
	}
	if !strings.Contains(sql, `ON CONFLICT ("email") DO NOTHING RETURNING "uuid"`) {
		t.Fatalf("sql: %q", sql)
	}
}

func TestInsertWaitlistRegistrySQL(t *testing.T) {
	t.Parallel()

	token := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	sql, args, err := InsertWaitlistRegistry("jane@acme.com", "uk", "explicit", token)
	if err != nil {
		t.Fatalf("InsertWaitlistRegistry: %v", err)
	}

	if len(args) != 4 {
		t.Fatalf("args: got %v", args)
	}

	if !strings.Contains(sql, `INSERT INTO "registry"`) {
		t.Fatalf("sql: %q", sql)
	}
	if !strings.Contains(sql, `ON CONFLICT ("email") DO NOTHING`) {
		t.Fatalf("sql: %q", sql)
	}
	if !strings.Contains(sql, `"waitlist_token"`) {
		t.Fatalf("sql: %q", sql)
	}
}

func TestListActiveIndustriesSQL(t *testing.T) {
	t.Parallel()

	sql, args, err := ListActiveIndustries()
	if err != nil {
		t.Fatalf("ListActiveIndustries: %v", err)
	}

	if len(args) != 1 || args[0] != true {
		t.Fatalf("args: got %v", args)
	}

	if !strings.Contains(sql, `FROM "industries"`) {
		t.Fatalf("sql: %q", sql)
	}
}

func TestListActiveRolesSQL(t *testing.T) {
	t.Parallel()

	sql, args, err := ListActiveRoles()
	if err != nil {
		t.Fatalf("ListActiveRoles: %v", err)
	}

	if len(args) != 1 || args[0] != true {
		t.Fatalf("args: got %v", args)
	}

	// Must be schema-qualified so admin.roles (earlier in search_path) is not used.
	if !strings.Contains(sql, `FROM waitlist.roles`) {
		t.Fatalf("sql: %q", sql)
	}
	if !strings.Contains(sql, `emoji`) {
		t.Fatalf("sql: %q", sql)
	}
}

func TestListBusinessTypesSQL(t *testing.T) {
	t.Parallel()

	sql, args, err := ListBusinessTypes()
	if err != nil {
		t.Fatalf("ListBusinessTypes: %v", err)
	}

	if len(args) != 1 || args[0] != true {
		t.Fatalf("args: got %v", args)
	}

	if !strings.Contains(sql, `FROM "business_types"`) {
		t.Fatalf("sql: %q", sql)
	}
}

func TestInsertAccountUserSQL(t *testing.T) {
	t.Parallel()

	sql, args, err := InsertAccountUser("jerry@example.com", "hash")
	if err != nil {
		t.Fatalf("InsertAccountUser: %v", err)
	}

	if len(args) != 2 {
		t.Fatalf("args: got %v", args)
	}

	if !strings.Contains(sql, `INSERT INTO "users"`) {
		t.Fatalf("sql: %q", sql)
	}
}
