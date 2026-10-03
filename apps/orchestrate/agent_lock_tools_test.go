package orchestrate

// A locked agent is closed to every agent unless the user says otherwise, not
// only to update_agent and delete_agent. The tools that attach something TO an
// agent (a tool, skill, machine or pipeline, a bulletin board) rewrite what it
// can do as surely as an update does, and they checked ownership but not the
// lock, so Builder could re-equip an agent the user had locked against exactly
// that.
//
// The lock is the user's say-so, so with the user watching a write to a locked
// agent ASKS them (allow this change, unlock, deny), and with nobody watching
// it is refused. The tests below with no AskInChat are the unattended case.

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func lockAgent(t *testing.T, db Database, a AgentRecord) AgentRecord {
	t.Helper()
	locked, err := setAgentLocked(db, a, true)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	return locked
}

// The gate refuses a locked agent even for its own owner when nobody is there
// to ask: the owner IS who is calling when Builder runs, and the lock exists
// to stop that. Ownership stays agentEditRefusal's, and a share is refused
// whatever anyone answers, since it is not this person's to waive.
func TestTheChangeGateRefusesALockedAgentUnattended(t *testing.T) {
	a := AgentRecord{ID: "a1", Name: "Ledger", Owner: "alice", Locked: true}
	msg := agentChangeGate(nil, nil, &a, "alice", "attach skill \"x\"")
	if msg == "" {
		t.Fatal("the owner's own tools could change a locked agent with nobody watching")
	}
	if !strings.Contains(msg, "locked") || !strings.Contains(msg, "Ledger") {
		t.Errorf("the refusal should name the agent and say it is locked: %s", msg)
	}
	a.Locked = false
	if msg := agentChangeGate(nil, nil, &a, "alice", "x"); msg != "" {
		t.Errorf("an unlocked own agent was refused: %s", msg)
	}
	if msg := agentEditRefusal(AgentRecord{ID: "a1", Owner: "alice", Locked: true}, "alice"); msg != "" {
		t.Errorf("agentEditRefusal is ownership only; the lock is the gate's: %s", msg)
	}
	asked := false
	yes := func(string, string, []string) string { asked = true; return lockAllowOnce }
	shared := AgentRecord{ID: "s1", Name: "Theirs", Owner: "bob", Locked: true}
	if msg := agentChangeGate(yes, nil, &shared, "alice", "x"); msg == "" || asked {
		t.Errorf("another user's agent must be refused without asking (asked=%v): %q", asked, msg)
	}
}

// Each answer on the card does what its button says.
func TestTheLockCardsThreeAnswers(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	base, _ := saveAgent(db, AgentRecord{Name: "Ledger", Owner: "u", OrchestratorPrompt: "p"})

	answer := func(pick string) func(string, string, []string) string {
		return func(prompt, detail string, yes []string) string {
			if !strings.Contains(prompt, "Ledger") || !strings.Contains(detail, "attach skill") {
				t.Errorf("the card should name the agent and the change: %q / %q", prompt, detail)
			}
			if len(yes) != 2 || yes[0] != lockAllowOnce || yes[1] != lockUnlock {
				t.Errorf("buttons = %v", yes)
			}
			return pick
		}
	}

	ag := lockAgent(t, db, base)
	if msg := agentLockGate(answer(lockAllowOnce), db, &ag, "attach skill \"x\""); msg != "" {
		t.Errorf("allow this change was refused: %s", msg)
	}
	if got, _ := loadAgent(db, ag.ID); !got.Locked {
		t.Error("allowing one change unlocked the agent")
	}

	if msg := agentLockGate(answer(lockUnlock), db, &ag, "attach skill \"x\""); msg != "" {
		t.Errorf("unlock was refused: %s", msg)
	}
	if got, _ := loadAgent(db, ag.ID); got.Locked || ag.Locked {
		t.Errorf("unlock left it locked: store=%v copy=%v", got.Locked, ag.Locked)
	}

	ag = lockAgent(t, db, ag)
	msg := agentLockGate(answer(""), db, &ag, "attach skill \"x\"")
	if !strings.Contains(msg, "declined") || !strings.Contains(msg, "Do not retry") {
		t.Errorf("a deny should say the user declined and not to retry: %q", msg)
	}
	if got, _ := loadAgent(db, ag.ID); !got.Locked {
		t.Error("a deny unlocked the agent")
	}
}

// With the user watching, add_tool asks and lands on an approval, and the
// agent stays locked after an allow-once.
func TestAddToolAsksAboutALockedAgent(t *testing.T) {
	for _, c := range []struct {
		pick       string
		lands      bool
		lockedTail bool
	}{{lockAllowOnce, true, true}, {lockUnlock, true, false}, {"", false, true}} {
		sess, _, helper := addToolTestSess(t)
		lockAgent(t, sess.DB, helper)
		sess.AskInChat = func(string, string, []string) string { return c.pick }
		_, err := addToolTool{}.RunWithSession(shellToolArgs(map[string]any{"agent": helper.Name}), sess)
		got, _ := loadAgent(sess.DB, helper.ID)
		if lands := agentHasTool(sess, got, "sentiment_analyzer"); lands != c.lands {
			t.Errorf("answer %q: tool landed=%v (err=%v), want %v", c.pick, lands, err, c.lands)
		}
		if got.Locked != c.lockedTail {
			t.Errorf("answer %q: locked afterwards=%v, want %v", c.pick, got.Locked, c.lockedTail)
		}
	}
}

