package orchestrate

// The rule editor's markers, driven rather than read: a rule tied to two tools
// in the editor is a rule the server binds to both, and taking one off leaves
// the other. The editor used to hold one tool, so a second replaced the first.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheEditorTiesARuleToSeveralTools(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readFile(t, "assets/web_assets.html")
	const open, close = "          var G_BLOCK = 'block'", "          function gSlug(s) {"
	i, j := strings.Index(src, open), strings.Index(src, close)
	if i < 0 || j < i {
		t.Fatal("the rule marker helpers have moved; this test no longer reads them")
	}
	harness := src[i:j] + `
var line = '#web_search #fetch_url ? never put an internal name in a search';
var out = {tools: gTools(line), body: gBody(line), mode: gMode(line)};
out.both = gCompose(out.body, out.mode, false, [], out.tools);
out.one = gCompose(out.body, out.mode, false, [], out.tools.filter(function(t) { return t !== 'web_search'; }));
out.none = gCompose(out.body, out.mode, false, [], []);
out.dupes = gTools('#a #A #a- never');
console.log(JSON.stringify(out));
`
	tmp := filepath.Join(t.TempDir(), "markers.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	raw, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	var got struct {
		Tools           []string
		Body, Mode      string
		Both, One, None string
		Dupes           []string
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	if strings.Join(got.Tools, ",") != "web_search,fetch_url" || got.Mode != "recover" {
		t.Errorf("the editor read %v (%s) off the rule", got.Tools, got.Mode)
	}
	if strings.Join(got.Dupes, ",") != "a" {
		t.Errorf("one tool written three ways is %v", got.Dupes)
	}
	// What the editor writes, the server binds the same way.
	for _, c := range []struct{ line, want string }{
		{got.Both, "web_search or fetch_url"}, {got.One, "fetch_url"}, {got.None, ""},
	} {
		r := parseGuardrailRule(c.line)
		if r.boundTo() != c.want || r.Text != "never put an internal name in a search" || !r.Correctable {
			t.Errorf("the editor wrote %q, which the server reads as bound to %q (%+v), want %q", c.line, r.boundTo(), r, c.want)
		}
	}
	// And each row shows the tools as chips beside a picker that adds one.
	if !strings.Contains(src, "var bound = gTools(text);") || !strings.Contains(src, "setTools(bound.concat([toolSel.value]))") {
		t.Error("the rule row no longer edits a list of tools")
	}
}
