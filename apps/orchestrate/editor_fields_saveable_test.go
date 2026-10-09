package orchestrate

// A control the editor renders but PATCH refuses is worse than a missing one:
// it looks set, saves nothing, and says nothing. enable_notes shipped the other
// way round for a while — settable only by Builder's authoring tools, with the
// Memory pane telling owners to "enable them in the agent editor", which had no
// such control.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var editorFieldRE = regexp.MustCompile(`ui\.FormField\{Field: "([a-z0-9_]+)"`)

// Every field the agent editor renders must be one PATCH will accept.
func TestEveryEditorFieldIsSaveable(t *testing.T) {
	src, err := os.ReadFile("page_agent.go")
	if err != nil {
		t.Fatal(err)
	}
	// Fields the editor shows that PATCH refuses BY DESIGN.
	//
	// Two reasons, kept apart because they are different promises. A protected
	// field has its own endpoint so a partial save cannot reach it. A
	// full-record field rides the POST path, which decodes the whole
	// AgentRecord and never consults this allowlist — "machine" is one, and
	// treating it as missing was this test's own first false positive.
	savedAnotherWay := map[string]bool{
		"guardrails": true, "guardrails_disabled": true, "locked": true,
		"id": true, "owner": true, "created": true,
		"machine": true,
	}
	var missing []string
	for _, m := range editorFieldRE.FindAllStringSubmatch(string(src), -1) {
		name := m[1]
		if savedAnotherWay[name] || patchAgentFields[name] {
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) > 0 {
		t.Errorf("the editor renders %s, which PATCH will refuse — the control would save nothing. "+
			"Add to patchAgentFields, or to this test's savedAnotherWay list if it rides the full-record POST.",
			strings.Join(missing, ", "))
	}
}

// And the specific one this came from.
func TestWorkingNotesIsSettableFromTheEditor(t *testing.T) {
	if !patchAgentFields["enable_notes"] {
		t.Error("enable_notes must be saveable: the Memory pane tells owners to enable it in the editor")
	}
	src, err := os.ReadFile("page_agent.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `Field: "enable_notes"`) {
		t.Error("the editor must offer the toggle the Memory pane points at")
	}
}
