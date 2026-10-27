package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

func assistTestAgent() AgentRecord {
	return AgentRecord{
		ID: "a1", Owner: "craig@example.com", Name: "Scout",
		Description:        "Looks things up.",
		OrchestratorPrompt: "You are Scout.",
		Rules:              "Cite every claim.",
		AllowedTools:       []string{"web_search"},
	}
}

// The allowlist is what keeps a conversation from changing what an agent can
// REACH. An unrecognized field must be dropped, not applied: the failure of a
// denylist here is silent and permanent, while the failure of an allowlist is
// a change that does not appear.
func TestAssistOnlyProposesFieldsItMayChange(t *testing.T) {
	rec := assistTestAgent()
	got := parseAssistReply(`{"reply":"Tightened it.","changes":[
		{"field":"rules","why":"it had none about training","value":"Cite every claim.\nNever answer from training."},
		{"field":"allowed_tools","why":"it needs the web","value":"web_search,fetch_url"},
		{"field":"exposed","why":"so people can use it","value":"true"},
		{"field":"max_worker_rounds","why":"more room","value":"40"}
	]}`, rec)

	if len(got.Changes) != 1 || got.Changes[0].Field != "rules" {
		t.Fatalf("changes = %+v, want the rules change alone", got.Changes)
	}
	if got.Changes[0].Label != "Rules" {
		t.Errorf("label = %q", got.Changes[0].Label)
	}
	if got.Changes[0].Current != rec.Rules {
		t.Errorf("the current value is not carried, so nothing can be diffed: %q", got.Changes[0].Current)
	}
	if got.Reply != "Tightened it." {
		t.Errorf("reply = %q", got.Reply)
	}
}

// A change that changes nothing wastes the one decision this dialog asks for.
func TestAssistDropsNoOpChanges(t *testing.T) {
	rec := assistTestAgent()
	got := parseAssistReply(`{"reply":"Looks fine.","changes":[
		{"field":"rules","why":"unchanged","value":"Cite every claim."},
		{"field":"name","why":"blank","value":"   "}
	]}`, rec)
	if len(got.Changes) != 0 {
		t.Errorf("changes = %+v, want none", got.Changes)
	}
}

// Two changes to one field would apply in whichever order the loop happened to
// run, so only the first survives.
func TestAssistKeepsOneChangePerField(t *testing.T) {
	got := parseAssistReply(`{"reply":"x","changes":[
		{"field":"name","value":"First"},
		{"field":"name","value":"Second"}
	]}`, assistTestAgent())
	if len(got.Changes) != 1 || got.Changes[0].Value != "First" {
		t.Errorf("changes = %+v", got.Changes)
	}
}

// A model that answers in prose asked a question or answered one. That is
// worth more to the person than a failure toast, so it degrades to a reply.
func TestAssistDegradesToAPlainReply(t *testing.T) {
	got := parseAssistReply("It already refuses to answer from training, so I would leave it.", assistTestAgent())
	if len(got.Changes) != 0 {
		t.Errorf("changes invented from prose: %+v", got.Changes)
	}
	if !strings.HasPrefix(got.Reply, "It already refuses") {
		t.Errorf("reply = %q", got.Reply)
	}
	// And a fenced object is still an object, however plainly the model was
	// asked not to fence it.
	fenced := parseAssistReply("```json\n{\"reply\":\"Done.\",\"changes\":[{\"field\":\"name\",\"value\":\"Ranger\"}]}\n```", assistTestAgent())
	if len(fenced.Changes) != 1 || fenced.Changes[0].Value != "Ranger" {
		t.Errorf("a fenced reply was not parsed: %+v", fenced)
	}
}

// The prompt has to show the agent as the PERSON sees it, or the advice
// contradicts what is on their screen, and it has to carry the shape's own
// guidance when there is one rather than re-deriving worse answers.
func TestAssistPromptCarriesTheAgentAndItsShape(t *testing.T) {
	rec := assistTestAgent()
	rec.ShapeID = "conversational"
	p := buildAgentAssistPrompt(rec)

	if !strings.Contains(p, "Scout") || !strings.Contains(p, "Cite every claim.") {
		t.Error("the prompt does not carry the agent's own values")
	}
	if !strings.Contains(p, "Reaches web_search.") {
		t.Error("the prompt does not describe the agent the way the user sees it")
	}
	if !strings.Contains(p, "conversational shape") || !strings.Contains(p, "Build this when") {
		t.Error("the shape's own recipe is not in the prompt, so assist would re-derive it worse")
	}
	// Every assistable field is named with its current value, and the two
	// rules that stop a silent lie are stated.
	for f := range assistableFields {
		if !strings.Contains(p, "("+f+")") {
			t.Errorf("field %q is not offered to the model", f)
		}
	}
	if !strings.Contains(p, "COMPLETE new value") {
		t.Error("nothing tells the model a partial value deletes the rest")
	}
	if !strings.Contains(p, "not yours to change") {
		t.Error("nothing stops the model claiming it changed the agent's reach")
	}

	// An agent that follows nothing gets no shape section rather than an
	// empty heading.
	plain := buildAgentAssistPrompt(assistTestAgent())
	if strings.Contains(plain, "What this kind of agent is meant to be") {
		t.Error("an agent with no shape was given a shape section")
	}
}

