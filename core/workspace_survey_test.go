package core

// The survey has to be right about two things a reaper would act on: how much
// is actually there, and which of it is regenerable. Getting the second wrong
// in either direction is worse than not surveying — under-count and a reaper
// deletes the only copy of something, over-count and it reports space it can't
// safely reclaim.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeAged(t *testing.T, path string, size int, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestSurveyBucketsByAgeAndFindsTheLargest(t *testing.T) {
	dir := t.TempDir()
	writeAged(t, filepath.Join(dir, "ancient.png"), 4096, 200*24*time.Hour)
	writeAged(t, filepath.Join(dir, "old.json"), 2048, 45*24*time.Hour)
	writeAged(t, filepath.Join(dir, "fresh.txt"), 128, time.Hour)
	writeAged(t, filepath.Join(dir, ".attachments", "spill.txt"), 8192, 100*24*time.Hour)

	u, ok := surveyOneWorkspace(dir, "alice", "", nil)
	if !ok {
		t.Fatal("survey found nothing")
	}
	if u.Files != 4 || u.Bytes != 4096+2048+128+8192 {
		t.Fatalf("files=%d bytes=%d", u.Files, u.Bytes)
	}
	// Newest is how long since anything was written — the signal for "abandoned".
	if u.Newest > 2*time.Hour {
		t.Errorf("newest = %v, want about an hour", u.Newest)
	}
	bands := map[string]int{}
	for _, b := range u.ByAge {
		bands[b.Label] = b.Files
	}
	if bands["over 180 days"] != 1 || bands["90-180 days"] != 1 || bands["30-90 days"] != 1 || bands["under 7 days"] != 1 {
		t.Errorf("age bands are wrong: %v", bands)
	}
	// Nested files count, and the largest list is sorted by size.
	if len(u.Largest) == 0 || u.Largest[0].Rel != filepath.Join(".attachments", "spill.txt") {
		t.Errorf("largest = %+v, want the nested 8k spill first", u.Largest)
	}
}

// A tool script is rewritten from the record on next dispatch, so it is
// reclaimable at the cost of one write — and must be counted apart from files
// that exist in one copy only.
func TestSurveySeparatesRegenerableScripts(t *testing.T) {
	dir := t.TempDir()
	writeAged(t, filepath.Join(dir, "get_market_data.py"), 1024, 30*24*time.Hour)
	writeAged(t, filepath.Join(dir, "report.pdf"), 2048, 30*24*time.Hour)

	u, _ := surveyOneWorkspace(dir, "alice", "", map[string]bool{"get_market_data.py": true})
	if u.Regenerable != 1 || u.RegenerableBytes != 1024 {
		t.Fatalf("regenerable = %d file(s) / %d bytes, want 1/1024", u.Regenerable, u.RegenerableBytes)
	}
	// The script must NOT also appear in the age bands, or the totals double-count.
	var banded int
	for _, b := range u.ByAge {
		banded += b.Files
	}
	if banded != 1 {
		t.Errorf("age bands hold %d file(s); the regenerable script should be excluded", banded)
	}
	for _, f := range u.Largest {
		if f.Rel == "get_market_data.py" {
			t.Error("a regenerable script is listed among the largest reclaimables")
		}
	}
}

func TestSurveyReportsNothingForAnEmptyTree(t *testing.T) {
	if _, ok := surveyOneWorkspace(t.TempDir(), "alice", "", nil); ok {
		t.Error("an empty workspace was reported as usage")
	}
	if FormatWorkspaceSurvey(nil) != "" {
		t.Error("an empty survey must render empty")
	}
}

func TestSurveyOutputNamesTheTotalAndTheScripts(t *testing.T) {
	out := FormatWorkspaceSurvey([]WorkspaceUsage{{
		Owner: "alice", Path: "/w/alice", Files: 2, Bytes: 3072,
		Regenerable: 1, RegenerableBytes: 1024, Newest: 48 * time.Hour,
		ByAge:   []WorkspaceAgeBand{{Label: "30-90 days", Files: 1, Bytes: 2048}},
		Largest: []WorkspaceFile{{Rel: "report.pdf", Bytes: 2048, Age: 40 * 24 * time.Hour}},
	}})
	for _, want := range []string{"alice", "report.pdf", "30-90 days", "rewrites on next dispatch", "Nothing was deleted"} {
		if !strings.Contains(out, want) {
			t.Errorf("survey output missing %q:\n%s", want, out)
		}
	}
}
