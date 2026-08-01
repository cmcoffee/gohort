package customapps

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A published app rendered its pipeline panel in full and then 403'd on every
// request the panel made: the history sat on "Loading…", the submit button did
// nothing, and nothing on screen connected either symptom to the link. The app
// was correct — publishing it was the thing that could not work.

func mustPage(t *testing.T, sections string) map[string]any {
	t.Helper()
	var page map[string]any
	if err := json.Unmarshal([]byte(`{"sections":`+sections+`}`), &page); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return page
}

func TestPublicSurfaceReplacesSessionBoundPanels(t *testing.T) {
	page := mustPage(t, `[
		{"title":"Deep Dive","no_chrome":true,"body":{"type":"pipeline_panel","submit_url":"pipeline/stream"}},
		{"title":"Results","body":{"type":"table","source":"records"}}
	]`)
	publicizeSessionPanels(page)

	secs := page["sections"].([]any)
	first := secs[0].(map[string]any)
	body := first["body"].(map[string]any)
	if body["type"] != "empty_state" {
		t.Fatalf("a pipeline panel must not be served on a public link, got %v", body["type"])
	}
	if _, still := body["submit_url"]; still {
		t.Error("the replacement must not keep an endpoint that refuses every call")
	}
	if hint, _ := body["hint"].(string); !strings.Contains(hint, "dashboard") {
		t.Errorf("the note must say where the panel DOES work, got %q", hint)
	}
	if _, nc := first["no_chrome"]; nc {
		t.Error("no_chrome was for the panel's own layout; an empty state wants the ordinary card")
	}
	if first["title"] != "Deep Dive" {
		t.Error("keep the section title — it says WHICH part is missing")
	}
	// The rest of a mixed app is untouched: publishing loses only the part that
	// could never have worked.
	if secs[1].(map[string]any)["body"].(map[string]any)["type"] != "table" {
		t.Error("a table works fine on a public link and must be left alone")
	}
}

func TestPublishNoteNamesTheLimitation(t *testing.T) {
	spec := AppSpec{Page: json.RawMessage(`{"sections":[{"body":{"type":"pipeline_panel"}}]}`)}
	note := publishLimitationNote(spec)
	if note == "" {
		t.Fatal("publishing an app whose main panel cannot work must say so")
	}
	if !strings.Contains(note, "multi-stage run") {
		t.Errorf("the note should name the part, got %q", note)
	}
	// Nothing to warn about → no warning. A note on every publish is a note
	// nobody reads.
	quiet := AppSpec{Page: json.RawMessage(`{"sections":[{"body":{"type":"table"}}]}`)}
	if n := publishLimitationNote(quiet); n != "" {
		t.Errorf("an app that publishes cleanly must publish quietly, got %q", n)
	}
	// An unreadable page is not an occasion to guess.
	if n := publishLimitationNote(AppSpec{Page: json.RawMessage(`not json`)}); n != "" {
		t.Errorf("unparseable page must produce no claim, got %q", n)
	}
}
