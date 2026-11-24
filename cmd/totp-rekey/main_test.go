package main

import (
	"context"
	"testing"

	"github.com/Angle-HR/server/internal/auth"
)

type fakeStore struct {
	rows  map[string]string
	race  string // id whose value changes under us
	swaps int
}

func (f *fakeStore) Secrets(context.Context) ([]secretRow, error) {
	var out []secretRow
	for id, s := range f.rows {
		out = append(out, secretRow{id, s})
	}
	return out, nil
}

func (f *fakeStore) Swap(_ context.Context, id, old, updated string) (bool, error) {
	if id == f.race || f.rows[id] != old {
		return false, nil
	}
	f.rows[id] = updated
	f.swaps++
	return true, nil
}

func TestRekey(t *testing.T) {
	oldC, _ := auth.NewTOTPCrypto("old-key")
	newOnly, _ := auth.NewTOTPCrypto("new-key")
	ring, _ := auth.NewTOTPCryptoKeys("new-key", []string{"old-key"})
	stranger, _ := auth.NewTOTPCrypto("stranger")

	mk := func() *fakeStore {
		a, _ := oldC.Encrypt("AAA")
		b, _ := oldC.Encrypt("BBB")
		c, _ := newOnly.Encrypt("CCC")
		d, _ := stranger.Encrypt("DDD")
		return &fakeStore{rows: map[string]string{"a": a, "b": b, "c": c, "d": d}, race: "b"}
	}

	dry := mk()
	st, err := rekey(context.Background(), dry, ring, true)
	if err != nil || st.Rekeyed != 2 || st.Current != 1 || st.Unreadable != 1 || dry.swaps != 0 {
		t.Fatalf("dry run: %+v swaps=%d err=%v", st, dry.swaps, err)
	}

	live := mk()
	st, err = rekey(context.Background(), live, ring, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Rekeyed != 1 || st.Raced != 1 || st.Current != 1 || st.Unreadable != 1 || live.swaps != 1 {
		t.Fatalf("live run: %+v", st)
	}
	if plain, err := newOnly.Decrypt(live.rows["a"]); err != nil || plain != "AAA" {
		t.Fatalf("a not readable with the new key alone: %q %v", plain, err)
	}
	// Idempotent: a second pass has nothing left to rekey except the raced row.
	st, _ = rekey(context.Background(), live, ring, false)
	if st.Rekeyed != 0 {
		t.Fatalf("second pass: %+v", st)
	}
}
