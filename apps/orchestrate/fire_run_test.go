package orchestrate

// A recurring fire end to end, against the scripted model: the gates, the
// pre-armed successor, the prompt, the loop, the card on the thread, the run
// in the ledger and the idle-clock renewal. Before these, nothing ran a fire
// past its gates, and the fire's wiring was pinned by reading its source.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// fireFixture is one recurring task on a fresh store, its agent and its
// thread, with the scripted model in place of the app's.
type fireFixture struct {
	t     *testing.T
	udb   Database
	agent AgentRecord
	llm   *FakeLLM
	p     orchUpdatePayload
}

func newFireFixture(t *testing.T, turns ...FakeTurn) *fireFixture {
	t.Helper()
	root := depStores(t, "u")
	saved := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = saved })
	PreInitScheduler()

	ff := &fireFixture{t: t, udb: UserDB(root, "u"), llm: &FakeLLM{Turns: turns}}
	orchRef.LLM = ff.llm
	a, err := saveAgent(ff.udb, AgentRecord{Name: "Herald", Owner: "u", OrchestratorPrompt: "You post the morning summary."})
	if err != nil {
		t.Fatal(err)
	}
	ff.agent = a
	ff.p = orchUpdatePayload{
		Username: "u", AgentID: a.ID, SessionID: "s-fire-" + UUIDv4(),
		Name: "Morning summary", Prompt: "Post the morning summary.", IntervalSeconds: 3600,
		// Every task is stamped at creation; it is half of the task's identity
		// (recurringTaskUID), which is what its notes are kept under.
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	// The scheduler's store outlives the test (PreInitScheduler is
	// first-writer-wins across the package): leave the queue as found.
	t.Cleanup(func() {
		for _, task := range ff.queued() {
			UnscheduleTask(task.ID)
		}
	})
	return ff
}

// queued is every scheduled occurrence of this fixture's task.
func (ff *fireFixture) queued() []ScheduledTask {
	var out []ScheduledTask
	for _, task := range ListScheduledTasks(OrchestrateScheduledUpdateKind) {
		var p orchUpdatePayload
		if json.Unmarshal(task.Payload, &p) == nil && p.SessionID == ff.p.SessionID {
			out = append(out, task)
		}
	}
	return out
}

// successor is the one armed next occurrence, decoded.
func (ff *fireFixture) successor() (orchUpdatePayload, bool) {
	q := ff.queued()
	if len(q) != 1 {
		return orchUpdatePayload{}, false
	}
	var p orchUpdatePayload
	json.Unmarshal(q[0].Payload, &p)
	return p, true
}

func (ff *fireFixture) fire(reArm bool) {
	ff.t.Helper()
	if err := fireOrchestrateUpdate(ff.t.Context(), ff.p, reArm); err != nil {
		ff.t.Fatalf("the fire did not run: %v", err)
	}
}

// cards are the thread's assistant messages.
func (ff *fireFixture) cards() []ChatMessage {
	s, _ := loadChatSession(ff.udb, ff.agent.ID, ff.p.SessionID)
	var out []ChatMessage
	for _, m := range s.Messages {
		if m.Role == "assistant" {
			out = append(out, m)
		}
	}
	return out
}

// run is the fire's one row in the ledger, in full: a listing leaves out the
// steps and the raw reply, which are kept beside the row.
func (ff *fireFixture) run() RunRecord {
	ff.t.Helper()
	runs := ListRuns(RootDB, "u", RunFilter{Task: ff.p.Name})
	if len(runs) != 1 {
		ff.t.Fatalf("want one run of %q in the ledger, have %d", ff.p.Name, len(runs))
	}
	full, ok := GetRun(RootDB, "u", runs[0].ID)
	if !ok {
		ff.t.Fatalf("run %s is listed but cannot be read", runs[0].ID)
	}
	return full
}

// request is the request a fire handed the model, found among the calls from
// the from'th on by its framing: the judges call the same model, so a call's
// position says nothing about whose it is.
func (ff *fireFixture) request(from int, framing string) string {
	for i := from; i < ff.llm.Calls(); i++ {
		msgs := ff.llm.Sent(i)
		for j := len(msgs) - 1; j >= 0; j-- {
			if msgs[j].Role == "user" && strings.Contains(msgs[j].Content, framing) {
				return msgs[j].Content
			}
		}
	}
	return ""
}

func noteCall(text string) FakeTurn {
	return FakeTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "update_notes", Args: map[string]any{"text": text}}}}
}

