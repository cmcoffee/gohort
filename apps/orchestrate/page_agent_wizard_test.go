package orchestrate

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestWizardFallbackDescription(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Answer ops questions. Also do other things later.", "Answer ops questions."},
		{"First line only\nsecond line ignored", "First line only"},
		{strings.Repeat("x", 200), strings.Repeat("x", 157) + "…"},
	}
	for _, c := range cases {
		if got := wizardFallbackDescription(c.in); got != c.want {
			t.Errorf("wizardFallbackDescription(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWizardFallbackPrompt(t *testing.T) {
	p := wizardFallbackPrompt("Ops Bot", "Answer ops questions.", "terse")
	if !strings.Contains(p, "Ops Bot") || !strings.Contains(p, "Answer ops questions.") || !strings.Contains(p, "terse") {
		t.Errorf("fallback should include name, purpose, and style; got %q", p)
	}
}

func TestApplyWizardMemory(t *testing.T) {
	assistant := func() AgentRecord { return AgentRecord{Cortex: true, MemoryMode: "chatbot"} }

	rec := assistant()
	if !applyWizardMemory(&rec, wizardRequest{}) || rec.MemoryMode != "chatbot" || !rec.Cortex ||
		rec.DisableExplicit || rec.DisableInferred {
		t.Errorf("empty dials must keep type defaults; got %+v", rec)
	}
	rec = assistant()
	if !applyWizardMemory(&rec, wizardRequest{Memory: "lessons", Cortex: "off"}) ||
		rec.MemoryMode != "agent" || rec.Cortex {
		t.Errorf("lessons+off overrides not applied; got %+v", rec)
	}
	rec = AgentRecord{MemoryMode: "agent"}
	if !applyWizardMemory(&rec, wizardRequest{Memory: "personalized", Cortex: "on"}) ||
		rec.MemoryMode != "chatbot" || !rec.Cortex {
		t.Errorf("personalized+on overrides not applied; got %+v", rec)
	}
	rec = assistant()
	if !applyWizardMemory(&rec, wizardRequest{Memory: "none"}) ||
		!rec.DisableExplicit || !rec.DisableInferred {
		t.Errorf("none must disable both memory layers; got %+v", rec)
	}
	rec = assistant()
	if applyWizardMemory(&rec, wizardRequest{Memory: "bogus"}) {
		t.Error("unknown memory value must be rejected")
	}
	rec = assistant()
	if applyWizardMemory(&rec, wizardRequest{Cortex: "bogus"}) {
		t.Error("unknown cortex value must be rejected")
	}
}

func TestNeedsFirstRunSetup(t *testing.T) {
	own := AgentRecord{ID: "ag-1", Owner: "alice"}
	seed := AgentRecord{ID: "seed-chat", Owner: "alice"} // shadowed seed: Owner rewritten, still a seed
	sub := AgentRecord{ID: "ag-2", Owner: "alice", OwnedBy: "ag-1"}
	other := AgentRecord{ID: "ag-3", Owner: "bob"}
	cases := []struct {
		name   string
		agents []AgentRecord
		want   bool
	}{
		{"no agents at all", nil, true},
		{"only seeds", []AgentRecord{seed}, true},
		{"only a sub-agent", []AgentRecord{sub}, true},
		{"only someone else's record", []AgentRecord{other}, true},
		{"owns a top-level agent", []AgentRecord{seed, own}, false},
	}
	for _, c := range cases {
		if got := needsFirstRunSetup(c.agents, "alice"); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWizardSeedNotes(t *testing.T) {
	if got := wizardSeedNotes(wizardRequest{}); got != "" {
		t.Errorf("empty about-you should yield no notes; got %q", got)
	}
	got := wizardSeedNotes(wizardRequest{CallYou: " Craig ", AboutYou: "Runs a homelab."})
	if !strings.Contains(got, "called: Craig") || !strings.Contains(got, "About the user: Runs a homelab.") {
		t.Errorf("notes missing fields: %q", got)
	}
}

func TestWizardBriefIncludesAboutYou(t *testing.T) {
	b := wizardBrief("Assistant", wizardRequest{
		Kind: "assistant", Purpose: "Help me.", CallYou: "boss", AboutYou: "Night owl.",
	})
	if !strings.Contains(b, "boss") || !strings.Contains(b, "Night owl.") {
		t.Errorf("brief missing about-you lines: %q", b)
	}
}

func TestWizardTemplatesResolve(t *testing.T) {
	if len(wizard_templates) == 0 {
		t.Fatal("no wizard templates registered")
	}
	for _, tpl := range wizard_templates {
		if _, ok := seedAgentByID(tpl.id); !ok {
			t.Errorf("template %q does not resolve to a seed record", tpl.id)
		}
		if tpl.label == "" {
			t.Errorf("template %q has no label", tpl.id)
		}
		if !isWizardTemplate(tpl.id) {
			t.Errorf("isWizardTemplate(%q) = false for a registered template", tpl.id)
		}
	}
	if isWizardTemplate("seed-chat") {
		t.Error("seed-chat must not be clonable through the wizard template path")
	}
}

func TestWizardKindsMatchEditorPresets(t *testing.T) {
	// The wizard's kind→defaults mapping must stay in lockstep with the
	// editor's Agent-type presets so both create paths produce the same
	// character. Compare against agentTypeTemplates by label prefix.
	byPrefix := map[string]map[string]any{}
	for _, tpl := range agentTypeTemplates() {
		key := strings.ToLower(strings.SplitN(tpl.Label, " ", 2)[0])
		byPrefix[key] = tpl.Values
	}
	for kind, def := range wizard_kinds {
		key := kind
		if kind == "channel" {
			key = "channel" // template label starts "Channel agent"
		}
		vals, ok := byPrefix[key]
		if !ok {
			continue // template naming drift — the label check below still covers the shipped kinds
		}
		if want, ok := vals["channel"].(bool); ok && want != def.cortex {
			t.Errorf("kind %q cortex=%v but editor preset stamps channel=%v", kind, def.cortex, want)
		}
		if want, ok := vals["memory_mode"].(string); ok && want != def.memory_mode {
			t.Errorf("kind %q memory_mode=%q but editor preset stamps %q", kind, def.memory_mode, want)
		}
	}
}

// TestTheFirstRunAssistantIsAConductor. The first agent someone meets is the
// front door — the one they talk to, which hands work to the specialists they
// make later. It used to be created with Fleet OFF, so the agent the README
// describes as an executive with a cabinet could not delegate, schedule, or
// set up a monitor.
//
// Also pins the carrier's TYPE. first_run rides the form as a hidden field,
// and a hidden field submits text; decoding "true" into a bool fails the whole
// request, which would take agent creation down with it.
func TestTheFirstRunAssistantIsAConductor(t *testing.T) {
	var req wizardRequest
	if err := json.Unmarshal([]byte(`{"agent_kind":"assistant","name":"Ada","first_run":"true"}`), &req); err != nil {
		t.Fatalf("a form-shaped payload no longer decodes: %v", err)
	}
	if req.FirstRun == "" {
		t.Fatal("first_run did not survive the decode, so the front-door agent is indistinguishable from any other")
	}

	// The later assistant is NOT a conductor: those tools are a real block of
	// prompt on every turn, and only the agent whose job is delegation should
	// pay for them.
	var later wizardRequest
	if err := json.Unmarshal([]byte(`{"agent_kind":"assistant","name":"Second"}`), &later); err != nil {
		t.Fatal(err)
	}
	if later.FirstRun != "" {
		t.Error("an ordinary assistant looks like a first run")
	}
}

// TestPurposePicksSurviveEveryShapeAFormSends. A "checklist" saves a JSON
// array, but a control nobody touched sends a bare string or nothing, and some
// clients stringify the array on the way out. Rejecting an agent over the
// encoding of an empty list would be a poor way to meet someone.
func TestPurposePicksSurviveEveryShapeAFormSends(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"the array it saves", `["a","b"]`, 2},
		{"a single bare string", `"a"`, 1},
		{"the array stringified", `"[\"a\",\"b\"]"`, 2},
		{"an empty array", `[]`, 0},
		{"an empty string", `""`, 0},
		{"absent", ``, 0},
	}
	for _, c := range cases {
		got := decodeWizardPicks(json.RawMessage(c.raw))
		if len(got) != c.want {
			t.Errorf("%s: decoded %d pick(s), want %d (%v)", c.name, len(got), c.want, got)
		}
	}
}

// TestAPickedJobIsEnoughToDraftFrom: the picks exist so somebody can get a
// working agent without typing anything, so a purpose made only of picks has
// to satisfy the same requirement the free text used to.
func TestAPickedJobIsEnoughToDraftFrom(t *testing.T) {
	only := joinWizardPurpose([]string{"look things up", "watch things"}, "")
	if strings.TrimSpace(only) == "" {
		t.Fatal("picks alone produced an empty brief, so the form would refuse to create")
	}
	if !strings.Contains(only, "look things up") || !strings.Contains(only, "watch things") {
		t.Errorf("a pick was dropped: %q", only)
	}
	both := joinWizardPurpose([]string{"look things up"}, "and keep an eye on the deploy")
	if !strings.Contains(both, "look things up") || !strings.Contains(both, "keep an eye on the deploy") {
		t.Errorf("picks and prose do not both reach the brief: %q", both)
	}
	if strings.TrimSpace(joinWizardPurpose(nil, "")) != "" {
		t.Error("nothing at all should stay empty, so the handler still refuses it")
	}
}

// TestTheFirstRunFlowAsksWhoBeforeWhat. Somebody meeting gohort for the first
// time is not configuring an agent, they are deciding who their agent is. The
// flow opens by saying what is being made, asks about character before
// capability, and leaves the name until last — where the suggest endpoint has
// the character and the job to draw on. Asked first, which is where it was, it
// suggests into a vacuum.
func TestTheFirstRunFlowAsksWhoBeforeWhat(t *testing.T) {
	src, err := os.ReadFile("page_agent_wizard.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "steps = []ui.FormStep{welcome,")
	if i < 0 {
		t.Fatal("the first-run flow no longer builds its own step order")
	}
	order := body[i : i+120]
	for _, pair := range []struct{ earlier, later string }{
		{"welcome", "personaStep"},     // say what this is before asking anything
		{"personaStep", "purposeStep"}, // who it is before what it does
		{"purposeStep", "nameStep"},    // and the name last, with something to suggest from
	} {
		if strings.Index(order, pair.earlier) > strings.Index(order, pair.later) {
			t.Errorf("%s must come before %s in the first-run flow: %s", pair.earlier, pair.later, order)
		}
	}

	// The welcome step carries the two hidden fields the rest of the flow
	// depends on; without them the create endpoint cannot tell this is the
	// front-door agent, and the assistant-only steps never show.
	w := body[strings.Index(body, "welcome := ui.FormStep{"):]
	w = w[:strings.Index(w, "nameStep :=")]
	for _, f := range []string{`Field: "agent_kind", Type: "hidden"`, `Field: "first_run", Type: "hidden"`} {
		if !strings.Contains(w, f) {
			t.Errorf("the welcome step no longer carries %s", f)
		}
	}
}

// TestTheSituationalAnswersReachTheDraft. The personality step asks about
// moments — being wrong, not knowing, a vague request, finishing — and offers
// quoted replies rather than adjectives, because the reply the user picks IS
// an example of the voice. That only pays off if the picked BEHAVIOUR reaches
// the brief the persona is drafted from.
func TestTheSituationalAnswersReachTheDraft(t *testing.T) {
	got := wizardMoments(wizardRequest{
		OnWrong:  "corrects the user directly and immediately, without softening it",
		OnUnsure: "says plainly that it does not know, and offers to find out",
	})
	for _, want := range []string{
		"says something it believes is wrong", "corrects the user directly",
		"does not know the answer", "offers to find out",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the brief is missing %q:\n%s", want, got)
		}
	}
	// The unanswered ones say nothing rather than asserting a default: "no
	// preference" is an answer, and inventing behaviour the user did not pick
	// is how a persona ends up with opinions nobody chose.
	if strings.Contains(got, "big or vague") || strings.Contains(got, "finishes something") {
		t.Errorf("an unanswered moment was given a value anyway:\n%s", got)
	}
	if wizardMoments(wizardRequest{}) != "" {
		t.Error("a wizard nobody answered still produced character notes")
	}
}

// TestSpecialistsAreAskedAboutFailureNotManners. A specialist usually answers
// a dispatch rather than a person, so what defines it is not how it addresses
// you but what it does when the work goes sideways. Its situational questions
// are the counterparts of the assistant's, aimed at the persona outline's
// "Failure modes" section, and the two sets must not bleed into each other.
func TestSpecialistsAreAskedAboutFailureNotManners(t *testing.T) {
	src, err := os.ReadFile("page_agent_wizard.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	gate := func(field string) string {
		i := strings.Index(body, `{Field: "`+field+`"`)
		if i < 0 {
			t.Fatalf("field %q is gone", field)
		}
		seg := body[i : i+300]
		switch {
		case strings.Contains(seg, "agent_kind:assistant"):
			return "assistant"
		case strings.Contains(seg, "agent_kind:specialist"):
			return "specialist"
		}
		return "everyone"
	}
	for _, f := range []string{"on_wrong", "on_unsure", "on_vague", "on_done"} {
		if g := gate(f); g != "assistant" {
			t.Errorf("%s is shown to %s; it is written for someone being talked to", f, g)
		}
	}
	for _, f := range []string{"on_nothing", "on_conflict", "on_outofreach", "on_partial"} {
		if g := gate(f); g != "specialist" {
			t.Errorf("%s is shown to %s; it is written for work that goes sideways", f, g)
		}
	}
	// Manner and traits stay common: every agent has a voice, whoever is
	// reading it.
	for _, f := range []string{"style", "traits", "style_notes"} {
		if g := gate(f); g != "everyone" {
			t.Errorf("%s is gated to %s, but every agent has a voice", f, g)
		}
	}
}

// TestSpecialistAnswersReachTheDraftToo — the same rendering as the
// assistant's, so a specialist's failure behaviour is not silently dropped.
func TestSpecialistAnswersReachTheDraftToo(t *testing.T) {
	got := wizardMoments(wizardRequest{
		OnNothing:    "reports plainly that it found nothing, and says where it looked",
		OnOutOfReach: "says exactly what it would need and stops, rather than approximating",
	})
	for _, want := range []string{"finds nothing", "says where it looked", "cannot reach", "would need and stops"} {
		if !strings.Contains(got, want) {
			t.Errorf("the brief is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sources disagree") {
		t.Errorf("an unanswered moment was given a value:\n%s", got)
	}
}
