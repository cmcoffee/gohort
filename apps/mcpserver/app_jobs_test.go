package mcpserver

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/apps/customapps"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// slowApp stands in for user-a's app "lab": lab_quick answers at once, and
// lab_wait runs until released or stopped, printing a line as it starts.
type slowApp struct {
	mu       sync.Mutex
	release  chan struct{}
	stopped  int
	finished int
}

func jobsFixture(t *testing.T) (*MCPServer, *slowApp) {
	t.Helper()
	prevAuth, prevRoot, prevApps := AuthDB, RootDB, offeringApps
	prevWait, prevResult, prevCap := appWait, resultWait, appJobCap
	root := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(root, "user-a", "pw-a-123", false)
	AuthDB = func() Database { return root }
	RootDB = root
	appWait, resultWait = 60*time.Millisecond, 60*time.Millisecond
	s := &slowApp{release: make(chan struct{})}
	offeringApps = func(user string) []customapps.OfferingApp {
		if user != "user-a" {
			return nil
		}
		return []customapps.OfferingApp{{Slug: "lab", Name: "Lab", Owner: "user-a", Tools: []AgentToolDef{
			{Tool: Tool{Name: "lab_quick", Description: "Answer at once."},
				Handler: func(_ context.Context, args map[string]any) (string, error) {
					return "quick:" + strings.TrimSpace(asString(args["text"])), nil
				}},
			{Tool: Tool{Name: "lab_wait", Description: "Run until released."},
				Handler: func(ctx context.Context, args map[string]any) (string, error) {
					customappsHear(ctx, "warming up "+asString(args["tag"]))
					select {
					case <-s.release:
						s.mu.Lock()
						s.finished++
						s.mu.Unlock()
						return "done:" + asString(args["tag"]), nil
					case <-ctx.Done():
						s.mu.Lock()
						s.stopped++
						s.mu.Unlock()
						return "", ctx.Err()
					}
				}},
		}}}
	}
	t.Cleanup(func() {
		select {
		case <-s.release:
		default:
			close(s.release)
		}
		AuthDB, RootDB, offeringApps = prevAuth, prevRoot, prevApps
		appWait, resultWait, appJobCap = prevWait, prevResult, prevCap
		appJobs.Lock()
		appJobs.m = map[string]*appJob{}
		appJobs.Unlock()
	})
	app := &MCPServer{}
	app.DB = root.Bucket("mcpserver")
	return app, s
}

