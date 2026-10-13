package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// approveEveryChange answers a change approval with Approve, for tests that
// drive authoring tools as an owner who is watching.
func approveEveryChange(prompt, detail string, yes []string) string {
	if len(yes) == 0 {
		return ""
	}
	return yes[0]
}

func changeTestStores(t *testing.T) Database {
	t.Helper()
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
	return &DBase{Store: kvlite.MemStore()}
}

// Something made in the session changes freely; something that existed asks
// once, and Approve covers it for the rest of the session.
func TestAChangeToWhatExistedAsksOncePerSession(t *testing.T) {
	udb := changeTestStores(t)
	asked := 0
	c := changeAsker{owner: "u", agentID: "seed-builder", agentName: "Builder", session: "s1", db: udb,
		ask: func(prompt, detail string, yes []string) string {
			asked++
			if !strings.Contains(prompt, "the tool wiki_read") {
				t.Errorf("prompt does not name the item: %q", prompt)
			}
			return yes[0]
		}}
	if msg := c.approve("tool", "fresh", "", false); msg != "" || asked != 0 {
		t.Fatalf("a new tool asked: %q", msg)
	}
	if msg := c.approve("tool", "fresh", "Change: description.", true); msg != "" || asked != 0 {
		t.Fatalf("a tool made in this session asked when changed: %q", msg)
	}
	for i := 0; i < 3; i++ {
		if msg := c.approve("tool", "wiki_read", "Change: url_template.", true); msg != "" {
			t.Fatalf("approved change refused: %q", msg)
		}
	}
	if asked != 1 {
		t.Errorf("asked %d times for one item in one session, want 1", asked)
	}
	// A different session asks again.
	c.session = "s2"
	c.approve("tool", "wiki_read", "", true)
	if asked != 2 {
		t.Errorf("another session reused this session's approval")
	}
}

func TestADeniedChangeIsRefused(t *testing.T) {
	udb := changeTestStores(t)
	c := changeAsker{owner: "u", agentID: "seed-builder", session: "s1", db: udb,
		ask: func(string, string, []string) string { return "" }}
	if msg := c.approve("app", "swing", "Delete the app.", true); !strings.Contains(msg, "declined") {
		t.Errorf("a denied change went ahead: %q", msg)
	}
}

// With nobody watching, the change waits in the approval queue (once), and
// approving it lets the agent make it in its next run.
func TestAnUnwatchedChangeQueuesAndApprovalLetsItThrough(t *testing.T) {
	udb := changeTestStores(t)
	c := changeAsker{owner: "u", agentID: "a1", session: "sched-1", db: udb}
	for i := 0; i < 2; i++ {
		if msg := c.approve("skill", "triage", "Delete it.", true); !strings.Contains(msg, "approval queue") {
			t.Fatalf("an unwatched change was not refused to the queue: %q", msg)
		}
	}
	var queued []Authorization
	for _, a := range ListAuthorizations(RootDB, "u") {
		if a.Action == changeApprovalAction {
			queued = append(queued, a)
		}
	}
	if len(queued) != 1 || queued[0].Handle != changeKey("skill", "triage") {
		t.Fatalf("queue holds %+v, want one entry for the skill", queued)
	}
	grantChange(udb, queued[0].Agent, queued[0].Handle)
	c.session = "sched-2"
	if msg := c.approve("skill", "triage", "Delete it.", true); msg != "" {
		t.Errorf("an approved change is still refused: %q", msg)
	}
	other := c
	other.agentID, other.session = "a2", "sched-3"
	if msg := other.approve("skill", "triage", "", true); msg == "" {
		t.Error("one agent's approval let a different agent through")
	}
}

// A sub-run with no viewer asks the chat that started it, rather than
// refusing because nobody watches the sub-run.
func TestASubRunConfirmationAsksTheCallingChat(t *testing.T) {
	var asked string
	sub := &chatTurn{askVia: func(prompt, detail string, yes []string) string {
		asked = prompt
		return yes[0]
	}}
	if !sub.escalateToolConfirm(toolConfirmRequest{tool: "agents", prompt: "Let A hand this to B?", because: "narrowed"}) {
		t.Fatal("approved in the calling chat, still refused")
	}
	if asked != "Let A hand this to B?" {
		t.Errorf("asked %q", asked)
	}
	no := &chatTurn{askVia: func(string, string, []string) string { return "" }}
	if no.escalateToolConfirm(toolConfirmRequest{tool: "agents", prompt: "Let A hand this to B?", because: "narrowed"}) {
		t.Error("denied in the calling chat, still allowed")
	}
}
