package orchestrate

// A schedule whose target is a pipeline.
//
// Before this, "run the research pipeline nightly" needed a wrapper agent
// whose entire job was to decide to call run_<pipeline> — a model call
// spent reaching a foregone conclusion, and an agent in somebody's fleet
// that was pure plumbing.

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAScheduleNamesExactlyOneTarget(t *testing.T) {
	// Both is refused rather than resolved by precedence: whichever the
	// runner checked first would be the one that ran, forever, and which
	// one depends on the order of an if.
	both := StandingAgent{Name: "n", Owner: "u", AgentID: "ag", PipelineID: "pl"}
	if err := both.ValidateTarget(); err == nil {
		t.Error("naming an agent AND a pipeline should be refused")
	} else if !strings.Contains(err.Error(), "an agent and a pipeline") {
		t.Errorf("the refusal should name what it was given: %v", err)
	}
	// The third target joins the same rule, and the message names which
	// pair it was handed rather than saying "not both" of three.
	if err := (StandingAgent{Name: "n", Owner: "u", PipelineID: "pl", MachineID: "m"}).ValidateTarget(); err == nil {
		t.Error("naming a pipeline AND a machine should be refused")
	} else if !strings.Contains(err.Error(), "a pipeline and a machine") {
		t.Errorf("the refusal should name what it was given: %v", err)
	}
	// Neither is refused too: a schedule that fires nothing still fires,
	// recording an attention entry every time — a job that exists to fail.
	if err := (StandingAgent{Name: "n", Owner: "u"}).ValidateTarget(); err == nil {
		t.Error("a schedule with no target should be refused")
	}
	for _, ok := range []StandingAgent{
		{Name: "n", Owner: "u", AgentID: "ag"},
		{Name: "n", Owner: "u", PipelineID: "pl"},
		{Name: "n", Owner: "u", MachineID: "m"},
	} {
		if err := ok.ValidateTarget(); err != nil {
			t.Errorf("%+v should be valid: %v", ok, err)
		}
	}
	if !(StandingAgent{PipelineID: "pl"}).TargetsPipeline() {
		t.Error("a pipeline schedule should say so")
	}
	if (StandingAgent{AgentID: "ag"}).TargetsPipeline() {
		t.Error("an agent schedule is not a pipeline one")
	}
}

// A target that is gone should be visible where somebody is looking at
// the schedule, not discovered when it fires. The pipeline case was
// unchecked — not a false positive (agentExists answers true for an
// empty id, deliberately) but a missing check, which is the quieter half
// of the same problem.
func TestAScheduleWhosePipelineIsGoneReportsIt(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	// standingAgentDependencyError resolves through the global RootDB, the
	// same way agentExists does — point it at this test's store.
	prev := RootDB
	RootDB = app.DB
	t.Cleanup(func() { RootDB = prev })

	def := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Nightly",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "p"}}})

	live := StandingAgent{Name: "nightly", Owner: user, PipelineID: def.ID}
	if got := standingAgentDependencyError(live); got != "" {
		t.Errorf("a schedule pointing at a stored pipeline is healthy: %q", got)
	}
	gone := StandingAgent{Name: "nightly", Owner: user, PipelineID: "pl-vanished"}
	got := standingAgentDependencyError(gone)
	// Pinned as properties, not phrasing: it must name the pipeline that is
	// missing and say it is gone. The wording gained a second case when a
	// schedule could fire a pipeline somebody SHARED — deleted and un-shared
	// are the same silence to the resolver and different problems to the reader.
	if !strings.Contains(got, "pl-vanished") || !strings.Contains(got, "deleted") {
		t.Errorf("a missing pipeline should be reported by id: %q", got)
	}
	// And an agent schedule still reports the agent, not the pipeline.
	if got := standingAgentDependencyError(StandingAgent{Owner: user, AgentID: "ag-vanished"}); !strings.Contains(got, "agent was deleted") {
		t.Errorf("the agent case should be unchanged: %q", got)
	}
}

