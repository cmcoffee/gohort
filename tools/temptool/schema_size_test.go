package temptool

import (
	"encoding/json"
	"testing"
)

// tool_def's parameter schema ships in the prompt of every authoring turn,
// whatever the agent is building. It is the single largest tool in that
// catalog, so protocol-specific tutorials added to it (the XML/CalDAV guidance
// that grew it by ~3KB while fixing one CalDAV build) are paid for by every
// unrelated build — and land inside the schema of the exact tool the model has
// to emit against.
//
// The cap is a ratchet, not a target. When it trips, the fix is almost never
// to raise it: move the detail into action="help", which already carries the
// full XML/CalDAV walkthrough, and leave the parameter description as a name,
// a one-line purpose, and a pointer. Progressive disclosure — the same reason
// has-args custom tools are lazy rather than inline.
const toolDefSchemaByteCap = 18000

func TestToolDefSchemaStaysSmall(t *testing.T) {
	b, err := json.Marshal(BuildToolDef().Params())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tool_def params schema: %d bytes (cap %d)", len(b), toolDefSchemaByteCap)
	if len(b) > toolDefSchemaByteCap {
		t.Errorf("tool_def's parameter schema is %d bytes, over the %d cap.\n"+
			"Every authoring turn pays this, on every build. Before raising the cap, check whether what you added is protocol- or vendor-specific — if so it belongs in action=\"help\", with a one-line pointer left in the parameter description.",
			len(b), toolDefSchemaByteCap)
	}
}