// The card's buttons resolve through the same path the browser's click does,
// and a custom yes reads as a yes there. Before, only allow/always did, so an
// "Unlock agent" click would have parked the tool as a deny.
func TestAskInChatReturnsThePickedLabel(t *testing.T) {
	for _, c := range []struct{ value, want string }{{"yes1", lockUnlock}, {"yes0", lockAllowOnce}, {"deny", ""}} {
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
		got := turn.askInChat("🔒 Ledger is locked.", "Change: x", []string{lockAllowOnce, lockUnlock})
		<-done
		if got != c.want {
			t.Errorf("clicked %q: got %q, want %q", c.value, got, c.want)
		}
		if out := buf.String(); !strings.Contains(out, lockUnlock) || !strings.Contains(out, "\"kind\":\"confirm\"") {
			t.Errorf("the card did not offer the buttons:\n%s", out)
		}
	}
	// Nobody watching is a no, without a card.
	if got := (&chatTurn{user: "asker"}).askInChat("p", "d", []string{"ok"}); got != "" {
		t.Errorf("no viewer answered %q", got)
	}
}

// Both ways add_tool names its target. The focus path had no check at all.
func TestAddToolCannotEquipALockedAgent(t *testing.T) {
	for _, viaFocus := range []bool{false, true} {
		sess, _, helper := addToolTestSess(t)
		lockAgent(t, sess.DB, helper)
		var extra map[string]any
		if !viaFocus {
			extra = map[string]any{"agent": helper.Name}
		}
		_, err := addToolTool{}.RunWithSession(shellToolArgs(extra), sess)
		if err == nil || !strings.Contains(err.Error(), "locked") {
			t.Errorf("viaFocus=%v: add_tool on a locked agent: err=%v", viaFocus, err)
		}
		got, _ := loadAgent(sess.DB, helper.ID)
		if agentHasTool(sess, got, "sentiment_analyzer") {
			t.Errorf("viaFocus=%v: the tool landed on the locked agent anyway", viaFocus)
		}
	}
}

func TestABulletinCannotBeWiredToALockedAgent(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	wren, _ := saveAgent(db, AgentRecord{Name: "Wren", Owner: "u", OrchestratorPrompt: "p"})
	lockAgent(t, db, wren)
	tool := bulletinsToolDef(&chatTurn{udb: db, user: "u"})
	if _, err := tool.Handler(context.Background(), map[string]any{"action": "create", "name": "news"}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"follow", "allow_poster"} {
		_, err := tool.Handler(context.Background(), map[string]any{"action": action, "board": "news", "agent": "Wren"})
		if err == nil || !strings.Contains(err.Error(), "locked") {
			t.Errorf("%s on a locked agent: err=%v", action, err)
		}
	}
	if a, _ := loadAgent(db, wren.ID); containsString(a.Bulletins, "news") {
		t.Error("the locked agent follows the board anyway")
	}
	if b, _ := loadBulletin(db, "news"); b.canPost(wren.ID) {
		t.Error("the locked agent was made a poster anyway")
	}
}

func TestAPipelineCannotBeAttachedToALockedAgent(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ag, _ := saveAgent(db, AgentRecord{Name: "Wren", Owner: "u", OrchestratorPrompt: "p"})
	lockAgent(t, db, ag)
	out, err := upsertTurn(db).pipelineCreateOrUpdate(map[string]any{
		"name": "digest", "stages": validStages(), "attach_to_agents": []any{"Wren"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := loadAgent(db, ag.ID); len(got.AttachedPipelines) != 0 {
		t.Error("the pipeline was attached to the locked agent")
	}
	if !strings.Contains(out, "locked") {
		t.Errorf("the result should say why the attach did not happen:\n%s", out)
	}
}

// Retiring a pipeline swaps it out of every agent that holds it. A locked
// agent keeps it, so the old pipeline has to stay too: deleting it would leave
// the locked agent attached to nothing, which changes it more than the swap
// would have.
func TestRetiringAPipelineLeavesALockedAgentOnTheOldOne(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ct := upsertTurn(db)
	if _, err := ct.pipelineCreateOrUpdate(map[string]any{"name": "v1", "stages": validStages()}, false); err != nil {
		t.Fatal(err)
	}
	v1, ok := ct.findPipeline(map[string]any{"name": "v1"})
	if !ok {
		t.Fatal("v1 not stored")
	}
	open, _ := saveAgent(db, AgentRecord{Name: "Open", Owner: "u", OrchestratorPrompt: "p", AttachedPipelines: []string{v1.ID}})
	shut, _ := saveAgent(db, AgentRecord{Name: "Shut", Owner: "u", OrchestratorPrompt: "p", AttachedPipelines: []string{v1.ID}})
	lockAgent(t, db, shut)

	out, err := ct.pipelineCreateOrUpdate(map[string]any{"name": "v2", "stages": validStages(), "replaces": "v1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := loadAgent(db, shut.ID); len(got.AttachedPipelines) != 1 || got.AttachedPipelines[0] != v1.ID {
		t.Errorf("the locked agent's pipelines changed: %v", got.AttachedPipelines)
	}
	if got, _ := loadAgent(db, open.ID); len(got.AttachedPipelines) != 1 || got.AttachedPipelines[0] == v1.ID {
		t.Errorf("the unlocked agent was not swapped: %v", got.AttachedPipelines)
	}
	if _, ok := ct.findPipeline(map[string]any{"name": "v1"}); !ok {
		t.Error("v1 was deleted while a locked agent still uses it")
	}
	if !strings.Contains(out, "Did NOT retire") || !strings.Contains(out, "Shut") {
		t.Errorf("the result should say v1 was kept and for whom:\n%s", out)
	}
}
