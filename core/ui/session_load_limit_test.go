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
