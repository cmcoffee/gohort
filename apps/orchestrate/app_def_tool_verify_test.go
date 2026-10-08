package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A test param naming a setting used to win, so test ran a value the served
// app throws away: live, a setting is applied after the query params because
// anyone holding a link can set a param. The report also has to say which
// params reached the scripts, since the author cannot see the env otherwise.
func TestScriptArgsLetASettingBeatAParam(t *testing.T) {
	spec := AppSpec{Settings: []AppSetting{{Name: "difficulty", Default: "normal"}, {Name: "", Default: "x"}}}
	args, applied, shadowed := appScriptArgs(spec, "[]", map[string]any{"mode": "scene", "probe": 1, "difficulty": "hard"}, nil)
	if args["records"] != "[]" || args["mode"] != "scene" || args["probe"] != "1" {
		t.Fatalf("records and params must reach the scripts: %v", args)
	}
	if args["difficulty"] != "normal" {
		t.Errorf("a setting beats a param, as it does live; got difficulty=%v", args["difficulty"])
	}
	if strings.Join(applied, ", ") != `mode="scene", probe="1"` {
		t.Errorf("applied must list what reached the scripts, sorted: %v", applied)
	}
	if len(shadowed) != 1 || shadowed[0] != "difficulty" {
		t.Errorf("shadowed must name the param a setting overrode: %v", shadowed)
	}

	// A declared setting with no param is still present at its default.
	args, applied, shadowed = appScriptArgs(spec, "[]", nil, nil)
	if args["difficulty"] != "normal" || len(applied) != 0 || len(shadowed) != 0 {
		t.Errorf("no params: settings at their defaults and nothing to report; got %v %v %v", args, applied, shadowed)
	}
}

// The OK lines show what was printed. A script that catches its own failure
// prints a valid object, and a build ran test three times seeing only
// "printed a JSON object", never whether it was a scene or an error.
func TestOutputPreviewIsOneCappedLine(t *testing.T) {
	pretty := "{\n  \"error\": \"model refused\",\n  \"detail\": \"rate limited\"\n}"
	if got := appOutputPreview(pretty, 600); got != `{"error":"model refused","detail":"rate limited"}` {
		t.Errorf("JSON is compacted onto one line: %q", got)
	}
	long := `{"scene":"` + strings.Repeat("a", 700) + `"}`
	got := appOutputPreview(long, 600)
	if !strings.HasPrefix(got, `{"scene":"aaa`) || !strings.Contains(got, "... (112 more chars)") {
		t.Errorf("a long output is capped and says how much was cut: %q", got[len(got)-40:])
	}
	if got := appOutputPreview("not\n  json\tat all", 600); got != "not json at all" {
		t.Errorf("non-JSON folds its whitespace: %q", got)
	}
}

// verifyDataSourceSpec is an app whose only data source is fetched from an
// html section's code, the way a click-driven game asks for its next step.
func verifyDataSourceSpec(t *testing.T, html string) AppSpec {
	t.Helper()
	secs, err := json.Marshal([]map[string]any{{"kind": "html", "html": html}})
	if err != nil {
		t.Fatal(err)
	}
	return AppSpec{Slug: "the-game", Name: "The Game", Sections: secs,
		DataSources: []AppDataSource{{Name: "balance-step", Script: "print('{}')"}}}
}

// A source the page's code names but only calls on a click is not unwired:
// a page load never clicks. Failing it sent a build into a load-time warm-up
// fetch, a probe branch, and an extra section, all to satisfy the check.
// A source nothing references at all is still the failure it always was.
func TestInteractionOnlyDataSourceWarnsInsteadOfFailing(t *testing.T) {
	framed := `<!doctype html><html><body><button id="go">Go</button><script>
document.getElementById('go').onclick = function() {
  fetch('data/balance-step?mode=scene').then(function(r) { return r.json(); });
};
</script></body></html>`
	var b strings.Builder
	n, classes := appVerifyDataSources(&b, verifyDataSourceSpec(t, framed), &PageCheckReport{})
	if n != 0 || len(classes) != 0 {
		t.Fatalf("a source the page references must not fail: %d %v\n%s", n, classes, b.String())
	}
	if !strings.HasPrefix(b.String(), `WARN data source "balance-step"`) || !strings.Contains(b.String(), "on interaction") {
		t.Errorf("say it is referenced but only fetched on interaction:\n%s", b.String())
	}

	b.Reset()
	n, classes = appVerifyDataSources(&b, verifyDataSourceSpec(t, `<div>no fetch here, metadata/balance-step is not a path</div>`), &PageCheckReport{})
	if n != 1 || len(classes) != 1 || classes[0] != "source-unwired" {
		t.Fatalf("nothing references the source: still a failure, got %d %v\n%s", n, classes, b.String())
	}
	if !strings.HasPrefix(b.String(), `FAIL data source "balance-step"`) {
		t.Errorf("unwired source must FAIL:\n%s", b.String())
	}

	// Fetched on load is still OK, whatever the html says.
	b.Reset()
	rep := &PageCheckReport{Requests: []PageRequest{{URL: "http://127.0.0.1:8080/apps/the-game/data/balance-step", Status: 200}}}
	if n, _ := appVerifyDataSources(&b, verifyDataSourceSpec(t, ""), rep); n != 0 || !strings.HasPrefix(b.String(), "OK   data source") {
		t.Errorf("a fetched source is OK: %d\n%s", n, b.String())
	}
}

