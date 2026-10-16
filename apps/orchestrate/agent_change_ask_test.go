package orchestrate

// Changing an agent that already exists asks its owner, on every path that
// changes it: update_agent and delete_agent, and the tools that attach
// something TO an agent (a tool, skill, machine or pipeline, a bulletin
// board), which rewrite what it can do as surely as an update does. One
// Approve covers the agent for the rest of the session; with nobody watching
// the change is refused and waits in the owner's approval queue.
//
// This replaced the agent lock, which asked only about agents somebody had
// locked. Each test uses its own owner, because an Approve is remembered per
// owner and session for the life of the process.

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// denyEveryChange answers the owner's question with a no, and counts it.
func denyEveryChange(asked *int) func(string, string, []string) string {
	return func(string, string, []string) string { *asked++; return "" }
}

// The gate asks the owner about their own agent, naming it and the change,
// and refuses another user's without asking: a share is not theirs to waive.
func TestTheChangeGateAsksTheOwner(t *testing.T) {
	a := AgentRecord{ID: "a1", Name: "Ledger", Owner: "alice"}
	var gotKind, gotName, gotDetail string
	approve := func(kind, name, detail string, op ChangeOp) string {
		gotKind, gotName, gotDetail = kind, name, detail
		if op != ChangeEdit {
			t.Error("an existing agent was asked about as something new")
		}
		return ""
	}
	if msg := agentChangeGate(approve, &a, "alice", "attach skill \"x\""); msg != "" {
		t.Fatalf("an approved change was refused: %s", msg)
	}
	if gotKind != "agent" || gotName != "Ledger" || gotDetail != "Attach skill \"x\"." {
		t.Errorf("asked about %q %q %q", gotKind, gotName, gotDetail)
	}
	if msg := agentChangeGate(nil, &a, "alice", "x"); msg != "" {
		t.Errorf("with no approver nothing is asked: %s", msg)
	}
	asked := false
	shared := AgentRecord{ID: "s1", Name: "Theirs", Owner: "bob"}
	yes := func(string, string, string, ChangeOp) string { asked = true; return "" }
	if msg := agentChangeGate(yes, &shared, "alice", "x"); msg == "" || asked {
		t.Errorf("another user's agent must be refused without asking (asked=%v): %q", asked, msg)
	}
}

// Builder in a chat with the user may change the user's sub-agents; Builder
// dispatched by an agent may change only that agent's own.
func TestBuilderChangesASubAgentForTheUser(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	parent, _ := saveAgent(db, AgentRecord{Name: "OSINT", Owner: "u", OrchestratorPrompt: "p"})
	child, _ := saveAgent(db, AgentRecord{Name: "Social", Owner: "u", OwnedBy: parent.ID, OrchestratorPrompt: "p"})
	builder := ""
	for _, a := range coreSeedAgents() {
		if isBuilderAgent(a.ID) {
			builder = a.ID
		}
	}
	if builder == "" {
		t.Fatal("no Builder seed")
	}
	direct := &ToolSession{DB: db, AgentID: builder}
	if msg := agentFamilyRefusal(&child, direct); msg != "" {
		t.Errorf("Builder talking to the user could not change a sub-agent: %s", msg)
	}
	byParent := &ToolSession{DB: db, AgentID: builder, DispatchParentAgentID: parent.ID}
	if msg := agentFamilyRefusal(&child, byParent); msg != "" {
		t.Errorf("the parent's own Builder dispatch could not change its sub-agent: %s", msg)
	}
	byStranger := &ToolSession{DB: db, AgentID: builder, DispatchParentAgentID: "someone-else"}
	if msg := agentFamilyRefusal(&child, byStranger); !strings.Contains(msg, "belongs to another agent") {
		t.Errorf("another agent's Builder dispatch reached this sub-agent: %q", msg)
	}
	notBuilder := &ToolSession{DB: db, AgentID: "some-agent"}
	if msg := agentFamilyRefusal(&child, notBuilder); !strings.Contains(msg, "belongs to another agent") {
		t.Errorf("an agent that is not Builder changed another's sub-agent: %q", msg)
	}
}

