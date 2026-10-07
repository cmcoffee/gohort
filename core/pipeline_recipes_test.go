package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

// The shipped recipes run end to end on a fake model and a fake web. The
// debate (extras/debate.pipeline.json): positions, the shared evidence, a
// panel whose voices each look something up and cite it, every citation
// checked, and a judge. The deep research (extras/research.pipeline.json):
// sub-questions, each searched and answered in its own branch, a cited report,
// its citations checked, and a final pass that acts on the check. What two
// apps of hand-written Go did, as data the framework runs.

// promptLLM answers by what it is asked rather than in order: a panel's
// voices and a verify stage's checks run in parallel, so no script of turns
// could say which call comes when.
type promptLLM struct {
	mu    sync.Mutex
	asked []string
	route func(prompt string) string
}

func (p *promptLLM) Chat(_ context.Context, msgs []Message, _ ...ChatOption) (*Response, error) {
	prompt := ""
	for _, m := range msgs {
		prompt += m.Content + "\n"
	}
	p.mu.Lock()
	p.asked = append(p.asked, prompt)
	p.mu.Unlock()
	return &Response{Content: p.route(prompt)}, nil
}

func (p *promptLLM) ChatStream(ctx context.Context, msgs []Message, h StreamHandler, opts ...ChatOption) (*Response, error) {
	return p.Chat(ctx, msgs, opts...)
}

func (p *promptLLM) count(substr string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, a := range p.asked {
		if strings.Contains(a, substr) {
			n++
		}
	}
	return n
}

func readRecipe(t *testing.T, name string) PipelineDef {
	t.Helper()
	raw, err := os.ReadFile("../extras/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var def PipelineDef
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatal(err)
	}
	return def
}

func TestTheShippedDebateRecipeRunsEndToEnd(t *testing.T) {
	def := readRecipe(t, "debate.pipeline.json")

	// Plenty of distinct sites, so the shared evidence and every voice's own
	// lookup each find pages nobody has read yet.
	var urls []string
	for i := 1; i <= 30; i++ {
		urls = append(urls, fmt.Sprintf("https://site%d.example/report", i))
	}
	tools, fetched := webTools(map[string][]string{
		"congestion pricing outcomes":     urls[:10],
		"transit ridership after pricing": urls[10:20],
		"evidence for my side":            urls[20:],
	}, nil)

	llm := &promptLLM{route: func(p string) string {
		switch {
		case strings.Contains(p, "Turn the question below into a debate"):
			return `{"not_debatable": false, "for_position": "Cities should price congestion", "against_position": "Cities should not price congestion",
				"for_expertise": "transport economics", "against_expertise": "household budgets",
				"queries": ["congestion pricing outcomes", "transit ridership after pricing"]}`
		case strings.Contains(p, "Before you answer, you may look things up"):
			return `["evidence for my side"]`
		case strings.Contains(p, "Does the SOURCE TEXT below support the CLAIM"):
			return `{"verdict": "supported", "why": "the page says so"}`
		case strings.Contains(p, "You are judging a debate"):
			return `{"winner": "for", "confidence": "moderate", "verdict": "FOR wins on the regional demand nobody answered",
				"reasoning": "AGAINST never addressed demand."}`
		case strings.Contains(p, "You are the advocate FOR"):
			return "Installers report steady demand across the region [1]."
		case strings.Contains(p, "You are the advocate AGAINST"):
			return "Solar panels on the roof cut the bill, which is the household view [2]."
		}
		t.Errorf("the recipe asked something this test does not know: %.200s", p)
		return ""
	}}

	var mu sync.Mutex
	cards := map[string]int{}
	var stages []string
	sink := func(ev PipelineEvent) {
		mu.Lock()
		defer mu.Unlock()
		if ev.Kind == "block" {
			stages = append(stages, ev.Title)
			if ev.Type == "card" {
				cards[ev.Title]++
			}
		}
	}
	app := &AppCore{LLM: llm}
	out, run, err := app.executePipelineHooks(context.Background(), def, "Should cities charge drivers to enter downtown?", nil, sink,
		PipelineHooks{Tools: tools})
	if err != nil {
		t.Fatalf("the debate did not finish: %v", err)
	}

	judge := run.outputs["judge"].Fields
	if judge["winner"] != "for" || judge["confidence"] != "moderate" {
		t.Errorf("the judge's verdict must come back as fields: %v (out %q)", judge, out)
	}
	check := run.outputs["check"].Fields
	if check["passed"] != true || check["checked"] != 6 {
		t.Errorf("all six cited turns must be checked against what was read: %v", check)
	}
	if !strings.Contains(run.outputs["rounds"].Text, "## Sources") {
		t.Errorf("the transcript's citations must be resolved into a Sources list:\n%s", run.outputs["rounds"].Text)
	}
	// 8 pages of shared evidence, then one page per voice per round.
	if len(*fetched) != 8+6 {
		t.Errorf("read %d pages, want 8 for the evidence and 6 for the voices", len(*fetched))
	}
	seen := map[string]bool{}
	for _, u := range *fetched {
		if seen[u] {
			t.Errorf("a page was read twice: %s", u)
		}
		seen[u] = true
	}
	if n := llm.count("Before you answer, you may look things up"); n != 6 {
		t.Errorf("each voice looks things up every round: %d lookups, want 6", n)
	}
	if cards["judge"] != 1 || cards["check"] != 1 {
		t.Errorf("the verdict and the check are drawn as cards: %v (blocks %v)", cards, stages)
	}
}

