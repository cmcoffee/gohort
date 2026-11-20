package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// liveEntryAppPath decides whether a live entry can be gated at all: a
// non-empty result is a mount prefix the access check runs against, and ""
// means "no way back into an app", which the live pill reads as Monitor-only.
// Getting that wrong either leaks a deep link past the access check or sends
// a reachable session to the Monitor, so the derivation is pinned here.
func TestLiveEntryAppPath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry LiveEntry
		want  string
	}{
		{"path wins over url", LiveEntry{Path: "/servitor", URL: "/other/?reconnect=x"}, "/servitor"},
		{"bare path", LiveEntry{Path: "/servitor"}, "/servitor"},
		{"url with reconnect query", LiveEntry{URL: "/servitor/?reconnect=abc"}, "/servitor"},
		{"url without trailing slash", LiveEntry{URL: "/servitor"}, "/servitor"},
		{"nested url gates on the mount prefix", LiveEntry{URL: "/servitor/sub/page?reconnect=x"}, "/servitor"},
		{"url with fragment", LiveEntry{URL: "/servitor/#top"}, "/servitor"},

		// No route back. Agent runs from the runs registry and queued tasks
		// carry a label and a status but no owning page — the Monitor case.
		{"empty entry", LiveEntry{}, ""},
		{"label only", LiveEntry{ID: "r1", Label: "some agent run", App: "Scout"}, ""},
		{"root url", LiveEntry{URL: "/"}, ""},

		// Not gateable: an off-host URL can't be resolved to a local app, so
		// it must not be treated as one and silently pass the access check.
		{"absolute url", LiveEntry{URL: "https://example.com/servitor/"}, ""},
		{"relative url", LiveEntry{URL: "servitor/?reconnect=x"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveEntryAppPath(tc.entry); got != tc.want {
				t.Errorf("liveEntryAppPath(%+v) = %q, want %q", tc.entry, got, tc.want)
			}
		})
	}
}

// ResolveHref is what both the live pill and the Monitor table navigate to,
// so a wrong result here is a dead link on two surfaces at once.
func TestLiveEntryResolveHref(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry LiveEntry
		want  string
	}{
		{"explicit url wins", LiveEntry{ID: "s1", Path: "/servitor", URL: "/servitor/?reconnect=s1"}, "/servitor/?reconnect=s1"},
		{"path plus id", LiveEntry{ID: "s1", Path: "/servitor"}, "/servitor/?reconnect=s1"},
		{"path with trailing slash", LiveEntry{ID: "s1", Path: "/servitor/"}, "/servitor/?reconnect=s1"},
		{"id is escaped", LiveEntry{ID: "a b&c", Path: "/servitor"}, "/servitor/?reconnect=a+b%26c"},

		// Nothing to link to — the pill falls back to the live view and the
		// Monitor leaves the row inert.
		{"path without id", LiveEntry{Path: "/servitor"}, ""},
		{"id without path", LiveEntry{ID: "s1"}, ""},
		{"empty", LiveEntry{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.ResolveHref(); got != tc.want {
				t.Errorf("ResolveHref(%+v) = %q, want %q", tc.entry, got, tc.want)
			}
		})
	}
}

// A live entry may name a second place for its owner to go — the conversation
// the work is running in — beside the place everyone else may look. The
// substitution is the owner's alone, and an entry with no owner has none.
func TestLiveEntryOwnerDestination(t *testing.T) {
	base := LiveEntry{ID: "r1", Owner: "craig", URL: "/monitor?run=r1", OwnerURL: "/orchestrate/?agent=a1&session=s1"}

	e := base
	e.applyOwnerDestination("craig")
	if e.URL != "/orchestrate/?agent=a1&session=s1" {
		t.Errorf("the owner goes to the conversation, got %q", e.URL)
	}
	for _, viewer := range []string{"dana", ""} {
		e = base
		e.applyOwnerDestination(viewer)
		if e.URL != "/monitor?run=r1" {
			t.Errorf("viewer %q must keep the shared destination, got %q", viewer, e.URL)
		}
	}
	e = base
	e.Owner = ""
	e.applyOwnerDestination("craig")
	if e.URL != "/monitor?run=r1" {
		t.Errorf("an entry with no owner has no owner's destination, got %q", e.URL)
	}
	e = base
	e.OwnerURL = ""
	e.applyOwnerDestination("craig")
	if e.URL != "/monitor?run=r1" {
		t.Errorf("no OwnerURL means the owner goes where everyone goes, got %q", e.URL)
	}
}

