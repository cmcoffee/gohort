package orchestrate

// Pipelines, on the page where things are kept.
//
// Everything the HTTP layer served — list, export, import, delete — was
// reachable from nothing: pipelines were authored in chat and attached
// from a picker, so "what do I have, and is any of it live" had no
// answer outside asking an agent.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"regexp"
)

func pipelinePageFixture(t *testing.T) (*OrchestrateApp, Database, string, PipelineDef) {
	t.Helper()
	app, udb, user := newTestOrchestrate(t)
	def := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Research",
		Description: "Decompose, dig, synthesize.",
		Stages: []PipelineStage{
			{Name: "plan", Kind: StageWorker, Prompt: "break it up",
				Output: []PipelineField{{Name: "queries", Type: FieldList, Desc: "searches"}}},
			{Name: "dig", Kind: StageFanout, FanOver: "plan.queries", Prompt: "research {item}",
				Tools: []string{"web_search"}},
			{Name: "answer", Kind: StageWorker, Prompt: "synthesize {stage:dig}", Model: "lead"},
		}})
	return app, udb, user, def
}

func TestThePipelineListSaysWhatEachOneIsAndWhoCanCallIt(t *testing.T) {
	app, udb, user, def := pipelinePageFixture(t)
	// An agent that can call it, and one that cannot.
	if _, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Wren",
		OrchestratorPrompt: "hi", AttachedPipelines: []string{def.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Idle", OrchestratorPrompt: "hi"}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/orchestrate/api/pipelines", nil)
	w := httptest.NewRecorder()
	app.handlePipelines(w, asUser(r, user))
	body := w.Body.String()

	// An unattached pipeline is inert — a tool no agent can call — so
	// this is the single most useful fact about one.
	if !strings.Contains(body, `"used_by_text":"Wren"`) {
		t.Errorf("the row should say who can call it:\n%s", body)
	}
	// What it is MADE of, so a list of names is not the only thing to
	// go on.
	if !strings.Contains(body, "2 worker") || !strings.Contains(body, "1 fanout") {
		t.Errorf("the row should say what kinds of stages it has:\n%s", body)
	}
	if !strings.Contains(body, `"edit_url":"/orchestrate/pipeline?id=`+def.ID+`"`) {
		t.Errorf("the row should open the pipeline:\n%s", body)
	}
	if !strings.Contains(body, `"stage_names":["plan","dig","answer"]`) {
		t.Errorf("and name its stages in order:\n%s", body)
	}
}

