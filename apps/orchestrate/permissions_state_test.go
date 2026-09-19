package orchestrate

// A segmented control must not offer a state its row cannot hold.
//
// Agent and contact rows have three stored states. A tool grant used to have
// one and a half: AutoApproveTools is a LIST, so it can only say yes, and
// "needs approval" was the absence of an entry — indistinguishable from never
// having decided. The control offered the segment anyway, so choosing it
// removed the entry and the row vanished, because the row existed only while
// the entry did.
//
// Fixed by giving the decision a record rather than by removing the segment:
// allow (and the grant is in the list) or ask (and it is not). Either way the
// row stays and shows which.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"

	"github.com/cmcoffee/gohort/core/notices"
)

// The round trip, driven: choosing "Needs approval" has to leave something
// behind, or the row goes and we are back where we started.
func TestNeedsApprovalIsAStateAToolRowCanHold(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	setAutoToolPolicy(db, nil, "alice", "agent-1", "send_email", PolicyAsk)

	got := listAutoToolPolicies(db, "alice")
	if len(got) != 1 {
		t.Fatalf("%d records, want 1: the decision was not stored, so the row leaves the page", len(got))
	}
	if got[0].AgentID != "agent-1" || got[0].Tool != "send_email" || got[0].Policy != PolicyAsk {
		t.Errorf("record = %+v", got[0])
	}

	// Flipping back is a state change, not a re-grant from nothing.
	setAutoToolPolicy(db, nil, "alice", "agent-1", "send_email", PolicyAllow)
	if got = listAutoToolPolicies(db, "alice"); len(got) != 1 || got[0].Policy != PolicyAllow {
		t.Fatalf("after allow: %+v", got)
	}

	// Remove is the one that ends it. That is what Remove means on every other
	// row of this page.
	removeAutoToolPolicy(db, nil, "alice", "agent-1", "send_email")
	if got = listAutoToolPolicies(db, "alice"); len(got) != 0 {
		t.Errorf("%d records survive a Remove: %+v", len(got), got)
	}
}

// Block used to be withheld from tool rows because the runtime had no way to
// honor it, and a segment reading "never" over a tool that went on queueing for
// approval is a control lying about the state it sets. The runtime now honors
// it, so the test inverts: the segment is offered, AND a write actually reaches
// the runner. The second half is the one that matters, because it is the half
// that was missing when the segment was first hidden.
func TestBlockingAToolReachesTheRunner(t *testing.T) {
	page := readFile(t, "page_chat.go")
	i := strings.Index(page, `{Label: "Blocked", Value: "block"`)
	if i < 0 {
		t.Fatal("the Blocked segment is gone entirely")
	}
	line := page[i:]
	if j := strings.Index(line, "\n"); j > 0 {
		line = line[:j]
	}
	if strings.Contains(line, `HideIf: "_autotool"`) {
		t.Errorf("Blocked is still hidden on tool rows, which can now store it: %s", line)
	}

	db := &DBase{Store: kvlite.MemStore()}
	udb := &DBase{Store: kvlite.MemStore()}
	if _, err := saveAgent(udb, AgentRecord{ID: "a", Owner: "alice", Name: "A", OrchestratorPrompt: "do the thing"}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	setAutoToolPolicy(db, udb, "alice", "a", "send_email", PolicyBlock)

	got := listAutoToolPolicies(db, "alice")
	if len(got) != 1 || got[0].Policy != PolicyBlock {
		t.Fatalf("the decision was not recorded as a block: %+v", got)
	}
	// The runner reads the agent record, not this table. A block that stopped
	// at the record would be a page telling the owner something the fire never
	// hears, which is the exact failure the segment was hidden to avoid.
	rec, _ := loadAgent(udb, "a")
	if len(rec.NoUnattendedTools) != 1 || rec.NoUnattendedTools[0] != "send_email" {
		t.Errorf("the mark never reached the agent the runner loads: %+v", rec.NoUnattendedTools)
	}
	if !autonomousNoUnattendedSet(udb, "a")["send_email"] {
		t.Error("the gate's own set does not carry the mark")
	}
	// And it refuses, ahead of every other clause.
	if autonomousToolAllowed(true, map[string]bool{"send_email": true},
		autonomousNoUnattendedSet(udb, "a"), "send_email", func(string) bool { return false }) {
		t.Error("a marked tool ran anyway; the mark has to beat both the sub-agent bypass and a standing grant")
	}

	// Moving off block clears the mark, or the two lists disagree and the
	// runner believes the older one.
	setAutoToolPolicy(db, udb, "alice", "a", "send_email", PolicyAllow)
	rec, _ = loadAgent(udb, "a")
	if len(rec.NoUnattendedTools) != 0 {
		t.Errorf("allow left the never-unattended mark in place: %+v", rec.NoUnattendedTools)
	}
}

// The record and the grant are two halves of one fact. The page reads the
// record; the unattended runner reads the list. A write that updated only one
// would show a state the deployment does not have.
func TestTheRecordAndTheGrantAgree(t *testing.T) {
	src := readFile(t, "tool_policy.go")
	body := src[strings.Index(src, "func setAutoToolPolicy("):]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "addAutoApproveTool(") || !strings.Contains(body, "removeAutoApproveTool(") {
		t.Error("setting a policy does not move the grant, so the runner and the page would disagree")
	}
	rm := src[strings.Index(src, "func removeAutoToolPolicy("):]
	if j := strings.Index(rm, "\nfunc "); j > 0 {
		rm = rm[:j]
	}
	if !strings.Contains(rm, "removeAutoApproveTool(") {
		t.Error("removing the record leaves the grant standing, so the tool still runs unattended")
	}
}

