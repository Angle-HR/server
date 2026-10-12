package hiringtypes

import "testing"

func TestCursorRoundTrip(t *testing.T) {
	at, id := "2026-10-08T09:25:09.519739Z", "00000000-0000-4000-8000-000000000001"
	gotAt, gotID, err := DecodeCursor(EncodeCursor(at, id))
	if err != nil || gotAt != at || gotID != id {
		t.Fatalf("round trip: %q %q %v", gotAt, gotID, err)
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	for _, c := range []string{"", "!!!", EncodeCursor("not-a-time", "00000000-0000-4000-8000-000000000001"),
		EncodeCursor("2026-10-08T09:25:09Z", "not-a-uuid"), EncodeCursor("", "")} {
		if _, _, err := DecodeCursor(c); err == nil {
			t.Errorf("cursor %q accepted", c)
		}
	}
}

func TestStaleRevisionMessage(t *testing.T) {
	if (&StaleRevisionError{Current: 4}).Error() == "" {
		t.Fatal("empty message")
	}
}
