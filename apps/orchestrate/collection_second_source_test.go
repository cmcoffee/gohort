package orchestrate

// A second source (collection_second_source.go): a secondary page a machine
// picked goes in only when an independent page says the same; contradicted
// and unconfirmed pages are held, and a contradicted one can be argued out
// against the page that contradicted it.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// secondReply is the second source's ruling, as the model writes it.
func secondReply(answer string, source int, says string) string {
	return `{"source":` + string(rune('0'+source)) + `,"says":"` + says + `","why":"read it","answer":"` + answer + `"}`
}

// secondFixture stands in for the search and reading: the pages "read" are
// one on another site and one on the candidate's own. It records whether the
// pipeline ran and what it was given.
type secondRun struct {
	ran   int
	input string
	def   PipelineDef
}

func secondFixture(t *testing.T, search bool) *secondRun {
	t.Helper()
	run := &secondRun{}
	prevAvail, prevRun := secondSearchAvailable, secondRunPipeline
	secondSearchAvailable = func() bool { return search }
	secondRunPipeline = func(_ *OrchestrateApp, ctx context.Context, _ string, def PipelineDef, input string, h PipelineHooks) (string, error) {
		run.ran++
		run.input, run.def = input, def
		if _, err := h.Dispatch(ctx, "a voice", "x"); !errors.Is(err, ErrNoSuchAgent) {
			t.Errorf("only the judge is dispatched: %v", err)
		}
		h.Status("gather look: reading 2 pages")
		return h.Dispatch(ctx, secondJudge, input+"\n\n## The pages read\n[1] Other site - https://other.example/a\n[2] Same site - https://example.com/b\n\n## What they say\n...")
	}
	t.Cleanup(func() { secondSearchAvailable, secondRunPipeline = prevAvail, prevRun })
	return run
}

// A secondary page goes in when an independent page confirms it, and the
// result names that page. One "confirmed" only by its own site, or not at
// all, is held as unconfirmed; one contradicted is held with the page that
// contradicted it. A primary page never waits on a second source.
func TestASecondaryPageNeedsAnIndependentSource(t *testing.T) {
	const page = "https://example.com/post"
	secondary := vetReplyFrom("secondary: a blog post about it", "adds", 0, "")
	f := vetFixture(t, map[string]string{page: "The audit log rotates daily, a reader found."},
		FakeTurn{Content: secondary}, FakeTurn{Content: secondReply("confirmed", 1, "rotates daily")},
		FakeTurn{Content: secondary}, FakeTurn{Content: secondReply("confirmed", 2, "rotates daily")},
		FakeTurn{Content: secondary}, FakeTurn{Content: secondReply("contradicted", 1, "rotates weekly")},
		FakeTurn{Content: vetReply("adds", 0, "")})
	run := secondFixture(t, true)
	c := f.c
	cand := vetCandidate{URL: page, Title: "A post", Text: "The audit log rotates daily."}

	res := f.T.checkPage(context.Background(), c, cand, nil)
	if !res.addsIt() || res.OtherURL != "https://other.example/a" || !strings.Contains(res.note(), "other.example") {
		t.Errorf("confirmed elsewhere should go in, naming the confirming page: %+v", res)
	}
	if run.def.Stages[0].Kind != StageGather || run.def.Stages[1].Agent != secondJudge || !strings.Contains(run.def.Stages[0].Prompt, "the audit log rotates daily") {
		t.Errorf("the pipeline should search for the claim, then rule: %+v", run.def.Stages)
	}
	if err := run.def.Validate(); err != nil {
		t.Errorf("the second-source pipeline does not validate: %v", err)
	}
	if !strings.Contains(run.input, "Its site: example.com") {
		t.Errorf("the judge should be told the candidate's site:\n%s", run.input)
	}

	res = f.T.checkPage(context.Background(), c, cand, nil)
	if res.Answer != vetUnconfirmed || heldFor(res) != "unconfirmed" {
		t.Errorf("the candidate's own site is no second source: %+v", res)
	}

	res = f.T.checkPage(context.Background(), c, cand, nil)
	if res.Answer != vetContradicted || res.OtherURL != "https://other.example/a" || res.Against != "rotates weekly" {
		t.Errorf("contradicted: %+v", res)
	}

	ran := run.ran
	if res := f.T.checkPage(context.Background(), c, cand, nil); !res.addsIt() || run.ran != ran {
		t.Errorf("a primary page should go in without a second source: %+v (searched %d)", res, run.ran-ran)
	}
}