// Reach and exposure are decisions with consequences past the conversation,
// and they belong at the control that owns them, with its help text and its
// confirmation.
func TestAssistCannotTouchReachOrExposure(t *testing.T) {
	for _, f := range []string{"allowed_tools", "exposed", "hidden", "shape_id", "max_worker_rounds", "force_private", "fleet"} {
		if _, ok := assistableFields[f]; ok {
			t.Errorf("%q is assistable, and it should be changed at the control that owns it", f)
		}
	}
}

// The dialog is injected HTML, so the checks a compiler cannot make have to
// live here: that it talks to the endpoints it means to, and that no format
// verb went unfilled. A stray %!q(MISSING) in a script tag is a dialog that
// silently never opens.
func TestAssistDialogHTML(t *testing.T) {
	got := agentAssistHTML("agent-123")

	for _, want := range []string{
		"../api/agents/'+id+'/assist", // ask
		"../api/agents/'+id",          // apply, via the PATCH allowlist
		"'PATCH'",
		"window.uiOpenModal", // THE modal, not a hand-rolled overlay
		// The shared conversation pane and its client: the spinner, Stop
		// and web lookups every ✨ has, not a conversation of its own.
		"window.uiAssistChat({",
		"send:window.uiAssistPost('../api/agents/'+id+'/assist'),",
		"web:true,",
		"onResult:function(res){",
		"agent-123",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the dialog does not contain %q", want)
		}
	}
	if strings.Contains(got, "%!") {
		t.Error("a format verb went unfilled, which ships a script that cannot parse")
	}
	// The button is the forms' ✨ Get assistance pill, words and all: a bare
	// ✨ in the header said nothing about what it was.
	if !strings.Contains(got, "b.className='ui-form-assist'; b.textContent='✨ Get assistance';") {
		t.Error("the agent's assist button is not labelled like every form's ✨ Get assistance")
	}
	// Applying must not be able to reach past what assist may propose: the
	// patch is built from the returned changes, so nothing here should name a
	// reach or exposure field.
	for _, forbidden := range []string{"allowed_tools", "exposed", "force_private", "max_worker_rounds"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the dialog names %q; reach and exposure belong to the controls that own them", forbidden)
		}
	}
	// triggers is a list on the record, and sending the box as a string would
	// store one trigger that is the whole box.
	if !strings.Contains(got, "c.field==='triggers'") {
		t.Error("triggers is applied without being split into a list")
	}
}

// One ✨ per agent: the edit page's own assist sees the whole agent, so its
// section forms carry no Get assistance of their own (seen: two sparkles).
func TestTheAgentEditPageHasOneAssist(t *testing.T) {
	fields := []ui.FormField{
		{Field: "name", Label: "Name"},
		{Type: "header", Label: "Prompts"},
		{Field: "orchestrator_prompt", Label: "Persona", Type: "textarea"},
		{Type: "header", Label: "Rules"},
		{Field: "rules", Label: "Rules", Type: "textarea"},
	}
	secs := splitAgentFormSections("a1", "../api/agents?id=a1", fields, "")
	if len(secs) == 0 {
		t.Fatal("no sections")
	}
	raw, _ := json.Marshal(secs)
	if strings.Contains(string(raw), `"assist":true`) {
		t.Error("an edit section's form should not carry its own ✨")
	}
}

// The agent editor's assists run on core/assist: what reached the person
// before reaches them now, through the one handler. A conversation turn
// comes back as the reply and the cleaned draft; a request with no message
// is still the one-shot suggestion; the whole-agent assist still proposes
// only the fields it may change; the routes are registered private worker
// stages.

func assistAppFixture(t *testing.T, fake *FakeLLM) (*OrchestrateApp, func(path, body string) *http.Request) {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:u", AuthUser{Username: "u"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	T := &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}, LLM: fake}}
	req := func(path, body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), "u")})
		return r
	}
	return T, req
}

