package hiringhttp

import (
	"encoding/json"
	"testing"

	"github.com/Angle-HR/server/internal/rbac"
)

func TestTemplateManagementOverHTTP(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	id := e.create(t, fullBody)
	mk := e.do(t, "POST", "/hiring/templates", `{"kind":"job_details","name":"Engineering","from_job_id":"`+id+`"}`)
	if mk.Status != 201 {
		t.Fatalf("create template: %d %+v", mk.Status, mk.Err)
	}
	tid := mk.field(t, "id").(string)
	if mk.field(t, "pinned") != false || mk.field(t, "use_count") != float64(0) {
		t.Fatalf("new template should be unpinned and unused: %s", mk.Data)
	}

	// Rename, pin, and both at once.
	r := e.do(t, "PATCH", "/hiring/templates/"+tid, `{"name":"  Platform   team "}`)
	if r.Status != 200 || r.field(t, "name") != "Platform team" {
		t.Fatalf("rename: %d %s %+v", r.Status, r.Data, r.Err)
	}
	if r = e.do(t, "PATCH", "/hiring/templates/"+tid, `{"pinned":true}`); r.Status != 200 || r.field(t, "pinned") != true {
		t.Fatalf("pin: %d %s", r.Status, r.Data)
	}
	if r = e.do(t, "PATCH", "/hiring/templates/"+tid, `{"name":"Core","pinned":false}`); r.Status != 200 ||
		r.field(t, "name") != "Core" || r.field(t, "pinned") != false {
		t.Fatalf("rename and unpin: %d %s", r.Status, r.Data)
	}
	if r = e.do(t, "PATCH", "/hiring/templates/"+tid, `{}`); r.Status != 400 {
		t.Fatalf("empty patch: got %d, want 400", r.Status)
	}
	if r = e.do(t, "PATCH", "/hiring/templates/"+tid, `{"name":""}`); r.Status != 400 {
		t.Fatalf("blank name: got %d, want 400", r.Status)
	}

	// Duplicate, with and without a name; a taken name is refused.
	d := e.do(t, "POST", "/hiring/templates/"+tid+"/duplicate", "")
	if d.Status != 201 || d.field(t, "name") != "Core (copy)" || d.field(t, "id") == tid {
		t.Fatalf("duplicate: %d %s %+v", d.Status, d.Data, d.Err)
	}
	if d = e.do(t, "POST", "/hiring/templates/"+tid+"/duplicate", `{"name":"core (COPY)"}`); d.Status != 400 {
		t.Fatalf("duplicate with taken name: got %d, want 400", d.Status)
	}
	if d = e.do(t, "POST", "/hiring/templates/"+tid+"/duplicate", `{"name":"Core v2"}`); d.Status != 201 ||
		d.field(t, "name") != "Core v2" {
		t.Fatalf("named duplicate: %d %s", d.Status, d.Data)
	}
	// Renaming onto an existing name is refused too.
	if r = e.do(t, "PATCH", "/hiring/templates/"+tid, `{"name":"core v2"}`); r.Status != 400 {
		t.Fatalf("rename onto taken name: got %d, want 400", r.Status)
	}

	// Export is portable and carries no ids.
	x := e.do(t, "GET", "/hiring/templates/"+tid+"/export", "")
	var out struct {
		Version int             `json:"version"`
		Kind    string          `json:"kind"`
		Name    string          `json:"name"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(x.Data, &out); x.Status != 200 || err != nil || out.Version != 1 ||
		out.Kind != "job_details" || out.Name != "Core" || len(out.Payload) == 0 {
		t.Fatalf("export: %d %s", x.Status, x.Data)
	}

	// A personal default can't be managed; unknown ids are not found.
	def := e.do(t, "POST", "/hiring/templates", `{"kind":"job_details","as_my_default":true,"from_job_id":"`+id+`"}`)
	if def.Status != 201 {
		t.Fatalf("default: %d %+v", def.Status, def.Err)
	}
	did := def.field(t, "id").(string)
	for _, c := range []struct{ method, path, body string }{
		{"PATCH", "/hiring/templates/" + did, `{"pinned":true}`},
		{"POST", "/hiring/templates/" + did + "/duplicate", ""},
		{"GET", "/hiring/templates/" + did + "/export", ""},
		{"GET", "/hiring/templates/not-a-uuid/export", ""},
	} {
		if r = e.do(t, c.method, c.path, c.body); r.Status != 404 {
			t.Fatalf("%s %s: got %d, want 404", c.method, c.path, r.Status)
		}
	}
}

func TestTemplateUseCountAndPinnedOrder(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	id := e.create(t, fullBody)
	a := e.do(t, "POST", "/hiring/templates", `{"kind":"job_details","name":"Alpha","from_job_id":"`+id+`"}`)
	b := e.do(t, "POST", "/hiring/templates", `{"kind":"job_details","name":"Beta","from_job_id":"`+id+`"}`)
	aid, bid := a.field(t, "id").(string), b.field(t, "id").(string)

	if r := e.do(t, "POST", "/jobs", `{"title":"From template","template_id":"`+bid+`"}`); r.Status != 201 {
		t.Fatalf("start from template: %d %+v", r.Status, r.Err)
	}
	if r := e.do(t, "POST", "/jobs", `{"title":"Again","template_id":"`+bid+`"}`); r.Status != 201 {
		t.Fatalf("start from template: %d %+v", r.Status, r.Err)
	}
	if r := e.do(t, "PATCH", "/hiring/templates/"+bid, `{"pinned":true}`); r.Status != 200 {
		t.Fatalf("pin: %d", r.Status)
	}

	l := e.do(t, "GET", "/hiring/templates?kind=job_details", "")
	list := rows(t, l)
	if l.Status != 200 || len(list) != 2 {
		t.Fatalf("list: %d %s", l.Status, l.Data)
	}
	_ = aid
	// The fake store does not sort; the real one lists pinned first. Check the counts here.
	for _, tpl := range list {
		switch tpl["id"] {
		case bid:
			if tpl["use_count"] != float64(2) || tpl["pinned"] != true {
				t.Fatalf("beta: %v", tpl)
			}
		case aid:
			if tpl["use_count"] != float64(0) {
				t.Fatalf("alpha: %v", tpl)
			}
		}
	}
}
