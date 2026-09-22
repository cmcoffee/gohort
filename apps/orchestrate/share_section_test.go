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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
	if !strings.Contains(block, `Field: "everyone", Type: "select"`) {
		t.Error("the audience is not one choice")
	}
	// NOT the legacy exposed flag: it is read-only and migrates to two
	// decisions at once, everyone may use it AND a card on the dashboard,
	// which were deliberately split apart.
	if strings.Contains(block, `Field: "exposed"`) {
		t.Error("the audience writes the retired flag, which welds the dashboard shortcut back onto publishing")
	}
	// Through its own door, because publishing is REQUESTED and not applied.
	if !strings.Contains(block, "permissions/audience") {
		t.Error("the audience PATCHes the record, bypassing the approval gate")
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

// A FormPanel loads the record and sends back everything it loaded, not only
// the fields it draws. So a panel owning four toggles PATCHes the whole agent,
// protected keys included, at their current values - and refusing those made
// every panel on the Security page fail with a list of fields nobody touched.
func TestAnEchoedProtectedFieldIsNotRefused(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	rec := AgentRecord{ID: "a40", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p",
		Everyone: true, Tools: []TempTool{{Name: "kept", CommandTemplate: "echo hi"}}}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	// A panel echoing protected keys back UNCHANGED, alongside one it owns.
	body := `{"everyone":true,"owner":"alice","share_no_uploads":true}`
	r := httptest.NewRequest(http.MethodPatch, "/api/agents/a40", strings.NewReader(body))
	w := httptest.NewRecorder()
	app.handleAgentOne(w, asUser(r, "alice"))
	if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
		t.Fatalf("an echoed protected field was refused: %d %s", w.Code, w.Body.String())
	}
	got, ok := loadAgent(udb, "a40")
	if !ok {
		t.Fatal("the agent is gone")
	}
	if !got.ShareNoUploads {
		t.Error("the field the panel owned did not save")
	}
	// An actual CHANGE to a protected field is still refused, which is the
	// case the guard was written for.
	r2 := httptest.NewRequest(http.MethodPatch, "/api/agents/a40",
		strings.NewReader(`{"everyone":false}`))
	w2 := httptest.NewRecorder()
	app.handleAgentOne(w2, asUser(r2, "alice"))
	if w2.Code != http.StatusBadRequest {
		t.Errorf("changing a protected field through PATCH was allowed: %d %s", w2.Code, w2.Body.String())
	}
}

// Publishing is requested, not applied. An owner who could not flip the toggle
// on the privileges card must not be able to publish by posting the audience.
func TestTheAudienceGoesThroughTheApprovalGate(t *testing.T) {
	src := mustRead(t, "console_permissions.go")
	i := strings.Index(src, "func (T *OrchestrateApp) handleConsolePermissionAudience")
	if i < 0 {
		t.Fatal("the audience door is gone")
	}
	// Bounded: this function is the last thing in the file, so a fixed window
	// runs off the end.
	end := i + 2400
	if end > len(src) {
		end = len(src)
	}
	body := src[i:end]
	if !strings.Contains(body, "agentPublishNeedsApproval") {
		t.Error("the audience sets publication without asking anybody")
	}
	// Turning it OFF stays direct: nobody needs permission to stop sharing.
	if !strings.Contains(body, "rec.Everyone = on") {
		t.Error("the audience never writes the live flag")
	}
	// Says REQUESTED rather than answering 204, or the page shows an agent as
	// published while an administrator has not looked at it yet.
	if !strings.Contains(body, `"requested"`) {
		t.Error("a request is reported as done")
	}
}
