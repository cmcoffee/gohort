package core

// The reaper deletes files. Every test here is about what it must NOT touch —
// that is the side where a mistake is unrecoverable.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReapTakesOnlyRecognizedArtifacts(t *testing.T) {
	dir := t.TempDir()
	old := 30 * 24 * time.Hour
	writeAged(t, filepath.Join(dir, "gen-abc123.png"), 100, old)
	writeAged(t, filepath.Join(dir, "video-xyz789.mp4"), 200, old)
	writeAged(t, filepath.Join(dir, "edit-def456.png"), 50, old)
	// Not ours: a person's file that happens to start the same way, a script,
	// and a name whose extension does not match its prefix.
	writeAged(t, filepath.Join(dir, "video-notes.txt"), 10, old)
	writeAged(t, filepath.Join(dir, "get_market_data.py"), 10, old)
	writeAged(t, filepath.Join(dir, "invoice.pdf"), 10, old)
	writeAged(t, filepath.Join(dir, "gen-.png"), 10, old) // nothing between prefix and ext

	got := scanReapable(dir, "alice", "", 14*24*time.Hour)
	names := map[string]bool{}
	for _, c := range got {
		names[c.Name] = true
	}
	if len(got) != 3 {
		t.Fatalf("selected %d file(s): %v", len(got), names)
	}
	for _, want := range []string{"gen-abc123.png", "video-xyz789.mp4", "edit-def456.png"} {
		if !names[want] {
			t.Errorf("%q was not selected", want)
		}
	}
	for _, mustKeep := range []string{"video-notes.txt", "get_market_data.py", "invoice.pdf", "gen-.png"} {
		if names[mustKeep] {
			t.Errorf("%q was selected for deletion", mustKeep)
		}
	}
}

// The casefile case, and the reason the scan uses ReadDir rather than WalkDir:
// a subdirectory is never entered, so nothing inside one can be selected even
// if it is named exactly like an artifact.
func TestReapNeverEntersSubdirectories(t *testing.T) {
	dir := t.TempDir()
	old := 60 * 24 * time.Hour
	writeAged(t, filepath.Join(dir, "casefile", "golabi_redacted.pdf"), 4096, old)
	writeAged(t, filepath.Join(dir, "casefile", "video-evidence.mp4"), 8192, old)
	writeAged(t, filepath.Join(dir, ".attachments", "gen-spill.png"), 2048, old)

	if got := scanReapable(dir, "alex", "", 14*24*time.Hour); len(got) != 0 {
		t.Fatalf("selected %d file(s) from subdirectories: %+v", len(got), got)
	}
}

func TestReapRespectsTheRetentionWindow(t *testing.T) {
	dir := t.TempDir()
	writeAged(t, filepath.Join(dir, "video-old.mp4"), 100, 20*24*time.Hour)
	writeAged(t, filepath.Join(dir, "video-new.mp4"), 100, 2*24*time.Hour)

	got := scanReapable(dir, "alice", "", 14*24*time.Hour)
	if len(got) != 1 || got[0].Name != "video-old.mp4" {
		t.Fatalf("window not respected: %+v", got)
	}
}

// A dry run that disagrees with the run is worse than no dry run: it is
// permission granted for something else. Both use one walk.
func TestDryRunMatchesWhatIsRemoved(t *testing.T) {
	dir := t.TempDir()
	old := 30 * 24 * time.Hour
	writeAged(t, filepath.Join(dir, "gen-a.png"), 100, old)
	writeAged(t, filepath.Join(dir, "video-b.mp4"), 900, old)
	writeAged(t, filepath.Join(dir, "keep.pdf"), 500, old)

	planned := scanReapable(dir, "alice", "", 14*24*time.Hour)
	var plannedBytes int64
	for _, c := range planned {
		plannedBytes += c.Bytes
	}
	removed := 0
	var removedBytes int64
	for _, c := range planned {
		if err := os.Remove(c.Path); err == nil {
			removed++
			removedBytes += c.Bytes
		}
	}
	if removed != len(planned) || removedBytes != plannedBytes {
		t.Fatalf("removed %d/%d files, %d/%d bytes", removed, len(planned), removedBytes, plannedBytes)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.pdf")); err != nil {
		t.Error("a file outside the plan was removed")
	}
}

func TestReapReportNamesProducersAndProtection(t *testing.T) {
	out := FormatReapCandidates([]ReapCandidate{
		{Owner: "alice", Name: "video-a.mp4", Producer: "download_video", Bytes: 146 << 20, Age: 14 * 24 * time.Hour},
		{Owner: "alice", Name: "gen-b.png", Producer: "generate_image", Bytes: 512 << 10, Age: 30 * 24 * time.Hour},
	})
	for _, want := range []string{"download_video", "generate_image", "video-a.mp4", "casefile/"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
	if FormatReapCandidates(nil) != "" {
		t.Error("an empty plan must render empty")
	}
}
