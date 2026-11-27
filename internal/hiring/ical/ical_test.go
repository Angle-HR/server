package ical

import (
	"strings"
	"testing"
	"time"
)

func TestCalendarInviteAndCancel(t *testing.T) {
	e := Event{UID: "abc", Sequence: 2, Start: time.Date(2026, 12, 3, 14, 0, 0, 0, time.FixedZone("x", 3600)), Duration: 45 * time.Minute,
		Summary: "Interview: Engineer, Acme; round 1", Location: "https://meet.example/x", Description: "Line one\nLine two",
		OrganizerEmail: "jobs@acme.test", OrganizerName: `Acme "HR"`, AttendeeEmail: "ada@x.test", AttendeeName: "Ada", Now: time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)}
	got := Calendar(e)
	for _, want := range []string{"METHOD:REQUEST\r\n", "UID:abc@openhr\r\n", "DTSTART:20261203T130000Z\r\n", "DTEND:20261203T134500Z\r\n", "SEQUENCE:2\r\n",
		"STATUS:CONFIRMED\r\n", `SUMMARY:Interview: Engineer\, Acme\; round 1`, `DESCRIPTION:Line one\nLine two`, `ORGANIZER;CN="Acme HR":mailto:jobs@acme.test`, "mailto:ada@x.test"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	e.Cancelled = true
	c := Calendar(e)
	if !strings.Contains(c, "METHOD:CANCEL\r\n") || !strings.Contains(c, "STATUS:CANCELLED\r\n") {
		t.Errorf("cancel:\n%s", c)
	}
}

func TestLongLinesFoldWithoutSplittingRunes(t *testing.T) {
	got := Calendar(Event{UID: "u", Start: time.Now(), Duration: time.Hour, Summary: strings.Repeat("é", 100), OrganizerEmail: "a@b.test", AttendeeEmail: "c@d.test"})
	for _, l := range strings.Split(got, "\r\n") {
		if len(l) > 75 {
			t.Errorf("line of %d octets: %q", len(l), l)
		}
	}
	if strings.Contains(got, "�") {
		t.Error("a rune was split")
	}
}

func TestInjectionIsStripped(t *testing.T) {
	got := Calendar(Event{UID: "u", Start: time.Now(), Duration: time.Hour, Summary: "x\r\nATTENDEE:mailto:evil@x.test", OrganizerEmail: "a@b.test\r\nX:y",
		AttendeeEmail: "c@d.test", Location: "l\nBEGIN:VEVENT"})
	if strings.Contains(got, "\r\nATTENDEE:mailto:evil") || strings.Contains(got, "\r\nBEGIN:VEVENT\r\nBEGIN") || strings.Contains(got, "\r\nX:y") {
		t.Errorf("injected lines:\n%s", got)
	}
}
