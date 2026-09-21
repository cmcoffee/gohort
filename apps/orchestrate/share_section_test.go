package orchestrate

// The share controls, as the editor's rail draws them.
//
// Reported live as "I'm not seeing the control I asked for in Agent -> Edit ->
// Share". They were rendering, as three sibling entries: the recipient picker
// under "Share with users", and the rest under two titles that read like
// unrelated settings. Sharing is ONE operation asked in three steps, and a flat
// rail gave somebody who went looking under Share no reason to believe the
// other two belonged to it.

import (
	"strings"
	"testing"
)

// shareSectionSource is the block of page_agent.go that builds the three
// sections, which is the unit these assertions are about.
func shareSectionSource(t *testing.T) string {
	t.Helper()
	src := packageSource(t)
	i := strings.Index(src, `Title:    "Share",`)
	if i < 0 {
		t.Fatal("no section titled Share on the agent editor")
	}
	j := strings.Index(src[i:], `Title:    "What it reaches"`)
	if j < 0 {
		t.Fatal("the reach inventory is not built beside the share picker")
	}
	k := strings.Index(src[i+j:], "})")
	return src[i : i+j+k]
}

// One rail entry, with its parts nested under it.
func TestSharingIsOneRailEntryWithItsPartsUnderIt(t *testing.T) {
	block := shareSectionSource(t)
	for _, part := range []string{`Title:    "What they get"`, `Title:    "What it reaches"`} {
		i := strings.Index(block, part)
		if i < 0 {
			t.Fatalf("missing %s", part)
		}
		// Indent is the rail's own primitive for "sub-part of the thing above".
		if !strings.Contains(block[i:i+200], "Indent:   1,") {
			t.Errorf("%s is drawn as a sibling of Share, not a part of it", part)
		}
	}
}

// A section FormPanel on this page PATCHes. A POST sends that panel's fields as
// the WHOLE record and wipes everything else on the agent, which is the entire
// reason splitAgentFormSections exists; a new section added later must not
// quietly reintroduce it.
func TestEverySectionPanelOnTheAgentEditorPatches(t *testing.T) {
	block := shareSectionSource(t)
	if !strings.Contains(block, `Method:  "PATCH"`) {
		t.Error("the share settings panel does not PATCH, so saving it would wipe the rest of the agent")
	}
	if strings.Contains(block, `PostURL: source,`) {
		t.Error("a section panel posts the whole record back; PATCH with the id in the query instead")
	}
}

// The four switches are the ones the split actually reads. A field renamed on
// one side and not the other is a control that saves and does nothing.
func TestTheSwitchesOnScreenAreTheOnesTheRuntimeReads(t *testing.T) {
	block := shareSectionSource(t)
	src := packageSource(t)
	for field, reader := range map[string]string{
		"share_hold_cortex":     "ShareHoldCortex",
		"share_hold_reference":  "ShareHoldReference",
		"share_memory_explicit": "ShareMemoryExplicit",
		"share_no_uploads":      "ShareNoUploads",
	} {
		if !strings.Contains(block, `Field: "`+field+`"`) {
			t.Errorf("%s is not on the panel", field)
		}
		if !strings.Contains(src, "t.agent."+reader) && !strings.Contains(src, "agent."+reader) {
			t.Errorf("%s is saved by the panel and read by nothing", field)
		}
		if !patchAgentFields[field] {
			t.Errorf("%s is on the panel but not saveable: the PATCH allowlist drops it", field)
		}
	}
}
