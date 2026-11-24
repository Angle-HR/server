package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

type fakeStore struct{ fail map[string]bool }

func (f fakeStore) TenantIDs(context.Context) ([]string, error) { return []string{"a", "b", "c"}, nil }
func (f fakeStore) EnqueueLiveJobEvents(_ context.Context, id string) (int, error) {
	if f.fail[id] {
		return 0, errors.New("boom")
	}
	return 2, nil
}

func TestBackfillRegionContinuesPastAFailingCompany(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	n, err := backfillRegion(context.Background(), log, "uk", fakeStore{fail: map[string]bool{"b": true}})
	if n != 4 || err == nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	n, err = backfillRegion(context.Background(), log, "uk", fakeStore{})
	if n != 6 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
