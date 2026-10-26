package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/messaging"
	"github.com/cmcoffee/gohort/core/ui"
)

func familyFixture(t *testing.T) (*OrchestrateApp, Database) {
	t.Helper()
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	prevHook := messaging.ChannelAgentFor
	messaging.ChannelAgentFor = channelAgentFor
	prevSched := ScheduleAgentFor
	ScheduleAgentFor = scheduleAgentFor
	t.Cleanup(func() { messaging.ChannelAgentFor, ScheduleAgentFor = prevHook, prevSched })
	for _, a := range []AgentRecord{
		{ID: "a-writer", Name: "Writer", Owner: "alice", OrchestratorPrompt: "p"},
		{ID: "a-critic", Name: "Critic", Owner: "alice", OrchestratorPrompt: "p", OwnedBy: "a-writer"},
		{ID: "a-nit", Name: "Nitpicker", Owner: "alice", OrchestratorPrompt: "p", OwnedBy: "a-critic"},
	} {
		if _, err := saveAgent(udb, a); err != nil {
			t.Fatal(err)
		}
	}
	return app, udb
}

// A channel is its top-level agent's: saved against a sub-agent (two levels
// down, even), it lands on the parent; one bound before that moves at start-up.
func TestAChannelBelongsToTheTopLevelAgent(t *testing.T) {
	familyFixture(t)
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "alice", Name: "Team", AgentID: "a-nit"})
	if ch, _ := GetChannel(RootDB, "alice", "c1"); ch.AgentID != "a-writer" {
		t.Errorf("bound to %q, want the top-level parent", ch.AgentID)
	}

	// One bound before the rule: written past the hook, as an old binary did.
	messaging.ChannelAgentFor = nil
	SaveChannel(RootDB, Channel{ID: "c2", Owner: "alice", Name: "Old", AgentID: "a-critic"})
	messaging.ChannelAgentFor = channelAgentFor
	moveSubAgentChannels()
	if ch, _ := GetChannel(RootDB, "alice", "c2"); ch.AgentID != "a-writer" {
		t.Errorf("an existing sub-agent channel was not moved: %q", ch.AgentID)
	}
}

// Binding one from a sub-agent's page says where it goes instead.
func TestBindingAChannelToASubAgentNamesTheParent(t *testing.T) {
	app, _ := familyFixture(t)
	r := httptest.NewRequest(http.MethodPost, "/api/channels?agent_id=a-critic", strings.NewReader(`{"name":"Team"}`))
	w := httptest.NewRecorder()
	app.handleChannels(w, asUser(r, "alice"))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Writer") {
		t.Errorf("binding to a sub-agent: %d %s", w.Code, w.Body.String())
	}
}

// The Scheduler on any member of a family lists the whole family's
// schedules: the parent's shows what its sub-agents run, and a sub-agent's
// shows the family it belongs to.
func TestTheSchedulerListsTheWholeFamily(t *testing.T) {
	app, _ := familyFixture(t)
	for _, sa := range []StandingAgent{
		{Owner: "alice", Name: "Morning digest", AgentID: "a-nit", Mission: "m", IntervalSeconds: 3600},
		{Owner: "alice", Name: "Weekly review", AgentID: "a-writer", Mission: "m", IntervalSeconds: 3600},
		{Owner: "alice", Name: "Someone else", AgentID: "a-unrelated", Mission: "m", IntervalSeconds: 3600},
	} {
		SaveStandingAgent(RootDB, sa)
	}
	for _, agent := range []string{"a-writer", "a-critic"} {
		r := httptest.NewRequest(http.MethodGet, "/api/console/scheduler?agent="+agent, nil)
		w := httptest.NewRecorder()
		routed(app)(w, asUser(r, "alice"))
		body := w.Body.String()
		if !strings.Contains(body, "Morning digest") || !strings.Contains(body, "Weekly review") {
			t.Errorf("%s's Scheduler does not list the family: %s", agent, body)
		}
		if strings.Contains(body, "Someone else") {
			t.Errorf("%s's Scheduler lists an agent outside the family", agent)
		}
		if strings.Count(body, `"name":"Morning digest"`) != 1 {
			t.Errorf("%s's Scheduler lists a schedule twice", agent)
		}
		// A schedule made for a sub-agent wakes the parent and says whose
		// work it is; the parent's own does not.
		if !strings.Contains(body, `"runs":"for sub-agent Nitpicker"`) {
			t.Errorf("%s's Scheduler does not say Morning digest is for the sub-agent: %s", agent, body)
		}
		if strings.Count(body, `"runs":`) != 1 {
			t.Errorf("%s's Scheduler marks a row the parent runs itself: %s", agent, body)
		}
	}
}

