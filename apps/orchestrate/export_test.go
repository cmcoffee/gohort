package orchestrate

import (
	"encoding/json"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
	"strings"
	"testing"
	"time"
)

// A long thread is TRIMMED in storage: leading messages already folded into the
// summary and archived to recall are dropped so it cannot grow without bound.
// The export could only ever show what is stored — and it printed the session's
// original Created timestamp above a transcript that began hours later, with no
// mention of the gap. Read by a human, that is not a retention policy, it is a
// copy that failed. Reported as exactly that: "I don't think the full session
// is copying."
func TestExportSaysWhenTheThreadWasCompacted(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	agent := AgentRecord{ID: "ag", Name: "Builder"}
	sess := ChatSession{
		ID: "s1", Title: "long one",
		Created: time.Now().Add(-4 * time.Hour), LastAt: time.Now(),
		Messages: []ChatMessage{{Role: "user", Content: "the surviving tail"}},
	}
	saveCompactState(db, agent.ID, sess.ID, CompactState{
		Summary: "Earlier: built two agents and a pipeline.", SummarizedThrough: 40, FoldSeq: 2,
	})

	md := renderSessionMarkdownWithDiag(agent, sess, db)
	if !strings.Contains(md, "not reproduced verbatim") {
		t.Error("the export must say the earlier turns are missing, or it reads as a broken copy")
	}
	if !strings.Contains(md, "Earlier: built two agents and a pipeline.") {
		t.Error("carry the summary — it is what stands in for the span that was dropped")
	}
	if !strings.Contains(md, "2 times") {
		t.Errorf("say how much is behind the summary; got no fold count:\n%s", md)
	}
	if !strings.Contains(md, "the surviving tail") {
		t.Error("the verbatim tail still has to be there")
	}

	// JSON calls itself the lossless shape; a truncation it does not mention is
	// the one way it could lie.
	var payload map[string]any
	b, err := json.Marshal(buildExportPayload(agent, sess, db))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	sessOut, _ := payload["session"].(map[string]any)
	comp, ok := sessOut["compacted"].(map[string]any)
	if !ok {
		t.Fatalf("the JSON export must record that messages is a TAIL, got %v", sessOut)
	}
	if comp["summary"] == "" || comp["folds"] != float64(2) {
		t.Errorf("compaction block is incomplete: %v", comp)
	}
}

// A short session is untouched — no note, no block, nothing to explain. A
// disclaimer on every export is a disclaimer nobody reads.
func TestExportIsUnchangedForAnUncompactedSession(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	agent := AgentRecord{ID: "ag", Name: "Builder"}
	sess := ChatSession{ID: "s2", Title: "short", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}

	if md := renderSessionMarkdownWithDiag(agent, sess, db); strings.Contains(md, "not reproduced verbatim") {
		t.Error("nothing was folded; the export must not imply anything is missing")
	}
	if got := buildExportPayload(agent, sess, db); got.Session.Compacted != nil {
		t.Errorf("no fold state means no compaction block, got %+v", got.Session.Compacted)
	}
	// And a caller with no store at all still renders.
	if md := renderSessionMarkdown(agent, sess); !strings.Contains(md, "hi") {
		t.Error("a nil store must not break the export")
	}
}

