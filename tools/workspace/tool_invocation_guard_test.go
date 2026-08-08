// workspace(action="run") is the sandbox that shell-mode tools execute in,
// and it says so. The model drew the obvious next inference — that a
// registered tool can be invoked from there — and spent a confirmation-gated
// round finding out otherwise, twice, in two different syntaxes. Neither
// failure mentioned tools: one was "python: command not found", the other a
// TypeError from subprocess.
//
// The resemblance is real, so the guard names the right door rather than
// denying the resemblance.
package workspace

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func sessionWith(t *testing.T, custom []string, callable []string) *ToolSession {
	t.Helper()
	sess := &ToolSession{}
	for _, n := range custom {
		if err := sess.AppendTempTool(&TempTool{Name: n, Description: "x"}); err != nil {
			t.Fatalf("append %q: %v", n, err)
		}
	}
	sess.SetAvailableTools(callable)
	return sess
}

// Both observed shapes, plus the direct-call shape, from a tool whose schema
// the lazy split has not loaded yet.
func TestTheTwoShapesItActuallyTriedAreCaught(t *testing.T) {
	sess := sessionWith(t, []string{"get_top_stories"}, []string{"workspace", "web_search"})

	for _, cmd := range []string{
		`python -c "from tools import get_top_stories; print(get_top_stories(category='all'))"`,
		`python3 -c "subprocess.run([sys.executable, '-m', 'get_top_stories'])"`,
		`get_top_stories --category all`,
	} {
		msg := flagToolInvocation(cmd, sess)
		if msg == "" {
			t.Errorf("not caught: %s", cmd)
			continue
		}
		// The correction is only useful if it names the call that works.
		if !strings.Contains(msg, "load_tool") {
			t.Errorf("a lazy tool's correction must name load_tool:\n%s", msg)
		}
		if !strings.Contains(msg, "get_top_stories") {
			t.Errorf("the correction must name the tool:\n%s", msg)
		}
	}
}

// Already callable — no load_tool round-trip to suggest, just call it.
func TestALoadedToolIsToldToCallItDirectly(t *testing.T) {
	sess := sessionWith(t, []string{"get_top_stories"}, []string{"get_top_stories"})
	msg := flagToolInvocation(`python3 -m get_top_stories`, sess)
	if msg == "" {
		t.Fatal("a loaded tool invoked through the shell should still be corrected")
	}
	if strings.Contains(msg, "load_tool") {
		t.Errorf("nothing to load — this tool is already callable:\n%s", msg)
	}
}

// The fetch family genuinely IS reachable in the sandbox, as a PATH shim and
// as an import. Flagging it would break the documented iterate-and-test flow:
// a script under test calls fetch_url exactly as it will at dispatch.
func TestTheFetchFamilyIsNotFlagged(t *testing.T) {
	sess := sessionWith(t, nil, []string{"fetch_url", "fetch_via", "browse_page", "web_search"})
	for _, cmd := range []string{
		`python3 -c "from gohort import fetch_url; print(fetch_url('https://x.test'))"`,
		`fetch_url --url https://x.test`,
		`fetch_via --credential ts3_api --url /1/clientlist`,
		`browse_page https://x.test`,
	} {
		if msg := flagToolInvocation(cmd, sess); msg != "" {
			t.Errorf("%s\nwas refused: %s", cmd, msg)
		}
	}
}

// Ordinary shell must stay ordinary. Bare-word tool names (`workspace`) are
// excluded by the underscore rule; a file named after a tool is data.
func TestOrdinaryShellIsUntouched(t *testing.T) {
	sess := sessionWith(t, []string{"get_top_stories"}, []string{"workspace", "web_search", "date_math"})
	for _, cmd := range []string{
		`ls -la`,
		`cd workspace && cat notes.txt`,
		`cat get_top_stories.py`,
		`python3 ./get_top_stories.py`,
		`python3 tools/get_top_stories.py --dry-run`,
		`echo "my_local_helper ran"`,
		`grep -r "def main" .`,
	} {
		if msg := flagToolInvocation(cmd, sess); msg != "" {
			t.Errorf("%s\nwas refused: %s", cmd, msg)
		}
	}
}

// A registered (non-custom) tool is just as unreachable from the shell.
func TestARegisteredToolIsCaughtToo(t *testing.T) {
	sess := sessionWith(t, nil, []string{"web_search"})
	msg := flagToolInvocation(`web_search --query "news today"`, sess)
	if msg == "" || !strings.Contains(msg, "web_search") {
		t.Errorf("web_search is not a shell command either, got: %q", msg)
	}
}

// A nil session must not make the guard refuse everything.
func TestNoSessionMeansNoOpinion(t *testing.T) {
	if msg := flagToolInvocation(`python3 -m get_top_stories`, nil); msg != "" {
		t.Errorf("with no catalog there is nothing to assert: %s", msg)
	}
}
