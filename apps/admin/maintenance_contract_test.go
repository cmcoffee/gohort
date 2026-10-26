package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the Maintenance section gets back, through the real routes and the
// gate in front of them: the passes (all, and one section's), running one and
// what its row then says, where a pass has got to and how it ended, stopping
// one, the migration markers, and the scheduled tasks with deleting one. Each
// as an administrator, as a user who is not one, by the wrong method and with
// a missing key. The registries are the process's, so only this test's pass
// and task are recorded. Compared with testdata/maintenance_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestMaintenanceRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = adb
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	// A pass's progress and outcome are kept process-wide, past the test: a
	// key of this run's own, recorded as ct.tidy, starts with neither.
	key := fmt.Sprintf("ct.tidy-%d", time.Now().UnixNano())
	RegisterMaintenanceFunc("Housekeeping", key, "Tidy the test's things", "Counts three.", func(ctx context.Context) int {
		ReportMaintenanceProgress(ctx, "2 of 3")
		return 3
	})
	PreInitScheduler()
	taskID, err := ScheduleTask("ct.contract", map[string]string{"what": "a test task"}, time.Now().Add(24*365*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnscheduleTask(taskID) })

	aList := func(out string) string {
		var rows []json.RawMessage
		if json.Unmarshal([]byte(out), &rows) == nil {
			return "a list"
		}
		return out
	}
	ct := &contract{
		dates:   true,
		rewrite: strings.NewReplacer(taskID, "<task>", key, "ct.tidy"),
		// Rows are kept after the masks: this run's pass reads as ct.tidy.
		keep: func(row string) bool {
			return strings.Contains(row, `"Key":"ct.tidy"`) || strings.Contains(row, `"kind":"ct.`)
		},
	}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) {
		ct.shape = nil
		// The markers are every app's, from every run of this process.
		if strings.HasPrefix(path, "/api/migrations") {
			ct.shape = aList
		}
		ct.run(mux, adb, user, method, path, body)
	}

	for _, p := range []string{"/api/maintenance", "/api/maintenance?group=Housekeeping", "/api/maintenance/progress?key=" + key,
		"/api/migrations", "/api/scheduled-tasks"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
	}
	do("alice", "POST", "/api/maintenance?key="+key, "")
	do("root", "POST", "/api/maintenance", "")
	do("root", "POST", "/api/maintenance?key=ct.nope", "")
	do("root", "POST", "/api/maintenance?key="+key, "")
	do("root", "GET", "/api/maintenance?group=Housekeeping", "")
	do("root", "GET", "/api/maintenance?group=Reports", "")
	do("root", "GET", "/api/maintenance/progress?key="+key, "")
	do("root", "GET", "/api/maintenance/progress?key=ct.nope", "")

	do("alice", "POST", "/api/maintenance/cancel?key="+key, "")
	do("root", "GET", "/api/maintenance/cancel?key="+key, "")
	do("root", "POST", "/api/maintenance/cancel?key="+key, "")

	do("alice", "DELETE", "/api/scheduled-tasks?id="+taskID, "")
	do("root", "DELETE", "/api/scheduled-tasks", "")
	do("root", "DELETE", "/api/scheduled-tasks?id="+taskID, "")
	do("root", "GET", "/api/scheduled-tasks", "")

	ct.check(t, "testdata/maintenance_contract.golden")
}