// add_tool asks, both ways it names its target (the focus path had no check
// at all), and a no leaves the agent as it was.
func TestAddToolAsksBeforeEquippingAnAgent(t *testing.T) {
	for _, viaFocus := range []bool{false, true} {
		for _, approve := range []bool{false, true} {
			sess, _, helper := addToolTestSess(t)
			// Its own session per case: an Approve lasts the session.
			sess.ChatSessionID = "addtool-" + map[bool]string{true: "focus", false: "named"}[viaFocus] + map[bool]string{true: "-yes", false: "-no"}[approve] + time.Now().Format("150405.000000")
			saveAuthoringInProgress(sess.DB, sess.ChatSessionID, helper.ID)
			asked := 0
			sess.ApproveChange = changeApprover(sess, sess.Username, "builder", "Builder", func(string, string, []string) string {
				asked++
				if approve {
					return "Approve"
				}
				return ""
			})
			var extra map[string]any
			if !viaFocus {
				extra = map[string]any{"agent": helper.Name}
			}
			_, err := addToolTool{}.RunWithSession(shellToolArgs(extra), sess)
			got, _ := loadAgent(sess.DB, helper.ID)
			lands := agentHasTool(sess, got, "sentiment_analyzer")
			if asked != 1 {
				t.Errorf("viaFocus=%v approve=%v: asked %d times, want once", viaFocus, approve, asked)
			}
			if lands != approve {
				t.Errorf("viaFocus=%v approve=%v: tool landed=%v (err=%v)", viaFocus, approve, lands, err)
			}
			if !approve && (err == nil || !strings.Contains(err.Error(), "declined")) {
				t.Errorf("viaFocus=%v: a no should say the owner declined: %v", viaFocus, err)
			}
		}
	}
}

// The card's buttons resolve through the same path the browser's click does,
// and any yes label reads as a yes there.
func TestAskInChatReturnsThePickedLabel(t *testing.T) {
	for _, c := range []struct{ value, want string }{{"yes1", "Second"}, {"yes0", "First"}, {"deny", ""}} {
		buf := &syncBuf{}
		turn := &chatTurn{user: "asker", sse: &sseWriter{live: buf}}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; i < 500; i++ {
				answered := false
				toolConfirms.Range(func(k, v any) bool {
					if p := v.(*pendingToolConfirm); p.user == "asker" {
						toolConfirms.Delete(k)
						p.resolve(c.value)
						answered = true
						return false
					}
					return true
				})
				if answered {
					return
				}
				time.Sleep(2 * time.Millisecond)
			}
		}()
		got := turn.askInChat("Let Builder change the agent Ledger?", "Change: x", []string{"First", "Second"})
		<-done
		if got != c.want {
			t.Errorf("clicked %q: got %q, want %q", c.value, got, c.want)
		}
		if out := buf.String(); !strings.Contains(out, "Second") || !strings.Contains(out, "\"kind\":\"confirm\"") {
			t.Errorf("the card did not offer the buttons:\n%s", out)
		}
	}
	// Nobody watching is a no, without a card.
	if got := (&chatTurn{user: "asker"}).askInChat("p", "d", []string{"ok"}); got != "" {
		t.Errorf("no viewer answered %q", got)
	}
}

func TestABulletinIsWiredToAnAgentOnlyWithItsOwnersYes(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	wren, _ := saveAgent(db, AgentRecord{Name: "Wren", Owner: "bulletin-owner", OrchestratorPrompt: "p"})
	asked := 0
	turn := &chatTurn{udb: db, user: "bulletin-owner", agent: AgentRecord{ID: "builder"}, changeAsk: denyEveryChange(&asked)}
	tool := bulletinsToolDef(turn)
	if _, err := tool.Handler(context.Background(), map[string]any{"action": "create", "name": "news"}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"follow", "allow_poster"} {
		_, err := tool.Handler(context.Background(), map[string]any{"action": action, "board": "news", "agent": "Wren"})
		if err == nil || !strings.Contains(err.Error(), "declined") {
			t.Errorf("%s with the owner saying no: err=%v", action, err)
		}
	}
	if asked != 2 {
		t.Errorf("asked %d times, want once per refused change", asked)
	}
	if a, _ := loadAgent(db, wren.ID); containsString(a.Bulletins, "news") {
		t.Error("the agent follows the board anyway")
	}
	if b, _ := loadBulletin(db, "news"); b.canPost(wren.ID) {
		t.Error("the agent was made a poster anyway")
	}
}

