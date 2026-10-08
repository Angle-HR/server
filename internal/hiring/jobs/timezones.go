package jobs

import (
	"fmt"
	"sort"
	"time"
	_ "time/tzdata" // zone data travels with the binary so the list works in minimal containers
)

// Timezone is one IANA zone with its current UTC offset.
type Timezone struct {
	Name          string `json:"name"`
	Offset        string `json:"offset"`
	OffsetSeconds int    `json:"offset_seconds"`
}

var zoneSet = func() map[string]bool {
	m := make(map[string]bool, len(zoneNames))
	for _, n := range zoneNames {
		m[n] = true
	}
	return m
}()

// ValidTimezone reports whether name is one of the supported IANA zones.
func ValidTimezone(name string) bool { return zoneSet[name] }

// FormatOffset renders seconds east of UTC as +HH:MM.
func FormatOffset(seconds int) string {
	sign := '+'
	if seconds < 0 {
		sign = '-'
		seconds = -seconds
	}
	return fmt.Sprintf("%c%02d:%02d", sign, seconds/secondsPerHour, seconds%secondsPerHour/secondsPerMinute)
}

const (
	secondsPerMinute = 60
	secondsPerHour   = 60 * secondsPerMinute
)

// Timezones lists every supported zone with its offset at now, sorted by offset then name.
func Timezones(now time.Time) []Timezone {
	out := make([]Timezone, 0, len(zoneNames))
	for _, n := range zoneNames {
		loc, err := time.LoadLocation(n)
		if err != nil {
			continue
		}
		_, off := now.In(loc).Zone()
		out = append(out, Timezone{Name: n, Offset: FormatOffset(off), OffsetSeconds: off})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OffsetSeconds != out[j].OffsetSeconds {
			return out[i].OffsetSeconds < out[j].OffsetSeconds
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// UTCOffsets returns the distinct UTC offsets in zones, in ascending order.
func UTCOffsets(zones []Timezone) []Timezone {
	seen := map[int]bool{}
	var out []Timezone
	for _, z := range zones {
		if seen[z.OffsetSeconds] {
			continue
		}
		seen[z.OffsetSeconds] = true
		out = append(out, Timezone{Name: "UTC" + z.Offset, Offset: z.Offset, OffsetSeconds: z.OffsetSeconds})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OffsetSeconds < out[j].OffsetSeconds })
	return out
}
