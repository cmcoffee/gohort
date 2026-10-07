package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A finished run can be put through a follow-up: a report written from it, a
// re-synthesis, a fold of its own follow-ups back into it. Each makes a new
// run linked to its parent, on the same runner as any run.

// followUpSurface is a run surface with two follow-ups whose work records what
// it was handed.
func followUpSurface(t *testing.T) (RunSurface, *followUpCalls) {
	t.Helper()
	calls := &followUpCalls{}
	s := runSurface(t, func(ctx context.Context, input string, _ map[string]string, sink PipelineSink) (string, error) {
		return "the answer to " + input, nil
	})
	s.FollowUps = []PipelineDef{
		{Name: "Write report", Description: "A briefing from the result", Stages: []PipelineStage{{Name: "w", Prompt: "{input}"}}},
		{Name: "Consolidate", Stages: []PipelineStage{{Name: "c", Prompt: "{input} {children}"}}},
	}
	s.FollowUpWork = func(def PipelineDef) RunWork {
		return func(ctx context.Context, input string, vars map[string]string, sink PipelineSink) (string, error) {
			calls.add(def.Name, input, vars)
			return def.Name + " of: " + input, nil
		}
	}
	return s, calls
}

type followUpCalls struct {
	mu  sync.Mutex
	got []struct {
		name, input string
		vars        map[string]string
	}
}

func (c *followUpCalls) add(name, input string, vars map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, struct {
		name, input string
		vars        map[string]string
	}{name, input, vars})
}

func (c *followUpCalls) last() (string, string, map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.got) == 0 {
		return "", "", nil
	}
	g := c.got[len(c.got)-1]
	return g.name, g.input, g.vars
}

// waitFinished waits for a stored run to stop running.
func waitFinished(t *testing.T, s RunSurface, id string) PipelineRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if run, ok := LoadPipelineRun(s.DB, s.User, s.OwnerID, id); ok && !run.Running {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s never finished", id)
	return PipelineRun{}
}

func followUp(s RunSurface, name, runID string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	(&AppCore{}).ServeRuns(rec, httptest.NewRequest(http.MethodPost, "/followup/"+name+"/"+runID, nil), s, "followup/"+name+"/"+runID)
	return rec
}

// The panel asks what a run can be put through.
func TestFollowUpsAreListedForThePanel(t *testing.T) {
	s, _ := followUpSurface(t)
	rec := httptest.NewRecorder()
	(&AppCore{}).ServeRuns(rec, httptest.NewRequest(http.MethodGet, "/followups", nil), s, "followups")
	var list []map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 2 || list[0]["name"] != "write_report" || list[0]["label"] != "Write report" || list[0]["title"] != "A briefing from the result" {
		t.Fatalf("follow-ups listed as %v", list)
	}
	none := runSurface(t, nil)
	rec = httptest.NewRecorder()
	(&AppCore{}).ServeRuns(rec, httptest.NewRequest(http.MethodGet, "/followups", nil), none, "followups")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("a surface with none answers an empty list, got %q", rec.Body.String())
	}
}

// A follow-up makes a new run from the finished one: its output in, linked
// back to it, and listed with the way back.
func TestAFollowUpMakesALinkedRunFromTheResult(t *testing.T) {
	s, calls := followUpSurface(t)
	parentID, _ := startRun(t, s)
	parent := waitFinished(t, s, parentID)

	rec := followUp(s, "write_report", parentID)
	if !strings.Contains(rec.Body.String(), "event: done") {
		t.Fatalf("the follow-up must stream to the end:\n%s", rec.Body.String())
	}
	name, input, vars := calls.last()
	if name != "Write report" || input != parent.Output {
		t.Errorf("follow-up ran %q over %q, want Write report over the parent's output %q", name, input, parent.Output)
	}
	if vars["{parent_input}"] != "go" {
		t.Errorf("{parent_input} must be what the parent was asked, got %q", vars["{parent_input}"])
	}
	var child PipelineRun
	for _, r := range ListPipelineRuns(s.DB, s.User, s.OwnerID) {
		if r.ParentID == parentID {
			child = r
		}
	}
	if child.ID == "" || child.FollowUp != "write_report" || !strings.HasPrefix(child.Title, "Write report: ") || child.Output != "Write report of: "+parent.Output {
		t.Fatalf("the follow-up run must be stored, linked and titled: %+v", child)
	}
	list := httptest.NewRecorder()
	(&AppCore{}).ServeRuns(list, httptest.NewRequest(http.MethodGet, "/sessions", nil), s, "sessions")
	if !strings.Contains(list.Body.String(), `"ParentID":"`+parentID+`"`) {
		t.Errorf("the sidebar row must carry ParentID, for the way back:\n%s", list.Body.String())
	}
}

