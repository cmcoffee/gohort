package orchestrate

import (
	"strings"
	"testing"
	"time"

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
	if msg := c.approve("tool", "fresh", "", ChangeNew); msg != "" || asked != 0 {
		t.Fatalf("a new tool asked: %q", msg)
	}
	if msg := c.approve("tool", "fresh", "Change: description.", ChangeEdit); msg != "" || asked != 0 {
		t.Fatalf("a tool made in this session asked when changed: %q", msg)
	}
	for i := 0; i < 3; i++ {
		if msg := c.approve("tool", "wiki_read", "Change: url_template.", ChangeEdit); msg != "" {
			t.Fatalf("approved change refused: %q", msg)
		}
	}
	if asked != 1 {
		t.Errorf("asked %d times for one item in one session, want 1", asked)
	}
	// A different session asks again.
	c.session = "s2"
	c.approve("tool", "wiki_read", "", ChangeEdit)
	if asked != 2 {
		t.Errorf("another session reused this session's approval")
	}
}

func TestADeniedChangeIsRefused(t *testing.T) {
	udb := changeTestStores(t)
	c := changeAsker{owner: "u", agentID: "seed-builder", session: "s1", db: udb,
		ask: func(string, string, []string) string { return "" }}
	if msg := c.approve("app", "swing", "Delete the app.", ChangeDelete); !strings.Contains(msg, "declined") {
		t.Errorf("a denied change went ahead: %q", msg)
	}
}

