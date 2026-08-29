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
