package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func lazyDefs(names ...string) []AgentToolDef {
	var out []AgentToolDef
	for _, n := range names {
		out = append(out, AgentToolDef{Tool: Tool{Name: n, Description: "Does the " + n + " thing. More detail follows here."}})
	}
	return out
}

// A tool groups by its API: a named credential, else (no_auth, a public API)
// the host it calls, else its category. no_auth is never a key: every public
// API shares it.
func TestAToolGroupsByItsAPIThenHostThenCategory(t *testing.T) {
	for _, c := range []struct {
		lt   TempTool
		cat  string
		want string
	}{
		{TempTool{Mode: TempToolModeAPI, Credential: "GitLab"}, "", "api:gitlab"},
		{TempTool{Mode: TempToolModeAPI, Credential: "no_auth", CommandTemplate: "https://api.Example.com/v1/{q}"}, "", "host:api.example.com"},
		{TempTool{Mode: TempToolModeToolbox, Credential: "no_auth", Actions: []TempToolAction{{URLTemplate: "https://tides.example/x"}}}, "", "host:tides.example"},
		{TempTool{HookCapabilities: []string{"fetch_via:confluence"}}, "Docs", "api:confluence"},
		{TempTool{Mode: TempToolModeAPI, Credential: "no_auth", CommandTemplate: "/relative/{q}"}, "", ""},
		{TempTool{}, "Music", "cat:music"},
		{TempTool{}, "", ""},
	} {
		if got, _, _ := toolGroupKey(&c.lt, c.cat); got != c.want {
			t.Errorf("%+v / %q: key %q, want %q", c.lt, c.cat, got, c.want)
		}
	}
}

// Three or more tools with one key collapse, named from their label, the same
// names every time; two do not. Two labels that slug alike get distinct names.
func TestGroupsCollapseFromThreeWithStableNames(t *testing.T) {
	keys := map[string][3]string{
		"gl_a": {"api:gitlab", "gitlab", "api"}, "gl_b": {"api:gitlab", "gitlab", "api"}, "gl_c": {"api:gitlab", "gitlab", "api"},
		"m_a": {"cat:music", "Music", "category"}, "m_b": {"cat:music", "Music", "category"},
		"x_a": {"cat:gitlab", "GitLab", "category"}, "x_b": {"cat:gitlab", "GitLab", "category"}, "x_c": {"cat:gitlab", "GitLab", "category"},
	}
	keyOf := func(n string) (string, string, string) { k := keys[n]; return k[0], k[1], k[2] }
	lazy := lazyDefs("gl_a", "m_a", "gl_b", "x_a", "m_b", "gl_c", "x_b", "x_c", "solo")
	g := groupTools(lazy, keyOf)
	if len(g) != 2 {
		t.Fatalf("groups %v, want gitlab twice and not music", g)
	}
	api, cat := g["group:gitlab"], g["group:gitlab-2"]
	if strings.Join(api.Members, ",") != "gl_a,gl_b,gl_c" || api.Kind != "api" || cat.Kind != "category" {
		t.Fatalf("groups = %+v", g)
	}
	for i := 0; i < 5; i++ {
		again := groupTools(lazy, keyOf)
		if fmt.Sprint(again) != fmt.Sprint(g) {
			t.Fatal("the same tools grouped differently: the list is part of the prompt")
		}
	}
}

// The listing shows a group as one line where its first tool was, leaves the
// rest out, keeps the ungrouped tools as they were, and says how to load a
// group only when there is one.
func TestTheListShowsAGroupAsOneLine(t *testing.T) {
	lazy := lazyDefs("gl_a", "solo", "gl_b", "gl_c")
	groups := map[string]toolGroup{"group:gitlab": {Name: "group:gitlab", Label: "gitlab", Kind: "api", Members: []string{"gl_a", "gl_b", "gl_c"}}}
	out := lazyToolSectionWith(lazy, groups)
	want := "- `group:gitlab` 3 tools on the gitlab API: `gl_a`, `gl_b`, `gl_c`\n- `solo` Does the solo thing.\n"
	if !strings.Contains(out, want) {
		t.Fatalf("section lacks the grouped lines in order:\n%s", out)
	}
	if strings.Count(out, "gl_b") != 1 || !strings.Contains(out, "`group:` name") {
		t.Fatalf("members repeated, or no word on loading a group:\n%s", out)
	}
	if plain := lazyToolSectionFor(lazy); strings.Contains(plain, "group:") {
		t.Fatalf("no groups, but the section talks about them:\n%s", plain)
	}
}

