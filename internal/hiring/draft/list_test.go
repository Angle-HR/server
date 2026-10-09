package draft

import (
	"context"
	"testing"

	"github.com/Angle-HR/server/internal/rbac"
)

func TestListFiltersNarrowTheList(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	owner := caller(userA, rbac.RoleFounder)
	remote := newDraft(t, s, owner,
		`{"title":"Remote cook","workplace_type":"remote","employment_type":"full_time","markets":[{"market_code":"UK"}]}`)
	newDraft(t, s, owner,
		`{"title":"Onsite chef","workplace_type":"onsite","employment_type":"contract"}`)

	cases := []struct {
		name string
		q    ListQuery
		want []string // job ids expected, empty means none
	}{
		{"workplace", ListQuery{WorkplaceType: "remote"}, []string{remote.ID}},
		{"employment", ListQuery{EmploymentType: "full_time"}, []string{remote.ID}},
		{"market is case insensitive", ListQuery{Market: "uk"}, []string{remote.ID}},
		{"creator", ListQuery{CreatedBy: userA}, nil},
		{"nobody created these", ListQuery{CreatedBy: userB}, []string{}},
	}
	for _, tc := range cases {
		items, _, err := s.List(ctx, owner, tc.q)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.want == nil {
			if len(items) != 2 {
				t.Fatalf("%s: got %d jobs, want both", tc.name, len(items))
			}
			continue
		}
		if len(items) != len(tc.want) {
			t.Fatalf("%s: got %d jobs, want %d", tc.name, len(items), len(tc.want))
		}
		for i, id := range tc.want {
			if items[i].ID != id {
				t.Fatalf("%s: got %s, want %s", tc.name, items[i].ID, id)
			}
		}
	}
}

func TestListRowCarriesWorkplaceTypeAndAlwaysHasManagers(t *testing.T) {
	s, _ := newService()
	owner := caller(userA, rbac.RoleFounder)
	newDraft(t, s, owner, `{"title":"Remote cook","workplace_type":"remote"}`)
	items, _, err := s.List(context.Background(), owner, ListQuery{})
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %v %v", items, err)
	}
	if items[0].WorkplaceType != "remote" {
		t.Fatalf("workplace type: %q", items[0].WorkplaceType)
	}
	if items[0].Managers == nil {
		t.Fatal("managers must be an empty list, not null")
	}
}

func TestListRejectsBadFiltersAndSort(t *testing.T) {
	s, _ := newService()
	owner := caller(userA, rbac.RoleFounder)
	_, _, err := s.List(context.Background(), owner, ListQuery{
		CreatedBy: "x", Assignee: "y", EmploymentType: "nope", WorkplaceType: "nope", LocationMode: "nope",
		Market: "UKK", CreatedFrom: "31/10/2026", CreatedTo: "tomorrow", Sort: "title", Order: "sideways",
	})
	for _, path := range []string{"created_by", "assignee", "employment_type", "workplace_type", "location_mode",
		"market", "created_from", "created_to", "sort", "order"} {
		wantField(t, err, path)
	}
}

func TestExportSelectedKeepsOnlyTheRequestedJobs(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	owner := caller(userA, rbac.RoleFounder)
	a := newDraft(t, s, owner, `{"title":"Cook"}`)
	newDraft(t, s, owner, `{"title":"Chef"}`)

	rows, err := s.ExportSelected(ctx, owner, nil, []string{a.ID})
	if err != nil || len(rows) != 1 || rows[0].ID != a.ID {
		t.Fatalf("selected export: %v %v", rows, err)
	}
	rows, err = s.ExportSelected(ctx, owner, nil, nil)
	if err != nil || len(rows) != 2 {
		t.Fatalf("full export: %v %v", rows, err)
	}
	if _, err = s.ExportSelected(ctx, owner, nil, []string{"nope"}); err == nil {
		t.Fatal("a bad id should be refused")
	}
	if _, err = s.ExportSelected(ctx, caller(userB, rbac.RoleHR2), nil, []string{a.ID}); !isForbidden(err) {
		t.Fatalf("export without permission: %v", err)
	}
}
