package findtools

import (
	"context"
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

// Run inside an agent loop, the search sees the tools the round was given:
// a tool handed to this turn alone is in no index, and a search that missed
// it told the agent it was not installed (an agent with a board tool, asked
// to clear the board, searched five times and then went looking for the
// board's file on disk).
func TestFindToolsSeesTheToolsThisTurnHas(t *testing.T) {
	llm := &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "find_tools", Args: map[string]any{"query": "clear bulletin board"}}}},
		{Content: "Done.", Repeat: true},
	}}
	app := &AppCore{LLM: llm}
	board := AgentToolDef{
		Tool:    Tool{Name: "bulletin_board", Description: "Post to or clear a bulletin board other agents follow."},
		Handler: func(context.Context, map[string]any) (string, error) { return "ok", nil },
	}
	find := ChatToolToAgentToolDefWithSession(&FindToolsTool{}, &ToolSession{})
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "clear the board"}},
		AgentLoopConfig{Tools: []AgentToolDef{find, board}, MaxRounds: 3}); err != nil {
		t.Fatal(err)
	}
	var result string
	for _, m := range llm.Sent(1) {
		for _, r := range m.ToolResults {
			result += r.Content
		}
	}
	if !strings.Contains(result, `"name":"bulletin_board"`) || !strings.Contains(result, "already in your tools this turn") {
		t.Errorf("the search should name the board tool the round already has:\n%s", result)
	}
	if strings.Contains(result, `"name":"find_tools"`) {
		t.Error("the search should not offer itself")
	}

	// Outside a loop there is no round, and the search is the index alone.
	if TurnToolsFromContext(context.Background()) != nil {
		t.Error("no round outside a loop")
	}
}
