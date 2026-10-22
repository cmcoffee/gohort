package servitor

// An agent's investigation that needs the owner: a command beyond its grants
// parks on a card the owner reaches by a link, the agent's call comes back
// with that link straight away, and the answer is delivered to the
// conversation afterwards. A read-only run nobody can answer declines without
// raising a card, which it used to raise and decline in the same moment.

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

var gateHit = []risk_hit{{cat: RiskSysControl, reason: "restarts a service"}}

const gateCmd = "systemctl restart web"

// liveRun registers sid as a live session, as a run does, and returns a
// reader of its events.
func liveRun(t *testing.T, sid string) func() []probeEvent {
	t.Helper()
	probeSessions.Register(sid, "test", func() {}).SetOwner("alice")
	return func() []probeEvent {
		evs, _ := probeSessions.SnapshotEvents(sid)
		return evs
	}
}

func kinds(evs []probeEvent, kind string) []probeEvent {
	var out []probeEvent
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func TestAReadOnlyRunDeclinesWithoutACard(t *testing.T) {
	events := liveRun(t, "guide-investigate-t1")
	register(t, "guide-investigate-t1", "alice", false)
	pr := &probeRun{ctx: context.Background(), id: "guide-investigate-t1", appliance: Appliance{ID: "box-1", Name: "Box"}}
	err := pr.gateHits(gateCmd, gateHit)
	if err == nil || !strings.Contains(err.Error(), "read-only") || strings.Contains(err.Error(), "denied by user") {
		t.Fatalf("a read-only run should decline and say why, not report a denial nobody gave: %v", err)
	}
	if len(kinds(events(), "confirm")) != 0 {
		t.Error("a card was raised that nobody can answer")
	}
	if st := kinds(events(), "status"); len(st) == 0 || !strings.Contains(st[len(st)-1].Text, "Declined") {
		t.Errorf("the session should say what was declined: %+v", st)
	}
}

func TestAnAgentWithNoLinkIsRefused(t *testing.T) {
	events := liveRun(t, "agent-t2")
	register(t, "agent-t2", "alice", true)
	for name, ctx := range map[string]context.Context{
		"no park":      WithActingAgent(context.Background(), "agent-1"),
		"park says no": withApprovalByLink(WithActingAgent(context.Background(), "agent-1"), func(string) bool { return false }),
	} {
		pr := &probeRun{ctx: ctx, id: "agent-t2", appliance: Appliance{ID: "box-1"}}
		if err := pr.gateHits(gateCmd, gateHit); err == nil || !strings.Contains(err.Error(), "standing permission") {
			t.Errorf("%s: the agent should be refused with what would permit it: %v", name, err)
		}
	}
	if len(kinds(events(), "confirm")) != 0 {
		t.Error("a refused command raised a card")
	}
}

// A command that parks waits on an answerable card, and the card is settled
// under the same id when it is answered, so a page arriving later draws it
// answered.
func TestAParkedCommandWaitsOnTheOwnersCard(t *testing.T) {
	const sid = "agent-t3"
	events := liveRun(t, sid)
	ch := make(chan bool, 1)
	confirmChans.Store(sid, pendingConfirm{ch: ch, owner: "alice", interactive: true})
	t.Cleanup(func() { confirmChans.Delete(sid) })
	var parked string
	ctx := withApprovalByLink(WithActingAgent(context.Background(), "agent-1"), func(cmd string) bool { parked = cmd; return true })
	pr := &probeRun{ctx: ctx, id: sid, confirm: ch, appliance: Appliance{ID: "box-1"}}
	got := make(chan error, 1)
	go func() { got <- pr.gateHits(gateCmd, gateHit) }()

	var card probeEvent
	for i := 0; i < 200; i++ {
		if c := kinds(events(), "confirm"); len(c) > 0 {
			card = c[0]
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if card.Card == "" || parked != gateCmd {
		t.Fatalf("the command should park on a card, told to the investigation: %+v %q", card, parked)
	}
	raised := chatEventFor(sid, card)
	if delivered := deliverConfirm("alice", raised["id"].(string), true); delivered != sid {
		t.Fatalf("the owner's Allow should reach the parked command, went to %q", delivered)
	}
	if err := <-got; err != nil {
		t.Fatalf("an allowed command should run: %v", err)
	}
	res := kinds(events(), "confirm_resolved")
	if len(res) != 1 || res[0].Reason != "allow" {
		t.Fatalf("the card should be settled as allowed: %+v", res)
	}
	if settled := chatEventFor(sid, res[0]); settled["id"] != raised["id"] || settled["value"] != "allow" {
		t.Errorf("the settle should name the card it settles: raised %v, settled %v", raised["id"], settled)
	}
}

func TestTheLinkNeedsSomeoneToFollowIt(t *testing.T) {
	prevRun, prevLink := TaskRunnerFunc, ConversationLinkFunc
	t.Cleanup(func() { TaskRunnerFunc, ConversationLinkFunc = prevRun, prevLink })
	TaskRunnerFunc = func(*ToolSession, string, func(context.Context) (TaskProduct, error)) (TaskRun, error) {
		return TaskRun{}, nil
	}
	ConversationLinkFunc = func(*ToolSession) ConversationLink { return ConversationLink{Path: "/orchestrate/?agent=a&session=s"} }
	watched := func() *ToolSession {
		return &ToolSession{Username: "alice", ChatSessionID: "s", PendingApprovalPrompt: func(Authorization) {}}
	}
	if !approvalByLinkPossible(watched()) {
		t.Fatal("a watched web turn should take a link")
	}
	for name, s := range map[string]*ToolSession{
		"no session":      nil,
		"nobody watching": {Username: "alice", ChatSessionID: "s"},
		"a text thread":   func() *ToolSession { x := watched(); x.ChannelChatID = "chat-1"; return x }(),
		"in background":   func() *ToolSession { x := watched(); x.Detached = true; return x }(),
		"no conversation": func() *ToolSession { x := watched(); x.ChatSessionID = ""; return x }(),
	} {
		if approvalByLinkPossible(s) {
			t.Errorf("%s: should not take a link", name)
		}
	}
	ConversationLinkFunc = func(*ToolSession) ConversationLink { return ConversationLink{} }
	if approvalByLinkPossible(watched()) {
		t.Error("a turn with no conversation to link back to should not take a link")
	}
}

// The whole of it, with the session played: the agent's call comes back with
// the link while the command waits, the owner's answer lets the run finish,
// and its answer is what the background task delivers.
func TestAnAgentInvestigationParksAndReportsBack(t *testing.T) {
	app, _ := sourcesApp(t)
	prevRun, prevLink, prevInv := TaskRunnerFunc, ConversationLinkFunc, runInvestigation
	t.Cleanup(func() { TaskRunnerFunc, ConversationLinkFunc, runInvestigation = prevRun, prevLink, prevInv })
	var task func(context.Context) (TaskProduct, error)
	TaskRunnerFunc = func(_ *ToolSession, _ string, fn func(context.Context) (TaskProduct, error)) (TaskRun, error) {
		task = fn
		return TaskRun{ID: "task-1"}, nil
	}
	ConversationLinkFunc = func(*ToolSession) ConversationLink {
		return ConversationLink{Path: "/orchestrate/?agent=a1&session=c1", Title: "Disk trouble", Agent: "Ops"}
	}
	var sidSeen string
	runInvestigation = func(T *Servitor, ctx context.Context, sid, user, ownerUser string, a Appliance, confirm chan bool, msgs []Message, udb Database) {
		sidSeen = sid
		if ActingAgent(ctx) != "agent-1" {
			emit(sid, probeEvent{Kind: "error", Text: "not acting for the agent"})
			return
		}
		if park := approvalByLink(ctx); park == nil || !park(gateCmd) {
			emit(sid, probeEvent{Kind: "error", Text: "could not park"})
			return
		}
		if !<-confirm {
			emit(sid, probeEvent{Kind: "reply", Text: "It was not allowed."})
			return
		}
		emit(sid, probeEvent{Kind: "reply", Text: "The web service is back."})
	}
	sess := &ToolSession{Username: "alice", ChatSessionID: "c1", PendingApprovalPrompt: func(Authorization) {}}
	out, err := app.InvestigateForAgent(context.Background(), sess, "alice", "lab-box", "why is the site down?", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	body, marked := TakeFrameworkResultMark(out)
	if !marked || !strings.Contains(body, "/servitor/?reconnect="+sidSeen) || !strings.Contains(body, gateCmd) || !strings.Contains(body, "Nothing has run") {
		t.Fatalf("the agent should be handed the link and the command, as the framework's word: %q", out)
	}
	if strings.Contains(body, "—") {
		t.Error("an em-dash in what the agent is told")
	}
	if task == nil {
		t.Fatal("the rest of the run should be handed to the background")
	}
	if origin := kinds(func() []probeEvent { e, _ := probeSessions.SnapshotEvents(sidSeen); return e }(), "origin"); len(origin) != 1 || origin[0].Origin.URL != "/orchestrate/?agent=a1&session=c1" {
		t.Fatalf("the session should open on who started it, with the way back: %+v", origin)
	}
	v, _ := confirmChans.Load(sidSeen)
	v.(pendingConfirm).ch <- true
	prod, err := task(context.Background())
	if err != nil || prod.Text != "The web service is back." {
		t.Fatalf("the background task should deliver the investigation's answer: %q %v", prod.Text, err)
	}
}

// Nobody to hand a link to: the investigation runs as it always did, read
// only and acting for the agent, and the agent waits for its answer.
func TestWithNobodyWatchingTheInvestigationRunsInline(t *testing.T) {
	app, _ := sourcesApp(t)
	prevInv := runInvestigation
	t.Cleanup(func() { runInvestigation = prevInv })
	ran := false
	runInvestigation = func(*Servitor, context.Context, string, string, string, Appliance, chan bool, []Message, Database) {
		ran = true
	}
	prevRun, prevLink := TaskRunnerFunc, ConversationLinkFunc
	t.Cleanup(func() { TaskRunnerFunc, ConversationLinkFunc = prevRun, prevLink })
	TaskRunnerFunc = func(*ToolSession, string, func(context.Context) (TaskProduct, error)) (TaskRun, error) {
		return TaskRun{}, nil
	}
	ConversationLinkFunc = func(*ToolSession) ConversationLink {
		return ConversationLink{Path: "/orchestrate/?agent=a1&session=c1"}
	}
	for name, sess := range map[string]*ToolSession{
		"a schedule, no live prompt":          {Username: "alice", ChatSessionID: "c1"},
		"someone else in a shared agent chat": {Username: "bob", ChatSessionID: "c1", PendingApprovalPrompt: func(Authorization) {}},
	} {
		ran = false
		// InvestigateSync tries the system for real here, and the fixture's
		// has no host, so it ends at once; what matters is which path ran.
		app.InvestigateForAgent(context.Background(), sess, "alice", "lab-box", "status?", "agent-1")
		if ran {
			t.Errorf("%s: should not start a session that can park", name)
		}
	}
}

func TestTheStoredAnswerSaysWhoAsked(t *testing.T) {
	_, udb := sourcesApp(t)
	s := saveSession(udb, "lab-box", chatSession{Name: "why?", Messages: []sessionMsg{{Role: "user", Content: "why?"}, {Role: "assistant", Content: "Because."}}})
	noteOrigin(udb, "lab-box", s.ID, investigationOrigin{Agent: "Ops", Title: "Disk trouble", URL: "/orchestrate/?agent=a1&session=c1"})
	got, _ := loadSession(udb, "lab-box", s.ID)
	if c := got.Messages[1].Content; !strings.Contains(c, "Because.") || !strings.Contains(c, "Asked by Ops in [Disk trouble](/orchestrate/?agent=a1&session=c1)") {
		t.Errorf("the stored answer should link back to the conversation: %q", c)
	}
}
