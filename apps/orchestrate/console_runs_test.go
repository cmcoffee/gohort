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

	w := get("/api/console/runs", T.handleConsoleRuns)
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
	if row.ID != rec.ID || row.Status != "ok" || row.Run != "digest → Nightly digest" || row.Summary != "3 changes found." {
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

	w = get("/api/console/run-detail?id="+rec.ID, T.handleConsoleRunDetail)
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
	w = get("/api/console/run-detail?id="+theirs.ID, T.handleConsoleRunDetail)
	if body := w.Body.String(); body != "{}\n" && body != "{}" {
		t.Fatalf("foreign run detail = %q, want empty object", body)
	}
}
