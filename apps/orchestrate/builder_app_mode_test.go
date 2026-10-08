package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An app session gets the web developer's instructions; a tool session, a
// session with no intake, and any other agent do not.
func TestBuilderAppModeFollowsTheIntake(t *testing.T) {
	turn := func(agent string, msgs ...ChatMessage) *chatTurn {
		return &chatTurn{agent: AgentRecord{ID: agent}, session: &ChatSession{Messages: msgs}}
	}
	app := ChatMessage{Role: "user", IntakeValues: map[string]string{"action": "Create", "kind": "App", "goal": "weather"}}
	tool := ChatMessage{Role: "user", IntakeValues: map[string]string{"action": "Create", "kind": "Tool"}}
	later := ChatMessage{Role: "user", Content: "make the chart bigger"}

	if b := turn("seed-builder", app, later).renderBuilderAppModeBlock(); !strings.Contains(b, "Building an app") || !strings.Contains(b, "window.app") {
		t.Fatalf("an app session, later turn: %q", b)
	}
	if turn("seed-builder", tool).builderAppMode() {
		t.Error("a tool session is in app mode")
	}
	if turn("seed-builder", app, tool).builderAppMode() {
		t.Error("the latest intake (Tool) should decide")
	}
	if turn("seed-builder", later).builderAppMode() {
		t.Error("a session with no intake is in app mode")
	}
	if turn("seed-chat", app).builderAppMode() {
		t.Error("an agent other than Builder is in app mode")
	}
	if strings.Contains(builderAppModeShipped, "—") {
		t.Error("prompt text carries an em dash")
	}
}

// In an app session, an app of only typed sections is not done unless its
// notes say the person asked for a plain list; a chat, run or workbench app
// is another shape and passes.
func TestAppModeAsksForThePage(t *testing.T) {
	appTurn := &chatTurn{agent: AgentRecord{ID: "seed-builder"}, session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", IntakeValues: map[string]string{"kind": "App"}}}}}
	toolTurn := &chatTurn{agent: AgentRecord{ID: "seed-builder"}}
	typed := AppSpec{Sections: []byte(`[{"kind":"form"},{"kind":"table"},{"kind":"display"}]`)}
	if p := appTurn.appModeTypedOnlyProblem(typed); !strings.Contains(p, "ONE html section") {
		t.Fatalf("typed-only in app mode: %q", p)
	}
	if toolTurn.appModeTypedOnlyProblem(typed) != "" {
		t.Error("outside app mode a typed app is fine")
	}
	typed.Notes = "Plain list: the user wants a bare checklist."
	if appTurn.appModeTypedOnlyProblem(typed) != "" {
		t.Error("a plain list on purpose was objected to")
	}
	for _, ok := range []string{`[{"kind":"html","html":"<!DOCTYPE html>"}]`, `[{"kind":"form"},{"kind":"chat"}]`, `[{"kind":"pipeline"}]`} {
		if appTurn.appModeTypedOnlyProblem(AppSpec{Sections: []byte(ok)}) != "" {
			t.Errorf("%s objected to", ok)
		}
	}
}

// What a script printed, named as what it is: a JSON string holding JSON was
// called an object on one line and an array on the next.
func TestTheKindOfOutputIsNamed(t *testing.T) {
	if k := appJSONKind(`{"location": "Santa Cruz"}`); !strings.Contains(k, "encoded twice") {
		t.Errorf("double-encoded: %q", k)
	}
	if appJSONKind([]any{}) != "an array" || appJSONKind(map[string]any{}) != "an object" || appJSONKind("hi") != "a bare string" || appJSONKind(3.0) != "a bare value" {
		t.Error("kinds")
	}
	probs := appDisplayShape("display section \"Now\"", "w", map[string]any{"pairs": []any{map[string]any{"field": "x"}}}, `{"a":1}`)
	if len(probs) != 1 || strings.Contains(probs[0], "an array") || !strings.Contains(probs[0], "encoded twice") {
		t.Fatalf("display message: %q", probs)
	}
}

// Sections written without a kind are read with the kind the builder infers,
// so a form-and-table app is still seen as one.
func TestTypedOnlyReadsInferredKinds(t *testing.T) {
	spec := AppSpec{Sections: []byte(`[{"fields":[{"name":"city"}]},{"columns":[{"field":"city"}]},{"pairs":[{"field":"temp"}]}]`)}
	if !appTypedOnly(spec) {
		t.Fatal("kind-less form/table/display not seen as typed-only")
	}
}

// The verify that passed {"temperature": "None°F", "conditions": null, ...}:
// most of the values are empty, which is a script reading keys its input
// does not have.
func TestEmptyValuesAreCounted(t *testing.T) {
	out := map[string]any{"city": "San Francisco", "state": "CA", "temperature": "None°F", "conditions": nil, "humidity": "None%", "wind_speed": "None mph", "forecast": ""}
	empty, total := appEmptyValues(out)
	if total != 7 || empty != 5 {
		t.Fatalf("empty %d of %d", empty, total)
	}
	good := map[string]any{"city": "Reno", "temperature": "71°F", "conditions": "Clear", "note": "Nonetheless sunny"}
	if empty, _ := appEmptyValues(good); empty != 0 {
		t.Fatalf("a real forecast counted %d empty", empty)
	}
}

// The kind is read from the intake's packed text when no intake values were
// stored, by the label Builder's own form gives the field.
func TestAppModeReadsTheIntakeText(t *testing.T) {
	builder := AgentRecord{ID: "seed-builder", IntakeForm: IntakeFormSpec{{Name: "kind", Label: "What kind of thing?"}}}
	turn := &chatTurn{agent: builder, session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "**What do you want to do?:** Create\n\n**What kind of thing?:** App\n\n**What should it do?:** weather"},
	}}}
	if kind, from := turn.intakeKind(); kind != "App" || from != "text" {
		t.Fatalf("kind %q from %q", kind, from)
	}
	if !turn.builderAppMode() {
		t.Fatal("the packed text did not turn on app mode")
	}
	turn.session.Messages = append(turn.session.Messages, ChatMessage{Role: "user", Content: "**What kind of thing?:** Tool"})
	if turn.builderAppMode() {
		t.Fatal("the latest intake (Tool) should decide")
	}
}