// When a second source cannot be looked for, a secondary page is neither
// added nor held.
func TestNoSearchMeansNoSecondaryPage(t *testing.T) {
	const page = "https://example.com/post"
	f := vetFixture(t, map[string]string{page: "The audit log rotates daily."},
		FakeTurn{Content: vetReplyFrom("secondary: a forum answer", "adds", 0, "")})
	run := secondFixture(t, false)
	tool := f.T.submitTool([]Collection{f.c}, "sub").(*GroupedTool)
	add, _ := tool.Action("add_url")
	out, err := add.Handler(map[string]any{"url": page}, &ToolSession{DB: f.store("alice"), Username: "alice"})
	if err != nil || !strings.Contains(out, "Not added") || !strings.Contains(out, "second source") {
		t.Fatalf("%q %v", out, err)
	}
	if saved, _ := loadCollection(f.store("alice"), "alice", "col-v"); len(saved.IngestedURLs) != 0 || len(f.T.listHeld(f.c)) != 0 || run.ran != 0 {
		t.Errorf("added %v, held %d, searched %d", saved.IngestedURLs, len(f.T.listHeld(f.c)), run.ran)
	}
}

// The curator's own text is secondary whatever the check says of it: it
// needs a second source, and without one it is turned away, not held.
func TestTheCuratorsOwnTextNeedsASecondSource(t *testing.T) {
	f := vetFixture(t, nil,
		FakeTurn{Content: vetReply("adds", 0, "")}, FakeTurn{Content: secondReply("unconfirmed", 0, "")})
	run := secondFixture(t, true)
	tool := f.T.submitTool([]Collection{f.c}, "sub").(*GroupedTool)
	add, _ := tool.Action("add_text")
	out, err := add.Handler(map[string]any{"title": "Note", "text": "The audit log rotates daily."}, &ToolSession{DB: f.store("alice"), Username: "alice"})
	if err != nil || run.ran != 1 || !strings.Contains(out, "Not added") || !strings.Contains(out, "no independent source") {
		t.Fatalf("searched %d: %q %v", run.ran, out, err)
	}
	if len(f.T.listHeld(f.c)) != 0 || len(f.docs()) != 1 {
		t.Errorf("the curator's unconfirmed text should be neither held nor added: %v", f.docs())
	}
}

