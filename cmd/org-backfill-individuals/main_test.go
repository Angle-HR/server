package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Angle-HR/server/internal/orgstore"
	"github.com/Angle-HR/server/internal/region"
)

type fakeEnsurer struct {
	calls []orgstore.Personal
	fail  string
}

func (f *fakeEnsurer) EnsurePersonal(_ context.Context, _ region.Region, p orgstore.Personal, _ orgstore.CaseQueue) (*orgstore.PersonalOrg, error) {
	if p.UserID == f.fail {
		return nil, errors.New("boom")
	}
	f.calls = append(f.calls, p)
	return &orgstore.PersonalOrg{ID: "org-" + p.UserID, Status: "pending"}, nil
}

func cands() []candidate {
	return []candidate{
		{orgstore.Personal{UserID: "u1", Name: "Ada Lovelace", CountryID: "c1"}},
		{orgstore.Personal{UserID: "u2", Name: "", CountryID: "c1"}},
		{orgstore.Personal{UserID: "u3", Name: "Bo Peep", CountryID: "c2"}},
	}
}

func TestBackfill_dryRunWritesNothing(t *testing.T) {
	f := &fakeEnsurer{}
	var out bytes.Buffer
	res := Backfill(context.Background(), &out, region.RegionUK, cands(), map[string]string{"c1": "nigeria"}, true, f, nil)
	if len(f.calls) != 0 || res.Created != 0 || res.Found != 3 || res.Skipped != 1 {
		t.Fatalf("dry run must not write: %+v calls %v", res, f.calls)
	}
	if strings.Contains(out.String(), "Ada") || strings.Contains(out.String(), "Lovelace") {
		t.Fatalf("names must not be printed: %s", out.String())
	}
}

func TestBackfill_createsSkipsAndCountsErrors(t *testing.T) {
	f := &fakeEnsurer{fail: "u3"}
	var out bytes.Buffer
	res := Backfill(context.Background(), &out, region.RegionUK, cands(), map[string]string{"c1": "nigeria"}, false, f, nil)
	if res.Created != 1 || res.Skipped != 1 || res.Errors != 1 {
		t.Fatalf("res = %+v", res)
	}
	if f.calls[0].CountrySlug != "nigeria" {
		t.Fatalf("slug not resolved: %+v", f.calls[0])
	}
}