// customappsHear says a line as an action's script would, to whoever the
// call's context names (customapps.WithCallProgress).
func customappsHear(ctx context.Context, line string) {
	customapps.HearCallLine(ctx, line)
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// callText is a tools/call's text, with "ERR " in front of an error result.
func callText(resp map[string]any) string {
	res, _ := resp["result"].(map[string]any)
	var b strings.Builder
	if e, _ := res["isError"].(bool); e {
		b.WriteString("ERR ")
	}
	parts, _ := res["content"].([]any)
	for _, p := range parts {
		if m, ok := p.(map[string]any); ok {
			s, _ := m["text"].(string)
			b.WriteString(s)
		}
	}
	return b.String()
}

var jobIDRE = regexp.MustCompile(`job ([0-9a-f]{12})`)

// An app's call over MCP that runs long answers with its job instead of
// holding the request, and the app's result tool collects it once it ends.
// A quick call answers as before. The result tool is listed beside the app's
// tools, on the app's own address and on the general one.
func TestALongAppCallAnswersWithItsJob(t *testing.T) {
	app, s := jobsFixture(t)
	key := MintAccountTokenScoped("user-a", "claude", &TokenScope{Features: []string{MCPFeatureKey}}).Token
	for _, path := range []string{"/app/lab/", "/"} {
		if got := toolNames(rpcTo(app, path, key, "tools/list", map[string]any{})); !contains(got, "lab_result") {
			t.Errorf("%s lists %v, without lab_result", path, got)
		}
	}
	if got := callText(rpcTo(app, "/app/lab/", key, "tools/call", map[string]any{"name": "lab_quick", "arguments": map[string]any{"text": "hi"}})); got != "quick:hi" {
		t.Errorf("a quick call answered %q", got)
	}
	first := callText(rpcTo(app, "/app/lab/", key, "tools/call", map[string]any{"name": "lab_wait", "arguments": map[string]any{"tag": "A"}}))
	m := jobIDRE.FindStringSubmatch(first)
	if m == nil || !strings.Contains(first, "Still working") || !strings.Contains(first, "lab_result") || !strings.Contains(first, "warming up A") {
		t.Fatalf("a long call answered %q, want its job, its latest line and the result tool", first)
	}
	job := m[1]
	if got := callText(rpcTo(app, "/app/lab/", key, "tools/call", map[string]any{"name": "lab_result", "arguments": map[string]any{"job": job}})); !strings.Contains(got, "Still working") {
		t.Errorf("collecting a running job answered %q", got)
	}
	close(s.release)
	if got := callText(rpcTo(app, "/", key, "tools/call", map[string]any{"name": "lab_result", "arguments": map[string]any{"job": job}})); got != "done:A" {
		t.Errorf("collecting the ended job on the general address answered %q", got)
	}
	if got := callText(rpcTo(app, "/app/lab/", key, "tools/call", map[string]any{"name": "lab_result", "arguments": map[string]any{"job": job}})); got != "done:A" {
		t.Errorf("an ended job's answer is not kept: %q", got)
	}
}

// A client that goes away does not stop the run it asked for.
func TestAnAppCallOutlivesItsClient(t *testing.T) {
	_, s := jobsFixture(t)
	tok := MintAccountTokenScoped("user-a", "claude", &TokenScope{Features: []string{MCPFeatureKey}})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, _, err := callAppTool(ctx, &tok, "lab", "lab_wait", map[string]any{"tag": "B"})
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-errc; err == nil {
		t.Error("the call whose client left answered as if it had waited")
	}
	close(s.release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		fin, stop := s.finished, s.stopped
		s.mu.Unlock()
		if fin == 1 {
			break
		}
		if stop > 0 || time.Now().After(deadline) {
			t.Fatalf("the run ended with its client: finished=%d stopped=%d", fin, stop)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stop: true stops a running job; the job then answers that it was stopped.
// Another key never reads or stops a job it did not start.
func TestAnAppJobCanBeStoppedByItsKeyAlone(t *testing.T) {
	app, s := jobsFixture(t)
	key := MintAccountTokenScoped("user-a", "claude", &TokenScope{Features: []string{MCPFeatureKey}}).Token
	other := MintAccountTokenScoped("user-a", "desktop", &TokenScope{Features: []string{MCPFeatureKey}}).Token
	m := jobIDRE.FindStringSubmatch(callText(rpcTo(app, "/app/lab/", key, "tools/call", map[string]any{"name": "lab_wait", "arguments": map[string]any{"tag": "C"}})))
	if m == nil {
		t.Fatal("no job")
	}
	if got := callText(rpcTo(app, "/app/lab/", other, "tools/call", map[string]any{"name": "lab_result", "arguments": map[string]any{"job": m[1], "stop": true}})); !strings.HasPrefix(got, "ERR ") || !strings.Contains(got, "no job") {
		t.Errorf("another key reached the job: %q", got)
	}
	if got := callText(rpcTo(app, "/app/lab/", key, "tools/call", map[string]any{"name": "lab_result", "arguments": map[string]any{"job": m[1], "stop": true}})); !strings.Contains(got, "Stopped lab_wait") {
		t.Errorf("stopping answered %q", got)
	}
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped != 1 {
		t.Errorf("the run was not stopped (stopped=%d)", stopped)
	}
	if got := callText(rpcTo(app, "/app/lab/", key, "tools/call", map[string]any{"name": "lab_result", "arguments": map[string]any{"job": m[1]}})); !strings.Contains(got, "was stopped") {
		t.Errorf("a stopped job answered %q", got)
	}
}

// A key has at most appJobCap calls running; the next is refused, naming them.
func TestAKeysRunningAppCallsAreCapped(t *testing.T) {
	app, _ := jobsFixture(t)
	appJobCap = 2
	key := MintAccountTokenScoped("user-a", "claude", &TokenScope{Features: []string{MCPFeatureKey}}).Token
	for i, tag := range []string{"1", "2"} {
		if got := callText(rpcTo(app, "/app/lab/", key, "tools/call", map[string]any{"name": "lab_wait", "arguments": map[string]any{"tag": tag}})); !strings.Contains(got, "Still working") {
			t.Fatalf("call %d answered %q", i, got)
		}
	}
	got := callText(rpcTo(app, "/app/lab/", key, "tools/call", map[string]any{"name": "lab_quick", "arguments": map[string]any{"text": "x"}}))
	if !strings.HasPrefix(got, "ERR ") || !strings.Contains(got, "already has 2 app calls running") || !strings.Contains(got, "lab_wait") {
		t.Errorf("a call over the cap answered %q", got)
	}
	other := MintAccountTokenScoped("user-a", "desktop", &TokenScope{Features: []string{MCPFeatureKey}}).Token
	if got := callText(rpcTo(app, "/app/lab/", other, "tools/call", map[string]any{"name": "lab_quick", "arguments": map[string]any{"text": "y"}})); got != "quick:y" {
		t.Errorf("another key was held to this key's cap: %q", got)
	}
}
