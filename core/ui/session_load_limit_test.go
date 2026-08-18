package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The client's "reset" value for the per-open message limit and the server's
// "serve the whole thread" value are both spelled with a number, and for a
// while they were the SAME number.
//
// openSession reset sessionLoadLimit to 0 on every ordinary open, and 0 is not
// "unset" on the wire — the send guard is `>= 0`, so it went out as limit=0,
// which resolveTailLimit defines as no trim at all. (It has to mean that, or
// "Show earlier" could never reach the top of a thread longer than its steps
// ever cover.) The result was a tail limit that had no effect from the browser:
// every open shipped and rendered the entire transcript, message_offset always
// came back 0, and with nothing trimmed the "Show earlier" pill never rendered.
// A slow thread and a missing button, both from one character.
//
// Source-level, deliberately: the bug IS the constant, and a behavioral harness
// for this panel would have to stub most of a DOM to reach it.
func TestSessionLoadLimitResetsToUnsetNotZero(t *testing.T) {
	path := filepath.Join("assets", "runtime", "30_agent_loop_panel.js")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	src := string(b)

	if !strings.Contains(src, "sessionLoadLimit = -1") {
		t.Error("sessionLoadLimit must reset to -1 (send no limit, let the server default apply)")
	}
	// Any assignment of literal 0 is the regression, wherever it appears.
	if strings.Contains(src, "sessionLoadLimit = 0") {
		t.Error("sessionLoadLimit = 0 asks the server for the WHOLE thread — use -1 to mean unset")
	}
	// The send guard has to stay >= 0 for an explicit 0 to remain reachable by
	// a caller that genuinely wants everything; -1 is what keeps it off the URL.
	if !strings.Contains(src, "if (sessionLoadLimit >= 0)") {
		t.Error("the limit param guard changed; re-check that -1 still means \"do not send it\"")
	}
}

// One press of "Show earlier" must ask for a bounded step, not double what is
// already loaded — doubling made the button slower every time it was used.
func TestShowEarlierAsksForOneChunk(t *testing.T) {
	path := filepath.Join("assets", "runtime", "30_agent_loop_panel.js")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	src := string(b)
	if !strings.Contains(src, "sessionLoadLimit = loadedCount + offset + chunk") {
		t.Error("\"Show earlier\" should add ONE chunk to what is loaded + skipped")
	}
	if strings.Contains(src, "(loadedCount + offset) * 2") {
		t.Error("the doubling step is back; each press re-renders everything it already had")
	}
}

// After "Show earlier" the reader must stay on the message they were reading,
// with the older chunk arriving above them. Landing at the top sends you past a
// chunk you have not read yet — on the second press, every time.
//
// The anchor is a MESSAGE, not a scroll offset or a height delta: heights are
// still settling when the restore runs, so a delta measured then is wrong a
// frame later and re-applying it double-corrects, while an element's position
// can be re-read as the layout changes and converges.
func TestShowEarlierKeepsThePosition(t *testing.T) {
	path := filepath.Join("assets", "runtime", "30_agent_loop_panel.js")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	src := string(b)
	for _, want := range []string{"captureEarlierAnchor()", "restoreEarlierAnchor()"} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %s — the press would jump to the top again", want)
		}
	}
	// getBoundingClientRect, never offsetTop: offsetTop is measured against the
	// nearest POSITIONED ancestor and silently changes meaning when one appears.
	if strings.Contains(src, "earlierAnchor") && !strings.Contains(src, "getBoundingClientRect().top - paneTop") {
		t.Error("anchor offset must come from getBoundingClientRect, not offsetTop")
	}
}

// A "Show earlier" press must not blank the pane while it fetches. The wipe
// used to run before the request was even sent, so the reader stared at an
// empty panel for a whole round trip — to be handed back content they already
// had on screen, plus a chunk above it.
//
// The eager clear stays for a DIFFERENT session, where the blank is the
// feedback that the click registered.
func TestShowEarlierDoesNotBlankThePane(t *testing.T) {
	path := filepath.Join("assets", "runtime", "30_agent_loop_panel.js")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	src := string(b)
	if !strings.Contains(src, "if (!keepLimit) clearConvoPanes();") {
		t.Error("a same-thread re-render must NOT clear before the fetch")
	}
	if !strings.Contains(src, "if (keepLimit) clearConvoPanes();") {
		t.Error("a same-thread re-render must clear late, just before the rebuild")
	}
	// The maps and the DOM have to be emptied together: an msgEls entry pointing
	// at a detached node is a scrub button that PATCHes the wrong index.
	clear := src[strings.Index(src, "function clearConvoPanes()"):]
	clear = clear[:strings.Index(clear, "\n    }")]
	for _, want := range []string{"msgEls = {}", "convoLog.innerHTML = ''", "activityLog.innerHTML = ''"} {
		if !strings.Contains(clear, want) {
			t.Errorf("clearConvoPanes must reset %s alongside the rest", want)
		}
	}
}