// A grant made before this table existed, or by approving a request (which
// writes the list directly), must still appear.
func TestAGrantWithNoRecordIsStillListed(t *testing.T) {
	src := readFile(t, "console_permissions.go")
	i := strings.Index(src, `for _, ag := range listAgents(udb, user) {`)
	if i < 0 {
		t.Fatal("the tool rows are gone")
	}
	zone := src[i:]
	if j := strings.Index(zone, "writeJSON(w, out)"); j > 0 {
		zone = zone[:j]
	}
	if !strings.Contains(zone, "AutoApproveTools") {
		t.Error("the rows no longer read the grant list, so a grant with no record vanishes")
	}
	if !strings.Contains(zone, "listAutoToolPolicies(") {
		t.Error("the rows no longer read the records, so a needs-approval row cannot exist")
	}
	if !strings.Contains(zone, "seenTool[") {
		t.Error("the union is not de-duplicated; a granted tool with a record would list twice")
	}
}

// The control's middle value has to persist for the OTHER rows too. If "ask"
// ever stopped normalising to a non-empty stored value, agent and contact rows
// would start vanishing for exactly the reason the tool rows just did.
func TestAskIsAStoredStateNotAnAbsence(t *testing.T) {
	src := readFileForTest(t, "../../core/authorizations.go")
	i := strings.Index(src, "func normPolicy(")
	if i < 0 {
		t.Fatal("normPolicy is gone")
	}
	if !strings.Contains(src[i:i+300], "PolicyAsk") {
		t.Error("normPolicy no longer returns PolicyAsk, so Needs approval would store nothing and drop the row")
	}
	j := strings.Index(src, "func listPolicies(")
	if !strings.Contains(src[j:j+900], `p != ""`) {
		t.Error("listPolicies no longer filters on a non-empty stored value; this test's premise has moved")
	}
}

