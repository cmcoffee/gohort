package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A chat embedded in an app is ABOUT something. Listing every conversation the
// user ever had with that agent gets worse the more the app is used: the
// sessions about the open document are buried under the ones about every other
// document. AppContext is what lets the app ask for the ones that belong here.
func TestSessionListScopesToOneAppContext(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	mk := func(id, ctx string) {
		if _, err := saveChatSession(db, ChatSession{
			ID: id, AgentID: "ag", Title: id, AppContext: ctx,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("about-a", "guide-a")
	mk("about-b", "guide-b")
	mk("unfiled", "")

	got := map[string]string{}
	for _, s := range listChatSessions(db, "ag") {
		got[s.ID] = s.AppContext
	}
	if got["about-a"] != "guide-a" {
		t.Fatalf("the listing must carry AppContext or the filter is a silent no-op: %+v", got)
	}

	keep := func(appContext string) map[string]bool {
		out := map[string]bool{}
		for _, s := range listChatSessions(db, "ag") {
			if appContext != "" && s.AppContext != "" && s.AppContext != appContext {
				continue
			}
			out[s.ID] = true
		}
		return out
	}

	a := keep("guide-a")
	if !a["about-a"] {
		t.Error("a session filed under this document must be listed with it")
	}
	if a["about-b"] {
		t.Error("a session about a DIFFERENT document is exactly what scoping exists to hide")
	}
	// Shown everywhere rather than nowhere: conversations that predate the field
	// must not disappear the day scoping ships.
	if !a["unfiled"] {
		t.Error("an unfiled session must stay reachable, or scoping loses the user's history")
	}

	all := keep("")
	if len(all) != 3 {
		t.Errorf("an empty scope means no scoping at all; got %v", all)
	}
}

// The back-fill: continuing an old conversation beside a document files it
// there. Without it the unfiled set never drains and every guide's list keeps
// showing every legacy session forever.
func TestAppContextBackfillsOnceAndDoesNotFollowTheUser(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	if _, err := saveChatSession(db, ChatSession{ID: "s1", AgentID: "ag", Title: "legacy"}); err != nil {
		t.Fatal(err)
	}

	// This mirrors the rule in the send path: fill when empty, never overwrite.
	apply := func(reqContext string) {
		sess, ok := loadChatSession(db, "ag", "s1")
		if !ok {
			t.Fatal("session vanished")
		}
		if sess.AppContext == "" && reqContext != "" {
			sess.AppContext = reqContext
		}
		if _, err := saveChatSession(db, sess); err != nil {
			t.Fatal(err)
		}
	}

	apply("guide-a")
	if s, _ := loadChatSession(db, "ag", "s1"); s.AppContext != "guide-a" {
		t.Fatalf("continuing a legacy session beside a document should file it there, got %q", s.AppContext)
	}

	// Opening a different guide and continuing the SAME conversation must not
	// move it. AppContext records where a conversation started, not where the
	// user happens to be — otherwise a session silently disappears from the
	// list it belonged to.
	apply("guide-b")
	if s, _ := loadChatSession(db, "ag", "s1"); s.AppContext != "guide-a" {
		t.Errorf("AppContext must not follow the open document; got %q", s.AppContext)
	}
}