func TestThePipelinePageReadsInTheOrderItRuns(t *testing.T) {
	app, _, user, def := pipelinePageFixture(t)
	r := httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("the page does not render: %d", w.Code)
	}
	body := w.Body.String()

	// One section per stage, numbered, in run order — the rail is the
	// stage list.
	for _, want := range []string{"1. plan", "2. dig", "3. answer"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// Each says what KIND it is, in the spec's own vocabulary.
	if !strings.Contains(body, "Runs once per element of plan.queries") {
		t.Error("a fanout should say what it fans over")
	}
	if !strings.Contains(body, "A worker step") {
		t.Error("a plain stage should say so")
	}
	// The stage is EDITABLE: a form per stage, loading and posting to
	// the same address, so a save merges onto what is stored rather
	// than replacing it with what one panel happened to show.
	for _, want := range []string{
		`"source":"api/pipelines/` + def.ID + `/stages?name=dig"`,
		`"post_url":"api/pipelines/` + def.ID + `/stages?name=dig"`,
		`"field":"fan_over"`, `"field":"tools"`, `"field":"model"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the stage form is missing %s", want)
		}
	}
	// The declared contract is EDITABLE now (v0.6.239) — a rows control
	// per field, with the two questions asked in order. What stays a
	// fact is only the part the control does not edit.
	if !strings.Contains(body, `"field":"output"`) || !strings.Contains(body, `"type":"rows"`) {
		t.Error("the declared contract should be editable, not just described")
	}
	if !strings.Contains(body, "Something this stage works out") {
		t.Error("the asked-vs-filled question should be asked before the name")
	}
	// And a stage can be removed, with a confirm.
	if !strings.Contains(body, "Remove this stage") {
		t.Error("no way to remove a stage")
	}
	// A new stage lands at the end.
	if !strings.Contains(body, "Add a stage") {
		t.Error("no way to add one")
	}
	// Name and description are editable, because the description is what
	// an agent reads when deciding whether to call it.
	if !strings.Contains(body, `"field":"description"`) || !strings.Contains(body, `"method":"PUT"`) {
		t.Error("the pipeline's own fields should be editable")
	}
	// Export is here, next to the thing being exported.
	if !strings.Contains(body, "/api/pipelines/"+def.ID+"/export") {
		t.Error("no export on the page")
	}
	// A pipeline nobody can reach is a 404, not somebody else's.
	r = httptest.NewRequest("GET", "/orchestrate/pipeline?id=nope", nil)
	w = httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for an unknown pipeline, got %d", w.Code)
	}
}

// The advice the tool reports has a home in the UI too, phrased the same
// way the machine editor phrases it.
func TestThePipelinePageShowsWhatIsWorthALook(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Bad",
		Stages: []PipelineStage{
			{Name: "plan", Kind: StageWorker, Prompt: "plan it. Respond only with valid JSON.",
				Output: []PipelineField{{Name: "queries", Type: FieldList, Desc: "q"}}},
			{Name: "answer", Kind: StageWorker, Prompt: "answer {stage:plan.queries}"},
		}})
	r := httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	body := w.Body.String()
	if !strings.Contains(body, "Worth a look") || !strings.Contains(body, "stage plan") {
		t.Errorf("the finding the tool reports should be on the page too:\n%s", body[:min(len(body), 400)])
	}
	// And it must not read as breakage.
	if !strings.Contains(body, "Nothing here refuses a save") {
		t.Error("advice should be phrased as a suggestion")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// The picture, pinned above the stages. SectionNav shows one section at
// a time, so a diagram in a section of its own could never be on screen
// with the stage being read — and a branch or a fanout is exactly what
// a stage's own section cannot show.
func TestThePipelinePageDrawsItAndTheBoxesAreDoors(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Research",
		Stages: []PipelineStage{
			{Name: "plan", Kind: StageWorker, Prompt: "p",
				Output: []PipelineField{{Name: "queries", Type: FieldList, Desc: "q"}}},
			{Name: "gate", Kind: StageBranch, When: "plan.queries", SkipTo: "answer"},
			{Name: "dig", Kind: StageFanout, FanOver: "plan.queries", Prompt: "{item}"},
			{Name: "answer", Kind: StageWorker, Prompt: "done"},
		}})

	r := httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	body := w.Body.String()

	if !strings.Contains(body, `"sticky"`) {
		t.Fatal("the map should be pinned, not parked in a section")
	}
	// Inline SVG — links inside an imaged one are inert, and the point
	// of drawing the stages is that they are the stages the rail
	// navigates. "<" arrives escaped inside the page's JSON.
	if !strings.Contains(body, "u003csvg") {
		t.Fatal("no picture on the page")
	}
	// Every box a door, addressed by the SAME slug the section nav
	// computes from the section title.
	for _, want := range []string{`href=\"#1-plan\"`, `href=\"#3-dig\"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the map is missing the link %s", want)
		}
	}
	// The branch's two futures are both drawn, or the picture claims a
	// pipeline that always jumps.
	if !strings.Contains(body, "otherwise") || !strings.Contains(body, "when plan.queries") {
		t.Error("a branch should show both arms")
	}
	// One diagram, not two: the machine editor shipped the same picture
	// twice for a while and it was just a second thing to scroll past.
	if n := strings.Count(body, "u003csvg"); n != 1 {
		t.Errorf("the pipeline should be drawn once, found %d", n)
	}
	// A pipeline with no stages has nothing to draw and must not render
	// a broken box.
	empty := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Empty"})
	r = httptest.NewRequest("GET", "/orchestrate/pipeline?id="+empty.ID, nil)
	w = httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("an empty pipeline should still open: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "u003csvg") {
		t.Error("nothing to draw, so nothing should be drawn")
	}
}

