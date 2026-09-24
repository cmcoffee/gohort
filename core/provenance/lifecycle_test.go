package provenance

import (
	"testing"
	"time"
)

func TestClassifyMemKind(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	day := func(y int, m time.Month, d int) string {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	}
	cases := []struct {
		note string
		kind MemKind
		date string // for events
	}{
		{"User just returned from a trip to Oregon (as of August 24, 2026)", MemKindEvent, day(2026, 8, 24)},
		{"Flew to Denver yesterday for a conference", MemKindEvent, day(2026, 9, 23)},
		{"Has a dentist appointment on 2026-10-02", MemKindEvent, day(2026, 10, 2)},
		{"Went to a concert on Dec 28", MemKindEvent, day(2025, 12, 28)}, // a December date written in September is last December only if > a month ahead
		{"Pending edits for the photo: new plate, beach background", MemKindOpenItem, ""},
		{"Wants to look into the backup schedule later", MemKindOpenItem, ""},
		{"Pending edits on the trip photos", MemKindOpenItem, ""}, // waiting work wins over the trip it mentions
		{"Prefers dark mode", MemKindFact, ""},
		{"Birthday is March 3", MemKindFact, ""},
		{"Runs a 5090 for the main model, updated 2026-09-05", MemKindFact, ""},
	}
	for _, c := range cases {
		kind, at := ClassifyMemKind(c.note, now)
		if kind != c.kind {
			t.Errorf("%q: kind = %v, want %v", c.note, kind, c.kind)
			continue
		}
		if c.date != "" && at.Format("2006-01-02") != c.date {
			t.Errorf("%q: date = %s, want %s", c.note, at.Format("2006-01-02"), c.date)
		}
	}
}

func TestParseEventDate(t *testing.T) {
	jan := time.Date(2027, 1, 5, 9, 0, 0, 0, time.UTC)
	if at, ok := ParseEventDate("back from the lake on Dec 28", jan); !ok || at.Year() != 2026 {
		t.Errorf("a late-December date written in January is last year's, got %v %v", at, ok)
	}
	if _, ok := ParseEventDate("Marco 12 said so", jan); ok {
		t.Error("a name that starts like a month is not a date")
	}
	if at, ok := ParseEventDate("24 August 2026", jan); !ok || at.Month() != time.August || at.Day() != 24 {
		t.Errorf("day-month-year: %v %v", at, ok)
	}
	if _, ok := ParseEventDate("no date here", jan); ok {
		t.Error("found a date in text with none")
	}
}
