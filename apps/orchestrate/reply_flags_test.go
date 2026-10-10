package orchestrate

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/replyguard"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A flag is filed against the stored reply it names (newest match first, or
// one that starts the same way), with the message the person sent before it
// and the model that wrote it.
func TestAFlagFindsTheReplyItIsAbout(t *testing.T) {
	sess := ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "summarise the news"},
		{Role: "assistant", Content: "Here is the summary.", Usage: &ChatMessageUsage{Model: "old-model"}},
		{Role: "user", Content: "and again"},
		{Role: "assistant", Content: "Here is the summary.", Usage: &ChatMessageUsage{Model: "gemini-2.5-flash"}},
		{Role: "user", Content: "one more thing"},
		{Role: "assistant", Content: "A long answer that the bubble shows with a trailing space"},
	}}
	i, asked, ok := findFlaggedReply(sess, "  Here is the summary. ")
	if !ok || i != 3 || asked != "and again" || sess.Messages[i].Usage.Model != "gemini-2.5-flash" {
		t.Errorf("the newest matching reply, with what came before it: %d %q %v", i, asked, ok)
	}
	if _, asked, ok := findFlaggedReply(sess, "A long answer that the bubble shows with a trailing space, then edited"); ok {
		t.Errorf("a longer text is not a match: %q", asked)
	}
	if _, _, ok := findFlaggedReply(sess, "never said"); ok {
		t.Error("a reply that is not in the session is not matched")
	}
}

// The early-answer check is a reply guard too: shadow counts it and holds
// nothing back.
func TestTheEarlyAnswerCheckIsAReplyGuard(t *testing.T) {
	if !replyguard.Known(earlyAnswerGuard) {
		t.Fatal("early-answer is not registered")
	}
	if !earlyAnswerActs("worker", "any-model", "text") {
		t.Error("on by default")
	}
}

// A reply carries one mark: the other thumb replaces it (and drops the old
// flag from the queue), taking it back removes it, and a reloaded thread
// shows the mark only on the reply it was made on.
func TestAReplyCarriesOneMarkThatCanBeTakenBack(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	msgs := func() []ChatMessage {
		return []ChatMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "Hello there."},
			{Role: "user", Content: "more"},
			{Role: "assistant", Content: "Here is more."},
		}
	}
	up := replyFlag{ID: "f1", User: "u", AgentID: "a", SessionID: "s", Verdict: flagUp, Reply: "Hello there."}
	db.Set(replyFlagTable, up.ID, up)
	refFlag(db, up, 1)
	m := msgs()
	markFlaggedReplies(db, "u", "a", "s", m)
	if m[1].Flag != flagUp || m[1].FlagID != "f1" || m[3].Flag != "" {
		t.Fatalf("the thumbs-up shows on its reply only: %+v", m)
	}
	down := replyFlag{ID: "f2", User: "u", AgentID: "a", SessionID: "s", Verdict: flagDown, Reply: "Hello there."}
	db.Set(replyFlagTable, down.ID, down)
	refFlag(db, down, 1)
	m = msgs()
	markFlaggedReplies(db, "u", "a", "s", m)
	var gone replyFlag
	if m[1].Flag != flagDown || m[1].FlagID != "f2" || db.Get(replyFlagTable, "f1", &gone) {
		t.Errorf("the other thumb replaces the first, which leaves the queue: %+v", m[1])
	}
	unflag(db, down)
	m = msgs()
	markFlaggedReplies(db, "u", "a", "s", m)
	if m[1].Flag != "" || db.Get(replyFlagTable, "f2", &gone) {
		t.Errorf("taken back, the mark and the flag are gone: %+v", m[1])
	}
	// A retry that put a different reply at that position does not inherit it.
	refFlag(db, up, 1)
	m = msgs()
	m[1].Content = "A different reply."
	markFlaggedReplies(db, "u", "a", "s", m)
	if m[1].Flag != "" {
		t.Error("a mark is not shown on a reply it was not made on")
	}
	m = msgs()
	markFlaggedReplies(db, "someone-else", "a", "s", m)
	if m[1].Flag != "" {
		t.Error("another person does not see this person's mark")
	}
}

// Configure > Security opens the Security button's view, and never the
// standalone page.
func TestConfigureSecurityIsAShortcutToTheView(t *testing.T) {
	raw, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	i := strings.Index(src, "uiRegisterClientAction('orchestrate_secure_agent'")
	if i < 0 {
		t.Fatal("the orchestrate_secure_agent action is gone")
	}
	end := strings.Index(src[i:], "});")
	action := src[i : i+end]
	if !strings.Contains(action, "uiOpenNavItem('Security')") {
		t.Error("it opens the Security view")
	}
	if strings.Contains(action, "location.href") || strings.Contains(action, "/access") {
		t.Error("it must never go to the standalone page")
	}
}

// Agent > Share opens the Security view on its Share tab, never a page.
func TestAgentShareOpensTheShareTab(t *testing.T) {
	raw, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	i := strings.Index(src, "uiRegisterClientAction('orchestrate_share_agent'")
	if i < 0 {
		t.Fatal("the orchestrate_share_agent action is missing")
	}
	action := src[i : i+strings.Index(src[i:], "});")]
	if !strings.Contains(action, "uiOpenNavItem('Security', {tab: 'Share'})") || strings.Contains(action, "location.href") {
		t.Errorf("it opens the Share tab in the view, never a page: %s", action)
	}
}