func TestTheFieldSuggestRunsOnTheAssistHandler(t *testing.T) {
	fake := &FakeLLM{Turns: []FakeTurn{
		{Content: "Made it shorter.\n```draft\n\"You are Scout.\"\n```"},
		{Content: "Scout\nRanger\nSeeker"},
	}}
	T, req := assistAppFixture(t, fake)
	call := func(body string) (*httptest.ResponseRecorder, map[string]any) {
		w := httptest.NewRecorder()
		T.handleAgentSuggest(w, req("/api/agents/suggest", body))
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}
	// A turn of the workbench: the reply, and the draft cleaned for its field.
	w, out := call(`{"field":"orchestrator_prompt","message":"shorter","draft":"You are Scout, a helper.","mode":"write"}`)
	if w.Code != 200 || out["reply"] != "Made it shorter." || out["value"] != "You are Scout." {
		t.Fatalf("a workbench turn: %d %s", w.Code, w.Body.String())
	}
	cfg := fake.Config(0)
	if cfg.RouteKey != "app.orchestrate.suggest" || cfg.Think == nil || *cfg.Think || !strings.Contains(cfg.SystemPrompt, "ONE value in an AI agent's definition") {
		t.Errorf("the turn should carry its own prompt, route and no thinking: %q %v", cfg.RouteKey, cfg.Think)
	}
	// No message: the one-shot, a name with a few to choose from.
	w, out = call(`{"field":"name","record":{"description":"looks things up"}}`)
	if vals, _ := out["values"].([]any); w.Code != 200 || len(vals) < 2 {
		t.Errorf("the one-shot name should offer several: %d %s", w.Code, w.Body.String())
	}
	if w, _ := call(`{"field":"allowed_tools","message":"x"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a field that is not suggestable: %d", w.Code)
	}
	if !IsPrivateStage("app.orchestrate.suggest") {
		t.Error("the suggest route should be a private worker stage")
	}
}

func TestTheWholeAgentAssistRunsOnTheAssistHandler(t *testing.T) {
	fake := &FakeLLM{Turns: []FakeTurn{{Content: `{"reply":"Added a rule.","changes":[{"field":"rules","why":"stops guessing","value":"Cite every claim. Say when you do not know."},{"field":"allowed_tools","value":"browse_page"}]}`}}}
	T, req := assistAppFixture(t, fake)
	udb := &DBase{Store: kvlite.MemStore()}
	rec := assistTestAgent()
	rec.Owner = "u"
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	T.handleAgentAssist(w, req("/api/agents/a1/assist", `{"message":"it guesses"}`), "u", udb, rec.ID)
	var out assistReply
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out.Reply != "Added a rule." || len(out.Changes) != 1 || out.Changes[0].Field != "rules" {
		t.Fatalf("want the reply and the one change it may make: %d %s", w.Code, w.Body.String())
	}
	if fake.Config(0).RouteKey != "app.orchestrate.assist" || !IsPrivateStage("app.orchestrate.assist") {
		t.Error("the whole-agent assist's route should be registered, a private worker stage")
	}
	w = httptest.NewRecorder()
	T.handleAgentAssist(w, req("/api/agents/a1/assist", `{"message":"x"}`), "someone-else", udb, rec.ID)
	if w.Code != http.StatusForbidden {
		t.Errorf("another person's agent: %d", w.Code)
	}
}

// The dialog is the shared conversation pane now, so a turn arrives as
// uiAssistPost sends it: streamed lines asked for, the mode and window on it,
// web lookups when the person turned them on. The last line still carries
// the changes, and the lookups are allowed here, as on every ✨.
func TestTheWholeAgentAssistAnswersTheSharedPane(t *testing.T) {
	fake := &FakeLLM{Turns: []FakeTurn{{Content: `{"reply":"Added a rule.","changes":[{"field":"rules","why":"stops guessing","value":"Cite every claim. Say when you do not know."}]}`}}}
	T, req := assistAppFixture(t, fake)
	udb := &DBase{Store: kvlite.MemStore()}
	rec := assistTestAgent()
	rec.Owner = "u"
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := req("/api/agents/a1/assist", `{"message":"it guesses","history":[],"mode":"write","window":"w1","web":true}`)
	r.Header.Set("Accept", "application/x-ndjson")
	T.handleAgentAssist(w, r, "u", udb, rec.ID)
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	var out assistReply
	json.Unmarshal([]byte(lines[len(lines)-1]), &out)
	if w.Code != 200 || out.Reply != "Added a rule." || len(out.Changes) != 1 || out.Changes[0].Field != "rules" {
		t.Fatalf("the last line should be the reply and its change: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(fake.Config(0).SystemPrompt, "turned on web lookups") {
		t.Error("web lookups the person turned on should be allowed here")
	}
}
