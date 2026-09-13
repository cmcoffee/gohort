package core

import (
	"strings"
	"testing"
)

// An owner-driven move goes through the same transition as the walk's own:
// the hop is logged with the reason, an unknown phase is refused and leaves
// the cursor where it was.
func TestMoveCursorLogsTheHopAndRefusesUnknown(t *testing.T) {
	def := MachineDef{Name: "Triage", Start: "a", Phases: []MachinePhase{{Name: "a"}, {Name: "b"}}}
	cur := &MachineCursor{Phase: "a", State: MachineState{"a": {Text: "done"}}}
	var notes []string
	ph, err := def.MoveCursor(cur, "b", "because", func(k, d string) { notes = append(notes, k) })
	if err != nil || ph.Name != "b" || cur.Phase != "b" {
		t.Fatalf("move: %v %+v", err, cur)
	}
	if len(cur.Log) != 1 || cur.Log[0].From != "a" || cur.Log[0].To != "b" || cur.Log[0].Why != "because" || cur.Log[0].At.IsZero() {
		t.Fatalf("log = %+v", cur.Log)
	}
	if _, err := def.MoveCursor(cur, "zzz", "x", nil); err == nil || !strings.Contains(err.Error(), "no phase zzz") {
		t.Fatalf("unknown phase: %v", err)
	}
	if cur.Phase != "b" {
		t.Fatal("a refused move must not touch the cursor")
	}
	if _, err := def.MoveCursor(nil, "a", "x", nil); err == nil {
		t.Fatal("nil cursor must be refused")
	}
}