// "Callable by" was read-only from the day the list existed. A pipeline
// attached to nothing is a tool no agent has — the single most useful
// fact about one — and the page could state it and not change it.
func TestAPipelineCanBeGivenToAnAgentFromItsOwnPage(t *testing.T) {
	app, udb, user, def := pipelinePageFixture(t)
	wren, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Wren", OrchestratorPrompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	// An agent that already holds ANOTHER pipeline: attaching this one
	// must add to its list, not replace what it runs. That is the real
	// difference from a machine, which is single-select.
	other := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Other",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "p"}}})
	busy, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Busy", OrchestratorPrompt: "hi",
		AttachedPipelines: []string{other.ID}})
	if err != nil {
		t.Fatal(err)
	}

	body := `{"agents":["` + wren.ID + `","` + busy.ID + `"]}`
	r := httptest.NewRequest("POST", "/api/pipelines/"+def.ID+"/agents", strings.NewReader(body))
	w := httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("attach: %d %s", w.Code, w.Body.String())
	}
	got, _ := loadAgent(udb, busy.ID)
	if len(got.AttachedPipelines) != 2 {
		t.Fatalf("attaching should ADD to the list, got %v", got.AttachedPipelines)
	}

	// Unchecking removes only this pipeline.
	r = httptest.NewRequest("POST", "/api/pipelines/"+def.ID+"/agents", strings.NewReader(`{"agents":[]}`))
	w = httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	got, _ = loadAgent(udb, busy.ID)
	if len(got.AttachedPipelines) != 1 || got.AttachedPipelines[0] != other.ID {
		t.Errorf("detaching one must leave the agent's others alone: %v", got.AttachedPipelines)
	}

	// And the control is on the page.
	page := httptest.NewRecorder()
	app.handlePipelinePage(page, asUser(httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil), user))
	if !strings.Contains(page.Body.String(), "Assign to agents") {
		t.Error("the page cannot attach the pipeline it describes")
	}
}

// What a run costs, derived. A fanout or a loop turns one line of a
// stage list into twelve calls, and somebody choosing between three
// stages and six should see the price where they are choosing.
func TestThePipelinePageSaysWhatARunCosts(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Costly",
		Stages: []PipelineStage{
			{Name: "plan", Kind: StageWorker, Prompt: "p",
				Output: []PipelineField{{Name: "queries", Type: FieldList, Desc: "q"},
					{Name: "skip", Type: FieldBool, Desc: "s"}}},
			{Name: "gate", Kind: StageBranch, When: "plan.skip", SkipTo: "answer"},
			{Name: "dig", Kind: StageFanout, FanOver: "plan.queries", Prompt: "{item}"},
			{Name: "answer", Kind: StageWorker, Prompt: "done"},
		}})
	r := httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	body := w.Body.String()

	if !strings.Contains(body, "plan, answer cost one model call each") {
		t.Error("plain stages should be counted plainly")
	}
	// The multiplier is the point, and the number comes FROM the cap so
	// it cannot go stale.
	if !strings.Contains(body, "once per item, up to 12") {
		t.Error("a fanout should say it is many calls")
	}
	if !strings.Contains(body, "gate make no model call at all") {
		t.Error("a branch is free and should say so")
	}
	// Duplicate is here too, and it is a client action because a
	// toolbar POST would stay on the original.
	if !strings.Contains(body, "pipeline_duplicate") {
		t.Error("no way to take a copy before experimenting")
	}
}