// A consolidation reads what the earlier follow-ups produced.
func TestAFollowUpReadsTheRunsChildren(t *testing.T) {
	s, calls := followUpSurface(t)
	parentID, _ := startRun(t, s)
	waitFinished(t, s, parentID)
	followUp(s, "write_report", parentID)
	followUp(s, "consolidate", parentID)
	_, _, vars := calls.last()
	if !strings.Contains(vars["{children}"], "## Write report: ") || !strings.Contains(vars["{children}"], "Write report of:") {
		t.Errorf("{children} must carry each finished child under its title, got %q", vars["{children}"])
	}
}

// What cannot be followed up says why rather than starting a run that fails.
func TestAFollowUpRefusesWhatItCannotRun(t *testing.T) {
	s, _ := followUpSurface(t)
	SavePipelineRun(s.DB, s.User, PipelineRun{ID: "running12345", PipelineID: s.OwnerID, Title: "x", Running: true})
	SavePipelineRun(s.DB, s.User, PipelineRun{ID: "emptyout1234", PipelineID: s.OwnerID, Title: "x"})
	for _, c := range []struct {
		name, run string
		code      int
	}{
		{"write_report", "running12345", http.StatusConflict},
		{"write_report", "emptyout1234", http.StatusBadRequest},
		{"no_such_followup", "emptyout1234", http.StatusNotFound},
		{"write_report", "nobodysrun12", http.StatusNotFound},
	} {
		if got := followUp(s, c.name, c.run).Code; got != c.code {
			t.Errorf("%s on %s: %d, want %d", c.name, c.run, got, c.code)
		}
	}
	// Another user's run is not there to follow up.
	other := s
	other.User = "someone-else"
	parentID, _ := startRun(t, s)
	waitFinished(t, s, parentID)
	if got := followUp(other, "write_report", parentID).Code; got != http.StatusNotFound {
		t.Errorf("another user's run: %d, want 404", got)
	}
}

// A follow-up run interrupted by a restart resumes as that follow-up, not as
// the pipeline it belongs to.
func TestAFollowUpRunResumesAsItself(t *testing.T) {
	withQueue(t)
	s, calls := followUpSurface(t)
	s.Kind = "followtest"
	run := PipelineRun{ID: "child1234567", PipelineID: s.OwnerID, Title: "Write report: go", Date: time.Now(), Running: true, ParentID: "p1", FollowUp: "write_report"}
	SavePipelineRun(s.DB, s.User, run)
	QueueAdd(run.ID, "run:followtest", run.Title, runQueueParams{User: s.User, OwnerID: s.OwnerID, Input: "the result", FollowUp: "write_report"}, "")
	(&AppCore{}).RegisterRunRestore("followtest", func(user, ownerID, key string) (RunSurface, bool) { return s, true })
	QueueRestore()
	got := waitFinished(t, s, run.ID)
	if name, input, _ := calls.last(); name != "Write report" || input != "the result" || got.Output != "Write report of: the result" {
		t.Errorf("resumed as %q over %q (output %q), want the follow-up over its stored input", name, input, got.Output)
	}
}

// Follow-ups are checked when the pipeline is saved.
func TestFollowUpsAreCheckedOnSave(t *testing.T) {
	base := func(fus ...PipelineDef) PipelineDef {
		return PipelineDef{Name: "p", Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "x"}}, FollowUps: fus}
	}
	ok := PipelineDef{Name: "Report", Stages: []PipelineStage{{Name: "r", Kind: StageWorker, Prompt: "{input}"}}}
	if err := base(ok).Validate(); err != nil {
		t.Fatalf("a good follow-up refused: %v", err)
	}
	nested := ok
	nested.FollowUps = []PipelineDef{ok}
	for name, bad := range map[string]PipelineDef{
		"unnamed":   base(PipelineDef{Stages: ok.Stages}),
		"duplicate": base(ok, ok),
		"nested":    base(nested),
		"no stages": base(PipelineDef{Name: "Empty"}),
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s follow-up should be refused", name)
		}
	}
}
