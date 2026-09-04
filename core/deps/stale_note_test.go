package deps

import (
	"strings"
	"testing"
	"time"
)

// The note has to name the version, its age, and what to do. An error that says
// only "out of date" sends the reader back to the log they were not reading.
func TestStaleNoteNamesTheVersionAgeAndFix(t *testing.T) {
	// Built from fixed inputs, not from whatever is installed: the first draft
	// skipped on a healthy host, so it asserted nothing on the machine where
	// someone had just run the update.
	note := staleNoteText("yt-dlp", "2026.07.04", "56 days old",
		"update with `sudo yt-dlp -U`")
	for _, want := range []string{"yt-dlp", "2026.07.04", "56 days old", "yt-dlp -U"} {
		if !strings.Contains(note, want) {
			t.Errorf("note is missing %q:\n%s", want, note)
		}
	}
	// A suffix, so the tool's own message still leads and keeps naming the URL.
	if !strings.HasPrefix(note, " (") {
		t.Errorf("note should append, not replace: %q", note)
	}
}

// Nothing to say about a tool that is absent, current, or not date-versioned.
// A note on every failure would be noise, and noise is what gets ignored.
func TestStaleNoteIsSilentWhenItHasNothingToAdd(t *testing.T) {
	if got := StaleNote("ffmpeg"); got != "" {
		t.Errorf("ffmpeg carries no staleAfter, so it can never be stale; got %q", got)
	}
	if got := StaleNote("no-such-binary"); got != "" {
		t.Errorf("unknown dependency should say nothing; got %q", got)
	}
}

// The staleness rule itself: yt-dlp releases roughly weekly and its extractors
// rot with the sites, which is why it is the one dependency with a deadline.
func TestDateVersionStaleReadsAYtDlpVersion(t *testing.T) {
	old := time.Now().AddDate(0, 0, -56).Format("2006.01.02")
	stale, note := dateVersionStale(old, 42*24*time.Hour)
	if !stale || !strings.Contains(note, "56 days old") {
		t.Errorf("a 56-day-old build should be stale at a 42-day threshold; got %v %q", stale, note)
	}
	fresh := time.Now().AddDate(0, 0, -3).Format("2006.01.02")
	if stale, _ := dateVersionStale(fresh, 42*24*time.Hour); stale {
		t.Error("a three-day-old build is current")
	}
	// Not every tool is date-versioned; ffmpeg's "6.1.1" must never parse as one.
	if stale, _ := dateVersionStale("6.1.1", 42*24*time.Hour); stale {
		t.Error("a semver build was read as a stale date")
	}
}

// The age of a date-stamped build must not depend on the hour it is asked or
// the zone it is asked from. A bare date carries no zone, so it is read as a
// LOCAL date; reading it as midnight UTC put the count a day out for part of
// every day — "57 days old" at 5pm and "56 days old" the same morning, for a
// build that aged not at all in between.
func TestAgeDoesNotDependOnTheHourOrTheZone(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo") // UTC+9
	if err != nil {
		t.Skip("zoneinfo unavailable")
	}
	la, err := time.LoadLocation("America/Los_Angeles") // UTC-7/-8
	if err != nil {
		t.Skip("zoneinfo unavailable")
	}

	// One build, dated 56 days before each "today", asked at four hours of the
	// day in three zones. Every answer must be 56.
	for _, zone := range []*time.Location{time.UTC, tokyo, la} {
		for _, hour := range []int{0, 3, 12, 23} {
			now := time.Date(2026, 9, 3, hour, 30, 0, 0, zone)
			version := now.AddDate(0, 0, -56).Format("2006.01.02")
			stale, note := dateVersionStaleAt(version, 42*24*time.Hour, now)
			if !stale {
				t.Errorf("%s %02d:30: a 56-day-old build is stale past 42 days", zone, hour)
				continue
			}
			if note != "56 days old" {
				t.Errorf("%s %02d:30: got %q, want \"56 days old\"", zone, hour, note)
			}
		}
	}
}

// The threshold itself. A build exactly at the line is not yet stale; one an
// hour past it is. Stated with a fixed clock so it means the same thing at
// every hour of the day.
func TestStaleThresholdBoundary(t *testing.T) {
	now := time.Date(2026, 9, 3, 17, 0, 0, 0, time.UTC)
	staleAfter := 42 * 24 * time.Hour

	// Dated exactly 42 days ago at midnight: 17 hours PAST the threshold.
	if stale, _ := dateVersionStaleAt(now.AddDate(0, 0, -42).Format("2006.01.02"), staleAfter, now); !stale {
		t.Error("42 days and 17 hours is past a 42-day threshold")
	}
	// Dated 42 days ago, asked at midnight: exactly on the line, not past it.
	midnight := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	if stale, _ := dateVersionStaleAt(midnight.AddDate(0, 0, -42).Format("2006.01.02"), staleAfter, midnight); stale {
		t.Error("exactly at the threshold is not yet past it")
	}
	if stale, _ := dateVersionStaleAt(now.AddDate(0, 0, -41).Format("2006.01.02"), staleAfter, now); stale {
		t.Error("41 days is inside a 42-day threshold")
	}
}

// A version stamped in a zone ahead of the reader's is not from the future in
// any way that matters: it reports as current, not as a negative age.
func TestATomorrowDatedBuildIsNotStale(t *testing.T) {
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	stale, note := dateVersionStaleAt(now.AddDate(0, 0, 1).Format("2006.01.02"), 42*24*time.Hour, now)
	if stale || note != "" {
		t.Errorf("a build dated tomorrow is current, got %v %q", stale, note)
	}
}