// Approval inherits UP the ownership chain, because the parent vouched for the
// child. A restriction has to inherit DOWN, or it is advisory: an owner who
// marks a tool never-unattended and then watches the agent build a sub-agent
// that runs it has been told a policy applied that did not.
func TestARestrictionInheritsDownTheChain(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	seed := func(id, ownedBy string, marks ...string) {
		t.Helper()
		if _, err := saveAgent(udb, AgentRecord{
			ID: id, Owner: "alice", Name: id, OrchestratorPrompt: "work",
			OwnedBy: ownedBy, NoUnattendedTools: marks,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("parent", "", "send_email")
	seed("child", "parent")
	seed("grandchild", "child")

	for _, id := range []string{"parent", "child", "grandchild"} {
		if !autonomousNoUnattendedSet(udb, id)["send_email"] {
			t.Errorf("%s does not carry the mark its parent was given", id)
		}
	}
	// And the sub-agent bypass does not launder it. That bypass exists because
	// the parent chose the child's toolset, which is precisely why a limit the
	// parent carries has to come with it.
	marks := autonomousNoUnattendedSet(udb, "grandchild")
	if autonomousToolAllowed(true, nil, marks, "send_email", func(string) bool { return false }) {
		t.Error("a sub-agent ran a tool its ancestor was marked never to run unattended")
	}
	// A tool nobody marked is unaffected: this is an exception list, not an
	// allow-list wearing a different name.
	if !autonomousToolAllowed(false, nil, marks, "web_search", func(string) bool { return false }) {
		t.Error("an unmarked tool was refused; the default is still allow")
	}
}

// The complaint this came from: rows in the Permissions pane naming framework
// tools nobody had ever gated. They were grants, approved under the rule that
// refused every NeedsConfirm tool, left behind when the rule became "a tool
// attached to an agent is a tool it may use". The gate allows those with or
// without the entry, so the row claimed a permission that was not being
// withheld. A grant appears while it is load-bearing and not otherwise.
func TestAnInertGrantIsNotAPermission(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	if _, err := saveAgent(udb, AgentRecord{
		ID: "a", Owner: "alice", Name: "A", OrchestratorPrompt: "work",
		AutoApproveTools: []string{"recall", "send_email"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// recall dispatches through no credential, so nothing was ever going to ask
	// about it; send_email's credential is set to.
	if toolAlwaysConfirms(udb, "alice", nil, "recall") {
		t.Fatal("a framework tool with no credential is being treated as confirming")
	}
	rec, _ := loadAgent(udb, "a")
	if !autonomousToolAllowed(false, nil, nil, "recall", func(n string) bool { return toolAlwaysConfirms(udb, "alice", nil, n) }) {
		t.Error("recall is refused unattended, which would make its grant load-bearing after all")
	}
	if len(rec.AutoApproveTools) != 2 {
		t.Errorf("the stored grants were altered: %+v", rec.AutoApproveTools)
	}
	// The record is left alone on purpose. Pruning somebody's stored decisions
	// because they are currently inert is a data change made on a guess: if the
	// credential is ever set back to confirming, the grant means something
	// again and should be there.
}

// A withheld tool is the refusal with nothing waiting on it: no queue entry, no
// badge, and a run line nobody reads unless they already suspect something. So
// it has to leave a durable trace, or a schedule quietly does three quarters of
// its job forever. A queued one gets a notice too, so a 5am fire is news before
// 9am rather than a pane nobody opened.
func TestBothRefusalsReachTheOwner(t *testing.T) {
	root := pinRootDB(t)
	app := &OrchestrateApp{}

	app.notifyToolWithheld("alice", "nightly", "send_email")
	app.notifyToolQueued("alice", "nightly", "call_billing")

	list := notices.List(root, "alice")
	if len(list) != 2 {
		t.Fatalf("expected a notice for each refusal kind, got %d: %+v", len(list), list)
	}
	var stopped, blocked int
	for _, n := range list {
		switch n.Kind {
		case notices.KindStopped:
			stopped++
			if !strings.Contains(n.Body, "Nothing is waiting on you") {
				t.Errorf("a settled decision reads as though it needs action: %q", n.Body)
			}
		case notices.KindBlocked:
			blocked++
			if !strings.Contains(n.Body, "Permissions pane") {
				t.Errorf("a pending decision does not say where to go: %q", n.Body)
			}
		}
	}
	if stopped != 1 || blocked != 1 {
		t.Errorf("the two refusals were not kept apart: stopped=%d blocked=%d", stopped, blocked)
	}

	// Every fire after the first folds in. Twenty-four alerts a day is how a
	// notification surface gets turned off before it is ever useful.
	for i := 0; i < 5; i++ {
		app.notifyToolWithheld("alice", "nightly", "send_email")
	}
	if got := len(notices.List(root, "alice")); got != 2 {
		t.Errorf("repeats did not fold: %d rows", got)
	}
	if got := notices.Unread(root, "alice"); got != 2 {
		t.Errorf("badge counts occurrences rather than notices: %d", got)
	}
}

// notify_me is the agent explicitly reaching out, and it used to be a text and
// nothing else: a missing bridge returned an error and what the agent had to
// say was gone. It is now kept either way, which is the one thing
// Notifications exists for.
func TestWhatAnAgentSaysIsKept(t *testing.T) {
	root := pinRootDB(t)
	recordAgentNotice("alice", "nightly", "The export finished.\nIt found 3 new rows and skipped 1 malformed one.")

	list := notices.List(root, "alice")
	if len(list) != 1 {
		t.Fatalf("expected one notice, got %d", len(list))
	}
	n := list[0]
	if n.Kind != notices.KindReport {
		t.Errorf("an agent's own message implies a decision: %q", n.Kind)
	}
	// The title is the first line. A row four paragraphs long is one nobody
	// scans past.
	if n.Title != "The export finished." {
		t.Errorf("title is not the first line: %q", n.Title)
	}
	if !strings.Contains(n.Body, "3 new rows") {
		t.Errorf("the rest was dropped: %q", n.Body)
	}
	// A long single line is truncated for the row and kept whole in the body,
	// so nothing an agent said is lost to the layout.
	recordAgentNotice("alice", "nightly", strings.Repeat("x", 300))
	for _, x := range notices.List(root, "alice") {
		if len([]rune(x.Title)) > 130 {
			t.Errorf("an untruncated title of %d runes would break the row", len([]rune(x.Title)))
		}
		if strings.HasSuffix(x.Title, "…") && len(x.Body) < 300 {
			t.Error("the truncated text was not kept in full anywhere")
		}
	}
}

// One rule for anything an agent says out of band: it is kept, and ONE
// preference decides whether it also chases you. The prefix is what makes a
// forwarded copy usable, because a text on a phone has no other context.
func TestAForwardedNoticeSaysWhereItCameFrom(t *testing.T) {
	if got := noticePrefix("Nightly digest"); got != "[Nightly digest@"+ServiceName()+"]:" {
		t.Errorf("an agent's notice does not name the agent and the deployment: %q", got)
	}
	// No agent: still names the deployment, because somebody with two gohorts
	// needs to know which one is talking.
	got := noticePrefix("")
	if !strings.HasPrefix(got, "[") || !strings.Contains(got, ServiceName()) || strings.Contains(got, "@") {
		t.Errorf("a sourceless notice has the wrong shape: %q", got)
	}

	// The agent-aware send is the one the bridge already tags "[<name>] " on
	// the wire, so notify_me asks for the sourceless form. Both together read
	// "[Wren] [Wren@Gohort] ...", which is the duplication this pins against.
	src := readFile(t, "operator_tools.go")
	if !strings.Contains(src, `outbound := noticePrefix("") + " " + text`) {
		t.Error("notify_me is naming the agent again, which the bridge tag already does")
	}
}

// "Never chosen" and "deliberately nowhere" are different answers, and the
// setting has to tell them apart: the first is a default to supply, the second
// is a decision to respect. Getting this wrong either silently stops delivering
// messages somebody relied on, or hands them back a default they just declined.
func TestNeverChosenIsNotTheSameAsOff(t *testing.T) {
	savedReady, savedDB := NoticePhoneReady, AuthDB
	t.Cleanup(func() { NoticePhoneReady, AuthDB = savedReady, savedDB })
	db := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return db }

	store := func(where string) {
		t.Helper()
		db.Set(AuthTable, "user:alice", AuthUser{Username: "alice", NotifyForward: where})
	}

	// Unset sends nowhere, EVEN when a phone is available. Forwarding is
	// opt-in: a setting nobody has touched must not already be interrupting
	// somebody, or the first thing they learn about the feature is a message
	// they never asked for.
	NoticePhoneReady = func(string) bool { return true }
	store("")
	if got := ResolveNotifyForward(db, "alice"); got != "off" {
		t.Errorf("an unasked user is being forwarded to: %q", got)
	}
	// Explicitly off reads the same way, which is correct, and the two values
	// stay distinct in STORAGE because "has this person been asked" is a
	// separate question from "where does it go".
	store("off")
	if got := ResolveNotifyForward(db, "alice"); got != "off" {
		t.Errorf("a deliberate no was overridden by the default: %q", got)
	}
	store("email")
	if got := ResolveNotifyForward(db, "alice"); got != "email" {
		t.Errorf("an explicit choice was not kept: %q", got)
	}
	// And saving "nowhere" from the form stores the explicit value rather than
	// the blank, or the next read would hand the default straight back.
	AuthSetNotifyForward(db, "alice", "")
	if got := AuthGetNotifyForward(db, "alice"); got != "off" {
		t.Errorf("choosing nowhere stored %q, which reads as never having been asked", got)
	}
}

// A tool result that hands the model implementation detail gets it narrated
// back at the owner. notify_me used to report which transport it had used, and
// an agent duly answered "it's in your Notifications, forwarding to your phone
// is off, want me to turn that on?" — reporting on plumbing and offering to
// change a preference it does not own.
func TestNotifyMeDoesNotTellTheAgentWhereItWent(t *testing.T) {
	src := readFile(t, "operator_tools.go")
	start := strings.Index(src, `Name:        "notify_me"`)
	if start < 0 {
		t.Fatal("notify_me is gone")
	}
	body := src[start:]
	if end := strings.Index(body, "\n\t\t},\n"); end > 0 {
		body = body[:end]
	}
	// Every success path returns the same sentence, so there is nothing to
	// narrate and no branch that can drift into describing one.
	for _, leak := range []string{"forwarded to your phone", "was not texted", "Forwarding to your phone", "messaging bridge is not available"} {
		if strings.Contains(body, leak) {
			t.Errorf("notify_me's result still describes the transport: %q", leak)
		}
	}
	if !strings.Contains(body, "return notifySent, nil") {
		t.Error("the shared success result is gone; every path returning its own sentence is how this came back")
	}
	// The one exception earns its place: an undelivered attachment changes what
	// the agent should DO, rather than describing what happened.
	if !strings.Contains(body, "put anything essential from them into the message text") {
		t.Error("an undelivered attachment no longer tells the agent to say it in words")
	}
}