// What counts as the page naming a data endpoint.
func TestHTMLReferencesData(t *testing.T) {
	for _, tc := range []struct {
		html string
		want bool
	}{
		{`fetch('data/balance-step')`, true},
		{`fetch("./data/balance-step?x=1")`, true},
		{"fetch(`data/balance-step`)", true},
		{`fetch('/apps/the-game/data/balance-step')`, true},
		{`fetch('data/balance_step')`, true}, // served too: the name as saved
		{`fetch('data/balance-steps')`, false},
		{`metadata/balance-step`, false},
		{`fetch('data/' + name)`, false},
		{``, false},
	} {
		if got := appHTMLReferencesData(tc.html, "balance-step"); got != tc.want {
			t.Errorf("appHTMLReferencesData(%q) = %v, want %v", tc.html, got, tc.want)
		}
	}
}

// A check runs as its author, and a param cannot stand in for someone else:
// live no page can set caller, so a test that let it would pass a vote check
// the served app never sees.
func TestScriptArgsCallerIsTheAuthor(t *testing.T) {
	args, applied, shadowed := appScriptArgs(AppSpec{}, "[]", map[string]any{"caller": "mallory"}, map[string]string{"caller": "alice"})
	if args["caller"] != "alice" || len(applied) != 0 || len(shadowed) != 1 || shadowed[0] != "caller" {
		t.Fatalf("args %v applied %v shadowed %v", args, applied, shadowed)
	}
}

// A setting named after a script input would replace it.
func TestSettingCannotTakeAScriptInputName(t *testing.T) {
	got, notes := appSettings([]any{map[string]any{"name": "caller"}, map[string]any{"name": "records"}, map[string]any{"name": "goal"}})
	if len(got) != 1 || got[0].Name != "goal" || len(notes) != 2 || !strings.Contains(notes[0], "IGNORED") {
		t.Fatalf("got %+v notes %v", got, notes)
	}
}

// The check reads the records where the host keeps them. It read RootDB
// once, which is not the host's bucket, so every check ran on an empty store.
func TestCheckReadsTheHostsRecordStore(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	spec := AppSpec{Slug: "club", Owner: "alice", SharedCollections: []string{"votes"}}
	UserDB(RootDB, "alice").Set("custom_records:club", "stray", map[string]any{"id": "stray"})
	host := appscript.RecordBase(spec, "alice")
	host.Set("custom_records:club", "b1", map[string]any{"id": "b1", "title": "Dune"})
	host.Set(appscript.SharedTable("club", "votes"), "v1", map[string]any{"id": "v1", "by": "bob"})

	recs := appStoredRecords("alice", spec)
	if len(recs) != 1 || recs[0]["id"] != "b1" {
		t.Fatalf("records = %v, want the host's one", recs)
	}
	if got := appscript.SharedInput(host, spec); got != `{"votes":[{"by":"bob","id":"v1"}]}` {
		t.Fatalf("shared = %s", got)
	}
}

// shared is set by the framework, so a check, like the live app, does not
// let a param replace it.
func TestScriptArgsSharedCannotBeSent(t *testing.T) {
	fixed := map[string]string{"caller": "alice", "shared": `{"votes":[]}`}
	args, _, shadowed := appScriptArgs(AppSpec{}, "[]", map[string]any{"shared": `{"votes":[{"by":"x"}]}`}, fixed)
	if args["shared"] != `{"votes":[]}` || len(shadowed) != 1 || shadowed[0] != "shared" {
		t.Fatalf("args %v shadowed %v", args, shadowed)
	}
}