// --- agent loops among the running work ---

// loopRowsOf is the loops listed here now for app.
func loopRowsOf(app string) []LiveEntry {
	var rows []LiveEntry
	for _, e := range loopLiveEntries() {
		if e.App == app {
			rows = append(rows, e)
		}
	}
	return rows
}

// rowsAtEachCall is a tool that keeps the rows listed for app each time it is
// called, and a loop of app that calls it twice and answers.
func rowsAtEachCall(app string, seen *[][]LiveEntry, during func(ctx context.Context)) (*AppCore, []AgentToolDef) {
	probe := AgentToolDef{
		Tool: Tool{Name: "probe", Description: "d"},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			if during != nil {
				during(ctx)
			}
			*seen = append(*seen, loopRowsOf(app))
			return "ok", nil
		},
	}
	core := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "probe"}}},
		{ToolCalls: []ToolCall{{ID: "c2", Name: "probe"}}},
		{Content: "done"},
	}}}
	core.SetAppName(app)
	return core, []AgentToolDef{probe}
}

// An agent loop that nothing lists is listed while it runs: for the owner
// its caller named, in the caller's words, under its app, with its last round
// and tools as the status and a Stop. It is gone when the loop returns.
func TestAnAgentLoopNothingListsIsListedWhileItRuns(t *testing.T) {
	var seen [][]LiveEntry
	core, tools := rowsAtEachCall("looptest", &seen, nil)
	ctx := ListLoopAs(context.Background(), "alice", "Filing notes")
	steps := 0
	if _, _, err := core.RunAgentLoop(ctx, []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{Tools: tools, MaxRounds: 4, OnStep: func(StepInfo) { steps++ }}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if steps != 3 {
		t.Errorf("the caller's own OnStep heard %d rounds, want 3", steps)
	}
	if len(seen) != 2 || len(seen[0]) != 1 || len(seen[1]) != 1 {
		t.Fatalf("one row while it runs, at each call: %v", seen)
	}
	row := seen[0][0]
	if row.Label != "Filing notes" || row.Owner != "alice" || row.App != "looptest" {
		t.Errorf("the row is the caller's words, for its owner, under its app: %+v", row)
	}
	if row.AdminOnly || row.PublicLabel {
		t.Errorf("a named loop's row is its owner's, masked for everyone else: %+v", row)
	}
	if row.CancelURL != loopStopPath+"?id="+row.ID {
		t.Errorf("the row stops at %q, want %q", row.CancelURL, loopStopPath+"?id="+row.ID)
	}
	if got := seen[1][0].Status; got != "round 1, probe" {
		t.Errorf("status after the first round: %q", got)
	}
	for _, e := range loopLiveEntries() {
		if e.ID == row.ID {
			t.Errorf("the row outlived the loop: %+v", e)
		}
	}

	// The same status for a loop whose caller watches no rounds itself.
	seen = nil
	core, tools = rowsAtEachCall("looptest", &seen, nil)
	if _, _, err := core.RunAgentLoop(ctx, []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{Tools: tools, MaxRounds: 4}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(seen) != 2 || len(seen[1]) != 1 || seen[1][0].Status != "round 1, probe" {
		t.Errorf("status with no OnStep of the caller's: %v", seen)
	}
}

// A loop run under work that is listed already adds no row, and neither does
// a loop started inside a listed loop: one row per piece of work.
func TestALoopUnderListedWorkAddsNoRow(t *testing.T) {
	var seen [][]LiveEntry
	core, tools := rowsAtEachCall("looptest", &seen, nil)
	if _, _, err := core.RunAgentLoop(ListedWork(context.Background()), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{Tools: tools, MaxRounds: 4}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(seen) != 2 || len(seen[0]) != 0 {
		t.Fatalf("a loop under listed work added a row: %v", seen)
	}

	var inner [][]LiveEntry
	seen = nil
	outer, outerTools := rowsAtEachCall("looptest", &seen, func(ctx context.Context) {
		in, inTools := rowsAtEachCall("looptest", &inner, nil)
		if _, _, err := in.RunAgentLoop(ctx, []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{Tools: inTools, MaxRounds: 4}); err != nil {
			t.Errorf("inner loop: %v", err)
		}
	})
	if _, _, err := outer.RunAgentLoop(ListLoopAs(context.Background(), "alice", "Outer"), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{Tools: outerTools, MaxRounds: 4}); err != nil {
		t.Fatalf("outer loop: %v", err)
	}
	if len(inner) != 4 {
		t.Fatalf("the inner loops ran %d calls, want 4", len(inner))
	}
	for _, rows := range inner {
		if len(rows) != 1 || rows[0].Label != "Outer" {
			t.Fatalf("a loop inside a listed loop is under its row, not one of its own: %v", rows)
		}
	}
}

// withLoopUsers signs up an administrator (ops) and two others.
func withLoopUsers(t *testing.T) {
	t.Helper()
	prev := AuthDB
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "ops", "pw", true)
	AuthSetUser(db, "alice", "pw", false)
	AuthSetUser(db, "dana", "pw", false)
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
}

// A loop whose caller named no owner is listed as its app's agent work, for
// administrators only; one with an owner and no label is its owner's, under
// the same words.
func TestALoopNobodyNamedIsListedForAdministrators(t *testing.T) {
	withLoopUsers(t)
	var seen [][]LiveEntry
	core, tools := rowsAtEachCall("looptest", &seen, nil)
	if _, _, err := core.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{Tools: tools, MaxRounds: 4}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(seen) == 0 || len(seen[0]) != 1 {
		t.Fatalf("an unnamed loop is still listed: %v", seen)
	}
	row := seen[0][0]
	if row.Label != "Looptest agent work" || row.Owner != "" || !row.AdminOnly || !row.PublicLabel {
		t.Errorf("an unnamed loop is its app's agent work, for administrators: %+v", row)
	}
	if got := ViewLive(asUser(t, "/api/live", "dana"), []LiveEntry{row}); len(got) != 0 {
		t.Errorf("someone who is not an administrator sees it: %v", got)
	}
	if got := ViewLive(asUser(t, "/api/live", "ops"), []LiveEntry{row}); len(got) != 1 || got[0].Label != "Looptest agent work" {
		t.Errorf("an administrator sees it as written: %v", got)
	}

	// A label with no owner is shown unmasked to administrators, so it is
	// the framework's words, not the caller's.
	seen = nil
	core, tools = rowsAtEachCall("looptest", &seen, nil)
	if _, _, err := core.RunAgentLoop(ListLoopAs(context.Background(), "", "a private question"), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{Tools: tools, MaxRounds: 4}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if row := seen[0][0]; row.Label != "Looptest agent work" || !row.AdminOnly {
		t.Errorf("a label with no owner: %+v", row)
	}

	seen = nil
	core, tools = rowsAtEachCall("looptest", &seen, nil)
	if _, _, err := core.RunAgentLoop(ListLoopAs(context.Background(), "alice", ""), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{Tools: tools, MaxRounds: 4}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if row := seen[0][0]; row.Label != "Looptest agent work" || row.Owner != "alice" || row.AdminOnly {
		t.Errorf("an owner with no label: %+v", row)
	}
}

// heldLoop starts a loop of app under ctx that waits on its model until
// stopped, and returns its row and what the loop returned.
func heldLoop(t *testing.T, app string, ctx context.Context) (LiveEntry, <-chan error) {
	t.Helper()
	core := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{{Wait: make(chan struct{})}}}}
	core.SetAppName(app)
	done := make(chan error, 1)
	go func() {
		_, _, err := core.RunAgentLoop(ctx, []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{MaxRounds: 2})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if rows := loopRowsOf(app); len(rows) == 1 {
			return rows[0], done
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the loop was never listed")
	return LiveEntry{}, nil
}

func stopLoop(t *testing.T, row LiveEntry, user string) int {
	w := httptest.NewRecorder()
	handleLoopStop(w, postAs(asUser(t, row.CancelURL, user)))
	return w.Code
}

// Stop on a listed loop is its owner's, or an administrator's when it has no
// owner; anyone else gets the same 404 as a loop that is not running, and the
// loop runs on. A stopped loop ends with its context cancelled, and its row
// goes.
func TestAListedLoopStopsForItsOwnerOnly(t *testing.T) {
	withLoopUsers(t)
	row, done := heldLoop(t, "loopstop", ListLoopAs(context.Background(), "alice", "Held"))
	w := httptest.NewRecorder()
	handleLoopStop(w, asUser(t, row.CancelURL, "alice"))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("a GET stops nothing, got %d", w.Code)
	}
	for _, who := range []string{"dana", "ops"} {
		if code := stopLoop(t, row, who); code != http.StatusNotFound {
			t.Errorf("%s stopped someone else's loop: %d", who, code)
		}
	}
	select {
	case err := <-done:
		t.Fatalf("the loop ended before its owner stopped it: %v", err)
	default:
	}
	if code := stopLoop(t, row, "alice"); code != http.StatusOK {
		t.Fatalf("the owner's Stop: %d", code)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("a stopped loop ends cancelled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not reach the loop")
	}
	if rows := loopRowsOf("loopstop"); len(rows) != 0 {
		t.Errorf("a stopped loop is still listed: %v", rows)
	}
	if code := stopLoop(t, row, "alice"); code != http.StatusNotFound {
		t.Errorf("Stop on a loop that has ended: %d", code)
	}

	row, done = heldLoop(t, "loopstop", context.Background())
	if code := stopLoop(t, row, "dana"); code != http.StatusNotFound {
		t.Errorf("someone who is not an administrator stopped an unnamed loop: %d", code)
	}
	if code := stopLoop(t, row, "ops"); code != http.StatusOK {
		t.Fatalf("an administrator's Stop on an unnamed loop: %d", code)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("a stopped loop ends cancelled, got %v", err)
	}
}

// The step status names each tool once, in the order called.
func TestALoopsStatusIsItsRoundAndTools(t *testing.T) {
	st := StepInfo{Round: 3, ToolCalls: []ToolCall{{Name: "read_doc"}, {Name: "store_fact"}, {Name: "read_doc"}}}
	if got := loopStepStatus(st); got != "round 3, read_doc, store_fact" {
		t.Errorf("got %q", got)
	}
	if got := loopStepStatus(StepInfo{Round: 1}); got != "round 1" {
		t.Errorf("a round with no tools: %q", got)
	}
	if strings.Contains(loopAppLabel(""), "  ") || loopAppLabel("") != "Agent work" {
		t.Errorf("no app: %q", loopAppLabel(""))
	}
}

// A pipeline run is listed (liveRuns), so an agent loop its work runs adds no
// row of its own.
func TestAPipelineRunsLoopsAddNoRowOfTheirOwn(t *testing.T) {
	var seen [][]LiveEntry
	s := runSurface(t, func(ctx context.Context, input string, _ map[string]string, _ PipelineSink) (string, error) {
		core, tools := rowsAtEachCall("looptest", &seen, nil)
		_, _, err := core.RunAgentLoop(ctx, []Message{{Role: "user", Content: input}}, AgentLoopConfig{Tools: tools, MaxRounds: 4})
		return "done", err
	})
	id, _ := startRun(t, s)
	if run := waitFinished(t, s, id); run.Err != "" {
		t.Fatalf("run: %s", run.Err)
	}
	if len(seen) != 2 || len(seen[0]) != 0 || len(seen[1]) != 0 {
		t.Errorf("a pipeline run's loop added a row: %v", seen)
	}
}

// Stop is served: the dashboard mounts the route the rows point at. Checked
// against the source, as the peer routes are, since a test binary never runs
// ServeDashboard.
func TestALoopsStopRouteIsMounted(t *testing.T) {
	src, err := os.ReadFile("dashboard.go")
	if err != nil {
		t.Skipf("cannot read dashboard.go: %v", err)
	}
	if !strings.Contains(string(src), "mux.HandleFunc(loopStopPath, handleLoopStop)") {
		t.Error("dashboard.go does not mount handleLoopStop at loopStopPath")
	}
}