// Held pages say why, and offer what fits: an unconfirmed page can only be
// added or left out; a contradicted one can also be argued out against the
// page that contradicted it, and its ruling is read as add or leave out.
func TestHeldPagesOfferWhatFitsWhyTheyWereHeld(t *testing.T) {
	const page, page2, other = "https://example.com/post", "https://example.com/post2", "https://other.example/a"
	footer := strings.Repeat("Unrelated footer about something else entirely. ", 80)
	f := vetFixture(t, map[string]string{
		page:  "Intro.\n\nThe audit log rotates daily.\n\n" + footer,
		page2: "Intro.\n\nThe audit log rotates daily.\n\n" + footer,
		other: "Intro.\n\nThe audit log rotates weekly, per the maintainers.\n\n" + footer},
		FakeTurn{Content: `{"page_case":"one reader","document_case":"the maintainers [1]","why":"The maintainers' page [1] decides it.","answer":"document"}`})
	curations.mu.Lock()
	prevRuns := curations.runs
	curations.runs = map[string]*curateRun{}
	curations.mu.Unlock()
	t.Cleanup(func() {
		curations.mu.Lock()
		running := curations.runs
		curations.runs = prevRuns
		curations.mu.Unlock()
		for _, run := range running {
			run.cancel()
			<-run.done
		}
	})
	cand := vetCandidate{URL: page, Title: "A post"}
	if err := f.T.holdPage(f.c, cand, vetResult{Answer: vetUnconfirmed, Claim: "the audit log rotates daily"}); err != nil {
		t.Fatal(err)
	}
	if err := f.T.holdPage(f.c, vetCandidate{URL: page2, Title: "Another post"}, vetResult{Answer: vetContradicted, Against: "rotates weekly", OtherURL: other, OtherTitle: "Maintainers"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.T.collectionHeld(f.call(http.MethodGet, ""))
	rows := got.(map[string]any)["held"].([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	byFor := map[string]map[string]any{}
	for _, r := range rows {
		byFor[r["held_for"].(string)] = r
	}
	if u := byFor["unconfirmed"]; u == nil || u["can_settle"] != false || u["has_doc"] != false || !strings.Contains(u["conflict"].(string), "No independent source") {
		t.Errorf("unconfirmed: %+v", u)
	}
	ctr := byFor["contradicted"]
	if ctr == nil || ctr["can_settle"] != true || ctr["has_doc"] != false || !strings.Contains(ctr["conflict"].(string), other) {
		t.Fatalf("contradicted: %+v", ctr)
	}

	var input string
	var def PipelineDef
	prev := settleRunPipeline
	settleRunPipeline = func(_ *OrchestrateApp, ctx context.Context, _ string, d PipelineDef, in string, h PipelineHooks) (string, error) {
		def, input = d, in
		return h.Dispatch(ctx, settleJudge, in)
	}
	t.Cleanup(func() { settleRunPipeline = prev })
	sid := ctr["id"].(string)
	if _, err := f.T.collectionHeldSettleStart(f.call(http.MethodPost, sid), struct{}{}); err != nil {
		t.Fatal(err)
	}
	var end map[string]any
	for i := 0; i < 400 && end == nil; i++ {
		out, _ := f.T.collectionHeldSettleProgress(f.call(http.MethodGet, sid))
		if m := out.(map[string]any); m["outcome"] != nil {
			end = m
		}
		if end == nil {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if end == nil || !strings.Contains(end["outcome"].(string), "The other source is right: leave the page out") {
		t.Fatalf("outcome: %v", end)
	}
	if !strings.Contains(input, "## The other source: "+other) || !strings.Contains(input, "rotates weekly") || strings.Contains(input, "Unrelated footer") {
		t.Errorf("the other side should be the page that contradicted it:\n%s", input)
	}
	if v := def.Stages[0].Panel; len(v) != 2 || v[1] != "The other source" {
		t.Errorf("voices: %v", v)
	}
}

// The second source's cases go through its own prompt and reading.
func TestTheSecondSourceCasesRunThroughTheRuling(t *testing.T) {
	caseAnswers(t, secondVerdict.Key, []string{secondConfirmed, secondContradicted, secondUnconfirmed},
		caseReply{"confirmed", secondConfirmed, []string{secondReply("confirmed", 1, "rotated every day")}},
		caseReply{"same-site", secondUnconfirmed, []string{secondReply("unconfirmed", 0, "")}})
}

// The agent in charge adding a secondary page nobody else backs: not added,
// held as unconfirmed, and the agent is told so.
func TestAnAgentsUnconfirmedPageIsHeld(t *testing.T) {
	const page = "https://example.com/post"
	f := vetFixture(t, map[string]string{page: "The audit log rotates daily."},
		FakeTurn{Content: vetReplyFrom("secondary: a blog post", "adds", 0, "")}, FakeTurn{Content: secondReply("unconfirmed", 0, "")})
	secondFixture(t, true)
	tool := f.T.submitTool([]Collection{f.c}, "sub").(*GroupedTool)
	add, _ := tool.Action("add_url")
	out, err := add.Handler(map[string]any{"url": page}, &ToolSession{DB: f.store("alice"), Username: "alice"})
	if err != nil || !strings.Contains(out, "held for the owner") {
		t.Fatalf("%q %v", out, err)
	}
	if h := f.T.listHeld(f.c); len(h) != 1 || h[0].HeldFor != "unconfirmed" {
		t.Errorf("held: %+v", h)
	}
}