// The fleet Scheduler is every agent's schedules in one list, so each row says
// whose it is. An agent's own Scheduler does not repeat its name on every row.
func TestTheFleetSchedulerSaysWhoseEachScheduleIs(t *testing.T) {
	app, udb := familyFixture(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a-scout", Name: "Scout", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	for _, sa := range []StandingAgent{
		{Owner: "alice", Name: "Weekly review", AgentID: "a-writer", Mission: "m", IntervalSeconds: 3600},
		{Owner: "alice", Name: "Hourly sweep", AgentID: "a-scout", Mission: "m", IntervalSeconds: 3600},
	} {
		SaveStandingAgent(RootDB, sa)
	}
	SaveEventMonitor(RootDB, EventMonitor{Owner: "alice", Name: "Site up", Kind: EventKindHTTP, URL: "http://example.invalid", WakeAgent: "a-scout", IntervalSeconds: 300})
	get := func(q string) string {
		r := httptest.NewRequest(http.MethodGet, "/api/console/scheduler"+q, nil)
		w := httptest.NewRecorder()
		routed(app)(w, asUser(r, "alice"))
		return w.Body.String()
	}
	fleet := get("")
	for _, want := range []string{`"name":"(Writer) Weekly review"`, `"name":"(Scout) Hourly sweep"`, `"name":"(Scout) Site up"`} {
		if !strings.Contains(fleet, want) {
			t.Errorf("the fleet Scheduler should have %s:\n%s", want, fleet)
		}
	}
	own := get("?agent=a-scout")
	if !strings.Contains(own, `"name":"Hourly sweep"`) || strings.Contains(own, "(Scout)") {
		t.Errorf("an agent's own Scheduler does not name it on each row:\n%s", own)
	}
}

