package orchestrate

// The share controls, now that they are the Share tab of an agent's Security
// page rather than a rail entry in its editor.
//
// They were three sibling rail entries once, and somebody who went looking
// under Share found the recipient picker and no reason to believe the rest
// existed. Sharing is ONE operation asked in three steps: who may run it, who
// exactly, and what travels with it. The steps still have to arrive together,
// which is what these assertions are about; they are just a tab now, and a tab
// is what keeps them together.

import (
	"strings"
	"testing"
)

// shareSectionSource is the run of page_agent_access.go that builds the Share
// tab, which is the unit these assertions are about.
func shareSectionSource(t *testing.T) string {
	t.Helper()
	src := mustRead(t, "page_agent_access.go")
	i := strings.Index(src, `Title:    "Who may run this agent"`)
	if i < 0 {
		t.Fatal("no audience control on the Security page")
	}
	j := strings.Index(src[i:], `Group:    "Access"`)
	if j < 0 {
		t.Fatal("the Share group does not end; the tabs have moved")
	}
	return src[i : i+j]
}

// All three steps under one tab. Split across tabs they would be three
// unrelated settings again, which is the failure this arrangement replaced.
func TestSharingArrivesInOnePlace(t *testing.T) {
	block := shareSectionSource(t)
	for _, part := range []string{
		`Title:    "Who may run this agent"`, // the audience
		`Title: "The people you name"`,       // exactly who
		`Title:    "What a recipient sees"`,  // what travels
	} {
		if !strings.Contains(block, part) {
			t.Errorf("missing %s from the Share tab", part)
		}
	}
	// And it is GONE from the editor, not duplicated there.
	if strings.Contains(mustRead(t, "page_agent.go"), `Title:    "What they get"`) {
		t.Error("the editor still carries the share controls, so there are two places to set one thing")
	}
}

// One choice, not two switches. "Everyone" and "these people" read as
// independent grants that could both be on, when publishing makes the list a
// narrowing rather than an addition.
func TestTheAudienceIsASingleChoice(t *testing.T) {
	block := shareSectionSource(t)
	if !strings.Contains(block, `Field: "exposed", Type: "select"`) {
		t.Error("the audience is not one choice")
	}
	for _, label := range []string{"Everyone (publish globally)", "Only the people I name"} {
		if !strings.Contains(block, label) {
			t.Errorf("the audience choice does not offer %q", label)
		}
	}
}

// A section FormPanel on this page PATCHes. A POST sends that panel's fields as
// the whole record, so everything it does not show is blanked.
func TestEverySharePanelPatches(t *testing.T) {
	block := shareSectionSource(t)
	if !strings.Contains(block, `Method:      "PATCH"`) {
		t.Error("a share panel does not PATCH, so saving it would wipe the rest of the agent")
	}
	if strings.Contains(block, "PostURL:     source,") {
		t.Error("a section panel posts the whole record back; PATCH with the id in the path instead")
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

// A reach flag is an administrator's to grant, so the patch door reads it
// tolerantly and fails CLOSED. A select sends "true" where a toggle sends
// true, and a strict comparison read that as off: a control meaning "publish
// this" would have quietly unpublished instead.
func TestAReachFlagIsReadTolerantlyAndFailsClosed(t *testing.T) {
	for _, v := range []any{true, "true", "on", "YES", "1", float64(1)} {
		if !truthyPatchValue(v) {
			t.Errorf("%#v did not read as on", v)
		}
	}
	for _, v := range []any{false, "false", "off", "", "perhaps", float64(0), nil, []string{"true"}} {
		if truthyPatchValue(v) {
			t.Errorf("%#v read as on; an unparseable value must not grant reach", v)
		}
	}
}
