package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notes"
)

// The identity a recurring task's notes hang on has to survive a fire, and that
// is the one thing the obvious key does not do: every re-arm mints a new
// scheduler task id, so notes keyed on the id a console row is actioned by
// would be written where no later fire could look.
func TestRecurringNotesSurviveARearm(t *testing.T) {
	p := orchUpdatePayload{
		UID: "task-uid-1", Username: "craig", SessionID: "sess-1",
		CreatedAt: "2026-09-19T10:00:00Z", AgentID: "a1",
	}
	// What an arm does: copy the payload forward, count the fire.
	armed := p
	armed.FireCount++
	if a, b := recurringTaskNotes(p).ns, recurringTaskNotes(armed).ns; a != b {
		t.Fatalf("the next fire looks at a different notes row:\n  fire 1: %s\n  fire 2: %s", a, b)
	}
	// An edit-in-place rebuilds the payload from a spec. The spec carries the
	// UID for the same reason it carries CreatedAt: a retime must not orphan
	// what the task has accumulated.
	edited := orchUpdatePayload{UID: p.UID, Username: p.Username, SessionID: "moved", CreatedAt: "2026-09-20T10:00:00Z"}
	if recurringTaskNotes(edited).ns != recurringTaskNotes(p).ns {
		t.Error("an edit orphans the task's notes")
	}
}

// Tasks armed before the UID existed still need a stable row, and the pair that
// has always been carried forward untouched is the one to use.
func TestLegacyRecurringTaskKeyIsStable(t *testing.T) {
	p := orchUpdatePayload{Username: "craig", SessionID: "sess-1", CreatedAt: "2026-09-19T10:00:00Z"}
	armed := p
	armed.FireCount += 3
	if recurringTaskNotes(p).ns != recurringTaskNotes(armed).ns {
		t.Error("a legacy task's notes do not survive its own re-arm")
	}
	// Nothing to key on at all is not a shared bucket: it is no notes.
	if got := recurringTaskNotes(orchUpdatePayload{Username: "craig"}); got.live() {
		t.Errorf("an unidentifiable task got a notes row: %q", got.ns)
	}
}

// Ids are only unique within a surface, and within an owner. Both are in the
// key because the alternative is one task silently reading another's state.
func TestTaskNotesNamespacesDoNotCollide(t *testing.T) {
	seen := map[string]string{}
	for _, c := range []struct{ label, surface, owner, id string }{
		{"standing", notes.TaskSurfaceStanding, "craig", "nightly"},
		{"monitor", notes.TaskSurfaceMonitor, "craig", "nightly"},
		{"recurring", notes.TaskSurfaceRecurring, "craig", "nightly"},
		{"other owner", notes.TaskSurfaceStanding, "dana", "nightly"},
	} {
		ns := notes.TaskNamespace(c.surface, c.owner, c.id)
		if ns == "" {
			t.Fatalf("%s: no namespace", c.label)
		}
		if prev, dup := seen[ns]; dup {
			t.Errorf("%s shares a notes row with %s: %s", c.label, prev, ns)
		}
		seen[ns] = c.label
	}
}

