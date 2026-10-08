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