// The console has to offer the right KIND of target, and accept only
// that kind. A relink picker listing agents for a schedule that runs a
// pipeline is worse than no picker: every choice in it gets refused.
func TestRelinkOffersAndAcceptsTheRightTargetKind(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	prev := RootDB
	RootDB = app.DB
	t.Cleanup(func() { RootDB = prev })

	good := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Nightly",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "p"}}})
	agent, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Wren", OrchestratorPrompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	SaveStandingAgent(RootDB, StandingAgent{
		Name: "nightly", Owner: user, PipelineID: "pl-gone", Broken: true,
		BrokenReason: "its target pipeline was deleted (id pl-gone)"})
	SaveStandingAgent(RootDB, StandingAgent{
		Name: "morning", Owner: user, AgentID: "ag-gone", Broken: true})

	// The picker, asked about the PIPELINE schedule, offers pipelines.
	r := httptest.NewRequest("GET", "/api/console/agent-options?row=nightly", nil)
	w := httptest.NewRecorder()
	app.handleConsoleAgentOptions(w, asUser(r, user))
	body := w.Body.String()
	if !strings.Contains(body, good.ID) || !strings.Contains(body, "Nightly") {
		t.Errorf("the pipeline schedule's picker should list pipelines:\n%s", body)
	}
	if strings.Contains(body, agent.ID) {
		t.Errorf("it must not offer agents — every one would be refused:\n%s", body)
	}
	// Asked about the AGENT schedule, it offers agents, exactly as before.
	r = httptest.NewRequest("GET", "/api/console/agent-options?row=morning", nil)
	w = httptest.NewRecorder()
	app.handleConsoleAgentOptions(w, asUser(r, user))
	if !strings.Contains(w.Body.String(), agent.ID) {
		t.Errorf("the agent schedule's picker should still list agents:\n%s", w.Body.String())
	}

	// Relink accepts a pipeline for the pipeline schedule, and clears broken.
	r = httptest.NewRequest("POST", "/api/console/agents/relink?id=nightly&value="+good.ID, nil)
	w = httptest.NewRecorder()
	app.handleConsoleAgentRelink(w, asUser(r, user))
	if w.Code != 204 {
		t.Fatalf("relink: %d %s", w.Code, w.Body.String())
	}
	sa, _ := GetStandingAgent(RootDB, user, "nightly")
	if sa.PipelineID != good.ID || sa.Broken {
		t.Errorf("the schedule was not repointed: %+v", sa)
	}
	// And it must NOT have grown an agent target alongside it — the one
	// state ValidateTarget exists to refuse.
	if sa.AgentID != "" {
		t.Errorf("relink set an agent on a pipeline schedule: %q", sa.AgentID)
	}
	if err := sa.ValidateTarget(); err != nil {
		t.Errorf("the repointed schedule is invalid: %v", err)
	}

	// An agent id offered to a pipeline schedule is refused rather than
	// stored — this is what would have happened on every pick from the
	// old picker.
	r = httptest.NewRequest("POST", "/api/console/agents/relink?id=nightly&value="+agent.ID, nil)
	w = httptest.NewRecorder()
	app.handleConsoleAgentRelink(w, asUser(r, user))
	if w.Code != 400 {
		t.Errorf("an agent is not a pipeline: %d %s", w.Code, w.Body.String())
	}

	// The schedules rail row says WHAT it runs.
	r = httptest.NewRequest("GET", "/api/schedules", nil)
	w = httptest.NewRecorder()
	app.handleSchedules(w, asUser(r, user))
	if !strings.Contains(w.Body.String(), "pipeline · Nightly") {
		t.Errorf("the row should name the pipeline it fires:\n%s", w.Body.String())
	}
}