// The empty state is the feature. The agent-wide layer renders nothing when it
// is empty, and the framing that teaches it lives inside the block, so an agent
// with notes and nothing written was never told it had any. A task cannot be
// seeded by hand, so the first fire has to be told in the prompt.
func TestATaskWithNoNotesIsStillToldItHasThem(t *testing.T) {
	pinRootDB(t)
	tn := newTaskNotes(notes.TaskSurfaceStanding, "craig", "nightly")
	block := tn.block()
	if !strings.Contains(block, "update_notes") {
		t.Errorf("an empty task's block does not name the tool that fills it: %q", block)
	}
	if !strings.Contains(block, "nothing recorded yet") {
		t.Errorf("the empty state does not say it is empty: %q", block)
	}

	// Once written, the block is the notes.
	if _, err := tn.tool()[0].Handler(t.Context(), map[string]any{"text": "the build script needs --no-cache"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := tn.block(); !strings.Contains(got, "--no-cache") || strings.Contains(got, "nothing recorded yet") {
		t.Errorf("the block does not carry what was written: %q", got)
	}
}

// A write from a fire lands in the TASK's row, never the agent's. They are
// different lifetimes in different stores, and a write that crossed would
// outlive the task it belongs to.
func TestTaskNotesWriteDoesNotTouchTheAgentBlock(t *testing.T) {
	root := pinRootDB(t)
	tn := newTaskNotes(notes.TaskSurfaceMonitor, "craig", "pr-12")
	if _, err := tn.tool()[0].Handler(t.Context(), map[string]any{"text": "review is with the platform team"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := LoadOperatingNotes(root, factsNamespace("agent-1")).Text; got != "" {
		t.Errorf("a task write reached an agent's Working notes: %q", got)
	}
	if got := LoadOperatingNotes(root, tn.ns).Text; !strings.Contains(got, "platform team") {
		t.Errorf("the task's own row is empty: %q", got)
	}
}

// A fire carries ONE scratchpad. Two always-in-prompt blocks with no rule for
// choosing between them is how running state lands in the wrong one.
func TestAFireSuppressesTheAgentWideBlock(t *testing.T) {
	pinRootDB(t)
	if agentNotesSuppressed(context.Background()) {
		t.Error("an ordinary turn is being treated as a fire")
	}
	live := withTaskNotes(context.Background(), newTaskNotes(notes.TaskSurfaceStanding, "craig", "nightly"))
	if !agentNotesSuppressed(live) {
		t.Error("a fire under a task still renders the agent's own notes")
	}
	// A scope that could not be identified changes nothing: no suppression, no
	// row, today's behaviour exactly.
	dead := withTaskNotes(context.Background(), taskNotes{})
	if agentNotesSuppressed(dead) {
		t.Error("an unidentifiable task suppressed the agent's notes and offered nothing in their place")
	}
}

// The lifetime rule, which is the whole reason this layer is scoped to a task:
// deleting the task deletes what it knew. Parking must NOT, and the two are
// easy to confuse because both cancel the live scheduler entry.
func TestDeletingATaskDropsItsNotes(t *testing.T) {
	root := pinRootDB(t)
	tn := newTaskNotes(notes.TaskSurfaceStanding, "craig", "nightly")
	SaveOperatingNotes(root, tn.ns, "half of the export is already uploaded")

	DeleteStandingAgent(root, "craig", "nightly")
	if got := LoadOperatingNotes(root, tn.ns).Text; got != "" {
		t.Errorf("a deleted schedule left its notes behind: %q", got)
	}

	// A monitor, the same way.
	mn := newTaskNotes(notes.TaskSurfaceMonitor, "craig", "pr-12")
	SaveOperatingNotes(root, mn.ns, "the review is with the platform team")
	DeleteEventMonitor(root, "craig", "pr-12")
	if got := LoadOperatingNotes(root, mn.ns).Text; got != "" {
		t.Errorf("a deleted monitor left its notes behind: %q", got)
	}
}

// A parked recurring task is stopped and KEPT, and Resume gives it a fresh
// allowance. What it had worked out is most of what makes resuming different
// from starting over, so the park path must not go through the drop.
func TestParkingARecurringTaskKeepsItsNotes(t *testing.T) {
	root := pinRootDB(t)
	p := orchUpdatePayload{UID: "u1", Username: "craig", SessionID: "s1", CreatedAt: "2026-09-19T10:00:00Z"}
	tn := recurringTaskNotes(p)
	SaveOperatingNotes(root, tn.ns, "the credential was rotated; ask before retrying")

	parked := p
	parked.Broken, parked.BrokenReason = true, "its agent was deleted"
	if got := LoadOperatingNotes(root, recurringTaskNotes(parked).ns).Text; got == "" {
		t.Error("a parked task cannot see what it had worked out")
	}
	// And the end of the task does drop it.
	dropRecurringTaskNotes(p)
	if got := LoadOperatingNotes(root, tn.ns).Text; got != "" {
		t.Errorf("a cancelled task left its notes behind: %q", got)
	}
}

// Every row on the Scheduler can be asked what its task knows, and the row has
// to say WHICH task it is: a monitor and a standing agent may share a name, and
// a server left to guess from the id alone would hand one the other's notes.
func TestEverySchedulerRowCarriesItsKindForNotes(t *testing.T) {
	for _, c := range []struct {
		row           any
		section, kind string
	}{
		{consoleAgentRow{Name: "nightly", ID: "nightly"}, schedSectionStanding, schedKindStanding},
		{consoleRecurringRow{Name: "nightly", ID: "t-1"}, schedSectionRecurring, schedKindRecurring},
		{consoleMonitorRow{Name: "nightly", ID: "nightly"}, schedSectionMonitors, schedKindMonitor},
	} {
		m := schedulerRow(c.row, c.section, c.kind)
		if m["_notes"] != true {
			t.Errorf("%s row does not offer its notes", c.kind)
		}
		if m["_kind"] != c.kind {
			t.Errorf("%s row does not say which kind it is: %v", c.kind, m["_kind"])
		}
	}
}

// The owner-facing lookup is the ownership check: each kind is fetched from the
// asking user's own records, so somebody else's id is not refused, it is simply
// not found. Pinned because the alternative (resolve, then compare an owner
// field) is the shape that leaks the first time a comparison is forgotten.
func TestTaskNotesLookupIsScopedToTheOwner(t *testing.T) {
	root := pinRootDB(t)
	SaveStandingAgent(root, StandingAgent{Owner: "craig", Name: "nightly", AgentID: "a1"})
	SaveEventMonitor(root, EventMonitor{Owner: "craig", Name: "nightly", Kind: EventKindWatch})

	standing, ok := taskNotesFor("craig", schedKindStanding, "nightly")
	if !ok {
		t.Fatal("the owner cannot reach their own standing task's notes")
	}
	monitor, ok := taskNotesFor("craig", schedKindMonitor, "nightly")
	if !ok {
		t.Fatal("the owner cannot reach their own monitor's notes")
	}
	// Same name, same owner, two tasks: the kind is what keeps them apart.
	if standing.ns == monitor.ns {
		t.Errorf("a monitor and a standing agent with one name share a notes row: %s", standing.ns)
	}
	if _, ok := taskNotesFor("dana", schedKindStanding, "nightly"); ok {
		t.Error("another user reached this task's notes")
	}
	if _, ok := taskNotesFor("craig", schedKindStanding, "no-such-task"); ok {
		t.Error("a task that does not exist resolved to a notes row")
	}
}

// The owner's Save and the model's update_notes go through ONE write path, so
// the cap, the section splice and the refusal are the same sentence whoever is
// asking. Two implementations is how they come to disagree about a limit.
func TestTheOwnerAndTheAgentWriteTheSameWay(t *testing.T) {
	root := pinRootDB(t)
	SaveStandingAgent(root, StandingAgent{Owner: "craig", Name: "nightly", AgentID: "a1"})
	tn, ok := taskNotesFor("craig", schedKindStanding, "nightly")
	if !ok {
		t.Fatal("lookup")
	}
	if _, err := tn.tool()[0].Handler(t.Context(), map[string]any{"text": strings.Repeat("x", OperatingNotesCap+1)}); err == nil {
		t.Error("an over-cap write was accepted")
	}
	if _, err := tn.tool()[0].Handler(t.Context(), map[string]any{"section": "in flight", "text": "export is half uploaded"}); err != nil {
		t.Fatalf("section write: %v", err)
	}
	if got := tn.load().Text; !strings.Contains(got, "## in flight") {
		t.Errorf("the section did not land as a heading the owner can see: %q", got)
	}
}

// The Goals page reaches the same notes as the Scheduler. It is read-only about
// the schedule itself, deliberately, but what a goal's runs have worked out is
// the question that page asks: so it carries the one action, and it has to
// carry the kind with it for the same reason the Scheduler does.
func TestGoalRowsCanReachTheirTasksNotes(t *testing.T) {
	T, _, user := newTestOrchestrate(t)
	root := pinRootDB(t)
	SaveStandingAgent(root, StandingAgent{
		Owner: user, Name: "nightly", AgentID: "a1", Until: "the backlog is empty",
	})
	SaveEventMonitor(root, EventMonitor{
		Owner: user, Name: "pr-12", Kind: EventKindWatch, Until: "the PR is merged",
	})

	w := httptest.NewRecorder()
	T.handleConsoleGoals(w, asUser(httptest.NewRequest(http.MethodGet, "/api/console/goals", nil), user))
	if w.Code != http.StatusOK {
		t.Fatalf("goals: %d %s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected both goals listed, got %d: %s", len(rows), w.Body.String())
	}
	for _, row := range rows {
		if row["_notes"] != true {
			t.Errorf("goal %q offers no way to read what its runs learned", row["goal"])
		}
		// The pair has to RESOLVE, not merely be present: a row carrying the
		// wrong kind for its id is the failure this field exists to prevent,
		// and it looks identical in the JSON.
		kind, _ := row["_kind"].(string)
		id, _ := row["_id"].(string)
		if _, ok := taskNotesFor(user, kind, id); !ok {
			t.Errorf("goal %q carries a kind/id pair that resolves to no notes row: %q %q", row["goal"], kind, id)
		}
	}
}
