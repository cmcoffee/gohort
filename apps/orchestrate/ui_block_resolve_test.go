package orchestrate

import (
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The bug these cover: an actionable card settled only in the browser. The
// click's effect landed on the credential store, the block on the session
// record never changed, and every reload replayed the same card with its
// buttons live — a request the user had already answered, still asking.

func seedBlockSession(t *testing.T, db Database, agentID, sessID string, blocks ...UIBlock) {
	t.Helper()
	if _, err := saveChatSession(db, ChatSession{ID: sessID, AgentID: agentID, UIBlocks: blocks}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

func resolveBlock(t *testing.T, T *OrchestrateApp, db Database, agentID, sessID, blockID, body string) int {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/sessions/"+sessID+"/blocks/"+blockID+"/resolve", strings.NewReader(body))
	T.handleSessionBlockResolve(w, r, db, "u", agentID, sessID, blockID)
	return w.Code
}

// TestBlockResolveSurvivesReload is the whole point: the answer is stamped on
// the STORED block, so the next session load carries it.
func TestBlockResolveSurvivesReload(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	T := &OrchestrateApp{AppCore: AppCore{DB: db}}
	seedBlockSession(t, db, "ag", "s1",
		UIBlock{Type: "credential_update", ID: "credupdate-acme", Title: "acme"},
		UIBlock{Type: "html_artifact", ID: "artifact-1", Title: "dashboard"},
	)

	if code := resolveBlock(t, T, db, "ag", "s1", "credupdate-acme",
		`{"note":"Applied — the credential was updated (secret preserved)."}`); code != 204 {
		t.Fatalf("resolve: want 204, got %d", code)
	}

	s, ok := loadChatSession(db, "ag", "s1")
	if !ok {
		t.Fatal("session vanished")
	}
	if len(s.UIBlocks) != 2 {
		t.Fatalf("resolve must not add or drop blocks, got %d", len(s.UIBlocks))
	}
	if !strings.HasPrefix(s.UIBlocks[0].Resolved, "Applied") {
		t.Fatalf("answered card replays unresolved: %q", s.UIBlocks[0].Resolved)
	}
	if s.UIBlocks[1].Resolved != "" {
		t.Fatal("a display-only block must never be stamped")
	}
}

// TestBlockResolveIsIdempotent — a double-click, or a replayed card clicked
// again, must not error.
func TestBlockResolveIsIdempotent(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	T := &OrchestrateApp{AppCore: AppCore{DB: db}}
	seedBlockSession(t, db, "ag", "s1", UIBlock{Type: "credential_update", ID: "b1"})

	resolveBlock(t, T, db, "ag", "s1", "b1", `{"note":"Applied."}`)
	if code := resolveBlock(t, T, db, "ag", "s1", "b1", `{"note":"Dismissed — no changes made."}`); code != 204 {
		t.Fatalf("second resolve: want 204, got %d", code)
	}
	s, _ := loadChatSession(db, "ag", "s1")
	if s.UIBlocks[0].Resolved != "Dismissed — no changes made." {
		t.Fatalf("last answer should win, got %q", s.UIBlocks[0].Resolved)
	}
	// A card can outlive its stored block; there is simply nothing to stamp.
	if code := resolveBlock(t, T, db, "ag", "s1", "gone", `{"note":"x"}`); code != 204 {
		t.Fatalf("unknown block: want a quiet 204, got %d", code)
	}
}

// TestBlockResolveEmptyNoteStillSettles — the note is cosmetic; a card with no
// note must still stop asking.
func TestBlockResolveEmptyNoteStillSettles(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	T := &OrchestrateApp{AppCore: AppCore{DB: db}}
	seedBlockSession(t, db, "ag", "s1", UIBlock{Type: "credential_setup", ID: "b1", Title: "acme"})

	resolveBlock(t, T, db, "ag", "s1", "b1", `{"note":"   "}`)
	s, _ := loadChatSession(db, "ag", "s1")
	if s.UIBlocks[0].Resolved == "" {
		t.Fatal("an answered card with no note still replays as pending")
	}
}

// TestSettleResolvedBlocksDerivesOutOfBandSetup — the admin typed the secret in
// Admin > APIs and never touched the card. Nothing POSTed, so the card has to
// settle from the credential's own state or it asks forever.
func TestSettleResolvedBlocksDerivesOutOfBandSetup(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()

	if err := Secure().Save(SecureCredential{
		Name: "blockresolve_live", Type: SecureCredBearer, BaseURL: "https://example.com",
	}, "tok"); err != nil {
		t.Fatalf("save credential: %v", err)
	}

	blocks := settleResolvedBlocks([]UIBlock{
		{Type: "credential_setup", ID: "credsetup-blockresolve_live", Title: "blockresolve_live"},
		{Type: "credential_setup", ID: "credsetup-blockresolve_missing", Title: "blockresolve_missing"},
		{Type: "credential_update", ID: "credupdate-blockresolve_live", Title: "blockresolve_live"},
		{Type: "html_artifact", ID: "artifact-1", Title: "blockresolve_live"},
	}, "u")

	if blocks[0].Resolved == "" {
		t.Fatal("a credential that went live elsewhere must stop asking for its secret")
	}
	// A miss is ambiguous (deleted, or the store isn't attached) — settling on
	// it would retire live cards on a store that simply wasn't ready.
	if blocks[1].Resolved != "" {
		t.Fatalf("unknown credential must stay actionable, got %q", blocks[1].Resolved)
	}
	// Whether the user WANTS a config diff has no witness in the store.
	if blocks[2].Resolved != "" {
		t.Fatal("an unanswered credential_update has nothing to derive from")
	}
	if blocks[3].Resolved != "" {
		t.Fatal("display-only blocks are not actionable")
	}
}

// TestSettleResolvedBlocksKeepsStoredAnswer — a stored answer is the user's
// own words and outranks anything derived.
func TestSettleResolvedBlocksKeepsStoredAnswer(t *testing.T) {
	blocks := settleResolvedBlocks([]UIBlock{
		{Type: "credential_setup", ID: "b1", Title: "acme", Resolved: "Secret saved and enabled."},
	}, "u")
	if blocks[0].Resolved != "Secret saved and enabled." {
		t.Fatalf("stored answer overwritten: %q", blocks[0].Resolved)
	}
}