// The profile bar is a RADIO set with an explicit None, not a chip bar.
//
// Exactly one profile is in force at a time, so the selected pill is filled
// rather than outlined — and None has to be a control rather than the absence
// of a selection, or a profile can be turned on and never off.
func TestProfileBarIsARadioSetWithNone(t *testing.T) {
	path := filepath.Join("assets", "runtime", "50_codewriter_panel.js")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	src := string(b)
	for _, want := range []string{"ui-cw-prof-bar", "ui-cw-prof-pill", "setProfile('')"} {
		if !strings.Contains(src, want) {
			t.Errorf("profile bar missing %q", want)
		}
	}
	// It must live in the panel body, not inside a pane's header — the settings
	// it restores belong to the editor as much as the chat.
	if !strings.Contains(src, "main.appendChild(profBar);") {
		t.Error("the profile bar belongs in the panel body, above both panes")
	}
	if strings.Contains(src, "chatHdr.appendChild(profSelect)") {
		t.Error("the profile control is back in the chat header, where it reads as a chat setting")
	}
	// A remembered pick that no longer exists must not keep being POSTed.
	if !strings.Contains(src, "profList.some(function(p){ return p.id === pickedProfile; })") {
		t.Error("a deleted profile must fall back to None instead of POSTing a dead id")
	}
}

// Every profile pill needs a matching style, or the roster renders as
// unstyled buttons in a bar that looks broken.
func TestProfileBarIsStyled(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("assets", "runtime.css"))
	if err != nil {
		t.Fatalf("reading runtime.css: %v", err)
	}
	css := string(b)
	for _, want := range []string{".ui-cw-prof-bar", ".ui-cw-prof-lbl", ".ui-cw-prof-pill", ".ui-cw-prof-pill.active", ".ui-cw-prof-actions"} {
		if !strings.Contains(css, want) {
			t.Errorf("missing style for %s", want)
		}
	}
	// margin-left:auto belongs to the GROUP, once. On each button, flex hands
	// every auto margin a share of the free space and scatters them across the
	// bar — Update drifts to the middle while Save sits at the edge.
	at := strings.Index(css, ".ui-cw-prof-save {")
	if at < 0 {
		t.Fatal("the save button lost its style")
	}
	block := css[at:]
	if end := strings.Index(block, "}"); end > 0 {
		block = block[:end]
	}
	if strings.Contains(block, "margin-left: auto") {
		t.Error("the auto margin must be on .ui-cw-prof-actions, not on each button")
	}
}

// The panel comes back how it was left — language, sources, collections —
// whether or not the arrangement was ever named. That reset-to-defaults on
// every reload is the thing all of this exists to fix.
func TestPanelRestoresItsLastSettings(t *testing.T) {
	path := filepath.Join("assets", "runtime", "50_codewriter_panel.js")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	src := string(b)
	if !strings.Contains(src, "function rememberSettings()") || !strings.Contains(src, "function restoreSettings()") {
		t.Fatal("the panel must remember and restore its own control state")
	}
	// Restoring remembered state and applying a named profile are the same
	// operation on the same controls; two code paths for that is how they drift.
	if !strings.Contains(src, "if (st) applyProfile(st, initial);") {
		t.Error("restore should reuse applyProfile rather than its own setter")
	}
	// The option lists load independently, so a restore that ran before they
	// arrived matched nothing — and pickedCollections() walks that list, so it
	// would have SENT nothing while looking applied.
	if strings.Count(src, "restoreSettings();") < 2 {
		t.Error("both the collection and reference lists must trigger a restore")
	}
	// Language is restored once, on load: re-applying later fights the snippet
	// the user has since opened, whose language is the one that should show.
	if !strings.Contains(src, "var initial = !settingsRestored;") {
		t.Error("language must be restored only on the first restore after load")
	}
	if !strings.Contains(src, "if (initial && p.lang)") {
		t.Error("applyProfile must gate language on the initial restore")
	}
	// Every control that can change has to write the state back, or "how you
	// left it" silently means "how you left it before the last thing you did".
	if strings.Count(src, "rememberSettings()") < 5 {
		t.Error("language, collections and references must all record changes")
	}
}

// A multi-select saves the LIST. input.value on a multiple select is just the
// first selection — the shape that silently drops everything after it.
func TestMultiSelectSavesEveryChoice(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("assets", "runtime", "10_basics.js"))
	if err != nil {
		t.Fatalf("reading 10_basics.js: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "if (multi) input.multiple = true;") {
		t.Error("a select field with Multiple must render as a multi-select")
	}
	if !strings.Contains(src, "if (input.options[mi].selected) vals.push(input.options[mi].value);") {
		t.Error("a multi-select must save every selected option, not input.value")
	}
}

// A profile is saved FROM the bar, by snapshotting the live controls. Sending
// someone to a form to re-describe what the panel is already set to asks them
// to enter the same information twice, from memory.
func TestProfileIsSavedFromTheLiveControls(t *testing.T) {
	path := filepath.Join("assets", "runtime", "50_codewriter_panel.js")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	src := string(b)
	if !strings.Contains(src, "function saveCurrentProfile()") {
		t.Fatal("the bar must be able to save the current settings")
	}
	// Snapshot the CONTROLS, not a stored record — whatever the panel is set
	// to is the profile.
	for _, want := range []string{"lang: langSelect.value", "collections: pickedCollections()", "references: selectedRef"} {
		if !strings.Contains(src, want) {
			t.Errorf("the snapshot should read %q from the live panel", want)
		}
	}
	// Deleting the profile you are working under must not tear down the
	// settings you are working with — so the delete path drops the selection
	// and repaints, and never re-applies.
	at := strings.Index(src, "function doDeleteProfile(p)")
	if at < 0 {
		t.Fatal("the bar must be able to delete the selected profile")
	}
	del := src[at:]
	if end := strings.Index(del, "\n    function "); end > 0 {
		del = del[:end]
	}
	if strings.Contains(del, "applyProfile(") {
		t.Error("deleting a profile must not re-apply settings over the live panel")
	}
	if !strings.Contains(del, "renderProfBar()") {
		t.Error("delete should repaint the bar")
	}
}
