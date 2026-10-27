package orchestrate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Breadcrumbs from a DISPATCHED turn — a channel inbound, a cortex thread, a
// sub-agent run.
//
// These turns have no *session: the record belongs to the caller, so the turn
// carries only the ids of the trail it should write to. When those ids were
// never set, turnDiag hit its "no trail to write to" return and every guard on
// the path went silent — including the guardrail block, which had already
// worked out WHICH rule fired and then threw the answer away. The owner got a
// decline on their phone and no way to find out what stopped it.

// dispatchLikeTurn is a turn shaped the way agent_dispatch builds one: no
// session, a runtime identity that may differ from the owner, and the trail
// named explicitly.
func dispatchLikeTurn(root Database, owner, runtimeUser, agentID, sessionID string) *chatTurn {
	app := &OrchestrateApp{}
	app.DB = root
	return &chatTurn{
		app:           app,
		agent:         AgentRecord{ID: agentID, Name: "X", Owner: owner},
		user:          runtimeUser,
		udb:           UserDB(root, runtimeUser),
		ownerUser:     owner,
		ownerDB:       UserDB(root, owner),
		diagAgentID:   agentID,
		diagSessionID: sessionID,
	}
}

// TestDispatchedTurnLeavesABreadcrumb — the trail ids are what make a
// session-less turn accountable at all.
func TestDispatchedTurnLeavesABreadcrumb(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := dispatchLikeTurn(root, "u", "u", "a1", "chat-42")
	turn.turnDiag("guardrail-blocked", `Guardrail "never discuss pay" blocked a pre_output check`)

	got := sessionDiags(UserDB(root, "u"), "a1", "chat-42")
	if len(got) != 1 {
		t.Fatalf("expected one breadcrumb, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "guardrail-blocked" {
		t.Errorf("kind = %q", got[0].Kind)
	}
	// The rule is the whole point — a trail that says "something blocked this"
	// is barely better than silence.
	if !strings.Contains(got[0].Detail, "never discuss pay") {
		t.Errorf("the breadcrumb must name the rule, got %q", got[0].Detail)
	}
}

// TestBreadcrumbLandsInTheOwnersStore — a channel turn can run as a synthetic
// per-chat identity. The trail is read back through the REQUESTING user's own
// store, so a breadcrumb filed under the synthetic identity is filed where
// nobody will ever look.
func TestBreadcrumbLandsInTheOwnersStore(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := dispatchLikeTurn(root, "u", "phantom:chat123", "a1", "chat-42")
	turn.turnDiag("guardrail-input-blocked", `Guardrail "no addresses" refused the request`)

	if got := sessionDiags(UserDB(root, "u"), "a1", "chat-42"); len(got) != 1 {
		t.Errorf("owner cannot see the breadcrumb: %+v", got)
	}
	if got := sessionDiags(UserDB(root, "phantom:chat123"), "a1", "chat-42"); len(got) != 0 {
		t.Errorf("breadcrumb was filed under the synthetic identity: %+v", got)
	}
}

// TestNoTrailIdsStillSilent — the guard against writing breadcrumbs nowhere
// stays; a turn with genuinely no session to name must not invent one.
func TestNoTrailIdsStillSilent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := dispatchLikeTurn(root, "u", "u", "a1", "")
	turn.turnDiag("guardrail-blocked", "something")
	if got := sessionDiags(UserDB(root, "u"), "a1", ""); len(got) != 0 {
		t.Errorf("wrote a breadcrumb with no trail to write to: %+v", got)
	}
}

// sessionDiags reads a trail back the way the HTTP handler does.
func sessionDiags(udb Database, agentID, sessionID string) []SessionDiag {
	var list []SessionDiag
	if udb == nil {
		return nil
	}
	udb.Get(sessionDiagTable, agentID+":"+sessionID, &list)
	return list
}

// A guard that STOPPED something says so in the conversation, while the turn
// it interrupted is still on screen. The trail behind the ⚠ button is where
// you look once you already suspect a guard fired; these are how you find out
// that one did.

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
		"form-step-discarded", "provider-refusal",
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
		// Carries a blocking verb but is not a block: prose held on a bet
		// about the final reply, restored by lead-in-restored when the bet
		// loses. Shown as a card it read BLOCKED, in guardrail amber, twice in
		// half a minute, for a rendering decision that may undo itself.
		"lead-in-withheld",
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

