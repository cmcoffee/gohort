package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

const weatherScript = `import json, os
from urllib.parse import quote

@cache
def load_records():
    return json.loads(os.environ.get("records", "[]"))

def build_url(city):
    return "https://example.test/?q=" + quote(city)

def main():
    recs = load_records()
    if not recs:
        print("[]")
        return
    print(json.dumps([{"city": r["city"]} for r in recs]))

main()
`

// A Python function is its def line and the block indented under it,
// decorators included; the lines around it come through untouched.
func TestPyFunctionSpanCoversDefAndBlock(t *testing.T) {
	start, end, err := pyFunctionSpan(weatherScript, "build_url")
	if err != nil {
		t.Fatal(err)
	}
	got := weatherScript[start:end]
	want := "def build_url(city):\n    return \"https://example.test/?q=\" + quote(city)\n"
	if got != want {
		t.Fatalf("span = %q, want %q", got, want)
	}
	start, end, err = pyFunctionSpan(weatherScript, "load_records")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(weatherScript[start:end], "@cache\ndef load_records") {
		t.Fatalf("decorator not included: %q", weatherScript[start:end])
	}
	// The last function runs to its last content line, not the trailing call.
	start, end, _ = pyFunctionSpan(weatherScript, "main")
	if strings.Contains(weatherScript[start:end], "main()\n") && strings.HasSuffix(weatherScript[start:end], "main()\n") {
		t.Fatalf("span swallowed the top-level call: %q", weatherScript[start:end])
	}
}

func TestPyFunctionSpanRefusesUnknownAndAmbiguous(t *testing.T) {
	if _, _, err := pyFunctionSpan(weatherScript, "nope"); err == nil || !strings.Contains(err.Error(), "build_url") {
		t.Fatalf("unknown name should list what is defined: %v", err)
	}
	twice := "def f():\n    pass\n\ndef f():\n    pass\n"
	if _, _, err := pyFunctionSpan(twice, "f"); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Fatalf("duplicate def should refuse: %v", err)
	}
}

// Splicing a replacement keeps everything outside the function byte-for-byte —
// the whole reason to replace by name rather than re-send the script.
func TestReplaceScriptFunctionSpliceKeepsTheRest(t *testing.T) {
	start, end, err := pyFunctionSpan(weatherScript, "build_url")
	if err != nil {
		t.Fatal(err)
	}
	replacement := "def build_url(city):\n    return \"https://example.test/v2?q=\" + quote(city)\n"
	next := weatherScript[:start] + strings.TrimRight(replacement, "\n") + weatherScript[end:]
	if !strings.Contains(next, "/v2?q=") || strings.Contains(next, "\"https://example.test/?q=\"") {
		t.Fatal("replacement did not land")
	}
	before := strings.Replace(weatherScript, weatherScript[start:end], "", 1)
	after := strings.Replace(next, strings.TrimRight(replacement, "\n"), "", 1)
	if before != after {
		t.Fatalf("something outside the function changed:\n%s\n---\n%s", before, after)
	}
	if !scriptDefines("python", replacement, "build_url") || scriptDefines("python", "return 1", "build_url") {
		t.Fatal("scriptDefines should see the def line and nothing else")
	}
}

func TestApplyTextPatchUniqueOrRefuse(t *testing.T) {
	out, err := applyTextPatch(weatherScript, `"https://example.test/?q="`, `"https://example.test/v2?q="`, `data source "weather"`, "wx")
	if err != nil || !strings.Contains(out, "/v2?q=") {
		t.Fatalf("patch failed: %v", err)
	}
	if _, err := applyTextPatch(weatherScript, "recs", "", `data source "weather"`, "wx"); err == nil || !strings.Contains(err.Error(), "times") {
		t.Fatalf("a non-unique find should refuse and say so: %v", err)
	}
	if _, err := applyTextPatch(weatherScript, "nowhere", "", `data source "weather"`, "wx"); err == nil || !strings.Contains(err.Error(), "script=") {
		t.Fatalf("a missing find should point at get with script=: %v", err)
	}
}

func TestPickAppScriptByEitherSpelling(t *testing.T) {
	spec := AppSpec{
		DataSources: []AppDataSource{{Name: "weather-now", Script: "print('[]')"}},
		Actions:     []AppAction{{Name: "refresh", Script: "print('{}')"}, {Name: "weather-now", Script: "print('{}')"}},
	}
	if ref, err := pickAppScript(spec, "refresh"); err != nil || ref.kind != "action" || ref.idx != 0 {
		t.Fatalf("plain lookup: %+v %v", ref, err)
	}
	if ref, err := pickAppScript(spec, "Weather Now"); err == nil {
		t.Fatalf("a name on both lists must refuse, got %+v", ref)
	} else if !strings.Contains(err.Error(), "data:weather-now") {
		t.Fatalf("refusal should offer the qualified forms: %v", err)
	}
	if ref, err := pickAppScriptQualified(spec, "data:Weather Now"); err != nil || ref.kind != "data" {
		t.Fatalf("qualified lookup: %+v %v", ref, err)
	}
	if _, err := pickAppScript(spec, "missing"); err == nil || !strings.Contains(err.Error(), `action "refresh"`) {
		t.Fatalf("unknown name should list what exists: %v", err)
	}
	sum := appScriptSummary("python", weatherScript)
	if fns, _ := sum["functions"].([]string); len(fns) != 3 || fns[0] != "load_records" {
		t.Fatalf("summary functions = %v", sum["functions"])
	}
}

// The verify standing an author reads back must name the case: never,
// current pass, current fail, or verified only as an earlier revision.
func TestAppVerifyStatusNamesTheCase(t *testing.T) {
	spec := AppSpec{Updated: "2026-09-13T10:00:00Z"}
	if s := spec.VerifyStatus(); !strings.Contains(s, "never verified") {
		t.Fatalf("never: %q", s)
	}
	spec.Verify = &AppVerifyState{Against: spec.Updated, Pass: true, At: "2026-09-13T10:01:00Z"}
	if s := spec.VerifyStatus(); !strings.HasPrefix(s, "verified PASS") {
		t.Fatalf("pass: %q", s)
	}
	spec.Verify.Pass, spec.Verify.Summary = false, "FAIL — 2 problem(s)"
	if s := spec.VerifyStatus(); !strings.Contains(s, "FAIL") || !strings.Contains(s, "2 problem") {
		t.Fatalf("fail: %q", s)
	}
	spec.Updated = "2026-09-13T11:00:00Z"
	if s := spec.VerifyStatus(); !strings.Contains(s, "EARLIER revision") || appVerifyWord(spec) != "stale" {
		t.Fatalf("stale: %q / %s", s, appVerifyWord(spec))
	}
}
