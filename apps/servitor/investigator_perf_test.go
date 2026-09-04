package servitor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Every section must ride in exactly one batch, and every batch number must
// be one runQuickSnapshot actually executes — a section assigned to batch 3
// would silently never run.
func TestSnapshotBatchesCoverEverySection(t *testing.T) {
	covered := make(map[int]int)
	for b := 0; b < snapshotBatches; b++ {
		script := snapshotScript(b)
		for i := range snapshotSections {
			if strings.Contains(script, fmt.Sprintf("'%s%d'", snapshotMarker, i)) {
				covered[i]++
			}
		}
	}
	for i, s := range snapshotSections {
		if covered[i] != 1 {
			t.Errorf("section %q appears in %d batch scripts, want 1", s.label, covered[i])
		}
	}
}

// The batched snapshot renders exactly what the one-exec-per-section version
// did: sections in declaration order, empty ones dropped, a failed batch
// contributing nothing — and it does so in snapshotBatches execs, not nine.
func TestSnapshotRoundTrip(t *testing.T) {
	calls := 0
	exec := func(script string) (string, error) {
		calls++
		var out strings.Builder
		for i, s := range snapshotSections {
			if !strings.Contains(script, fmt.Sprintf("'%s%d'", snapshotMarker, i)) {
				continue
			}
			if s.label == "Process Tree" {
				return "", errors.New("session died")
			}
			fmt.Fprintf(&out, "%s%d\n", snapshotMarker, i)
			if s.label != "Containers" { // docker absent: marker with no body
				fmt.Fprintf(&out, "output of %s\nline two\n", s.label)
			}
		}
		return out.String(), nil
	}
	got := runQuickSnapshot(context.Background(), exec)
	if calls != snapshotBatches {
		t.Fatalf("snapshot took %d execs, want %d", calls, snapshotBatches)
	}
	if strings.Contains(got, "Containers") || strings.Contains(got, "Process Tree") {
		t.Errorf("empty and failed sections must be omitted:\n%s", got)
	}
	if strings.Contains(got, snapshotMarker) {
		t.Errorf("marker leaked into the rendered snapshot:\n%s", got)
	}
	last := -1
	for _, s := range snapshotSections {
		at := strings.Index(got, "### "+s.label+"\n```\noutput of "+s.label+"\nline two\n```\n")
		if s.label == "Containers" || s.label == "Process Tree" {
			continue
		}
		if at < 0 {
			t.Errorf("section %q missing or malformed:\n%s", s.label, got)
			continue
		}
		if at < last {
			t.Errorf("section %q rendered out of declaration order", s.label)
		}
		last = at
	}
}

func TestPruneTechniqueLines(t *testing.T) {
	existing := "- old auth via password (2025-01-01)\n- use sudo -n for reads (2025-02-02)\n- tail the journal (2025-03-03)"
	got := pruneTechniqueLines(existing, "- old auth via password (2025-01-01)\n\n- not a real line\n")
	want := "- use sudo -n for reads (2025-02-02)\n- tail the journal (2025-03-03)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if pruneTechniqueLines(existing, "NONE\n") != existing {
		t.Errorf("a verdict naming nothing stored must change nothing")
	}
}

// The fast path may only skip the verifier when nothing could be wrong: a
// reply whose every identifier is quoted verbatim from the findings. Any
// deviation the verifier is meant to catch must surface as a candidate.
func TestUnverifiedIdentifiers(t *testing.T) {
	findings := "nginx listens on 0.0.0.0:8080\nconfig: /etc/nginx/sites-enabled/app.conf\ntable user_sessions in db app_prod\nMySQL 8.0.36\n"
	clean := "**nginx** serves the app on port `8080` (bound to 0.0.0.0:8080). Its config lives at /etc/nginx/sites-enabled/app.conf, and sessions are in the `user_sessions` table of app_prod on MySQL 8.0.36, e.g. for logins."
	if got := unverifiedIdentifiers(clean, findings); len(got) != 0 {
		t.Errorf("clean reply flagged %v", got)
	}
	cases := map[string]string{
		"wrong underscore":     "sessions are in the user-sessions table",
		"wrong capitalization": "the Nginx config is at /etc/nginx/sites-enabled/app.conf",
		"wrong port":           "it listens on port 8081",
		"wrong path":           "config at /etc/nginx/conf.d/app.conf",
		"wrong version":        "running MySQL 8.0.37",
		"invented db":          "the app_staging database",
	}
	for name, reply := range cases {
		if got := unverifiedIdentifiers(reply, findings); len(got) == 0 {
			t.Errorf("%s: %q was not flagged", name, reply)
		}
	}
	// Prose and markdown decoration are not identifiers.
	for _, prose := range []string{"the service is healthy and nothing needs attention", "## Summary\n- it works.\n---"} {
		if got := unverifiedIdentifiers(prose, findings); len(got) != 0 {
			t.Errorf("prose flagged %v", got)
		}
	}
}
