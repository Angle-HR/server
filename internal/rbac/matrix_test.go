package rbac

import (
	"os"
	"regexp"
	"sort"
	"strings"
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

// The Go matrix must equal the SQL seed: the inserts of 000004, then the renames and inserts of 000008.
func TestMatrixMatchesSQLSeed(t *testing.T) {
	base, err := os.ReadFile("../../db/migrations/global_registry/000004_rbac_role_permissions.sql")
	if err != nil {
		t.Skip("seed file not found: ", err)
	}
	p2, err := os.ReadFile("../../db/migrations/global_registry/000008_rbac_phase2_permissions.sql")
	if err != nil {
		t.Skip("phase 2 seed file not found: ", err)
	}
	pair := regexp.MustCompile(`\('([a-z_0-9]+)', '([a-z_.0-9]+)'\)`)
	grants := map[string]bool{}
	for _, m := range pair.FindAllStringSubmatch(string(base), -1) {
		grants[m[1]+"|"+m[2]] = true
	}
	// Only the Up section of 000008: renames first, then inserts.
	up := strings.SplitN(string(p2), "-- +goose Down", 2)[0]
	rename := regexp.MustCompile(`SET permission = '([a-z_.0-9]+)'\s+WHERE permission = '([a-z_.0-9]+)'`)
	for _, m := range rename.FindAllStringSubmatch(up, -1) {
		for k := range grants {
			if strings.HasSuffix(k, "|"+m[2]) {
				delete(grants, k)
				grants[strings.TrimSuffix(k, m[2])+m[1]] = true
			}
		}
	}
	del := regexp.MustCompile(`DELETE FROM rbac.role_permissions WHERE role = '([a-z_0-9]+)' AND permission = '([a-z_.0-9]+)'`)
	for _, m := range del.FindAllStringSubmatch(up, -1) {
		delete(grants, m[1]+"|"+m[2])
	}
	ins := up[strings.Index(up, "INSERT INTO"):]
	for _, m := range pair.FindAllStringSubmatch(ins, -1) {
		grants[m[1]+"|"+m[2]] = true
	}
	var sqlPairs, goPairs []string
	for k := range grants {
		sqlPairs = append(sqlPairs, k)
	}
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

func TestPhase2Grants(t *testing.T) {
	has := func(r Role, p Permission) bool { return NewSet(DefaultMatrix[r]...).Has(p) }
	if !has(RoleHR2, JobStatusChange) || has(RoleLineManager, JobStatusChange) {
		t.Error("job.status.change: HR 2 yes, line manager no")
	}
	if has(RoleHR2, JobExport) || has(RoleHR2, JobManageAny) || !has(RoleHR1, JobExport) || !has(RoleFounder, JobManageAny) {
		t.Error("export and manage_any are Founder and HR 1 only")
	}
	if !has(RoleHR2, JobCollaboratorAddLimited) || has(RoleHR2, "job.collaborator.add_restricted") ||
		has(RoleHR2, JobCollaboratorAdd) {
		t.Error("add_limited renamed and held by HR 2")
	}
	if has(RoleHR1, AccountOwnershipTransfer) || !has(RoleFounder, AccountOwnershipTransfer) {
		t.Error("only the Founder transfers ownership")
	}
	if !has(RoleFounder, AccountAgreementsAccept) || !has(RoleLegal, AccountAgreementsAccept) || has(RoleHR1, AccountAgreementsAccept) {
		t.Error("only Founder and Legal accept the DPA")
	}
}
