package orchestrate

// A guard that STOPPED something says so in the conversation, while the turn
// it interrupted is still on screen. The trail behind the ⚠ button is where
// you look once you already suspect a guard fired; these are how you find out
// that one did.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// noticeFrames pulls the {kind:"notice"} payloads out of an SSE buffer.
func noticeFrames(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "data: ")
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		if m["kind"] == "notice" {
			out = append(out, m)
		}
	}
	return out
}

// The classifier is read off the kind slug, which is the convention every
// guard already follows: name the kind for what you DID.
func TestDiagLevelReadsTheVerbInTheKind(t *testing.T) {
	blocked := []string{
		"guardrail-blocked", "guardrail-input-blocked", "guardrail-halted",
		"guardrail-output-withheld", "tool-denied", "tool-scan-blocked",
		"tool-scan-action-blocked", "machine-tier-denied", "machine_exit_refused",
		"form-step-discarded", "provider-refusal", "lead-in-withheld",
	}
	for _, k := range blocked {
		if diagLevel(k) != diagLevelBlocked {
			t.Errorf("%q names something a guard stopped; it must reach the conversation", k)
		}
	}
	// Conditions, not actions. Real entries, and none of them is a thing the
	// reader has to be interrupted for.
	for _, k := range []string{
		"guardrail-no-verdict", "guardrail-error", "guardrail-appeal-honored",
		"skill_playbook_fired", "machine_not_on_dispatch", "consulted", "tool-grant",
	} {
		if diagLevel(k) != diagLevelNote {
			t.Errorf("%q records a condition, not a block — it belongs in the trail only", k)
		}
	}
}

// The live turn's own pane gets the notice, and the one frame carries the
// same name the trail will serve for the same entry.
func TestBlockingDiagReachesTheOpenConversation(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	buf := &syncBuf{}
	turn := &chatTurn{
		agent:   AgentRecord{ID: "lead", Name: "Lead"},
		udb:     udb,
		ctx:     context.Background(),
		sse:     &sseWriter{live: buf},
		session: &ChatSession{ID: "conv-1", AgentID: "lead"},
	}
	turn.turnDiag("guardrail-blocked", `Guardrail "no external posts" blocked a pre_action check: it posts publicly.`)

	frames := noticeFrames(t, buf.String())
	if len(frames) != 1 {
		t.Fatalf("expected one notice on the pane, got %d:\n%s", len(frames), buf.String())
	}
	f := frames[0]
	if f["level"] != diagLevelBlocked || f["type"] != "guardrail-blocked" {
		t.Errorf("notice frame = %+v", f)
	}
	if !strings.Contains(f["text"].(string), "no external posts") {
		t.Errorf("the notice must carry the guard's own sentence: %+v", f)
	}

	trail := decorateSessionDiags(parentTrailOf(udb, "lead", "conv-1"))
	if len(trail) != 1 {
		t.Fatalf("trail = %+v", trail)
	}
	// One identity across both routes. Without it a page that loads mid-run
	// receives this block twice (run buffer replay + trail replay) and shows
	// it twice.
	if f["id"] == "" || f["id"] != trail[0].ID {
		t.Fatalf("live id %q must match the trail's %q", f["id"], trail[0].ID)
	}
	if trail[0].Level != diagLevelBlocked {
		t.Errorf("the trail must classify the entry the same way: %+v", trail[0])
	}
}

// A note stays in the trail. The reader is not interrupted for a condition
// nobody has to act on — a card per condition teaches them to stop reading
// the cards.
func TestNonBlockingDiagStaysInTheTrail(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	buf := &syncBuf{}
	turn := &chatTurn{
		agent:   AgentRecord{ID: "lead", Name: "Lead"},
		udb:     udb,
		ctx:     context.Background(),
		sse:     &sseWriter{live: buf},
		session: &ChatSession{ID: "conv-1", AgentID: "lead"},
	}
	turn.turnDiag("skill_playbook_fired", "playbook 'triage' established the branch")

	if got := noticeFrames(t, buf.String()); len(got) != 0 {
		t.Fatalf("a note must not take a card: %+v", got)
	}
	if got := parentTrailOf(udb, "lead", "conv-1"); len(got) != 1 {
		t.Fatalf("...but it is still recorded: %+v", got)
	}
}