// You could not make a pipeline from the UI at all — the list offered
// Import and nothing else, which is the same gap machines had until
// v0.6.201: the page built for keeping them could not start one.
func TestThereAreThreeWaysToGetAPipeline(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	adminAuth(t, user)

	// New mints one that RUNS and lands you in it, rather than a form
	// asking for a name.
	r := httptest.NewRequest("GET", "/orchestrate/pipeline?new=1", nil)
	w := httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("new should land in the editor: %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/orchestrate/pipeline?id=") {
		t.Fatalf("wrong destination: %q", loc)
	}
	defs := ListPipelineDefs(udb, user)
	if len(defs) != 1 {
		t.Fatalf("nothing was minted: %d", len(defs))
	}
	if err := defs[0].Validate(); err != nil {
		t.Errorf("what it minted does not run: %v", err)
	}

	// Describe is a page, not a dialog.
	r = httptest.NewRequest("GET", "/orchestrate/pipeline?describe=1", nil)
	w = httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("the describe page does not render: %d", w.Code)
	}
	page := w.Body.String()
	if !strings.Contains(page, `"post_url":"/orchestrate/api/pipelines/draft"`) {
		t.Error("the describe page does not post to the draft endpoint")
	}
	if strings.Contains(page, "modal_button") {
		t.Error("drafting should not happen inside a dialog — its failures would be invisible")
	}

	// And the drafter refuses to store something that would not run,
	// because every other pipeline door refuses too and a stored one
	// that cannot run is a tool an agent will call and be failed by.
	app.LLM = &stubLLM{reply: `{"name":"Broken","stages":[
		{"name":"a","kind":"worker","prompt":"read {stage:nowhere.thing}"}]}`}
	r = httptest.NewRequest("POST", "/orchestrate/api/pipelines/draft",
		strings.NewReader(`{"description":"anything"}`))
	w = httptest.NewRecorder()
	app.handlePipelineDraft(w, asUser(r, user))
	if w.Code == 200 {
		t.Errorf("a draft that would not run must be refused: %s", w.Body.String())
	}
	if len(ListPipelineDefs(udb, user)) != 1 {
		t.Error("and must not be stored")
	}

	// A good draft lands, with the id the redirect substitutes.
	app.LLM = &stubLLM{reply: `{"name":"Research","description":"d","stages":[
		{"name":"plan","kind":"worker","prompt":"break it up",
		 "output":[{"name":"queries","type":"list","desc":"q"}]},
		{"name":"answer","kind":"worker","prompt":"answer {stage:plan.queries}"}]}`}
	r = httptest.NewRequest("POST", "/orchestrate/api/pipelines/draft",
		strings.NewReader(`{"description":"research things"}`))
	w = httptest.NewRecorder()
	app.handlePipelineDraft(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("draft: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"id":`) {
		t.Errorf("the response must carry the id the redirect substitutes: %s", w.Body.String())
	}
}

// Running one from its own page. The streaming endpoints have existed
// since a PipelineDef could back a custom app, and nothing on the
// pipeline's own surface called them — the only way to try a pipeline
// you had just built was to attach it to an agent and ask it nicely.
func TestAPipelineCanBeRunFromItsOwnPage(t *testing.T) {
	app, _, user, def := pipelinePageFixture(t)
	r := httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	body := w.Body.String()

	// The framework's panel, pointed at this pipeline's own endpoints —
	// not a bespoke run box. A custom app backed by a pipeline mounts
	// exactly this, so the transcript, the run history and cancel come
	// for free instead of in a second, worse copy.
	if !strings.Contains(body, `"type":"pipeline_panel"`) {
		t.Fatal("no run panel on the page")
	}
	for _, want := range []string{
		`"submit_url":"api/pipelines/` + def.ID + `/stream"`,
		`"sessions_list_url":"api/pipelines/` + def.ID + `/sessions"`,
		`"session_load_url":"api/pipelines/` + def.ID + `/sessions/{id}"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the panel is missing %s", want)
		}
	}
	// The endpoints it names are actually routed — a panel pointed at a
	// 404 is the same lie as a button that does nothing.
	r = httptest.NewRequest("GET", "/api/pipelines/"+def.ID+"/sessions", nil)
	w = httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	if w.Code == 404 {
		t.Error("the sessions endpoint the panel names is not routed")
	}
	// The input field is named for what the stream endpoint reads.
	if !strings.Contains(body, `"name":"topic"`) {
		t.Error("the input field should be one the stream endpoint accepts")
	}
	// And it says what a run actually costs somebody, because this is
	// not a rehearsal: a machine's Try it has no tools and does not run
	// the step it lands in; this spends real calls and reaches real
	// tools.
	if !strings.Contains(body, "REAL run") {
		t.Error("the panel should say a run is real")
	}
}

// The two editors present the same things in the same ORDER.
//
// They are siblings — a machine is a workflow a conversation sits in, a
// pipeline is one that runs to an end — and somebody moving between
// them should find the same controls in the same places. Within a day
// of each other they had drifted into two different orders: the
// pipeline put Assign and cost before its stages while the machine put
// them after, so "where is the thing I just used" had two answers.
//
// Compared as SEQUENCES rather than sets, because order is the thing
// that drifted, and the machine-only sections (its steps are named
// individually, and it has a checklist a pipeline cannot have) are
// dropped rather than special-cased into the pipeline.
func TestBothEditorsPresentTheSameOrder(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	m := SaveMachineDef(udb, MachineDef{Owner: user, Name: "M", Start: "s",
		Phases: []MachinePhase{{Name: "s", Prompt: "p", Resident: true}}})
	p := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "P",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "p"}}})

	shared := map[string]string{
		"The machine": "identity", "The pipeline": "identity",
		"Describe a change": "describe",
		"Add a step":        "add", "Add a stage": "add",
		"Try it": "try", "Run it": "try",
		"What a turn costs": "cost", "What a run costs": "cost",
		"Assign to agents": "assign",
		"Worth a look":     "advice",
	}
	rail := func(name, url string) []string {
		r := httptest.NewRequest("GET", url, nil)
		w := httptest.NewRecorder()
		if name == "machine" {
			app.handleMachinePage(w, asUser(r, user))
		} else {
			app.handlePipelinePage(w, asUser(r, user))
		}
		if w.Code != 200 {
			t.Fatalf("%s page: %d", name, w.Code)
		}
		var out []string
		seen := map[string]bool{}
		for _, mm := range regexp.MustCompile(`"title":"([^"]{2,40})"`).FindAllStringSubmatch(w.Body.String(), -1) {
			if key, ok := shared[mm[1]]; ok && !seen[key] {
				seen[key] = true
				out = append(out, key)
			}
		}
		return out
	}
	got := rail("machine", "/orchestrate/machine?id="+m.ID)
	want := rail("pipeline", "/orchestrate/pipeline?id="+p.ID)
	if len(got) != len(want) {
		t.Fatalf("the two editors offer different sections:\nmachine:  %v\npipeline: %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("the rails have drifted apart:\nmachine:  %v\npipeline: %v", got, want)
		}
	}
	if len(got) < 7 {
		t.Errorf("expected every shared section, found %v — a title changed and this test stopped checking", got)
	}
}

