package customapps

import (
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// The host's live runs leave their record on the app: a data source as the
// page asked for it, an action with its whole ending (a reply the host refuses
// is the action failing), and whatever each one logged.
func TestLiveRunsLeaveARecord(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "tally", Name: "Tally", Owner: "alice", RecordKey: "id"}
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	runAppScript = func(j appscript.Job) (string, error) {
		if j.OnLog == nil {
			t.Fatalf("%s %q ran with nothing hearing its log()", j.Kind, j.Name)
		}
		j.OnLog("info", "ran "+j.Name)
		switch j.Name {
		case "counts":
			return "", errors.New("Traceback: ZeroDivisionError")
		case "bad_reply":
			return "not json at all {", nil
		}
		return `{"message":"ok"}`, nil
	}

	if _, err := runDataSource("alice", T.recordBase(spec, "alice"), spec, AppDataSource{Name: "counts"}, map[string]any{}, "alice"); err == nil {
		t.Fatal("the stand-in data source should fail")
	}
	if _, err := runActionAndPersist("alice", T.recordBase(spec, "alice"), T.recordBase(spec, "alice"), spec, AppAction{Name: "bad_reply"}, map[string]any{}, "alice", scriptRun{Interactive: true}); err == nil {
		t.Fatal("an action whose reply is not an object should fail")
	}
	if _, err := runActionAndPersist("alice", T.recordBase(spec, "alice"), T.recordBase(spec, "alice"), spec, AppAction{Name: "nightly"}, map[string]any{}, "alice", scriptRun{}); err != nil {
		t.Fatal(err)
	}

	runs := appscript.RecentRuns("alice", "tally")
	if len(runs) != 3 {
		t.Fatalf("three runs should be recorded, got %d: %+v", len(runs), runs)
	}
	nightly, reply, counts := runs[0], runs[1], runs[2]
	if counts.Kind != "data" || counts.OK || !strings.Contains(counts.Error, "ZeroDivisionError") || counts.Trigger != "page" {
		t.Errorf("a failed data source: %+v", counts)
	}
	if reply.Kind != "action" || reply.OK || !strings.Contains(reply.Error, "must print a JSON object") {
		t.Errorf("a refused reply is recorded as the action failing: %+v", reply)
	}
	if !nightly.OK || nightly.Trigger != "schedule" || len(nightly.Log) != 1 || nightly.Log[0] != "ran nightly" {
		t.Errorf("an unattended run, with its log line: %+v", nightly)
	}
}