// load_tool takes a group's name and loads each member by its own name; an
// unknown group is reported, not silently dropped.
func TestLoadToolLoadsAWholeGroup(t *testing.T) {
	turn, sess := newAuthoringTestTurn(t)
	turn.lazyCustomToolDefs = map[string]AgentToolDef{}
	turn.lazyCustomToolNames = map[string]bool{}
	turn.loadedCustomTools = map[string]bool{}
	for _, td := range lazyDefs("gl_a", "gl_b", "gl_c", "other") {
		td.Tool.Parameters = map[string]ToolParam{"q": {Type: "string"}}
		turn.lazyCustomToolDefs[td.Tool.Name] = td
		turn.lazyCustomToolNames[td.Tool.Name] = true
	}
	turn.lazyToolGroups = map[string]toolGroup{"group:gitlab": {Name: "group:gitlab", Members: []string{"gl_a", "gl_b", "gl_c"}}}
	out, err := turn.loadToolToolDef(sess).Handler(context.Background(), map[string]any{"names": []any{"group:gitlab", "gl_a", "group:nope"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"gl_a", "gl_b", "gl_c"} {
		if !turn.loadedCustomTools[n] {
			t.Errorf("%s not loaded by its group", n)
		}
	}
	if turn.loadedCustomTools["other"] || !strings.Contains(out, "group:nope") {
		t.Errorf("loaded too much, or the unknown group went unsaid: %q", out)
	}
	if _, err := turn.loadToolToolDef(sess).Handler(context.Background(), map[string]any{"names": []any{"group:nope"}}); err == nil {
		t.Error("loading only an unknown group did not say so")
	}
}

// An API-less tool with no category of its own is filed by the worker once,
// in the background, among the categories already in use, and groups from the
// next turn. The owner's own category is never asked about, and a changed
// description is filed again.
func TestAnAPILessToolIsFiledOnceAndGroupsNextTurn(t *testing.T) {
	turn, sess := newAuthoringTestTurn(t)
	turn.ctx = context.Background()
	var mu sync.Mutex
	asked := map[string]int{}
	prev := toolCategoryAsk
	toolCategoryAsk = func(ctx context.Context, app *OrchestrateApp, msgs []Message) (string, error) {
		u := msgs[1].Content
		if !strings.Contains(u, "Categories already in use: Music") {
			return "", fmt.Errorf("not offered the categories in use: %q", u)
		}
		name := strings.Fields(strings.SplitN(u, "Tool: ", 2)[1])[0]
		mu.Lock()
		asked[name]++
		mu.Unlock()
		return `{"category": "music"}`, nil // in use as "Music": that spelling wins
	}
	t.Cleanup(func() { toolCategoryAsk = prev })
	sess.TempTools = []*TempTool{
		{Name: "mine", Category: "Music", Description: "the owner's own label"},
		{Name: "gen_a", Description: "make a song"},
		{Name: "gen_b", Description: "extract audio"},
	}
	lazy := lazyDefs("mine", "gen_a", "gen_b")
	if g := turn.toolGroupsFor(sess, lazy); len(g) != 0 {
		t.Fatalf("grouped before anything was filed: %v", g)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, a := autoToolCategory(turn.udb, sess.TempTools[1])
		_, b := autoToolCategory(turn.udb, sess.TempTools[2])
		if a && b {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the tools were never filed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if c, _ := autoToolCategory(turn.udb, sess.TempTools[1]); c != "Music" {
		t.Errorf("filed as %q, want the spelling in use", c)
	}
	g := turn.toolGroupsFor(sess, lazy)
	if m := g["group:music"].Members; strings.Join(m, ",") != "mine,gen_a,gen_b" {
		t.Fatalf("next turn's groups = %v", g)
	}
	mu.Lock()
	defer mu.Unlock()
	if asked["mine"] != 0 || asked["gen_a"] != 1 {
		t.Errorf("asked %v: the owner's label is never asked about, and a filed tool is not asked again", asked)
	}
	sess.TempTools[1].Description = "make a podcast"
	if _, ok := autoToolCategory(turn.udb, sess.TempTools[1]); ok {
		t.Error("a changed description kept its old filing")
	}
}

// A worker answer that names the tool itself, or nothing, is not a category.
func TestAFilingThatNamesTheToolIsRefused(t *testing.T) {
	prev := toolCategoryAsk
	t.Cleanup(func() { toolCategoryAsk = prev })
	for _, answer := range []string{`{"category": "gen_a"}`, `{"category": ""}`, `not json`} {
		toolCategoryAsk = func(context.Context, *OrchestrateApp, []Message) (string, error) { return answer, nil }
		if c, err := fileToolCategory(context.Background(), nil, &TempTool{Name: "gen_a"}, nil); err == nil {
			t.Errorf("%s filed as %q", answer, c)
		}
	}
}