// A delegated run builds its own chatTurn with a nil sse, so the guard that
// stopped a sub-agent had nowhere live to say so and the pane showed a tool
// chip sitting there. The sub-turn borrows the conversation it descends from
// and names itself, exactly as the trail mirror does.
func TestDispatchedBlockReachesTheWatchedConversation(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	buf := &syncBuf{}
	parentCtx := withDiagNotices(withDiagParent(context.Background(), "lead", "conv-1"), &sseWriter{live: buf})

	child := &chatTurn{
		agent: AgentRecord{ID: "researcher", Name: "Researcher"},
		udb:   udb,
		ctx:   parentCtx,
	}
	child.beginDispatchDiag("researcher", "sub-9")
	child.turnDiag("tool-denied", "send_email is not granted to this agent")

	frames := noticeFrames(t, buf.String())
	if len(frames) != 1 {
		t.Fatalf("the sub-agent's block must reach the open pane, got %d:\n%s", len(frames), buf.String())
	}
	text := frames[0]["text"].(string)
	if !strings.HasPrefix(text, "↳ Researcher: ") {
		t.Fatalf("a sub-agent's block must name the sub-agent, or it reads as the agent being talked to: %q", text)
	}
	// The mirrored trail entry and the live frame are the same entry.
	mirrored := decorateSessionDiags(parentTrailOf(udb, "lead", "conv-1"))
	if len(mirrored) != 1 || mirrored[0].ID != frames[0]["id"] {
		t.Fatalf("live id %v vs mirrored trail %+v", frames[0]["id"], mirrored)
	}
}

// Nobody watching: a scheduled fire or a monitor wake has no pane and no
// stamp. It still records, and it must not panic reaching for a sink.
func TestBlockWithNobodyWatchingJustRecords(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	background := &chatTurn{agent: AgentRecord{ID: "nightly", Name: "Nightly"}, udb: udb, ctx: context.Background()}
	background.diagAgentID, background.diagSessionID = "nightly", "fire-3"
	background.turnDiag("guardrail-halted", "three blocks in one turn")

	if got := parentTrailOf(udb, "nightly", "fire-3"); len(got) != 1 {
		t.Fatalf("trail = %+v", got)
	}
}

// Both writes come from ONE clock reading. Two readings would be two
// identities for what is, to the page, one breadcrumb.
func TestOneStampNamesEveryCopyOfABreadcrumb(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	buf := &syncBuf{}
	ctx := withDiagNotices(withDiagParent(context.Background(), "lead", "conv-1"), &sseWriter{live: buf})
	child := &chatTurn{agent: AgentRecord{ID: "researcher", Name: "Researcher"}, udb: udb, ctx: ctx}
	child.diagAgentID, child.diagSessionID = "researcher", "sub-1"
	child.turnDiag("guardrail-blocked", "stopped")

	own := decorateSessionDiags(parentTrailOf(udb, "researcher", "sub-1"))
	parent := decorateSessionDiags(parentTrailOf(udb, "lead", "conv-1"))
	if len(own) != 1 || len(parent) != 1 {
		t.Fatalf("own=%+v parent=%+v", own, parent)
	}
	if own[0].ID != parent[0].ID {
		t.Fatalf("one breadcrumb, one id: own %q vs parent %q", own[0].ID, parent[0].ID)
	}
	if got := noticeFrames(t, buf.String()); len(got) != 1 || got[0]["id"] != own[0].ID {
		t.Fatalf("live frame must carry that same id: %+v", got)
	}
}

// diagID is derived, so entries written before it existed get one too.
func TestDiagIDIsDerivedFromWhatIsStored(t *testing.T) {
	at := time.Date(2026, 9, 17, 10, 30, 0, 123456789, time.UTC)
	e := SessionDiag{At: at, Kind: "tool-denied", Detail: "old entry, no id in the store"}
	got := decorateSessionDiags([]SessionDiag{e})
	if got[0].ID != diagID(at, "tool-denied") || got[0].ID == "" {
		t.Fatalf("stored entry did not get an id: %+v", got[0])
	}
}