func TestAPipelineIsAttachedOnlyWithItsOwnersYes(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ag, _ := saveAgent(db, AgentRecord{Name: "Wren", Owner: "pipe-owner", OrchestratorPrompt: "p"})
	asked := 0
	ct := &chatTurn{user: "pipe-owner", udb: db, agent: AgentRecord{ID: "ag"}, changeAsk: denyEveryChange(&asked)}
	out, err := ct.pipelineCreateOrUpdate(map[string]any{
		"name": "digest", "stages": validStages(), "attach_to_agents": []any{"Wren"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := loadAgent(db, ag.ID); len(got.AttachedPipelines) != 0 {
		t.Error("the pipeline was attached although the owner said no")
	}
	if asked != 1 || !strings.Contains(out, "declined") {
		t.Errorf("the result should say why the attach did not happen (asked=%d):\n%s", asked, out)
	}
}

// Retiring a pipeline swaps it out of every agent that holds it. An agent its
// owner keeps off the swap keeps the old pipeline, so the old one has to stay
// too: deleting it would leave that agent attached to nothing.
func TestRetiringAPipelineLeavesADeclinedAgentOnTheOldOne(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ct := &chatTurn{user: "retire-owner", udb: db, agent: AgentRecord{ID: "ag"}}
	ct.changeAsk = approveEveryChange
	if _, err := ct.pipelineCreateOrUpdate(map[string]any{"name": "v1", "stages": validStages()}, false); err != nil {
		t.Fatal(err)
	}
	v1, ok := ct.findPipeline(map[string]any{"name": "v1"})
	if !ok {
		t.Fatal("v1 not stored")
	}
	open, _ := saveAgent(db, AgentRecord{Name: "Open", Owner: "retire-owner", OrchestratorPrompt: "p", AttachedPipelines: []string{v1.ID}})
	shut, _ := saveAgent(db, AgentRecord{Name: "Shut", Owner: "retire-owner", OrchestratorPrompt: "p", AttachedPipelines: []string{v1.ID}})
	ct.changeAsk = func(prompt, detail string, yes []string) string {
		if strings.Contains(prompt, "Shut") {
			return ""
		}
		return approveEveryChange(prompt, detail, yes)
	}

	out, err := ct.pipelineCreateOrUpdate(map[string]any{"name": "v2", "stages": validStages(), "replaces": "v1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := loadAgent(db, shut.ID); len(got.AttachedPipelines) != 1 || got.AttachedPipelines[0] != v1.ID {
		t.Errorf("the declined agent's pipelines changed: %v", got.AttachedPipelines)
	}
	if got, _ := loadAgent(db, open.ID); len(got.AttachedPipelines) != 1 || got.AttachedPipelines[0] == v1.ID {
		t.Errorf("the approved agent was not swapped: %v", got.AttachedPipelines)
	}
	if _, ok := ct.findPipeline(map[string]any{"name": "v1"}); !ok {
		t.Error("v1 was deleted while a declined agent still uses it")
	}
	if !strings.Contains(out, "Did NOT retire") || !strings.Contains(out, "Shut") {
		t.Errorf("the result should say v1 was kept and for whom:\n%s", out)
	}
}

// With nobody watching, the change is refused and waits in the owner's queue.
func TestAnUnwatchedChangeToAnAgentWaitsForTheOwner(t *testing.T) {
	pinRootDB(t)
	db := &DBase{Store: kvlite.MemStore()}
	wren, _ := saveAgent(db, AgentRecord{Name: "Wren", Owner: "queue-owner", OrchestratorPrompt: "p"})
	tool := bulletinsToolDef(&chatTurn{udb: db, user: "queue-owner", agent: AgentRecord{ID: "builder"}})
	if _, err := tool.Handler(context.Background(), map[string]any{"action": "create", "name": "news"}); err != nil {
		t.Fatal(err)
	}
	_, err := tool.Handler(context.Background(), map[string]any{"action": "follow", "board": "news", "agent": "Wren"})
	if err == nil || !strings.Contains(err.Error(), "approval queue") {
		t.Errorf("an unwatched change should wait for the owner: %v", err)
	}
	if a, _ := loadAgent(db, wren.ID); containsString(a.Bulletins, "news") {
		t.Error("the agent follows the board anyway")
	}
	queued := false
	for _, a := range ListAuthorizations(RootDB, "queue-owner") {
		if a.Action == changeApprovalAction && strings.Contains(a.Handle, "Wren") {
			queued = true
		}
	}
	if !queued {
		t.Error("nothing waits in the owner's approval queue")
	}
}

// A run an agent starts for the user is the user's run: the sub-turn has no
// viewer of its own, so its question card is the calling turn's.
func TestASubTurnAsksThroughTheCallingTurn(t *testing.T) {
	parent := &chatTurn{user: "asker", sse: &sseWriter{live: &syncBuf{}}}
	sub := &chatTurn{user: "asker", askVia: parent.chatAsker()}
	if sub.chatAsker() == nil {
		t.Fatal("the sub-turn should ask through the calling turn")
	}
	if (&chatTurn{user: "asker"}).chatAsker() != nil {
		t.Fatal("a turn with no viewer and no caller has no card")
	}
	// The gate reads the forwarded card, and the parent's answer decides.
	db := &DBase{Store: kvlite.MemStore()}
	saveAgent(db, AgentRecord{Name: "Wren", Owner: "subturn-owner", OrchestratorPrompt: "p"})
	answered := ""
	sub = &chatTurn{user: "subturn-owner", udb: db, agent: AgentRecord{ID: "builder", Name: "Builder"},
		askVia: func(prompt, detail string, yes []string) string { answered = prompt; return yes[0] }}
	tool := bulletinsToolDef(sub)
	if _, err := tool.Handler(context.Background(), map[string]any{"action": "create", "name": "news"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"action": "allow_poster", "board": "news", "agent": "Wren"}); err != nil {
		t.Fatalf("the forwarded card approved the change, got: %v", err)
	}
	if !strings.Contains(answered, "Wren") {
		t.Fatalf("the calling turn's card should have been asked, got %q", answered)
	}
}

// What follows an approval card goes below it. A round that only called a
// tool left its bubble open above the card, and the next round's text was
// written into that bubble, so it showed up above the card it came after.
func TestTextAfterAnApprovalCardOpensBelowIt(t *testing.T) {
	buf := &syncBuf{}
	turn := &chatTurn{user: "carder", sse: &sseWriter{live: buf}}
	above := turn.ensureBubbleForTool() // the tool-only round's bubble
	go func() {
		for i := 0; i < 500; i++ {
			answered := false
			toolConfirms.Range(func(k, v any) bool {
				if p := v.(*pendingToolConfirm); p.user == "carder" {
					toolConfirms.Delete(k)
					p.resolve("yes0")
					answered = true
					return false
				}
				return true
			})
			if answered {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	turn.askInChat("Run it?", "", []string{"Allow"})
	below := turn.ensureBubbleForTool()
	if below == above {
		t.Fatalf("what came after the card went back into the bubble above it (%s)", above)
	}
	out := buf.String()
	card, next := strings.Index(out, `"kind":"confirm"`), strings.Index(out, `"id":"`+below+`"`)
	if card < 0 || next < card {
		t.Errorf("the new bubble is not after the card:\n%s", out)
	}
}
