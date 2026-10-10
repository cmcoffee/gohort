package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cmcoffee/oddjob/core/sources"
)

// Gathering as a step: a pipeline names what to look up, and the run
// searches, picks the pages worth reading, reads them, and numbers them for a
// later stage to cite. And a panel whose voices each look things up before
// they speak.

var registerWebExtractors sync.Once

// webTools are stand-ins for web_search and fetch_url, in the same output
// formats, with extractors registered the way tools/websearch registers them.
// results maps a query to the URLs its search lists; a URL in broken fails to
// read. Every fetch is recorded.
func webTools(results map[string][]string, broken map[string]bool) ([]AgentToolDef, *[]string) {
	registerWebExtractors.Do(func() {
		sources.RegisterExtractor("web_search", func(_ map[string]any, result string) []sources.Fetched {
			var out []sources.Fetched
			lines := strings.Split(result, "\n")
			for i := 0; i+1 < len(lines); i += 2 {
				out = append(out, sources.Fetched{Title: strings.TrimSpace(lines[i][3:]), URL: strings.TrimSpace(lines[i+1])})
			}
			return out
		})
		sources.RegisterExtractor("fetch_url", func(_ map[string]any, result string) []sources.Fetched {
			head, text, ok := strings.Cut(result, "\n\n")
			if !ok || !strings.HasPrefix(head, "Fetched ") {
				return nil
			}
			return []sources.Fetched{{Title: "page", URL: strings.Fields(head)[1], Text: text}}
		})
	})
	var mu sync.Mutex
	fetched := &[]string{}
	search := func(_ context.Context, args map[string]any) (string, error) {
		var b strings.Builder
		for i, u := range results[fmt.Sprint(args["query"])] {
			fmt.Fprintf(&b, "%d. Result about %s\n   %s\n", i+1, u, u)
		}
		return b.String(), nil
	}
	fetch := func(_ context.Context, args map[string]any) (string, error) {
		u := fmt.Sprint(args["url"])
		mu.Lock()
		*fetched = append(*fetched, u)
		mu.Unlock()
		if broken[u] {
			return "", errors.New("403")
		}
		body := "Solar panels on the roof cut the bill. " + strings.Repeat("Installers report steady demand across the region. ", 12)
		return "Fetched " + u + " (900 chars):\n\n" + body, nil
	}
	return []AgentToolDef{
		{Tool: Tool{Name: "web_search"}, Handler: search},
		{Tool: Tool{Name: "fetch_url"}, Handler: fetch},
	}, fetched
}

func runGather(t *testing.T, def PipelineDef, tools []AgentToolDef, dispatch PipelineDispatch, llm LLM) (string, map[string]any, error) {
	t.Helper()
	if llm == nil {
		llm = &FakeLLM{Turns: []FakeTurn{{Content: "ok", Repeat: true}}}
	}
	app := &AppCore{LLM: llm}
	return app.RunPipelineDefHooks(context.Background(), def, "home solar", PipelineHooks{Tools: tools, Dispatch: dispatch})
}