// Moving an agent under a parent hands its schedules and channels to the
// parent at once, the schedules saying whose work they are; moving it back out
// leaves them with the parent and says so.
func TestMovingAnAgentSaysWhereItsSchedulesAndChannelsGo(t *testing.T) {
	app, udb := familyFixture(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a-scout", Name: "Scout", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	SaveStandingAgent(RootDB, StandingAgent{Owner: "alice", Name: "Hourly sweep", AgentID: "a-scout", Mission: "m", IntervalSeconds: 3600})
	SaveChannel(RootDB, Channel{ID: "c-scout", Owner: "alice", Name: "Pager", AgentID: "a-scout"})

	w := moveAgentReq(t, app, udb, "a-scout", `{"owned_by":"a-writer"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("move under: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "now wake Writer") || !strings.Contains(w.Body.String(), "answered by Writer") {
		t.Errorf("the move did not say where its schedule and channel went: %s", w.Body.String())
	}
	if sa, _ := GetStandingAgent(RootDB, "alice", "Hourly sweep"); sa.AgentID != "a-writer" || subAgentHandoffOf(sa.Mission) != "Scout" {
		t.Errorf("the schedule wakes %q with %q; want the parent, handing it to Scout", sa.AgentID, sa.Mission)
	}
	if ch, _ := GetChannel(RootDB, "alice", "c-scout"); ch.AgentID != "a-writer" {
		t.Errorf("the channel is bound to %q, want the new parent at once", ch.AgentID)
	}

	w = moveAgentReq(t, app, udb, "a-scout", `{"owned_by":""}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "1 schedule on its old parent still hand work to it") {
		t.Errorf("moving out did not say its schedule stays with the parent: %d %s", w.Code, w.Body.String())
	}
}

// A scheduled run by a sub-agent (a recurring task's fire, a monitor's wake)
// reports to its top-level parent's home thread as well, saying which
// sub-agent ran it; a top-level agent's report goes nowhere extra.
func TestASubAgentsScheduledReportReachesTheParent(t *testing.T) {
	_, udb := familyFixture(t)
	reportToParent(udb, "a-nit", ChatMessage{Role: "assistant", Content: "Three typos fixed.", ReportFrom: "Nightly pass", ReportKind: cortexKindScheduled})
	sess, ok := loadChatSession(udb, "a-writer", cortexSessionID("a-writer"))
	if !ok || len(sess.Messages) != 1 {
		t.Fatalf("the parent's home thread did not get the report: %+v", sess.Messages)
	}
	m := sess.Messages[0]
	if m.Content != "Three typos fixed." || m.ReportFrom != "Nightly pass (by Nitpicker)" || m.ReportKind != cortexKindScheduled {
		t.Errorf("report card = %+v", m)
	}
	reportToParent(udb, "a-writer", ChatMessage{Role: "assistant", Content: "own report"})
	if sess, _ := loadChatSession(udb, "a-writer", cortexSessionID("a-writer")); len(sess.Messages) != 1 {
		t.Error("a top-level agent's own report was copied to itself")
	}
}

// A sub-agent's page has no Scheduler or Channels of its own: the page maps
// each sub-agent to its parent (hide_flag on the Scheduler, the Channels
// section's channels_hide_flag), refreshed with the agent list. A sub-agent
// whose parent is gone is top-level again and keeps both.
func TestASubAgentsPageHidesSchedulerAndChannels(t *testing.T) {
	app, udb := familyFixture(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a-orphan", Name: "Orphan", Owner: "alice", OrchestratorPrompt: "p", OwnedBy: "a-deleted"}); err != nil {
		t.Fatal(err)
	}
	got := subAgentParentsFor(listAgents(udb, "alice"))
	if got["a-critic"] != "a-writer" || got["a-nit"] != "a-critic" {
		t.Errorf("sub-agents not mapped to their parents: %v", got)
	}
	for _, id := range []string{"a-writer", "a-orphan"} {
		if _, hidden := got[id]; hidden {
			t.Errorf("%s is top-level but would lose its Scheduler and Channels", id)
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/api/agent-options", nil)
	w := httptest.NewRecorder()
	app.handleAgentPickerOptions(w, asUser(r, "alice"))
	if !strings.Contains(w.Body.String(), `"sub_agent_of":{`) || !strings.Contains(w.Body.String(), `"a-critic":"a-writer"`) {
		t.Errorf("the agent list refresh does not carry the map: %s", w.Body.String())
	}

	r = httptest.NewRequest(http.MethodGet, "/", nil)
	w = httptest.NewRecorder()
	app.handleChatPage(w, asUser(r, "alice"))
	page := w.Body.String()
	for _, want := range []string{`window.ORCH_SUB_AGENT_OF = {`, `"channels_hide_flag":"ORCH_SUB_AGENT_OF"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the chat page is missing %s", want)
		}
	}
	// Only the agent's own Scheduler goes: the fleet-wide one in the Fleet
	// menu lists everybody's and stays.
	var hidden []string
	for _, item := range navItemsOf(t, page) {
		if item.HideFlag == "ORCH_SUB_AGENT_OF" {
			hidden = append(hidden, fmt.Sprintf("%s pinned=%v menu=%q", item.Label, item.Pinned, item.Menu))
		}
	}
	if len(hidden) != 1 || hidden[0] != `Scheduler pinned=true menu=""` {
		t.Errorf("hidden for sub-agents: %v, want only the pinned Scheduler", hidden)
	}
	// Both Scheduler views title a card with the schedule's name, not
	// whichever field sorts first.
	for _, item := range navItemsOf(t, page) {
		if item.Label == "Scheduler" && item.TitleField != "name" {
			t.Errorf("Scheduler (menu=%q) titles its cards with %q", item.Menu, item.TitleField)
		}
	}
}

// navItemsOf reads the orchestrator nav out of the rendered chat page's
// panel config.
func navItemsOf(t *testing.T, page string) []ui.OrchestratorNavItem {
	t.Helper()
	i := strings.Index(page, `"orchestrator_nav":[`)
	if i < 0 {
		t.Fatal("no orchestrator_nav on the chat page")
	}
	var items []ui.OrchestratorNavItem
	dec := json.NewDecoder(strings.NewReader(page[i+len(`"orchestrator_nav":`):]))
	if err := dec.Decode(&items); err != nil {
		t.Fatalf("orchestrator_nav: %v", err)
	}
	return items
}

// A sub-agent has no Cortex of its own: whatever its stored record says, it
// reads none, and every note that would land in one is filed in its
// top-level parent's, saying which sub-agent it is about.
func TestASubAgentsCortexNotesGoToTheParent(t *testing.T) {
	_, udb := familyFixture(t)
	stored, _ := loadAgent(udb, "a-critic")
	stored.Cortex = true
	if _, err := saveAgent(udb, stored); err != nil {
		t.Fatal(err)
	}
	if a, _ := loadAgent(udb, "a-critic"); a.Cortex {
		t.Error("a sub-agent still reads a Cortex of its own")
	}

	appendCortexObs(udb, "a-nit", "Guardrail", cortexKindOverflow, "Reply held back.")
	appendCortexObs(udb, "a-nit", "A machine", cortexKindRequest, "Asked in step 2.")
	appendCortexObs(udb, "a-writer", "Session", cortexKindSession, "Own note.")
	parent, _ := loadChatSession(udb, "a-writer", cortexSessionID("a-writer"))
	var froms []string
	for _, m := range parent.Messages {
		froms = append(froms, m.ReportFrom)
	}
	want := []string{"Guardrail (by Nitpicker)", "A machine (to Nitpicker)", "Session"}
	if strings.Join(froms, "|") != strings.Join(want, "|") {
		t.Errorf("parent's Cortex notes = %q, want %q", froms, want)
	}
	if own, _ := loadChatSession(udb, "a-nit", cortexSessionID("a-nit")); len(own.Messages) != 0 {
		t.Errorf("the sub-agent kept notes of its own: %+v", own.Messages)
	}

	// A request inside the family is the parent's own conversation; one
	// from outside is a note.
	if !familyRequest(udb, "a-writer", "a-critic") || !familyRequest(udb, "a-critic", "a-nit") {
		t.Error("a request inside the family would be noted on the parent")
	}
	if familyRequest(udb, "a-outsider", "a-critic") || familyRequest(udb, "a-critic", "a-writer") {
		t.Error("a request from outside, or one made of the parent, is not noted")
	}
}

// The sub-agent's page has no Cortex row, and its working thread (its
// Cortex id, kept for its recurring tasks and monitor wakes) is not in its
// session list either. A top-level agent, and a sub-agent whose parent was
// deleted, keep their row and their thread.
func TestASubAgentsPageHasNoCortexRow(t *testing.T) {
	_, udb := familyFixture(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a-orphan", Name: "Orphan", Owner: "alice", OrchestratorPrompt: "p", OwnedBy: "a-deleted"}); err != nil {
		t.Fatal(err)
	}
	agents := listAgents(udb, "alice")
	_, cortex, _ := agentPickerOptions(agents)
	rec := recordAgentsFor(agents, cortex)
	for _, id := range []string{"a-critic", "a-nit"} {
		if _, ok := rec[id]; ok {
			t.Errorf("%s has a Cortex row", id)
		}
		if _, ok := cortex[id]; ok {
			t.Errorf("%s has a Cortex home", id)
		}
	}
	for _, id := range []string{"a-writer", "a-orphan"} {
		if rec[id] != cortexSessionID(id) {
			t.Errorf("%s lost its Cortex row: %v", id, rec)
		}
	}

	for _, id := range []string{"a-nit", "a-writer"} {
		for _, sid := range []string{cortexSessionID(id), "s-" + id} {
			if err := appendToStoredSession(udb, id, sid, ChatSession{ID: sid, AgentID: id}, ChatMessage{Role: "assistant", Content: "x"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	listed := func(id string) map[string]bool {
		out := map[string]bool{}
		for _, s := range listChatSessions(udb, id) {
			out[s.ID] = true
		}
		return out
	}
	if l := listed("a-nit"); l[cortexSessionID("a-nit")] || !l["s-a-nit"] {
		t.Errorf("sub-agent's list = %v, want its sessions without its working thread", l)
	}
	if l := listed("a-writer"); !l[cortexSessionID("a-writer")] || !l["s-a-writer"] {
		t.Errorf("parent's list = %v, want its Cortex and its sessions", l)
	}
}

// A scheduled agent made for a sub-agent wakes the parent, and its report
// lands once in the parent's Cortex, none in the sub-agent's thread.
func TestASubAgentsScheduledRunLandsOnceOnTheParent(t *testing.T) {
	app, udb := familyFixture(t)
	registerStandingRunner(app)
	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		return StandingRunResult{Status: RunOK, Summary: "s", Raw: "Two typos fixed."}
	})
	t.Cleanup(func() { RegisterStandingRunner(nil); RegisterStandingReporter(nil) })
	SaveStandingAgent(RootDB, StandingAgent{Owner: "alice", Name: "Nightly pass", AgentID: "a-nit", Mission: "m", IntervalSeconds: 3600})
	if _, err := RunStandingAgentNow(context.Background(), RootDB, "alice", "Nightly pass"); err != nil {
		t.Fatal(err)
	}
	parent, _ := loadChatSession(udb, "a-writer", cortexSessionID("a-writer"))
	if len(parent.Messages) != 1 || !strings.Contains(parent.Messages[0].Content, "Two typos fixed.") || parent.Messages[0].ReportFrom != "Nightly pass" {
		t.Errorf("parent's Cortex = %+v, want the one report", parent.Messages)
	}
	if own, _ := loadChatSession(udb, "a-nit", cortexSessionID("a-nit")); len(own.Messages) != 0 {
		t.Errorf("the sub-agent's thread got %+v", own.Messages)
	}
}

// A right granted to a parent is its sub-agents' too, at any depth, since a
// sub-agent is listed in no picker a grant is made in; a restriction on a
// parent binds them as well. Tools are the exception that asks: a sub-agent
// carries a tool scoped to its parent only when its own list names it or it
// inherits its parents' tools.
func TestAParentsGrantsCoverItsSubAgents(t *testing.T) {
	_, udb := familyFixture(t)
	load := func(id string) AgentRecord {
		t.Helper()
		a, ok := loadAgent(udb, id)
		if !ok {
			t.Fatalf("no agent %s", id)
		}
		return a
	}
	outsider := AgentRecord{ID: "a-outsider", Owner: "alice"}

	// Bulletin boards: the parent's right reaches two levels down.
	saveBulletin(udb, bulletinBoard{Name: "radar", Posters: []string{"a-writer"}})
	board, _ := loadBulletin(udb, "radar")
	for _, id := range []string{"a-writer", "a-critic", "a-nit"} {
		if !board.allowsAgent(udb, load(id)) {
			t.Errorf("%s may not post under its top-level parent's right", id)
		}
	}
	if board.allowsAgent(udb, outsider) {
		t.Error("an agent outside the family may post")
	}
	// A grant made before rights lived on the parent named the sub-agent;
	// it moves to the top-level parent at start-up, once.
	saveBulletin(udb, bulletinBoard{Name: "old", Posters: []string{"a-nit", "a-writer"}})
	moveSubAgentPosters()
	if b, _ := loadBulletin(udb, "old"); strings.Join(b.Posters, ",") != "a-writer" {
		t.Errorf("posters after the move = %v, want the top-level parent once", b.Posters)
	}

	// APIs: "Only these agents" naming the parent admits its sub-agents, and
	// the parent's opt-out binds them.
	if err := Secure().Save(SecureCredential{Name: "weather", Type: SecureCredBearer, BaseURL: "https://weather.test", Owner: "alice"}, "k"); err != nil {
		t.Fatal(err)
	}
	if err := Secure().SetOnlyAgentsOwned("alice", "weather", []string{"a-writer"}); err != nil {
		t.Fatal(err)
	}
	if credentialDenySet(load("a-nit"), "alice")["weather"] {
		t.Error("a sub-agent of a listed parent is denied the API")
	}
	if !credentialDenySet(outsider, "alice")["weather"] {
		t.Error("an agent off the list, outside the family, may use the API")
	}
	writer := load("a-writer")
	writer.DisabledCredentials = []string{"news"}
	if _, err := saveAgent(udb, writer); err != nil {
		t.Fatal(err)
	}
	if !credentialDenySet(load("a-nit"), "alice")["news"] {
		t.Error("a parent's opt-out does not bind its sub-agent: it would be wider than its parent")
	}

	// Tools: scoped to the parent, carried by a sub-agent only on request.
	if err := AdminPersistTempTool(udb, "alice", TempTool{Name: "writer_kit", Description: "d", CommandTemplate: "echo w"}); err != nil {
		t.Fatal(err)
	}
	if !SetUserToolScopeAgents(udb, "alice", "writer_kit", []string{"a-writer"}) {
		t.Fatal("scope")
	}
	kit, _ := UserToolByName(udb, "alice", "writer_kit")
	critic := load("a-critic")
	if newPoolToolGate(udb, critic).admits(kit) {
		t.Error("a sub-agent that did not ask carries its parent's tool")
	}
	critic.AllowedTools = []string{"writer_kit"}
	if !newPoolToolGate(udb, critic).admits(kit) || !newPoolToolGate(udb, critic).reaches(kit) {
		t.Error("a sub-agent whose list names its parent's tool does not carry it")
	}
	critic.DisabledPersistentTools = []string{"writer_kit"}
	if newPoolToolGate(udb, critic).admits(kit) {
		t.Error("a sub-agent's own opt-out is overridden by its parent's grant")
	}
	nit := load("a-nit")
	nit.InheritParentTools = true
	if !newPoolToolGate(udb, nit).admits(kit) {
		t.Error("a sub-agent that inherits its parents' tools does not carry one scoped two levels up")
	}
	outsider.AllowedTools = []string{"writer_kit"}
	if newPoolToolGate(udb, outsider).admits(kit) {
		t.Error("naming a tool scoped to another family's agent reaches it")
	}
}

// What only a parent decides is decided on the parent: a parent forced
// Private makes its sub-agents Private (on their own schedules too), who a
// sub-agent may message is its top-level parent's decision (a block of its
// own still holds), and its Security page offers neither sharing nor who may
// call it, saying where each is decided instead.
func TestAParentDecidesForItsSubAgents(t *testing.T) {
	app, udb := familyFixture(t)
	load := func(id string) AgentRecord {
		t.Helper()
		a, _ := loadAgent(udb, id)
		return a
	}

	// Private mode.
	if agentForcesPrivate(load("a-nit")) {
		t.Fatal("a sub-agent of a parent that is not Private is Private")
	}
	writer := load("a-writer")
	writer.ForcePrivate = true
	if _, err := saveAgent(udb, writer); err != nil {
		t.Fatal(err)
	}
	if !agentForcesPrivate(load("a-nit")) || privateParentOf(load("a-nit")) != "Writer" {
		t.Error("a parent forced Private does not make its sub-agent Private")
	}
	if agentForcesPrivate(AgentRecord{ID: "a-outsider", Owner: "alice"}) {
		t.Error("an agent outside the family became Private")
	}

	// Messaging.
	const handle = "+15550100"
	if contactHolder("alice", "a-nit") != "a-writer" || contactHolder("alice", "a-writer") != "a-writer" {
		t.Error("a sub-agent's messaging is not its top-level parent's")
	}
	SetContactPolicy(RootDB, "alice", "a-writer", handle, PolicyAllow)
	if !familyContactAllowed("alice", "a-nit", handle) || familyContactBlocked("alice", "a-nit", handle) {
		t.Error("a contact the parent may message is not the sub-agent's")
	}
	SetContactPolicy(RootDB, "alice", "a-nit", handle, PolicyBlock)
	if !familyContactBlocked("alice", "a-nit", handle) {
		t.Error("a block recorded on the sub-agent no longer holds")
	}
	RemoveContactPolicy(RootDB, "alice", "a-nit", handle)
	SetContactPolicy(RootDB, "alice", "a-writer", handle, PolicyBlock)
	if !familyContactBlocked("alice", "a-nit", handle) {
		t.Error("a contact the parent may not message is open to its sub-agent")
	}

	// The Security page.
	page := func(id string) string {
		w := httptest.NewRecorder()
		app.renderAgentAccess(w, asUser(httptest.NewRequest(http.MethodGet, "/agent/"+id+"/access?format=json", nil), "alice"), "alice", udb, id)
		return w.Body.String()
	}
	sub, top := page("a-critic"), page("a-writer")
	for _, gone := range []string{`"group":"Share"`, "Who may call THIS agent", "Agents that may call it", "Let this agent message somebody",
		"Which agents it can call at all", "Dispatch target list", "What it can call"} {
		if strings.Contains(sub, gone) {
			t.Errorf("a sub-agent's Security page still offers %s", gone)
		}
		if !strings.Contains(top, gone) {
			t.Errorf("a top-level agent's Security page lost %s", gone)
		}
	}
	for _, want := range []string{"A sub-agent does not delegate", "Only Writer, the agent it belongs to", "Decided on Writer's Security page", "Writer is forced Private, so this agent is too"} {
		if !strings.Contains(sub, want) {
			t.Errorf("a sub-agent's Security page should say %q", want)
		}
	}
}

// A sub-agent's runs are held to and charged against its parents' caps, under
// the parent's own key, so a parent's limit counts its whole family. It has no
// caps of its own: they are the parent's to set.
func TestASubAgentCountsAgainstItsParentsCaps(t *testing.T) {
	app, udb := familyFixture(t)
	writer, _ := loadAgent(udb, "a-writer")
	writer.ActionQuotas = ActionQuotaMap{"post": 6}
	saveAgent(udb, writer)
	critic, _ := loadAgent(udb, "a-critic")
	critic.DailySpendUSD = 2
	critic.ActionQuotas = ActionQuotaMap{"post": 1}
	saveAgent(udb, critic)
	if critic, _ = loadAgent(udb, "a-critic"); critic.DailySpendUSD != 0 || len(critic.ActionQuotas) != 0 {
		t.Errorf("a sub-agent kept caps of its own: %v %v", critic.DailySpendUSD, critic.ActionQuotas)
	}

	nit, _ := loadAgent(udb, "a-nit")
	got := familyBudgets(nit)
	if len(got) != 1 || got[0].Key != "a-writer" || got[0].ActionQuotas["post"] != 6 {
		t.Fatalf("a sub-agent two levels down should carry the top parent's caps: %+v", got)
	}
	if got[0].Label != "Writer and the agents under it" {
		t.Errorf("label = %q", got[0].Label)
	}
	if b := familyBudgets(writer); b != nil {
		t.Errorf("a top-level agent shares no budget: %+v", b)
	}

	// The page says where they are set, and offers no form of its own.
	w := httptest.NewRecorder()
	app.renderAgentAccess(w, asUser(httptest.NewRequest(http.MethodGet, "/agent/a-nit/access?format=json", nil), "alice"), "alice", udb, "a-nit")
	if !strings.Contains(w.Body.String(), "counts against Writer's limits") {
		t.Error("a sub-agent's Limits tab should say its parent's limits cover it")
	}
	if strings.Contains(w.Body.String(), "Spend limit (US$ per 24 hours)") {
		t.Error("a sub-agent's Limits tab still offers its own spend limit")
	}

	// Builder is told where to set them, and clearing them is not refused.
	if msg := subAgentLimitsRefusal(udb, "a-writer", map[string]any{"daily_spend_usd": 3.0}); !strings.Contains(msg, "\"Writer\"") {
		t.Errorf("setting a sub-agent's spend should name its parent: %q", msg)
	}
	if msg := subAgentLimitsRefusal(udb, "a-writer", map[string]any{"daily_spend_usd": 0.0, "action_quotas": []any{}}); msg != "" {
		t.Errorf("clearing is not setting: %q", msg)
	}
	if msg := subAgentLimitsRefusal(udb, "", map[string]any{"daily_spend_usd": 3.0}); msg != "" {
		t.Errorf("a top-level agent sets its own: %q", msg)
	}
}

// A sub-agent delegates nothing on any path: no agents tool on its chat or
// scheduled turn, as it already had none when its parent called it. An
// authoring sub-agent keeps the read-only fleet view authoring needs.
func TestASubAgentIsALeafOnEveryPath(t *testing.T) {
	app, udb := familyFixture(t)
	critic, _ := loadAgent(udb, "a-critic")
	writer, _ := loadAgent(udb, "a-writer")
	sub := &chatTurn{app: app, udb: udb, user: "alice", agent: critic}
	if sub.agentsToolWanted() {
		t.Error("a sub-agent's own turn carries the agents tool")
	}
	if top := (&chatTurn{udb: udb, user: "alice", agent: writer}); !top.agentsToolWanted() {
		t.Error("a top-level agent lost the agents tool (its turn has no app, which reads as full reach)")
	}
}

// The New recurring task modal on a parent offers the parent and every
// sub-agent under it, a deeper one named with the one between; an agent with
// no sub-agents is offered nothing, so the modal draws no picker.
func TestTheRecurringTaskPickerOffersTheFamily(t *testing.T) {
	app, udb := familyFixture(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a-solo", Name: "Solo", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	get := func(agent string) []scheduleForOption {
		r := httptest.NewRequest(http.MethodGet, "/api/console/recurring/for-options?agent="+agent, nil)
		w := httptest.NewRecorder()
		routed(app)(w, asUser(r, "alice"))
		var opts []scheduleForOption
		if err := json.Unmarshal(w.Body.Bytes(), &opts); err != nil {
			t.Fatalf("%s: %v %s", agent, err, w.Body.String())
		}
		return opts
	}
	want := []scheduleForOption{
		{Value: "", Label: "Writer itself"},
		{Value: "a-critic", Label: "Sub-agent: Critic"},
		{Value: "a-nit", Label: "Sub-agent: Critic > Nitpicker"},
	}
	if got := get("a-writer"); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Writer's picker = %+v, want %+v", got, want)
	}
	if got := get("a-solo"); len(got) != 0 {
		t.Errorf("an agent with no sub-agents was offered a picker: %+v", got)
	}
}

// A recurring task made and edited on the parent's Scheduler for a sub-agent
// is the parent's, carrying the line that hands it on, in the thread it was
// made from. The editor shows the directive without that line and the
// sub-agent in the picker; saving keeps, changes or drops the line by it.
func TestARecurringTaskCanBeForASubAgent(t *testing.T) {
	app, _ := familyFixture(t)
	PreInitScheduler()
	post := func(h http.HandlerFunc, url, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
		w := httptest.NewRecorder()
		h(w, asUser(r, "alice"))
		return w
	}
	task := func() orchUpdatePayload {
		rts := listAgentRecurringTasks("alice", "")
		if len(rts) != 1 {
			t.Fatalf("want one recurring task, have %d", len(rts))
		}
		return rts[0].Payload
	}
	taskID := func() string { return listAgentRecurringTasks("alice", "")[0].TaskID }

	if w := post(routed(app), "/api/console/recurring/create",
		`{"agent_id":"a-writer","session_id":"s-open","prompt":"Check the build","for":"a-unrelated","pattern":"fixed","interval_minutes":60}`); w.Code != http.StatusBadRequest {
		t.Errorf("a task for an agent outside the family was accepted: %d %s", w.Code, w.Body.String())
	}
	if w := post(routed(app), "/api/console/recurring/create",
		`{"agent_id":"a-writer","session_id":"s-open","prompt":"Check the build","for":"a-nit","pattern":"fixed","interval_minutes":60}`); w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if p := task(); p.AgentID != "a-writer" || p.SessionID != "s-open" || subAgentHandoffOf(p.Prompt) != "Nitpicker" || !strings.HasPrefix(p.Prompt, "Check the build") {
		t.Errorf("created: runs %q in %q with %q", p.AgentID, p.SessionID, p.Prompt)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/console/recurring/get?id="+taskID(), nil)
	w := httptest.NewRecorder()
	routed(app)(w, asUser(r, "alice"))
	var rec struct {
		Prompt string              `json:"prompt"`
		For    string              `json:"for"`
		Opts   []scheduleForOption `json:"for_options"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Prompt != "Check the build" || rec.For != "a-nit" || len(rec.Opts) != 3 {
		t.Errorf("the editor got %+v", rec)
	}

	// Saved without touching the picker: the line comes back, once.
	if w := post(routed(app), "/api/console/recurring/update?id="+taskID(),
		`{"prompt":"Check the build twice","pattern":"fixed","interval_minutes":60}`); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	if p := task(); subAgentHandoffOf(p.Prompt) != "Nitpicker" || strings.Count(p.Prompt, subAgentHandoffLead) != 1 || !strings.HasPrefix(p.Prompt, "Check the build twice") {
		t.Errorf("an edit that left the picker alone: %q", p.Prompt)
	}

	if w := post(routed(app), "/api/console/recurring/update?id="+taskID(),
		`{"for":"a-critic","pattern":"fixed","interval_minutes":60}`); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	if p := task(); subAgentHandoffOf(p.Prompt) != "Critic" || strings.Count(p.Prompt, subAgentHandoffLead) != 1 {
		t.Errorf("handed to another sub-agent: %q", p.Prompt)
	}

	if w := post(routed(app), "/api/console/recurring/update?id="+taskID(),
		`{"for":"","pattern":"fixed","interval_minutes":60}`); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	if p := task(); p.Prompt != "Check the build twice" || p.AgentID != "a-writer" || p.SessionID != "s-open" {
		t.Errorf("given back to the parent: runs %q in %q with %q", p.AgentID, p.SessionID, p.Prompt)
	}
}

// A scheduled agent and an event monitor on the parent are handed to a
// sub-agent from their editors the same way: the editor shows the mission or
// brief without the handoff line and the sub-agent in the picker, and a save
// keeps, changes or drops the line by it.
func TestScheduledAgentsAndMonitorsCanBeForASubAgent(t *testing.T) {
	app, _ := familyFixture(t)
	PreInitScheduler()
	type view struct {
		Mission string              `json:"mission"`
		Brief   string              `json:"wake_brief"`
		For     string              `json:"for"`
		Opts    []scheduleForOption `json:"for_options"`
	}
	get := func(h http.HandlerFunc, url string) view {
		r := httptest.NewRequest(http.MethodGet, url, nil)
		w := httptest.NewRecorder()
		h(w, asUser(r, "alice"))
		var v view
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatalf("%s: %v %s", url, err, w.Body.String())
		}
		return v
	}
	post := func(h http.HandlerFunc, url, body string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
		w := httptest.NewRecorder()
		h(w, asUser(r, "alice"))
		if w.Code >= 300 {
			t.Fatalf("%s %s: %d %s", url, body, w.Code, w.Body.String())
		}
	}

	SaveStandingAgent(RootDB, StandingAgent{Owner: "alice", Name: "sweep", AgentID: "a-writer", Mission: "Check the feeds.", IntervalSeconds: 3600})
	if v := get(routed(app), "/api/console/agents/get?id=sweep"); v.For != "" || len(v.Opts) != 3 || v.Mission != "Check the feeds." {
		t.Errorf("the scheduled agent's editor got %+v", v)
	}
	post(routed(app), "/api/console/agents/update?id=sweep", `{"for":"a-nit","mission":"Check the feeds.","interval_minutes":60}`)
	if sa, _ := GetStandingAgent(RootDB, "alice", "sweep"); sa.AgentID != "a-writer" || subAgentHandoffOf(sa.Mission) != "Nitpicker" {
		t.Errorf("handed to a sub-agent: runs %q with %q", sa.AgentID, sa.Mission)
	}
	if v := get(routed(app), "/api/console/agents/get?id=sweep"); v.For != "a-nit" || v.Mission != "Check the feeds." {
		t.Errorf("the editor of one handed on got %+v", v)
	}
	post(routed(app), "/api/console/agents/update?id=sweep", `{"mission":"Check the feeds twice.","interval_minutes":60}`)
	if sa, _ := GetStandingAgent(RootDB, "alice", "sweep"); subAgentHandoffOf(sa.Mission) != "Nitpicker" || strings.Count(sa.Mission, subAgentHandoffLead) != 1 {
		t.Errorf("an edit that left the picker alone: %q", sa.Mission)
	}
	post(routed(app), "/api/console/agents/update?id=sweep", `{"for":"","interval_minutes":60}`)
	if sa, _ := GetStandingAgent(RootDB, "alice", "sweep"); sa.Mission != "Check the feeds twice." {
		t.Errorf("given back to the parent: %q", sa.Mission)
	}

	SaveEventMonitor(RootDB, EventMonitor{Owner: "alice", Name: "watch", Kind: "webhook", WakeAgent: "a-writer", WakeBrief: "Say what broke."})
	post(routed(app), "/api/console/monitors/update?id=watch", `{"for":"a-critic","wake_brief":"Say what broke."}`)
	if m, _ := GetEventMonitor(RootDB, "alice", "watch"); m.WakeAgent != "a-writer" || subAgentHandoffOf(m.WakeBrief) != "Critic" {
		t.Errorf("monitor handed to a sub-agent: wakes %q with %q", m.WakeAgent, m.WakeBrief)
	}
	if v := get(routed(app), "/api/console/monitors/get?id=watch"); v.For != "a-critic" || v.Brief != "Say what broke." || len(v.Opts) != 3 {
		t.Errorf("the monitor's editor got %+v", v)
	}
}

// Attaching a channel, or re-pointing one at another agent, leaves the
// agent's Cortex setting as its owner set it (channels_http.go).
func TestAttachingAChannelLeavesTheCortexSettingAlone(t *testing.T) {
	app, udb := familyFixture(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a-other", Name: "Other", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	post := func(url, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		app.handleChannels(w, asUser(httptest.NewRequest(http.MethodPost, url, strings.NewReader(body)), "alice"))
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", url, body, w.Code, w.Body.String())
		}
		return w
	}
	w := post("/api/channels?agent_id=a-writer", `{"name":"Team"}`)
	var ch Channel
	if err := json.Unmarshal(w.Body.Bytes(), &ch); err != nil || ch.ID == "" {
		t.Fatalf("created channel: %v %s", err, w.Body.String())
	}
	if a, _ := loadAgent(udb, "a-writer"); a.Cortex {
		t.Error("attaching a channel switched on the agent's Cortex reading")
	}
	post("/api/channels", fmt.Sprintf(`{"id":%q,"name":"Team","agent_id":"a-other"}`, ch.ID))
	if a, _ := loadAgent(udb, "a-other"); a.Cortex {
		t.Error("re-pointing a channel switched on the new agent's Cortex reading")
	}
}
