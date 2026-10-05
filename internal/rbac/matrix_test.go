package rbac

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

func TestMatrixNeverGrantsDenied(t *testing.T) {
	for role, perms := range DefaultMatrix {
		if len(NewSet(perms...)) != len(perms) {
			t.Errorf("%s is granted a never-granted permission", role)
		}
	}
}

func TestMatrixSeparationOfDuties(t *testing.T) {
	founder := NewSet(DefaultMatrix[RoleFounder]...)
	legal := NewSet(DefaultMatrix[RoleLegal]...)
	hr1 := NewSet(DefaultMatrix[RoleHR1]...)
	for _, p := range []Permission{FormEDIModuleEnable, FormAutomatedScreening, FormHealthQuestionAdd, JobRetentionSet} {
		if founder.Has(p) {
			t.Errorf("founder must not hold %s", p)
		}
		if !legal.Has(p) {
			t.Errorf("legal must hold %s", p)
		}
	}
	if hr1.Has("form.adjustments.view_health") {
		t.Error("HR must not see health detail")
	}
	if !NewSet(DefaultMatrix[RoleHR2]...).Has(JobCreate) || NewSet(DefaultMatrix[RoleHR2]...).Has(JobPublishExternal) {
		t.Error("HR 2 can create but not publish")
	}
}

// The Go matrix and the SQL seed must describe the same grants.
func TestMatrixMatchesSQLSeed(t *testing.T) {
	b, err := os.ReadFile("../../db/migrations/global_registry/000004_rbac_role_permissions.sql")
	if err != nil {
		t.Skip("seed file not found: ", err)
	}
	matches := regexp.MustCompile(`\('([a-z_0-9]+)', '([a-z_.0-9]+)'\)`).FindAllStringSubmatch(string(b), -1)
	sqlPairs := make([]string, 0, len(matches))
	for _, m := range matches {
		sqlPairs = append(sqlPairs, m[1]+"|"+m[2])
	}
	var goPairs []string
	for r, ps := range DefaultMatrix {
		for _, p := range ps {
			goPairs = append(goPairs, string(r)+"|"+string(p))
		}
	}
	sort.Strings(sqlPairs)
	sort.Strings(goPairs)
	if len(sqlPairs) != len(goPairs) {
		t.Fatalf("sql has %d grants, go has %d", len(sqlPairs), len(goPairs))
	}
	for i := range sqlPairs {
		if sqlPairs[i] != goPairs[i] {
			t.Fatalf("mismatch: sql %s vs go %s", sqlPairs[i], goPairs[i])
		}
	}
}