// The verb rule reads what a guard DID. What decides whether a card belongs in
// somebody's chat is whether they have anything to DO, and those differ twice.
func TestABlockIsSomethingTheReaderCanActOn(t *testing.T) {
	// Reversible: held on a bet the framework may undo before the reader has
	// finished the sentence.
	if diagLevel("lead-in-withheld") != diagLevelNote {
		t.Error("held prose that the framework restores on its own is not a block")
	}
	if diagLevel("lead-in-restored") != diagLevelNote {
		t.Error("a restoration is not a block either")
	}
	// By design: it stays withheld, and that is the correct answer rather than
	// a fault. Running somebody else's agent means no authoring tools; the
	// detail says "nothing is broken" in as many words, and it was showing an
	// ordinary user an amber BLOCKED card about a permission they never asked
	// for on an agent that is not theirs. Still in the trail for whoever is
	// debugging an absent tool_def; off the pane.
	if diagLevel("authoring_withheld") != diagLevelNote {
		t.Error("an expected absence is not a block")
	}
	// The verb still works for a withholding somebody SHOULD read: the warden
	// took content out of a reply they are about to act on.
	if diagLevel("guardrail-output-withheld") != diagLevelBlocked {
		t.Error("content removed from a reply must still reach the conversation")
	}
	// Every excluded kind must be spelled exactly as its call site writes it,
	// or the exclusion silently does nothing.
	for _, m := range []map[string]bool{diagProvisionalKinds, diagByDesignKinds} {
		for k := range m {
			if k != strings.ToLower(strings.TrimSpace(k)) {
				t.Errorf("%q will never match: the lookup folds case and trims first", k)
			}
		}
	}
	// And each one is a kind something actually writes, or the exclusion is
	// about a breadcrumb that no longer exists.
	src := packageSource(t)
	for _, m := range []map[string]bool{diagProvisionalKinds, diagByDesignKinds} {
		for k := range m {
			if !strings.Contains(src, `"`+k+`"`) {
				t.Errorf("no call site writes the diag kind %q", k)
			}
		}
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

func parentTrailOf(db Database, agentID, sessionID string) []SessionDiag {
	var list []SessionDiag
	db.Get(sessionDiagTable, agentID+":"+sessionID, &list)
	return list
}

// A dispatched turn's breadcrumb lands in its own sub-session trail AND in
// the conversation it descends from, tagged with the sub-agent's name — the
// only trail a person can open.
func TestDispatchedDiagMirrorsIntoTheParentConversation(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	parentCtx := withDiagParent(context.Background(), "lead", "conv-1")

	child := &chatTurn{
		agent: AgentRecord{ID: "researcher", Name: "Researcher"},
		udb:   udb,
		ctx:   parentCtx, // inherited from the parent's dispatch
	}
	child.beginDispatchDiag("researcher", "sub-9")
	child.turnDiag("guardrail-halted", "rule 'no external posts' stopped the reply")

	own := parentTrailOf(udb, "researcher", "sub-9")
	if len(own) == 0 || own[len(own)-1].Kind != "guardrail-halted" {
		t.Fatalf("own trail = %+v", own)
	}
	parent := parentTrailOf(udb, "lead", "conv-1")
	if len(parent) != 1 || parent[0].Kind != "guardrail-halted" || !strings.HasPrefix(parent[0].Detail, "↳ Researcher: ") {
		t.Fatalf("parent trail = %+v", parent)
	}
	// machine_not_on_dispatch, filed by beginDispatchDiag itself, mirrors too.
	withMachine := &chatTurn{agent: AgentRecord{ID: "planner", Name: "Planner", Machine: "m-1"}, udb: udb, ctx: parentCtx}
	withMachine.beginDispatchDiag("planner", "sub-10")
	found := false
	for _, d := range parentTrailOf(udb, "lead", "conv-1") {
		if d.Kind == "machine_not_on_dispatch" && strings.Contains(d.Detail, "↳ Planner") {
			found = true
		}
	}
	if !found {
		t.Fatalf("machine_not_on_dispatch not mirrored: %+v", parentTrailOf(udb, "lead", "conv-1"))
	}
}

// A live turn is its own conversation and never mirrors; a dispatch with no
// stamp on its context has nowhere to mirror to and writes only its own trail.
func TestDiagMirrorStaysQuietWithoutAParent(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	live := &chatTurn{agent: AgentRecord{ID: "lead", Name: "Lead"}, udb: udb, ctx: withDiagParent(context.Background(), "lead", "conv-1"),
		session: &ChatSession{ID: "conv-1", AgentID: "lead"}}
	live.turnDiag("spend-cap", "daily cap reached")
	if got := parentTrailOf(udb, "lead", "conv-1"); len(got) != 1 || strings.HasPrefix(got[0].Detail, "↳") {
		t.Fatalf("live turn should write once, untagged: %+v", got)
	}

	orphan := &chatTurn{agent: AgentRecord{ID: "researcher", Name: "Researcher"}, udb: udb, ctx: context.Background()}
	orphan.beginDispatchDiag("researcher", "sub-2")
	orphan.turnDiag("tool-denied", "x")
	if got := parentTrailOf(udb, "researcher", "sub-2"); len(got) != 1 {
		t.Fatalf("orphan own trail = %+v", got)
	}
	if got := parentTrailOf(udb, "lead", "conv-1"); len(got) != 1 {
		t.Fatalf("unstamped dispatch must not reach the conversation: %+v", got)
	}
}

// Depth: a grandchild dispatched from a child still lands in the ROOT
// conversation, because only the live turn stamps.
func TestDiagMirrorReachesTheRootAcrossDepth(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	root := withDiagParent(context.Background(), "lead", "conv-1")
	grandchild := &chatTurn{agent: AgentRecord{ID: "fetcher", Name: "Fetcher"}, udb: udb, ctx: root}
	grandchild.beginDispatchDiag("fetcher", "sub-sub-3")
	grandchild.turnDiag("provider-refusal", "429")
	if got := parentTrailOf(udb, "lead", "conv-1"); len(got) != 1 || !strings.Contains(got[0].Detail, "↳ Fetcher: 429") {
		t.Fatalf("root trail = %+v", got)
	}
}
