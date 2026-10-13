package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A reloaded thread draws a dispatched agent's calls inside the dispatch's
// card, as the live page did: saved calls carry their own id and their
// parent's, and are saved before the call they ran beneath, so the replay
// maps ids first and nests second. A call whose parent is not among them
// stays at the top, and calls with no ids replay as before.
func TestReplayNestsADispatchedAgentsCalls(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	panel := readRuntimeFile(t, "30_agent_loop_panel.js")
	fn := boundedRuntime(t, panel, "function persistedToolEntries(", "\n    }\n")
	harness := fn + `
function toolArgsSummary(a) { return a ? JSON.stringify(a) : ''; }
var top = persistedToolEntries('m1', [
  {name: 'web_search', label: '↳ [Researcher] web_search', result: 'r', id: 'c2', parent: 'c1'},
  {name: 'fetch_url', label: '↳ [Researcher] fetch_url', result: 'p', id: 'c3', parent: 'c1'},
  {name: 'agents', args: {action: 'run'}, result: 'done', id: 'c1'},
  {name: 'calculate', result: '4'},
  {name: 'orphan', result: 'x', id: 'c9', parent: 'gone'}
]);
var names = top.map(function(e) { return e.name; });
if (names.join(',') !== 'agents,calculate,orphan') throw new Error('top level: ' + names.join(','));
var kids = (top[0].children || []).map(function(e) { return e.name; });
if (kids.join(',') !== '↳ [Researcher] web_search,↳ [Researcher] fetch_url') throw new Error('nested: ' + kids.join(','));
if (top[0].output !== 'done' || top[0].children[0].output !== 'r') throw new Error('outputs lost');
var flat = persistedToolEntries('m2', [{name: 'a', result: '1'}, {name: 'b', result: '2'}]);
if (flat.length !== 2 || flat[0].children) throw new Error('calls without ids changed');
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "nest.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("replayed nesting:\n%s", out)
	}
}