// A gather reads the best pages across its searches: in turn from each, no
// weak source, no more than two from one site, a page that fails to read
// replaced by the next pick. What it read is numbered for a later stage.
func TestGatherReadsTheBestPagesAndNumbersThem(t *testing.T) {
	tools, fetched := webTools(map[string][]string{
		"solar cost":   {"https://a.example/1", "https://a.example/2", "https://a.example/3", "https://hubspot.com/blog/9"},
		"solar demand": {"https://b.example/1", "https://a.example/1", "https://c.example/1"},
	}, map[string]bool{"https://b.example/1": true})
	var writerPrompt string
	def := PipelineDef{Name: "g", Stages: []PipelineStage{
		{Name: "look", Kind: StageGather, Prompt: "- solar cost\n- solar demand", Count: 3},
		{Name: "write", Kind: StageAgent, Agent: "writer", Prompt: "Cite from {sources}\n\n{stage:look}"},
	}}
	_, _, err := runGather(t, def, tools, func(_ context.Context, _, p string) (string, error) {
		writerPrompt = p
		return "done", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range *fetched {
		if strings.Contains(u, "hubspot") {
			t.Errorf("a weak source was read: %s", u)
		}
		if u == "https://a.example/3" {
			t.Errorf("a third page from one site was read")
		}
	}
	if len(*fetched) != 4 {
		t.Errorf("three pages wanted, one failed and was replaced: fetched %v", *fetched)
	}
	for _, want := range []string{"[1] Result about", "[3] Result about", "> Solar panels on the roof"} {
		if !strings.Contains(writerPrompt, want) {
			t.Errorf("the writer must get the numbered pages and their passages, missing %q in:\n%s", want, writerPrompt)
		}
	}
	if strings.Contains(writerPrompt, "[4]") || strings.Contains(writerPrompt, "b.example/1") {
		t.Errorf("only pages actually read are numbered:\n%s", writerPrompt)
	}
}

// Its fields are what it read, and a second gather does not read a page the
// run already holds.
func TestGatherFieldsAndNoRereading(t *testing.T) {
	tools, fetched := webTools(map[string][]string{"home solar": {"https://a.example/1", "https://b.example/1"}}, nil)
	def := PipelineDef{Name: "g", Stages: []PipelineStage{
		{Name: "first", Kind: StageGather},
		{Name: "again", Kind: StageGather},
	}}
	out, fields, err := runGather(t, def, tools, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(*fetched) != 2 {
		t.Errorf("the second gather must not re-read what the first did: %v", *fetched)
	}
	if fields["found"] != 0 || !strings.Contains(out, "nothing new") {
		t.Errorf("the second gather found nothing new: %v %q", fields, out)
	}
}

// Without both tools in its reach a gather says so rather than reading nothing.
func TestGatherNeedsItsTools(t *testing.T) {
	def := PipelineDef{Name: "g", Stages: []PipelineStage{{Name: "look", Kind: StageGather, Prompt: "x"}}}
	if _, _, err := runGather(t, def, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "web_search and fetch_url") {
		t.Errorf("a gather with no tools must fail naming them, got %v", err)
	}
}

// A researching panel: each voice says what it would look up, reads it, and
// gets it in its prompt as numbered pages it can cite. A page one voice read
// is not read again by the other.
func TestPanelVoicesResearchBeforeTheySpeak(t *testing.T) {
	tools, fetched := webTools(map[string][]string{"solar payback": {"https://a.example/1", "https://b.example/1"}}, nil)
	var mu sync.Mutex
	prompts := map[string]string{}
	def := PipelineDef{Name: "p", Stages: []PipelineStage{{
		Name: "debate", Kind: StagePanel, Panel: []string{"Pro", "Con"}, Research: 1,
		Prompt: "As {voice}, argue about {input}. Evidence:\n{research}",
	}}}
	llm := &FakeLLM{Turns: []FakeTurn{{Content: `["solar payback"]`, Repeat: true}}}
	_, _, err := runGather(t, def, tools, func(_ context.Context, voice, p string) (string, error) {
		mu.Lock()
		prompts[voice] = p
		mu.Unlock()
		return voice + " speaks", nil
	}, llm)
	if err != nil {
		t.Fatal(err)
	}
	if len(*fetched) != 2 {
		t.Errorf("two voices, one page each, never the same page: %v", *fetched)
	}
	for _, v := range []string{"Pro", "Con"} {
		p := prompts[v]
		if strings.Contains(p, "{research}") || !strings.Contains(p, "Result about") || !strings.Contains(p, "Cite what you use") {
			t.Errorf("%s must get what it looked up in place of {research}:\n%s", v, p)
		}
	}
}

// gather and research are checked when the pipeline is saved, and a gather's
// fields are usable by what follows.
func TestGatherAndResearchAreCheckedOnSave(t *testing.T) {
	stages := func(s ...PipelineStage) PipelineDef { return PipelineDef{Name: "d", Stages: s} }
	panel := PipelineStage{Name: "p", Kind: StagePanel, Panel: []string{"a", "b"}, Prompt: "x"}
	tooMany := panel
	tooMany.Research = 9
	strayResearch := panel
	strayResearch.Prompt = "{research}"
	for name, bad := range map[string]PipelineDef{
		"gather with output":      stages(PipelineStage{Name: "g", Kind: StageGather, Output: []PipelineField{{Name: "x"}}}),
		"gather past its cap":     stages(PipelineStage{Name: "g", Kind: StageGather, Count: 40}),
		"research off a panel":    stages(PipelineStage{Name: "w", Kind: StageWorker, Prompt: "x", Research: 2}),
		"research past its cap":   stages(tooMany),
		"{research} unresearched": stages(strayResearch),
		"cite on a gather":        stages(PipelineStage{Name: "g", Kind: StageGather, Cite: true}),
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	good := stages(
		PipelineStage{Name: "g", Kind: StageGather, CountFrom: "{pages}", Render: "card", Card: map[string]string{"title": "found"}},
		PipelineStage{Name: "w", Kind: StageWorker, Prompt: "{stage:g.sources} {stage:g.found}"})
	if err := good.Validate(); err != nil {
		t.Errorf("a gather's fields must be usable: %v", err)
	}
	if q := gatherQueries(`["a", "b"]`); len(q) != 2 || q[1] != "b" {
		t.Errorf("a JSON list is one search per item: %v", q)
	}
	if q := gatherQueries("1. 2024 rates\n- solar\n\nsolar"); len(q) != 2 || q[0] != "2024 rates" {
		t.Errorf("list markers drop, a leading number stays, duplicates fold: %v", q)
	}
}