func TestTheShippedResearchRecipeRunsEndToEnd(t *testing.T) {
	def := readRecipe(t, "research.pipeline.json")
	var urls []string
	for i := 1; i <= 12; i++ {
		urls = append(urls, fmt.Sprintf("https://source%d.example/study", i))
	}
	tools, fetched := webTools(map[string][]string{
		"rooftop solar payback period": urls[:4],
		"solar adoption by region":     urls[4:8],
		"grid export tariffs":          urls[8:],
	}, nil)
	llm := &promptLLM{route: func(p string) string {
		switch {
		case strings.Contains(p, "Plan the research for the question below"):
			return `{"questions": ["rooftop solar payback period", "solar adoption by region", "grid export tariffs"]}`
		case strings.Contains(p, "Answer one sub-question from the pages below"):
			return "Installers report steady demand across the region [1]."
		case strings.Contains(p, "Write the report that answers"):
			return "Solar panels on the roof cut the bill [1]. Installers report steady demand [2]. Tariffs vary [40]."
		case strings.Contains(p, "Does the SOURCE TEXT below support the CLAIM"):
			return `{"verdict": "supported", "why": "the page says so"}`
		case strings.Contains(p, "Below is a research report and a check"):
			return "Solar panels on the roof cut the bill [1]. Installers report steady demand [2]."
		}
		t.Errorf("the recipe asked something this test does not know: %.200s", p)
		return ""
	}}
	out, run, err := (&AppCore{LLM: llm}).executePipelineHooks(context.Background(), def, "Is rooftop solar worth it?", nil, func(PipelineEvent) {}, PipelineHooks{Tools: tools})
	if err != nil {
		t.Fatalf("the research did not finish: %v", err)
	}
	if len(*fetched) != 9 {
		t.Errorf("three sub-questions, three pages each: read %d", len(*fetched))
	}
	check := run.outputs["check"].Fields
	if check["passed"] != false || len(check["unresolved_citations"].([]any)) != 1 {
		t.Errorf("the draft cited [40], which names nothing read: %v", check)
	}
	if !strings.Contains(out, "## Sources") || strings.Contains(out, "[40]") {
		t.Errorf("the final report is the revised one, with its sources listed:\n%s", out)
	}
	if len(def.FollowUps) != 1 || def.FollowUps[0].Name != "Briefing" {
		t.Errorf("the recipe offers a briefing on a finished report: %v", def.FollowUps)
	}
}
