package core

import "testing"

// TestSerialFireWiring guards the serial-fire plumbing the agent loop relies
// on: a GroupedTool marked serial-fire must report SerialFirePerBatch (and NOT
// SingleFirePerBatch), and ChatToolToAgentToolDef must propagate that onto the
// AgentToolDef. rebuildToolMaps keys off exactly these two flags — a serial
// tool that leaked into the single-fire set would have its excess batched
// calls dropped instead of run in order, reintroducing the tool_def
// delete-then-create footgun this replaced.
func TestSerialFireWiring(t *testing.T) {
	serial := NewGroupedTool("t_serial", "serial authoring tool")
	serial.AddAction("noop", &GroupedToolAction{
		Description: "noop",
		Handler:     func(args map[string]any, sess *ToolSession) (string, error) { return "ok", nil },
	})
	serial.SetSerialFirePerBatch(true)

	if !serial.SerialFirePerBatch() {
		t.Fatal("SerialFirePerBatch() should be true after SetSerialFirePerBatch(true)")
	}
	if serial.SingleFirePerBatch() {
		t.Fatal("a serial-fire tool must NOT also report single-fire")
	}

	def := ChatToolToAgentToolDefWithSession(serial, nil)
	if !def.SerialFirePerBatch {
		t.Error("AgentToolDef.SerialFirePerBatch not propagated from the tool")
	}
	if def.SingleFirePerBatch {
		t.Error("AgentToolDef.SingleFirePerBatch should be false for a serial-fire tool")
	}

	// A plain single-fire tool must still register as single-fire only.
	single := NewGroupedTool("t_single", "single-fire tool")
	single.AddAction("noop", &GroupedToolAction{
		Description: "noop",
		Handler:     func(args map[string]any, sess *ToolSession) (string, error) { return "ok", nil },
	})
	single.SetSingleFirePerBatch(true)
	sdef := ChatToolToAgentToolDefWithSession(single, nil)
	if !sdef.SingleFirePerBatch || sdef.SerialFirePerBatch {
		t.Errorf("single-fire tool mis-wired: single=%v serial=%v", sdef.SingleFirePerBatch, sdef.SerialFirePerBatch)
	}
}
