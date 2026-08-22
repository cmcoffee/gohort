// Where a schedule REPORTS, when nobody said.
//
// A schedule authored mid-conversation used to post its fires back into that
// conversation, which is the wrong thread for an agent that keeps a cortex: the
// cortex is the standing home for exactly this kind of background work, and
// interleaving hourly cycles into a live conversation buries the conversation.
// So an agent with a cortex now defaults there, on create AND on edit.
//
// The edit half is why "session" has to be a STORED value rather than an empty
// one. Empty means "nobody chose" and gets defaulted; if a deliberate move back
// to the session stored empty, the next timing edit would quietly send the
// reports to the cortex again.
package orchestrate

import "testing"

func TestScheduleSurfaceDefaultsToCortexOnlyWhenUnchosen(t *testing.T) {
	for _, tc := range []struct {
		name      string
		chosen    string
		hasCortex bool
		want      string
	}{
		{"unchosen on a cortex agent takes the cortex", "", true, "cortex"},
		{"unchosen without a cortex stays the session", "", false, ""},
		{"an explicit session survives the default", "session", true, "session"},
		{"an explicit background survives the default", "background", true, "background"},
		{"an explicit cortex is kept", "cortex", true, "cortex"},
	} {
		if got := scheduleSurfaceDefault(tc.chosen, tc.hasCortex); got != tc.want {
			t.Errorf("%s: scheduleSurfaceDefault(%q, %v) = %q, want %q", tc.name, tc.chosen, tc.hasCortex, got, tc.want)
		}
	}
}

// The picker's "session" has to come back as the word, and it has to mean the
// home session at fire time — the two halves of a durable move back.
func TestExplicitSessionChoiceIsStoredAndStillResolvesHome(t *testing.T) {
	got, ok := normalizeSurface("session")
	if !ok || got != "session" {
		t.Fatalf("normalizeSurface(\"session\") = %q, %v — want \"session\", true (empty would be re-defaulted on the next edit)", got, ok)
	}
	if again := scheduleSurfaceDefault(got, true); again != "session" {
		t.Fatalf("a stored session choice was re-defaulted to %q on a cortex agent", again)
	}
	sess, record := resolveSurface(got, "sess-42", "agent-1")
	if !record || sess != "sess-42" {
		t.Fatalf("resolveSurface(%q, home) = %q, record=%v — want the home session", got, sess, record)
	}
	// And an empty value (a client that sent nothing) still means "unchosen".
	if got, ok := normalizeSurface(""); !ok || got != "" {
		t.Fatalf("normalizeSurface(\"\") = %q, %v — want \"\", true", got, ok)
	}
}

func TestRecurringSurfaceArg(t *testing.T) {
	if got, err := recurringSurfaceArg(map[string]any{}, true); err != nil || got != "" {
		t.Fatalf("omitted `to` = %q, %v — want \"\", nil so the caller defaults it", got, err)
	}
	if _, err := recurringSurfaceArg(map[string]any{"to": "cortex"}, false); err == nil {
		t.Error("to=cortex on an agent with no cortex thread should be refused, not silently downgraded")
	}
	for _, v := range []string{"cortex", "session", "background"} {
		got, err := recurringSurfaceArg(map[string]any{"to": v}, true)
		if err != nil || got != v {
			t.Errorf("to=%q = %q, %v — want it honored", v, got, err)
		}
	}
	if _, err := recurringSurfaceArg(map[string]any{"to": "elsewhere"}, true); err == nil {
		t.Error("an unknown destination should error rather than fall through to a default")
	}
}
