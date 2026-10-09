package hiringhttp

import (
	"encoding/json"
	"testing"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/rbac"
)

func rows(t *testing.T, r reply) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := json.Unmarshal(r.Data, &out); err != nil {
		t.Fatalf("data is not a list: %s", r.Data)
	}
	return out
}

func TestListFiltersSortAndRowFieldsOverHTTP(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	e.create(t, `{"title":"Remote cook","workplace_type":"remote","employment_type":"full_time"}`)
	e.create(t, `{"title":"Onsite chef","workplace_type":"onsite","employment_type":"contract"}`)

	r := e.do(t, "GET", "/jobs?workplace_type=remote", "")
	list := rows(t, r)
	if r.Status != 200 || len(list) != 1 || list[0]["title"] != "Remote cook" || list[0]["workplace_type"] != "remote" {
		t.Fatalf("filtered list: %d %s", r.Status, r.Data)
	}
	if managers, ok := list[0]["managers"].([]any); !ok || len(managers) != 0 {
		t.Fatalf("managers should be an empty list: %s", r.Data)
	}
	if r = e.do(t, "GET", "/jobs?employment_type=contract&sort=created_at&order=asc", ""); r.Status != 200 ||
		len(rows(t, r)) != 1 {
		t.Fatalf("combined filters: %d %s", r.Status, r.Data)
	}
	for _, bad := range []string{"sort=title", "order=up", "workplace_type=moon", "created_from=yesterday", "assignee=x"} {
		if r = e.do(t, "GET", "/jobs?"+bad, ""); r.Status != 400 {
			t.Fatalf("%s: got %d, want 400", bad, r.Status)
		}
	}
}

func TestExportJSONAndSelectedIDs(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	id := e.create(t, `{"title":"Cook"}`)
	e.create(t, `{"title":"Chef"}`)

	r := e.do(t, "GET", "/jobs/export?format=json&ids="+id, "")
	if list := rows(t, r); r.Status != 200 || len(list) != 1 || list[0]["id"] != id {
		t.Fatalf("selected json export: %d %s", r.Status, r.Data)
	}
	if r = e.do(t, "GET", "/jobs/export?format=xml", ""); r.Status != 400 {
		t.Fatalf("bad format: %d", r.Status)
	}
	if r = e.do(t, "GET", "/jobs/export?ids=nope", ""); r.Status != 400 {
		t.Fatalf("bad ids: %d", r.Status)
	}
}

func TestMeShowsRolesAndPermissions(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	e.dir.caller.Roles = []string{"founder"}
	r := e.do(t, "GET", "/hiring/me", "")
	if r.Status != 200 {
		t.Fatalf("me: %d %+v", r.Status, r.Err)
	}
	var me struct {
		Roles       []string `json:"roles"`
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(r.Data, &me); err != nil {
		t.Fatal(err)
	}
	if len(me.Roles) != 1 || me.Roles[0] != "founder" {
		t.Fatalf("roles: %v", me.Roles)
	}
	found := false
	for _, p := range me.Permissions {
		if p == string(rbac.JobExport) {
			found = true
		}
	}
	if !found {
		t.Fatalf("founder should have job.export: %v", me.Permissions)
	}

	hr2 := newEnv(rbac.RoleHR2)
	r = hr2.do(t, "GET", "/hiring/me", "")
	if r.Status != 200 || string(r.Data) == "" {
		t.Fatalf("hr2 me: %d", r.Status)
	}
	if err := json.Unmarshal(r.Data, &me); err != nil {
		t.Fatal(err)
	}
	for _, p := range me.Permissions {
		if p == string(rbac.JobExport) {
			t.Fatalf("HR 2 must not have job.export: %v", me.Permissions)
		}
	}
}

func TestPeopleListNeedsCollaboratorPermission(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	e.store.People[userA] = hiringtypes.Member{UserID: userA, Name: "Ada Okafor", Email: "ada@acme.test"}
	e.store.People["aaaaaaaa-0000-4000-8000-000000000002"] = hiringtypes.Member{
		UserID: "aaaaaaaa-0000-4000-8000-000000000002", Name: "Ben Smith", Email: "ben@acme.test",
	}

	r := e.do(t, "GET", "/hiring/people", "")
	if list := rows(t, r); r.Status != 200 || len(list) != 2 {
		t.Fatalf("people: %d %s", r.Status, r.Data)
	}
	r = e.do(t, "GET", "/hiring/people?q=ben", "")
	if list := rows(t, r); len(list) != 1 || list[0]["name"] != "Ben Smith" {
		t.Fatalf("search: %s", r.Data)
	}

	emp := newEnv(rbac.RoleEmployee)
	if r = emp.do(t, "GET", "/hiring/people", ""); r.Status != 403 {
		t.Fatalf("employee people: %d", r.Status)
	}
}
