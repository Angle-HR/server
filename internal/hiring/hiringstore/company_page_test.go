package hiringstore

import (
	"errors"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/public"
)

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 30, 15, 123456000, time.UTC)
	id := "0b7d5d4e-7a54-4f0a-9d51-6a3f5f6e1c11"
	gotT, gotID, err := decodeCursor(encodeCursor(at, id))
	if err != nil {
		t.Fatal(err)
	}
	if !gotT.Equal(at) || gotID != id {
		t.Errorf("got %v %s", gotT, gotID)
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	for _, c := range []string{"!!!", "bm9waXBl", "", "MjAyNi0xMS0yMFQwOTozMDoxNVp8c2hvcnQ"} {
		if _, _, err := decodeCursor(c); !errors.Is(err, public.ErrBadQuery) {
			t.Errorf("%q: got %v", c, err)
		}
	}
}

func TestLikeEscape(t *testing.T) {
	if got := likeEscape(`50%_off\`); got != `50\%\_off\\` {
		t.Errorf("got %q", got)
	}
}
