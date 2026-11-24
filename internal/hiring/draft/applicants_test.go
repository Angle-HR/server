package draft

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Angle-HR/server/pkg/rbac"
)

type fakeApplicants struct {
	calls  [][]string
	counts map[string]ApplicantCount
	err    error
}

func (f *fakeApplicants) ApplicantCounts(_ context.Context, _ Caller, ids []string) (map[string]ApplicantCount, error) {
	f.calls = append(f.calls, ids)
	return f.counts, f.err
}

func TestListFillsApplicantCountsInOneBatch(t *testing.T) {
	s, _ := newService()
	c := caller(userA, rbac.RoleFounder)
	a := newDraft(t, s, c, `{"title":"Cook"}`)
	b := newDraft(t, s, c, `{"title":"Baker"}`)
	fa := &fakeApplicants{counts: map[string]ApplicantCount{a.ID: {Total: 5, New: 2}}}
	s.Applicants = fa
	items, _, err := s.List(context.Background(), c, ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fa.calls) != 1 || len(fa.calls[0]) != 2 {
		t.Fatalf("want one batched call for both jobs, got %v", fa.calls)
	}
	for _, it := range items {
		switch it.ID {
		case a.ID:
			if !it.ApplicantCountsAvailable || *it.ApplicantCount != 5 || *it.NewApplicantCount != 2 {
				t.Fatalf("counts not filled: %+v", it)
			}
		case b.ID:
			if !it.ApplicantCountsAvailable || *it.ApplicantCount != 0 || *it.NewApplicantCount != 0 {
				t.Fatalf("job without a row must be zero: %+v", it)
			}
		}
	}
}

func TestListSurvivesApplicantCountsFailure(t *testing.T) {
	s, _ := newService()
	c := caller(userA, rbac.RoleFounder)
	newDraft(t, s, c, `{"title":"Cook"}`)
	s.Applicants = &fakeApplicants{err: errors.New("applications down")}
	items, _, err := s.List(context.Background(), c, ListQuery{})
	if err != nil || len(items) != 1 {
		t.Fatalf("list must still work: items=%d err=%v", len(items), err)
	}
	it := items[0]
	if it.ApplicantCount != nil || it.NewApplicantCount != nil || it.ApplicantCountsAvailable {
		t.Fatalf("counts must be unavailable, not zero: %+v", it)
	}
	raw, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["applicant_count"]; !ok || v != nil || m["applicant_counts_available"] != false {
		t.Fatalf("json = %s", raw)
	}
}