func TestARecurringFireRunsEndToEnd(t *testing.T) {
	const reply = "Posted the morning summary: three items."
	ff := newFireFixture(t, noteCall("Item two is still waiting on the vendor."), FakeTurn{Content: reply, Repeat: true})
	ff.fire(true)

	// The request the model was given is the fire's framing around the task.
	if got := ff.request(0, "[SCHEDULED UPDATE: fire 1"); got == "" || !strings.Contains(got, ff.p.Prompt) {
		t.Errorf("the model was not handed the fire's request:\n%s", got)
	}

	cards := ff.cards()
	if len(cards) != 1 {
		t.Fatalf("want the fire's one report card on the thread, have %d", len(cards))
	}
	c := cards[0]
	if c.Content != reply || c.ReportFrom != ff.p.Name || c.ReportKind != cortexKindScheduled {
		t.Errorf("the card is not the fire's report: %+v", c)
	}
	if !strings.Contains(c.ReportDetail, "fire 1") || len(c.ToolCalls) == 0 || c.ToolCalls[0].Name != "update_notes" {
		t.Errorf("the card does not say which fire it was or what it did: detail=%q calls=%+v", c.ReportDetail, c.ToolCalls)
	}

	run := ff.run()
	if run.Status != RunOK || run.Trigger != "schedule" || run.Brief != ff.p.Prompt || len(run.Steps) == 0 {
		t.Errorf("the ledger row does not record the fire: %+v", run)
	}

	// The successor was armed before the fire ran, and the productive fire
	// renewed its idle clock.
	next, ok := ff.successor()
	if !ok {
		t.Fatalf("want exactly one armed successor, have %d", len(ff.queued()))
	}
	if next.FireCount != 1 || next.LastActive == "" {
		t.Errorf("the successor is not the next fire with a renewed idle clock: fire=%d last_active=%q", next.FireCount, next.LastActive)
	}

	// Reported into its session, so the cortex keeps a pointer to it.
	if got := cortexLines(t, ff.udb, ff.agent.ID); !strings.Contains(got, ff.p.Name) {
		t.Errorf("the cortex has no pointer to the fire:\n%s", got)
	}
}

// What one fire leaves in its notes is in the next fire's request.
func TestAFireReadsWhatTheLastOneLeft(t *testing.T) {
	const note = "Item two is still waiting on the vendor."
	ff := newFireFixture(t, noteCall(note), FakeTurn{Content: "Posted.", Repeat: true})
	ff.fire(true)
	next, ok := ff.successor()
	if !ok {
		t.Fatal("no armed successor to fire")
	}
	ff.p = next
	calls := ff.llm.Calls()
	ff.fire(true)
	if got := ff.request(calls, "[SCHEDULED UPDATE: fire 2"); got == "" || !strings.Contains(got, note) {
		t.Errorf("the second fire was not handed the first one's note:\n%s", got)
	}
}

// Run now fires once and leaves the schedule as it was: nothing armed, and
// the ledger says it was a manual run.
func TestARunNowFiresOnceAndArmsNothing(t *testing.T) {
	ff := newFireFixture(t, noteCall("checked"), FakeTurn{Content: "Posted.", Repeat: true})
	ff.fire(false)
	if q := ff.queued(); len(q) != 0 {
		t.Errorf("a Run now armed %d occurrence(s)", len(q))
	}
	if run := ff.run(); run.Trigger != "manual" || run.Status != RunOK {
		t.Errorf("the ledger row is not a manual run: %+v", run)
	}
	if len(ff.cards()) != 1 {
		t.Error("a Run now still posts its report")
	}
}

// A fire that only narrates what it is about to do posts nothing, says so on
// the thread's trail, and leaves the successor's idle clock alone.
func TestAFireThatOnlyNarratesPostsNothing(t *testing.T) {
	ff := newFireFixture(t, FakeTurn{Content: "Starting the cycle now. Let me check the feed.", Repeat: true})
	ff.fire(true)
	if n := len(ff.cards()); n != 0 {
		t.Errorf("a preamble-only fire posted %d card(s)", n)
	}
	if run := ff.run(); run.Status != RunOK || !strings.Contains(run.Summary, "preamble only") {
		t.Errorf("the ledger row does not say the fire was suppressed: %+v", run)
	}
	if diagsOf(RootDB, "u", ff.agent.ID, ff.p.SessionID, "recurring-fire-suppressed") != 1 {
		t.Error("the suppression left no breadcrumb on the thread")
	}
	if next, ok := ff.successor(); !ok || next.LastActive != "" {
		t.Errorf("an unproductive fire should leave the armed successor's idle clock alone: %+v %v", next, ok)
	}
}

// A fire whose model fails is recorded as failed, posts nothing, and keeps the
// chain: the successor was armed before the loop ran.
func TestAFireWhoseModelFailsIsRecordedAsFailed(t *testing.T) {
	ff := newFireFixture(t, FakeTurn{Err: errors.New("model unavailable"), Repeat: true})
	ff.fire(true)
	if n := len(ff.cards()); n != 0 {
		t.Errorf("a failed fire posted %d card(s)", n)
	}
	if run := ff.run(); run.Status != RunFailed || !strings.Contains(run.Err, "model unavailable") {
		t.Errorf("the ledger row does not record the failure: %+v", run)
	}
	if diagsOf(RootDB, "u", ff.agent.ID, ff.p.SessionID, "recurring-fire-failed") != 1 {
		t.Error("the failure left no breadcrumb on the thread")
	}
	if _, ok := ff.successor(); !ok {
		t.Error("a failed fire lost the chain")
	}
}
