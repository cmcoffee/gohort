package findtools

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Builder's session: schedule and bulletins sat in the load-before-use index,
// the prompt said "you have schedule", and eight find_tools calls for a
// recurring run came back with date_math and web_search. The index never
// holds a per-turn tool, so the search reads the turn's loadable list too.
func loadableForTest() []ToolMatch {
	return []ToolMatch{
		{Name: "schedule", Description: "Run an agent or a tool later, or every so often: at a time, every N hours, when a value crosses, when something is posted."},
		{Name: "bulletins", Description: "Bulletin boards an agent posts to and others read. Actions: list; create; allow_poster {board, agent}; follow."},
		{Name: "tool_def", Description: "Define a wrapper around a shell command or an HTTP API call."},
	}
}

func TestLoadableMatchesNamesScheduleForARecurringRun(t *testing.T) {
	for _, q := range []string{
		"schedule agent run recurring cron",
		"standing agent recurring run every week friday",
		"create a scheduled run of an agent on a timetable",
	} {
		hits := loadableMatches(q, loadableForTest())
		if len(hits) == 0 || hits[0].Name != "schedule" {
			t.Fatalf("%q: schedule should be the first hit, got %+v", q, hits)
		}
	}
	hits := loadableMatches("bulletin board grant poster allow_poster post permission", loadableForTest())
	if len(hits) == 0 || hits[0].Name != "bulletins" {
		t.Fatalf("bulletins should be the first hit for a board query, got %+v", hits)
	}
	if hits := loadableMatches("resize an image", loadableForTest()); len(hits) != 0 {
		t.Fatalf("an unrelated query must match nothing, got %+v", hits)
	}
}

func TestFindToolsSaysToLoadFirst(t *testing.T) {
	sess := &ToolSession{LoadableTools: loadableForTest}
	out, err := (&FindToolsTool{}).RunWithSession(map[string]any{"query": "schedule a recurring agent run every friday", "k": 5}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"name":"schedule"`) || !strings.Contains(out, "load_tool") {
		t.Fatalf("the reply should name schedule and say to load it first, got: %s", out)
	}
	if strings.Contains(out, `"name":"tool_def"`) {
		t.Fatalf("an unrelated loadable tool must not pad the reply: %s", out)
	}
}

// The app's note for a search rides on the reply whether or not the index
// found anything: a per-turn tool, or who holds the right to one, is what no
// index has.
func TestFindToolsCarriesTheAppsNote(t *testing.T) {
	note := func(q string) string {
		if strings.Contains(q, "board") {
			return "Bulletin boards: ask the poster."
		}
		return ""
	}
	for _, sess := range []*ToolSession{
		{FindToolsNote: note},
		{FindToolsNote: note, LoadableTools: loadableForTest},
	} {
		out, err := (&FindToolsTool{}).RunWithSession(map[string]any{"query": "clear the board"}, sess)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(out, "\n\nNOTE: Bulletin boards: ask the poster.") {
			t.Errorf("the note should close the reply: %q", out)
		}
	}
	out, _ := (&FindToolsTool{}).RunWithSession(map[string]any{"query": "resize an image"}, &ToolSession{FindToolsNote: note})
	if strings.Contains(out, "NOTE:") {
		t.Errorf("an empty note adds nothing: %q", out)
	}
}

func TestStemWord(t *testing.T) {
	for _, pair := range [][2]string{{"scheduled", "schedul"}, {"scheduling", "schedul"}, {"schedule", "schedul"}, {"bulletins", "bulletin"}, {"posted", "post"}, {"runs", "run"}, {"run", "run"}} {
		if got := stemWord(pair[0]); got != pair[1] {
			t.Fatalf("stem(%q) = %q, want %q", pair[0], got, pair[1])
		}
	}
}