// The editor page's ?id= is the PIPELINE. The run panel's deep-link
// bootstrap falls back to a bare "id" when an app has not named its param,
// so an unnamed panel here opened a session whose id was the pipeline's,
// got a 404, and painted "Load failed" over the empty state on every first
// load — cleared only by clicking New session.
func TestRunPanelNamesItsDeepLinkParam(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SavePipelineDef(udb, PipelineDef{
		Owner: user, Name: "Research",
		Stages: []PipelineStage{{Name: "one", Prompt: "do"}},
	})
	r := httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"deep_link_param":"session"`) {
		t.Error("the run panel must name its deep-link param, or the page's own ?id= reads as a session id")
	}
}

// Clicking a box opened the right section and then said nothing about
// which one — the map stayed a flat picture with no "you are here",
// which the machine editor's map has had all along. The rail highlights
// its own row; the diagram above it did not.
func TestThePipelineMapSaysWhichStageYouAreOn(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Research",
		Stages: []PipelineStage{
			{Name: "plan", Kind: StageWorker, Prompt: "p"},
			{Name: "refine", Kind: StageLoop, Count: 2, Body: []PipelineStage{
				{Name: "critique", Kind: StageWorker, Prompt: "c"},
			}},
		}})

	r := httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	body := w.Body.String()

	// The mark itself, and something to see when it lands.
	if !strings.Contains(body, "[data-node].here rect") {
		t.Error("nothing styles the stage you are on")
	}
	// Matched on the box's own link, not on a slug recomputed from its
	// name: the sections are numbered ("1. plan"), so the bare name in
	// data-node is not the anchor.
	if !strings.Contains(body, "getAttribute('href')") || !strings.Contains(body, "classList.toggle('here'") {
		t.Error("the map should mark the box whose link is the open section")
	}
	// A click in the map and a click in the rail both set the hash, and
	// the back button walks it.
	if !strings.Contains(body, "addEventListener('hashchange'") {
		t.Error("the mark should follow the hash, not just the first paint")
	}
	// A loop's body steps get sections of their own, so their boxes are
	// doors too — and a door is what can be marked.
	if !strings.Contains(body, `href=\"#refine-critique\"`) {
		t.Errorf("a body step's box should open its own section:\n%s", body)
	}
}