// With nobody watching, the change waits in the approval queue (once), and
// approving it lets the agent make it in its next run.
func TestAnUnwatchedChangeQueuesAndApprovalLetsItThrough(t *testing.T) {
	udb := changeTestStores(t)
	c := changeAsker{owner: "u", agentID: "a1", session: "sched-1", db: udb}
	for i := 0; i < 2; i++ {
		if msg := c.approve("skill", "triage", "Delete it.", ChangeDelete); !strings.Contains(msg, "approval queue") {
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
	if msg := c.approve("skill", "triage", "Delete it.", ChangeDelete); msg != "" {
		t.Errorf("an approved change is still refused: %q", msg)
	}
	other := c
	other.agentID, other.session = "a2", "sched-3"
	if msg := other.approve("skill", "triage", "", ChangeEdit); msg == "" {
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

// The item the owner picked in Builder's form maps to the keys its authoring
// tool asks with; Create, Other and an API pick nothing.
func TestIntakeChangeKeysFollowTheForm(t *testing.T) {
	cases := []struct {
		values map[string]string
		want   []string
	}{
		{map[string]string{"action": "Change", "kind": "App", "target": "swing"}, []string{"app:swing"}},
		{map[string]string{"action": "Fix", "kind": "Agent", "target": "Ledger (id: a1)"}, []string{"agent:a1"}},
		{map[string]string{"action": "Change", "kind": "Pipeline", "target": "digest"}, []string{"pipeline:digest", "tool:digest"}},
		{map[string]string{"action": "Fix", "kind": "Connection", "target": "prices"}, []string{"bridge:prices"}},
		{map[string]string{"action": "Fix", "kind": "Tool", "target": "wiki_read"}, []string{"tool:wiki_read"}},
		{map[string]string{"action": "Create", "kind": "App", "target": "swing"}, nil},
		{map[string]string{"action": "Change", "kind": "API", "target": "weather"}, nil},
		{map[string]string{"action": "Change", "kind": "Other"}, nil},
		{map[string]string{"action": "Change", "kind": "Tool"}, nil},
	}
	for _, c := range cases {
		if got := intakeChangeKeys(c.values); strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%v: got %v, want %v", c.values, got, c.want)
		}
	}
}

// Picking an item in Builder's form approves changes to it in that session:
// no card. Deleting it still asks, so does anything else, and Builder another
// agent sent asks even about the picked item.
func TestTheIntakeTargetIsApprovedForChangesOnly(t *testing.T) {
	udb := changeTestStores(t)
	var asked []string
	c := changeAsker{owner: "u", agentID: "seed-builder", agentName: "Builder", session: "s-intake", db: udb,
		ask: func(prompt, detail string, yes []string) string {
			asked = append(asked, prompt)
			return ""
		}}
	turn := &chatTurn{user: "u", agent: AgentRecord{ID: "seed-builder"}, session: &ChatSession{ID: "s-intake", Messages: []ChatMessage{
		{Role: "user", IntakeValues: map[string]string{"action": "Change", "kind": "App", "target": "swing"}},
		{Role: "user", Content: "**What do you want to do?:** Fix\n**What kind of thing?:** Tool\n**Which one?:** wiki_read"},
	}}}
	turn.noteIntakeTargets()

	if msg := c.approve("app", "swing", "Revise the app.", ChangeEdit); msg != "" || len(asked) != 0 {
		t.Fatalf("a change to the picked app asked or refused: %q, asked %v", msg, asked)
	}
	if msg := c.approve("app", "swing", "Delete the app.", ChangeDelete); !strings.Contains(msg, "declined") || len(asked) != 1 {
		t.Errorf("deleting the picked app did not ask: %q, asked %v", msg, asked)
	}
	if msg := c.approve("app", "other", "Revise the app.", ChangeEdit); !strings.Contains(msg, "declined") {
		t.Errorf("an app the form did not pick went ahead: %q", msg)
	}
	// Text shaped like the form is not the form.
	if msg := c.approve("tool", "wiki_read", "Change: description.", ChangeEdit); !strings.Contains(msg, "declined") {
		t.Errorf("a target read from message text was approved: %q", msg)
	}
	sent := c
	sent.sentBy = "a-helper"
	if msg := sent.approve("app", "swing", "Revise the app.", ChangeEdit); !strings.Contains(msg, "declined") {
		t.Errorf("Builder sent by an agent skipped the ask on the picked app: %q", msg)
	}
	// Builder may run what the form picked, to test it.
	if !changeApprovalsFor("u", "s-intake").covers(changeKey("app", "swing")) {
		t.Error("the picked app is not covered for running")
	}
}

// Only Builder's own form counts: another agent's session with the same
// values picks nothing.
func TestOnlyBuildersFormNamesATarget(t *testing.T) {
	turn := &chatTurn{user: "u", agent: AgentRecord{ID: "seed-chat"}, session: &ChatSession{ID: "s-other", Messages: []ChatMessage{
		{Role: "user", IntakeValues: map[string]string{"action": "Change", "kind": "App", "target": "swing"}},
	}}}
	turn.noteIntakeTargets()
	if changeApprovalsFor("u", "s-other").isNamed(changeKey("app", "swing")) {
		t.Error("a non-Builder agent's form approved a target")
	}
}

// An agent a watched turn sends is asked about as itself. Wiring the turn's
// live callbacks onto the sub-run used to replace the sub-run's approver with
// the sending agent's, so the card named the wrong agent.
func TestASentAgentIsAskedAboutAsItself(t *testing.T) {
	udb := changeTestStores(t)
	parent := &chatTurn{user: "u", udb: udb, agent: AgentRecord{ID: "a-lead", Name: "Lead"}, sse: &sseWriter{live: &syncBuf{}}}
	var prompts []string
	ask := func(prompt, detail string, yes []string) string { prompts = append(prompts, prompt); return "" }
	sub := &ToolSession{Username: "u", DB: udb, ChatSessionID: "s-sub", DispatchParentAgentID: "a-lead"}
	sub.ApproveChange = changeApprover(sub, "u", "a-helper", "Helper", ask)
	parent.wireLiveCallbacks(sub)
	done := make(chan struct{})
	go func() { sub.ApproveChange("skill", "triage", "Change it.", ChangeEdit); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sent agent's change waited on the sending turn's own card")
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "Let Helper change") {
		t.Errorf("the sent agent was asked about as %v", prompts)
	}
	// A session with no approver of its own still gets the turn's.
	own := &ToolSession{Username: "u", DB: udb}
	parent.wireLiveCallbacks(own)
	if own.ApproveChange == nil {
		t.Error("the turn's own session got no approver")
	}
}

// Two agents can share a name, so an agent is approved by its id. Keyed by
// name, making one "Report Writer" in a session let it change or delete the
// other without asking. The question still names it.
func TestMakingOneAgentDoesNotApproveItsNamesake(t *testing.T) {
	udb := changeTestStores(t)
	mine, _ := saveAgent(udb, AgentRecord{Name: "Report Writer", Owner: "u", OrchestratorPrompt: "p"})
	theirs, _ := saveAgent(udb, AgentRecord{Name: "Report Writer", Owner: "u", OrchestratorPrompt: "p"})
	var prompts []string
	c := changeAsker{owner: "u", agentID: "seed-builder", agentName: "Builder", session: "s1", db: udb,
		ask: func(prompt, detail string, yes []string) string {
			prompts = append(prompts, prompt)
			return ""
		}}
	c.approve("agent", mine.ID, "", ChangeNew)
	if msg := c.approve("agent", mine.ID, "Update it.", ChangeEdit); msg != "" || len(prompts) != 0 {
		t.Errorf("the agent made in this session asked to be changed: %q %v", msg, prompts)
	}
	if msg := c.approve("agent", theirs.ID, "Delete it.", ChangeDelete); msg == "" {
		t.Error("the other agent with the same name was deleted without asking")
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "the agent Report Writer") {
		t.Errorf("the question should name the agent, not its id: %v", prompts)
	}
}

// An approved change is not an approved deletion: deleting something whose
// change was approved in this session asks again. What the session made, it
// deletes freely.
func TestAnApprovedChangeDoesNotCoverADelete(t *testing.T) {
	udb := changeTestStores(t)
	asked := 0
	c := changeAsker{owner: "u", agentID: "seed-builder", session: "s-del", db: udb,
		ask: func(string, string, []string) string { asked++; return "Approve" }}
	if msg := c.approve("tool", "cal", "Change it.", ChangeEdit); msg != "" || asked != 1 {
		t.Fatalf("the change: %q asked %d", msg, asked)
	}
	if msg := c.approve("tool", "cal", "Delete it.", ChangeDelete); msg != "" || asked != 2 {
		t.Errorf("a delete rode an approved change: %q asked %d", msg, asked)
	}
	c.approve("tool", "probe", "", ChangeNew)
	if msg := c.approve("tool", "probe", "Delete it.", ChangeDelete); msg != "" || asked != 2 {
		t.Errorf("deleting what the session made asked: %q asked %d", msg, asked)
	}
}
