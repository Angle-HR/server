// Package ical writes the calendar event that goes with an interview email (RFC 5545). Only what an invite needs:
// one VEVENT with a method (REQUEST to invite or update, CANCEL to cancel).
package ical

import (
	"fmt"
	"strings"
	"time"
)

// Event is an interview as a calendar event.
type Event struct {
	// UID is stable for the interview, so an update or a cancellation replaces the invite already sent.
	UID      string
	Sequence int
	Start    time.Time
	Duration time.Duration
	Summary  string
	Location string
	// Description is plain text.
	Description string
	// OrganizerEmail receives the candidate's reply to the invite. Required.
	OrganizerEmail string
	OrganizerName  string
	AttendeeEmail  string
	AttendeeName   string
	Cancelled      bool
	Now            time.Time
}

const stamp = "20060102T150405Z"

// escape escapes a TEXT value (RFC 5545 3.3.11).
func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, "\r\n", `\n`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return strings.ReplaceAll(s, "\r", "")
}

// param makes a value safe inside a quoted parameter.
func param(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// addr makes an email address safe to put in a mailto: value.
func addr(s string) string {
	return strings.Map(func(r rune) rune {
		if r <= 0x20 || r == 0x7f || r == '<' || r == '>' || r == ';' || r == ',' || r == '"' {
			return -1
		}
		return r
	}, s)
}

// fold wraps a content line at 75 octets, continuing on lines that start with a space, without splitting a rune.
func fold(line string) string {
	if len(line) <= 75 {
		return line
	}
	var b strings.Builder
	n := 0
	for _, r := range line {
		w := len(string(r))
		if n+w > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += w
	}
	return b.String()
}

// Calendar returns the iCalendar text for the event, with CRLF line ends.
func Calendar(e Event) string {
	method, status := "REQUEST", "CONFIRMED"
	if e.Cancelled {
		method, status = "CANCEL", "CANCELLED"
	}
	now := e.Now
	if now.IsZero() {
		now = time.Now()
	}
	lines := []string{
		"BEGIN:VCALENDAR",
		"PRODID:-//OpenHR//Interviews//EN",
		"VERSION:2.0",
		"CALSCALE:GREGORIAN",
		"METHOD:" + method,
		"BEGIN:VEVENT",
		"UID:" + e.UID + "@openhr",
		"DTSTAMP:" + now.UTC().Format(stamp),
		"DTSTART:" + e.Start.UTC().Format(stamp),
		"DTEND:" + e.Start.Add(e.Duration).UTC().Format(stamp),
		fmt.Sprintf("SEQUENCE:%d", e.Sequence),
		"STATUS:" + status,
		"SUMMARY:" + escape(e.Summary),
	}
	if e.Location != "" {
		lines = append(lines, "LOCATION:"+escape(e.Location))
	}
	if e.Description != "" {
		lines = append(lines, "DESCRIPTION:"+escape(e.Description))
	}
	lines = append(lines, fmt.Sprintf(`ORGANIZER;CN="%s":mailto:%s`, param(e.OrganizerName), addr(e.OrganizerEmail)))
	lines = append(lines, fmt.Sprintf(`ATTENDEE;CN="%s";ROLE=REQ-PARTICIPANT;RSVP=TRUE:mailto:%s`, param(e.AttendeeName), addr(e.AttendeeEmail)))
	lines = append(lines, "END:VEVENT", "END:VCALENDAR")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(fold(l))
		b.WriteString("\r\n")
	}
	return b.String()
}