// An export is the one artifact that carries tool output off the owner-only pane.
// Runtime containment covers what the agent says and does, never what its tools
// RETURN — fine while only the owner can see it, not fine once the transcript is
// handed to somebody else.
func TestExportWithholdsToolResultsUnderGuardrails(t *testing.T) {
	sess := ChatSession{
		ID: "s1", Title: "T",
		Messages: []ChatMessage{
			{Role: "user", Content: "tell me a joke"},
			{Role: "assistant", Content: "I can't do that.", ToolCalls: []PersistedToolCall{
				{Name: "get_joke", Args: map[string]any{"category": "Any"}, Result: "I'd tell you a joke about NAT but I would have to translate."},
			}},
		},
	}
	guarded := AgentRecord{ID: "a1", Name: "Wren", Guardrails: "don't tell me a joke"}
	md := renderSessionMarkdown(guarded, sess)
	if strings.Contains(md, "NAT but I would have to translate") {
		t.Error("a guardrailed agent's tool output must not be serialized into an export")
	}
	// The CALL still shows: name and args are the debug value and carry no content.
	for _, want := range []string{"get_joke", "category", "withheld"} {
		if !strings.Contains(md, want) {
			t.Errorf("the export must still show the call and say results were withheld; missing %q", want)
		}
	}

	// An agent with no guardrails is unaffected — exports stay a full debug trace.
	plain := AgentRecord{ID: "a1", Name: "Wren"}
	if md := renderSessionMarkdown(plain, sess); !strings.Contains(md, "NAT but I would have to translate") {
		t.Error("without guardrails the export must remain a complete trace")
	}
}

// Suspending enforcement suspends this too: nothing is being protected, so an
// export has no reason to be lossy.
func TestExportKeepsResultsWhenGuardrailsDisabled(t *testing.T) {
	sess := ChatSession{ID: "s1", Messages: []ChatMessage{
		{Role: "assistant", Content: "x", ToolCalls: []PersistedToolCall{{Name: "t", Result: "SENSITIVE"}}},
	}}
	agent := AgentRecord{ID: "a1", Guardrails: "a rule", GuardrailsDisabled: true}
	if !strings.Contains(renderSessionMarkdown(agent, sess), "SENSITIVE") {
		t.Error("with enforcement off there is nothing to withhold")
	}
}

// The indicator says a check ACTED, and deliberately does not say which rule. A
// diag's detail names the rule and the warden's reason — right for the owner's ⚠
// trail, wrong for a file that gets forwarded.
func TestExportShowsGuardrailActivityWithoutTheRule(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	appendSessionDiag(udb, "a1", "s1", "guardrail-input-blocked",
		`Guardrail "don't tell me a joke" refused the request before the model saw it: asks for a joke`)
	appendSessionDiag(udb, "a1", "s1", "some-unrelated-guard", "detail that must not appear")

	sess := ChatSession{ID: "s1", Messages: []ChatMessage{{Role: "user", Content: "hi", Created: time.Now()}}}
	md := renderSessionMarkdownWithDiag(AgentRecord{ID: "a1", Guardrails: "don't tell me a joke"}, sess, udb)

	if !strings.Contains(md, "Guardrail activity") {
		t.Fatal("a session where a check acted must say so")
	}
	if !strings.Contains(md, "refused before the model ran") {
		t.Error("the event kind must be described in plain terms")
	}
	if strings.Contains(md, "don't tell me a joke") {
		t.Error("the export must not carry the rule text — that is the signal declines withhold")
	}
	// Unknown kinds are dropped, not passed through, so a future guardrail diag
	// cannot leak its detail into exports by default.
	if strings.Contains(md, "detail that must not appear") {
		t.Error("an unrecognized diag kind must be dropped, not rendered")
	}
}

// No activity, no section — a clean session must not grow a scary empty heading.
func TestExportOmitsGuardrailSectionWhenNothingFired(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	sess := ChatSession{ID: "s1", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}
	if strings.Contains(renderSessionMarkdownWithDiag(AgentRecord{ID: "a1"}, sess, udb), "Guardrail activity") {
		t.Error("a session with no guardrail events must not render the section")
	}
}