// Deleting the target. The agent half of this rule has always existed:
// delete an agent and the schedules that run it are marked broken, kept
// and relinkable. Without the pipeline half, deleting a pipeline left its
// schedules looking healthy until they fired.
func TestDeletingAPipelineMarksTheSchedulesThatRunIt(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	prev := RootDB
	RootDB = app.DB
	t.Cleanup(func() { RootDB = prev })
	wireDependencyGuards()

	def := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Nightly",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "p"}}})
	other := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Keep me",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "p"}}})
	SaveStandingAgent(RootDB, StandingAgent{Name: "nightly", Owner: user, PipelineID: def.ID})
	SaveStandingAgent(RootDB, StandingAgent{Name: "keeper", Owner: user, PipelineID: other.ID})
	SaveStandingAgent(RootDB, StandingAgent{Name: "agenty", Owner: user, AgentID: "ag-1"})

	DeletePipelineDef(udb, def.ID)

	broke, _ := GetStandingAgent(RootDB, user, "nightly")
	if !broke.Broken {
		t.Fatal("the schedule that ran it should be marked broken immediately, not at its next fire")
	}
	// Named, because a reason reading `runs deleted pipeline ""` helps
	// nobody — the name is gone from storage by the time the hook fires,
	// so it travels as an argument.
	if !strings.Contains(broke.BrokenReason, `"Nightly"`) {
		t.Errorf("the reason should name what was deleted: %q", broke.BrokenReason)
	}
	// KEPT, not deleted: the whole posture is broken-and-relinkable.
	if _, still := GetStandingAgent(RootDB, user, "nightly"); !still {
		t.Error("a broken schedule is kept so it can be repointed")
	}
	// And nothing else was touched.
	if keeper, _ := GetStandingAgent(RootDB, user, "keeper"); keeper.Broken {
		t.Error("a schedule running a different pipeline was marked broken")
	}
	if agenty, _ := GetStandingAgent(RootDB, user, "agenty"); agenty.Broken {
		t.Error("an agent schedule was marked broken by a pipeline deletion")
	}
}

// --- a schedule that fires a machine ------------------------------------

// The two refusals that earn the machine target its own runner, both made
// where somebody is reading rather than at whatever hour the schedule is
// armed for.
func TestAScheduledMachineMustBeOneThatRuns(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	converses := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Chatty", Start: "talk",
		Phases: []MachinePhase{{Name: "talk", Prompt: "hi", Resident: true}}})

	res := runStandingMachine(context.Background(), app, StandingAgent{
		Name: "nightly", Owner: user, MachineID: converses.ID,
	})
	if res.Status != RunAttention {
		t.Errorf("a conversational machine should be skipped with attention, got %v", res.Status)
	}
	if !strings.Contains(res.Summary, "converses rather than runs") {
		t.Errorf("the skip should say why: %s", res.Summary)
	}
}

// A machine can be SAVED half-built (the editor's whole posture), so a
// schedule armed against one still being written is the common case, not
// a defensive one.
func TestAScheduledMachineWithProblemsIsSkippedNotFired(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	// Unattended, but nothing finishes it.
	broken := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Spin", Start: "a", Unattended: true,
		Phases: []MachinePhase{
			{Name: "a", Prompt: "x", Next: "b"},
			{Name: "b", Prompt: "y", Next: "a"},
		}})

	res := runStandingMachine(context.Background(), app, StandingAgent{
		Name: "nightly", Owner: user, MachineID: broken.ID,
	})
	if res.Status != RunAttention {
		t.Errorf("a machine that will not run should be skipped, got %v", res.Status)
	}
	if !strings.Contains(res.Summary, "will not run yet") {
		t.Errorf("the skip should carry the first problem: %s", res.Summary)
	}
}

// A target that has been deleted or renamed is the schedule's problem to
// report, not something to fail silently at 3am.
func TestAScheduledMachineThatVanishedReportsItself(t *testing.T) {
	app, _, user := newTestOrchestrate(t)
	res := runStandingMachine(context.Background(), app, StandingAgent{
		Name: "nightly", Owner: user, MachineID: "no-such-machine",
	})
	if res.Status != RunAttention {
		t.Errorf("a missing target should be attention, got %v", res.Status)
	}
	if !strings.Contains(res.Summary, "no machine") {
		t.Errorf("the skip should name what it could not find: %s", res.Summary)
	}
}

// --- making a machine schedule from the page ----------------------------

func machineScheduleReq(t *testing.T, app *OrchestrateApp, user, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/console/machine-schedule/create", strings.NewReader(body))
	w := httptest.NewRecorder()
	app.handleConsoleMachineScheduleCreate(w, asUser(r, user))
	return w
}