// The map is drawn server-side, so an edit that changed the SHAPE —
// pointing a branch somewhere else, giving a loop a body — left the
// picture describing the pipeline as it was before the edit that was
// made while looking at it. The machine editor redraws; this one did
// not, because there was no endpoint to redraw FROM.
func TestThePipelineMapIsRedrawnWhenAStageChangesTheShape(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Research",
		Stages: []PipelineStage{
			{Name: "plan", Kind: StageWorker, Prompt: "p"},
			{Name: "gate", Kind: StageBranch, When: "ok"},
			{Name: "dig", Kind: StageWorker, Prompt: "d"},
			{Name: "answer", Kind: StageWorker, Prompt: "a"},
		}})

	// The endpoint the refresh calls draws the MAP — anchors and all —
	// or a redraw would quietly cost every box its link.
	r := httptest.NewRequest("GET", "/api/pipelines/"+def.ID+"/graph?links=1", nil)
	w := httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("graph fetch failed: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `href="#3-dig"`) {
		t.Errorf("the refreshed map should keep its links:\n%s", w.Body.String())
	}
	// And the plain endpoint stays plain — it is what a standalone image
	// or a saved copy gets.
	r = httptest.NewRequest("GET", "/api/pipelines/"+def.ID+"/graph", nil)
	w = httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	if strings.Contains(w.Body.String(), `href="#3-dig"`) {
		t.Error("anchors only mean something inside the page that has those sections")
	}

	// Sending a branch somewhere else adds an arrow: what the redraw
	// exists to show.
	before := strings.Count(pipelineGraphSVG(def), "stroke-dasharray")
	def.Stages[1].SkipTo = "answer"
	if after := strings.Count(pipelineGraphSVG(def), "stroke-dasharray"); after <= before {
		t.Error("a branch with a destination should draw its jump")
	}

	// The page DECLARES the refresh rather than scripting it: every
	// derived block is a card with a source, watching the route a stage
	// save posts to. The hand-rolled fetch-and-swap this replaced was
	// the second copy of the same twenty lines in this package.
	r = httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil)
	w = httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	body := w.Body.String()
	stages := `api/pipelines/` + def.ID + `/stages`
	for _, name := range []string{"map", "plan", "cost", "checklist"} {
		src := `"source":"api/pipelines/` + def.ID + `/block?name=` + name + `"`
		if !strings.Contains(body, src) {
			t.Errorf("the %s block should refetch itself; no source for it:\n%s", name, body)
		}
	}
	if n := strings.Count(body, `"refresh_on":["`+stages+`"]`); n != 4 {
		t.Errorf("want all four derived blocks watching the stage route, found %d", n)
	}
	// The mark is the page's own business, and a redraw is new elements.
	if !strings.Contains(body, "ui-card-refreshed") {
		t.Error("the you-are-here mark has to go back on after a redraw")
	}
	// Each block is served by the endpoint the card names.
	for _, tc := range []struct{ name, want string }{
		{"map", "machine-map-cap"},
		{"plan", "plan"},
		{"cost", "model call"},
		{"checklist", "read as instructions"},
	} {
		r = httptest.NewRequest("GET", "/api/pipelines/"+def.ID+"/block?name="+tc.name, nil)
		w = httptest.NewRecorder()
		app.handlePipelineOne(w, asUser(r, user))
		if w.Code != 200 {
			t.Errorf("block %q does not render: %d", tc.name, w.Code)
			continue
		}
		if !strings.Contains(strings.ToLower(w.Body.String()), strings.ToLower(tc.want)) {
			t.Errorf("block %q looks wrong:\n%s", tc.name, w.Body.String())
		}
	}
	// An unknown block is a 404, not an empty card: a card told to
	// refresh from a route that answers 200 with nothing would blank
	// itself on the first save.
	r = httptest.NewRequest("GET", "/api/pipelines/"+def.ID+"/block?name=nonsense", nil)
	w = httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	if w.Code != 404 {
		t.Errorf("an unknown block should 404, got %d", w.Code)
	}
}
