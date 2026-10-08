package orchestrate

import (
	"strings"
	"testing"
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