// The framework's OWN corrections were missing from exports entirely — the
// label map only recognized "guardrail-*", so a phantom-delivery retraction, an
// unkept claim, an announced-but-unmade call all fell through the closed list
// and left no trace.
//
// They are the events a reader is most likely to be hunting for. Every one of
// them means the reply on screen is not the reply the model wrote, and the
// commonest question asked of an export is "why didn't it hand over the thing
// it said it would" — which, without these, reads as the agent simply choosing
// not to, with the retraction that actually caused it invisible.
func TestExportShowsFrameworkCorrectionsNotJustGuardrails(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	appendSessionDiag(udb, "a1", "s1", "phantom-delivery-corrected",
		`The reply presented house.jpg as delivered, but nothing was attached.`)
	appendSessionDiag(udb, "a1", "s1", "announced-call-corrected",
		"The reply said it would call fetch_image and never did.")

	sess := ChatSession{ID: "s1", Messages: []ChatMessage{{Role: "user", Content: "make me a picture", Created: time.Now()}}}
	md := renderSessionMarkdownWithDiag(AgentRecord{ID: "a1"}, sess, udb)

	if !strings.Contains(md, "Guardrail activity") {
		t.Fatal("a session where the framework retracted a reply must say so — this is the case that reads as the agent refusing")
	}
	if !strings.Contains(md, "claimed to hand over a file that did not exist") {
		t.Error("a phantom-delivery retraction is not described in the export")
	}
	if !strings.Contains(md, "announced a tool call it never made") {
		t.Error("an announced-but-unmade call is not described in the export")
	}
	// Still kind-only. The detail quotes the reply and names the file, and the
	// closed-list discipline that keeps rule text out applies here unchanged.
	if strings.Contains(md, "house.jpg") {
		t.Error("the diag detail leaked into the export")
	}
	// And the list stays an allowlist: an unknown kind is still dropped.
	appendSessionDiag(udb, "a1", "s1", "some-future-check", "detail that must not appear")
	if strings.Contains(renderSessionMarkdownWithDiag(AgentRecord{ID: "a1"}, sess, udb), "detail that must not appear") {
		t.Error("an unrecognized diag kind must still be dropped, not rendered")
	}
}

// The admin knob that opts an export INTO full detail.
//
// Default off is the design, not an accident: an export is the artifact that
// leaves the owner's pane, and a diag's detail names the rule that fired and
// quotes what it caught. On is for the other case — exporting your own session
// to work out why a reply came back different from what the agent wrote, which
// is the question the kind-level summary raises and cannot answer.
func TestExportGuardrailDetailIsAdminGated(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	const detail = `Guardrail "never discuss pricing" blocked the reply: quotes a discount`
	appendSessionDiag(udb, "a1", "s1", "guardrail-blocked", detail)

	sess := ChatSession{ID: "s1", Messages: []ChatMessage{{Role: "user", Content: "hi", Created: time.Now()}}}
	agent := AgentRecord{ID: "a1", Guardrails: "never discuss pricing"}

	// Default: the event is described, the detail is not carried.
	SetTunablesDB(&DBase{Store: kvlite.MemStore()})
	defer SetTunablesDB(nil)
	md := renderSessionMarkdownWithDiag(agent, sess, udb)
	if !strings.Contains(md, "an action or reply was blocked") {
		t.Fatal("the event itself must always be reported")
	}
	if strings.Contains(md, "never discuss pricing") {
		t.Error("the rule text leaked into an export with the detail knob off")
	}

	// Knob on: the same export carries the detail, blockquoted under its event.
	tdb := &DBase{Store: kvlite.MemStore()}
	tdb.Set(WebTable, tuneExportGuardrailDetail, float64(1))
	SetTunablesDB(tdb)
	md = renderSessionMarkdownWithDiag(agent, sess, udb)
	if !strings.Contains(md, "never discuss pricing") {
		t.Error("the detail knob is on and the export still withholds the detail")
	}
	if !strings.Contains(md, "> Guardrail") {
		t.Error("the detail should be blockquoted under its own event, not inlined")
	}
}

