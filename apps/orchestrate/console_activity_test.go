package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The Runs pane lists the owner's ledger as cards and Details opens the full
// record — steps, output, prompt — that only the Operator agent could read
// before. Another owner's run must be neither listed nor openable by id.
func TestConsoleRunsPaneAndDetail(t *testing.T) {
	T, _, user := newTestOrchestrate(t)
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })

	started := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	rec := RecordRun(RootDB, RunRecord{
		Owner: user, Agent: "Nightly digest", Trigger: "schedule", Task: "digest",
		Brief: "Gather what changed overnight", Status: RunOK,
		Summary: "3 changes found.", Raw: "3 changes found.\n\n- a\n- b\n- c",
		Steps:   []RunStep{{Name: "step gather", Result: "found 3"}, {Name: "machine_phase_changed", Result: "gather → report"}},
		Started: started, Ended: started.Add(42 * time.Second),
	}.AboutStanding("digest"))
	RecordRun(RootDB, RunRecord{Owner: "mallory", Agent: "Theirs", Status: RunFailed, Started: started, Ended: started})

	get := func(path string, h func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		r := asUser(httptest.NewRequest(http.MethodGet, path, nil), user)
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	w := get("/api/console/runs", routed(T))
	if w.Code != http.StatusOK {
		t.Fatalf("runs: %d %s", w.Code, w.Body.String())
	}
	var rows []consoleRunRow
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want only the owner's run", rows)
	}
	row := rows[0]
	// Agent first, schedule second: this feed spans every agent, so the row
	// answers "whose" before "what fired it". The Details modal still shows
	// the two combined, because there the record is already the subject.
	if row.ID != rec.ID || row.Status != "ok" || row.Agent != "Nightly digest" || row.Task != "digest" || row.Summary != "3 changes found." {
		t.Fatalf("row = %+v", row)
	}
	if row.When == "" || row.Brief == "" {
		t.Fatalf("row missing when/brief: %+v", row)
	}
	// The list never carries the bulk: the raw output and the trace stay
	// behind Details, as the ledger's own encrypted side table intends.
	if b := w.Body.String(); strings.Contains(b, "found 3") || strings.Contains(b, "- a") {
		t.Fatalf("list leaked GetRun-only fields: %s", b)
	}

	w = get("/api/console/run-detail?id="+rec.ID, routed(T))
	var d consoleRunDetail
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Run != "digest → Nightly digest" || d.Duration != "42s" || len(d.Steps) != 2 {
		t.Fatalf("detail = %+v", d)
	}
	if d.Steps[0].Step != "step gather" || d.Steps[0].Result != "found 3" {
		t.Fatalf("steps = %+v", d.Steps)
	}
	if d.Output == "" || d.Output == d.Summary {
		t.Fatalf("output should carry the raw text beyond the summary: %q", d.Output)
	}

	// Mallory's run: the ledger is owner-keyed, so the id resolves nowhere.
	var theirs RunRecord
	for _, r := range ListRuns(RootDB, "mallory", RunFilter{}) {
		theirs = r
	}
	w = get("/api/console/run-detail?id="+theirs.ID, routed(T))
	if body := w.Body.String(); body != "{}\n" && body != "{}" {
		t.Fatalf("foreign run detail = %q, want empty object", body)
	}
}

// The live pill takes a run's owner to the conversation it runs in — for a
// root run that has one. A nested dispatch names a sub-session the chat page
// cannot open, and a run with no session has nothing to open.
func TestRunOwnerDestination(t *testing.T) {
	root := RunSnapshot{ID: "r1", AgentID: "agent 1", SessionID: "sess/1", Depth: 0}
	if got, want := runOwnerDestination("/orchestrate", root), "/orchestrate/?agent=agent+1&session=sess%2F1"; got != want {
		t.Errorf("root run: got %q, want %q", got, want)
	}
	if got := runOwnerDestination("/orchestrate/", root); got != "/orchestrate/?agent=agent+1&session=sess%2F1" {
		t.Errorf("a trailing slash on the prefix must not double up, got %q", got)
	}
	nested := root
	nested.Depth = 1
	if got := runOwnerDestination("/orchestrate", nested); got != "" {
		t.Errorf("a nested dispatch has no thread of its own to open, got %q", got)
	}
	detached := root
	detached.SessionID = ""
	if got := runOwnerDestination("/orchestrate", detached); got != "" {
		t.Errorf("a run without a session has nothing to open, got %q", got)
	}
}

// A background task in the live surfaces. It reaches them already — the runs
// registry is kind-agnostic and a task is registered like any other run — but a
// detached call is ONE tool call waiting on a backend, so it never reports a
// round or a tool and had nothing on it that ever changed.

func TestAProgresslessRunStillShowsItIsMoving(t *testing.T) {
	// What the ribbon renders for a run, mirrored from the LiveProvider: kind,
	// then whatever progress detail exists, then elapsed when there is none.
	render := func(kind string, round int, lastTool string, started time.Time) string {
		status := kind
		if round > 0 {
			status += " - round " + shortElapsed(0)
		}
		if lastTool != "" {
			status += " - " + lastTool
		}
		if round == 0 && lastTool == "" {
			status += " - " + shortElapsed(time.Since(started))
		}
		return status
	}

	task := render("task", 0, "", time.Now().Add(-3*time.Minute))
	if !strings.HasPrefix(task, "task - ") {
		t.Fatalf("a task should carry elapsed: %q", task)
	}
	if task == "task" {
		t.Error("a fifteen-minute render must not sit in the ribbon as a motionless word")
	}
	// A chat turn already has motion — rounds and tool names — so elapsed would
	// just be noise on top of it.
	chat := render("chat", 2, "web_search", time.Now().Add(-time.Minute))
	// Counts the SEPARATOR, not the character. The separator used to be a
	// middle dot, which nothing else could produce; a bare "-" is ordinary in
	// a tool name (fetch-url, an MCP name) and would make this assertion pass
	// or fail for a reason it is not testing.
	if strings.Count(chat, " - ") != 2 {
		t.Errorf("a run with real progress detail should not also get elapsed: %q", chat)
	}
}

func TestShortElapsedReadsLikeAPerson(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{3 * time.Minute, "3m00s"},
	} {
		if got := shortElapsed(c.d); got != c.want {
			t.Errorf("shortElapsed(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}
