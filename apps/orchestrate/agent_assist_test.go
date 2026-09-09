package orchestrate

import (
	"strings"
	"testing"
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
	rec.ShapeID = "research"
	p := buildAgentAssistPrompt(rec)

	if !strings.Contains(p, "Scout") || !strings.Contains(p, "Cite every claim.") {
		t.Error("the prompt does not carry the agent's own values")
	}
	if !strings.Contains(p, "Reaches web_search.") {
		t.Error("the prompt does not describe the agent the way the user sees it")
	}
	if !strings.Contains(p, "research shape") || !strings.Contains(p, "Build this when") {
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
		"agent-123",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the dialog does not contain %q", want)
		}
	}
	if strings.Contains(got, "%!") {
		t.Error("a format verb went unfilled, which ships a script that cannot parse")
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