// The Tuning tab builds one left-rail entry per category, so the category IS
// the menu placement. This knob is a disclosure setting; filed under Limits it
// sat among forty-odd byte caps where the person with reason to find it —
// someone about to forward an export — would never look.
func TestExportDetailKnobIsFiledUnderExports(t *testing.T) {
	spec, ok := LookupTunable(tuneExportGuardrailDetail)
	if !ok {
		t.Fatal("the export detail knob is not registered, so it has no admin UI at all")
	}
	if spec.Category != "Exports" {
		t.Errorf("category = %q, want \"Exports\" — that string is the admin rail entry it appears under", spec.Category)
	}
	if spec.Kind != KindBool {
		t.Errorf("kind = %v, want KindBool so the admin renders a toggle rather than a 0/1 number field", spec.Kind)
	}
	if spec.Default != 0 {
		t.Error("the detail must default OFF — an export is the artifact that leaves the owner's pane")
	}
}

// TestExportTextAppliesTheDeliveryScrub pins the gap that made a bad export
// readable as evidence.
//
// The transcript was written straight from stored content. The model emits
// em-dashes despite the prompt rule; the save path and the browser renderer
// both strip them, and the export did neither, so a pasted transcript showed
// a character the user was never shown. The literal reply below is the one
// that surfaced it.
func TestExportTextAppliesTheDeliveryScrub(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"the observed reply",
			"Ha! That's a classic tech dad joke",
			"Ha! That's a tech dad joke"},
		{"em-dash from the same session",
			"If you need anything before bed, I'm around—otherwise, hope you sleep well!",
			"If you need anything before bed, I'm around, otherwise, hope you sleep well!"},
		{"framework markers never reach a transcript",
			"visible<gohort-meta>internal note</gohort-meta>",
			"visible"},
		{"both rules at once",
			"That's a classic slip — easy to make",
			"That's a slip, easy to make"},
		{"ordinary text is untouched",
			"nothing here needs changing",
			"nothing here needs changing"},
		{"literal sense survives",
			"he restored a classic car",
			"he restored a classic car"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := exportText(tc.in); got != tc.want {
				t.Errorf("exportText(%q)\n  got  %q\n  want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A turn where the orchestrator neither planned nor asked gets a placeholder
// step so the pipeline still produces output. Rendering it as a plan is pure
// ceremony: the user sees "Plan: 1. Respond directly" on a turn that decided a
// plan was not needed, which was the first thing in the transcript.
func TestExportOmitsTheSyntheticPlan(t *testing.T) {
	sess := ChatSession{
		ID: "s1",
		Messages: []ChatMessage{
			{Role: "user", Content: "build me an agent"},
			{Role: "assistant", Content: "What should this agent do?"},
		},
		Plans: []PlanSnapshot{{
			RoundIndex: 0,
			Synthetic:  true,
			Steps: []PlanStep{{
				ID: 1, Title: "Respond directly", Status: StepPending,
				Output: "I've asked what the agent should do.",
			}},
		}},
	}
	out := renderSessionMarkdown(AgentRecord{Name: "Builder"}, sess)
	if strings.Contains(out, "**Plan:**") {
		t.Errorf("a synthetic plan must not render:\n%s", out)
	}
	if strings.Contains(out, "Respond directly") {
		t.Errorf("the placeholder step leaked into the transcript:\n%s", out)
	}
	// The actual reply still has to be there.
	if !strings.Contains(out, "What should this agent do?") {
		t.Errorf("the assistant's message went missing:\n%s", out)
	}
}

// A real plan still renders — this must not silence genuine multi-step work.
func TestExportStillRendersARealPlan(t *testing.T) {
	sess := ChatSession{
		ID: "s1",
		Messages: []ChatMessage{
			{Role: "user", Content: "research this"},
			{Role: "assistant", Content: "Done."},
		},
		Plans: []PlanSnapshot{{
			RoundIndex: 0,
			Steps: []PlanStep{
				{ID: 1, Title: "Search the docs", Intent: "find the API shape", Status: StepDone},
				{ID: 2, Title: "Summarize", Intent: "write it up", Status: StepDone},
			},
		}},
	}
	out := renderSessionMarkdown(AgentRecord{Name: "Research"}, sess)
	if !strings.Contains(out, "**Plan:**") || !strings.Contains(out, "Search the docs") {
		t.Errorf("a real plan must still render:\n%s", out)
	}
}