func TestAMachineScheduleIsBuiltFromTheForm(t *testing.T) {
	def := MachineDef{ID: "m-1", Name: "Nightly", Unattended: true}

	sa, err := buildMachineSchedule("alice", "nightly digest", def, "what changed today", "", 60, "ag-1")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if sa.MachineID != "m-1" {
		t.Errorf("it should point at the machine, got %q", sa.MachineID)
	}
	if !sa.TargetsMachine() || sa.TargetsPipeline() || strings.TrimSpace(sa.AgentID) != "" {
		t.Errorf("exactly one target: %+v", sa)
	}
	if sa.IntervalSeconds != 3600 {
		t.Errorf("the cadence should land in seconds, got %d", sa.IntervalSeconds)
	}
	if sa.ReportAgentID != "ag-1" {
		t.Errorf("it should land on the rail it was made from, got %q", sa.ReportAgentID)
	}
	// A cron cadence instead.
	// The scheduler's own spelling: "{days} {HH:MM}". A cadence the modal
	// offers has to be one this accepts, which is why the modal offers
	// choices rather than a box to type one into.
	if sa, err := buildMachineSchedule("alice", "n", def, "", "daily 08:00", 0, ""); err != nil || sa.Cron != "daily 08:00" {
		t.Errorf("a cron cadence should survive: %+v / %v", sa, err)
	}
}

// The subject reaches the first step as {input}, so an empty one is
// defaulted rather than sent as nothing.
func TestAMachineScheduleWithNoSubjectUsesTheMachinesName(t *testing.T) {
	sa, err := buildMachineSchedule("alice", "n", MachineDef{ID: "m-1", Name: "Nightly", Unattended: true}, "", "", 30, "")
	if err != nil {
		t.Fatal(err)
	}
	if sa.Mission != "Nightly" {
		t.Errorf("an empty subject should fall back to the machine's name, got %q", sa.Mission)
	}
}

// The refusals happen where somebody is reading, not at four in the morning.
func TestAMachineScheduleRefusesWhatCannotRun(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	converses := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Chatty", Start: "talk",
		Phases: []MachinePhase{{Name: "talk", Prompt: "hi", Resident: true}}})
	broken := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Spin", Start: "a", Unattended: true,
		Phases: []MachinePhase{{Name: "a", Prompt: "x", Next: "b"}, {Name: "b", Prompt: "y", Next: "a"}}})

	cases := map[string]struct{ id, want string }{
		"a conversation": {converses.ID, "converses rather than runs"},
		"a broken run":   {broken.ID, "will not run yet"},
		"nothing at all": {"no-such-id", "no such machine"},
	}
	for what, c := range cases {
		w := machineScheduleReq(t, app, user, `{"name":"x-`+what[:3]+`","machine_id":"`+c.id+`","interval_minutes":60}`)
		if w.Code == 200 {
			t.Errorf("%s should be refused", what)
			continue
		}
		if !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s: the refusal should say why, got %s", what, w.Body.String())
		}
	}
}

// A cadence the scheduler cannot read is refused before anything is saved.
func TestAMachineScheduleNeedsACadenceItUnderstands(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Nightly", Start: "write", Unattended: true,
		Phases: []MachinePhase{{Name: "write", Prompt: "write it"}}})

	_ = app
	_ = def
	if _, err := buildMachineSchedule(user, "none", MachineDef{ID: "m", Name: "N", Unattended: true}, "", "", 0, ""); err == nil {
		t.Error("a schedule with no cadence should be refused")
	}
	if _, err := buildMachineSchedule(user, "bad", MachineDef{ID: "m", Name: "N", Unattended: true}, "", "whenever", 0, ""); err == nil {
		t.Error("an unreadable cron should be refused")
	}
}

// Only machines that can be scheduled are offered.
func TestMachineOptionsOfferOnlyRuns(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	SaveMachineDef(udb, MachineDef{Owner: user, Name: "Nightly", Unattended: true,
		Phases: []MachinePhase{{Name: "write", Prompt: "w"}}})
	SaveMachineDef(udb, MachineDef{Owner: user, Name: "Chatty",
		Phases: []MachinePhase{{Name: "talk", Prompt: "hi", Resident: true}}})

	r := httptest.NewRequest("GET", "/api/console/machine-options", nil)
	w := httptest.NewRecorder()
	app.handleConsoleMachineOptions(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Nightly") {
		t.Errorf("a machine that runs should be offered: %s", body)
	}
	if strings.Contains(body, "Chatty") {
		t.Errorf("a conversation cannot be scheduled: %s", body)
	}
}
