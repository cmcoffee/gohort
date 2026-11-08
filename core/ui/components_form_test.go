package ui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A heading with no field under it is a menu entry that opens onto nothing.
// The form drops it as it is served, a trailing one included, and keeps every
// heading that has fields.
func TestAFormDropsAHeadingWithNothingUnderIt(t *testing.T) {
	fields := []FormField{
		{Field: "name", Label: "Name"},
		{Type: "header", Label: "Delegation"},
		{Type: "header", Label: "Intake form"},
		{Field: "intake_form", Label: "Intake form (JSON)"},
		{Type: "header", Label: "Leftover"},
	}
	if got := EmptyHeadings(fields); strings.Join(got, ",") != "Delegation,Leftover" {
		t.Fatalf("EmptyHeadings = %v", got)
	}
	b, err := json.Marshal(FormPanel{Fields: fields, Steps: []FormStep{{Fields: fields}}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, `"Delegation"`) || strings.Contains(s, `"Leftover"`) {
		t.Errorf("an empty heading was served: %s", s)
	}
	if strings.Count(s, `"Intake form"`) != 2 || strings.Count(s, `"intake_form"`) != 2 {
		t.Errorf("a heading with fields, or a field, went missing: %s", s)
	}
}

// A named heading shows and hides by its show_when, as a field does: a form
// that shows one group of fields at a time hides that group's headings with
// it. An unnamed one is not registered, so it always shows.
func TestANamedHeadingFollowsItsShowWhen(t *testing.T) {
	basics := mustRuntimePart(t, "10_basics.js")
	at := strings.Index(basics, "if (t === 'header') {")
	if at < 0 {
		t.Fatal("the heading branch is gone")
	}
	end := strings.Index(basics[at:], "return fieldWrap;")
	if end < 0 || !strings.Contains(basics[at:at+end], "if (f.field) fieldEls[f.field] = fieldWrap;") {
		t.Error("a named heading is not registered, so its show_when never applies")
	}
}

// A form's plain inputs are settings, never a sign-in: the browser is told
// not to fill a saved login into them. A text box just above a password box
// otherwise reads to it as a username, and a saved sign-in landed in a
// settings field that changed what the field meant.
func TestAFormFieldIsNeverFilledWithASavedLogin(t *testing.T) {
	i := strings.Index(runtimeJS, "// text, tel, anything else")
	if i < 0 {
		t.Fatal("the generic input branch moved; find it and keep this check on it")
	}
	branch := runtimeJS[i:min(i+1200, len(runtimeJS))]
	if !strings.Contains(branch, `input.setAttribute('autocomplete', t === 'password' ? 'new-password' : 'off');`) {
		t.Errorf("the generic form input no longer turns the browser's login fill off:\n%s", branch)
	}
}

// A form with FieldsSource reads its fields as it opens, before its record,
// in place of the ones the page was built with: a row added since the page
// loaded has its own fields. A failed read falls back to declared fields, or
// says it failed when there are none, as a failed Source does.
func TestAFormReadsItsFieldsAsItOpens(t *testing.T) {
	if raw, _ := json.Marshal(FormPanel{FieldsSource: "api/f?name={name}"}); !strings.Contains(string(raw), `"fields_source":"api/f?name={name}"`) {
		t.Fatalf("fields_source is not on the wire: %s", raw)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "10_basics.js")
	start, end := strings.Index(src, "    function loadFields() {"), strings.Index(src, "    // RefreshOn")
	if start < 0 || end < start {
		t.Fatal("the form's load is not found")
	}
	harness := `
function run(cfg, answers, done) {
  var stepsMode = false, order = [];
  var wrap = {uiForm: {}, textContent: ''};
  var current;
  function fetchJSON(url) {
    order.push(url);
    var a = answers[url];
    return a instanceof Error ? Promise.reject(a) : Promise.resolve(a);
  }
  function loadOptionSources() { order.push('options'); return Promise.resolve(); }
  function render() { order.push('render:' + (cfg.fields || []).map(function(f){ return f.field; }).join(',')); }
` + src[start:end] + `
  load();
  setTimeout(function(){ done(order, wrap); }, 20);
}
function want(cond, msg) { if (!cond) { console.log('FAIL ' + msg); process.exitCode = 1; } }
run({fields_source: 'f', source: 's', fields: [{field: 'old'}]},
  {f: [{field: 'key'}, {field: 'model'}], s: {}},
  function(order) {
    want(order.join(' ') === 'f options s render:key,model', 'fields read first, then the record: ' + order.join(' '));
  });
run({fields_source: 'f', source: 's'}, {f: {fields: [{field: 'a'}]}, s: {}},
  function(order) { want(order[order.length-1] === 'render:a', '{fields: [...]} is read too: ' + order.join(' ')); });
run({fields_source: 'f', source: 's', fields: [{field: 'old'}]}, {f: new Error('down'), s: {}},
  function(order) { want(order[order.length-1] === 'render:old', 'a failed read keeps declared fields: ' + order.join(' ')); });
run({fields_source: 'f', source: 's'}, {f: new Error('down'), s: {}},
  function(order, wrap) {
    want(wrap.textContent === 'Failed to load: down' && order.indexOf('s') < 0, 'no fields to fall back on says it failed: ' + wrap.textContent + ' ' + order.join(' '));
  });
run({source: 's', fields: [{field: 'plain'}]}, {s: {}},
  function(order) { want(order.join(' ') === 'options s render:plain', 'a form without it is as it was: ' + order.join(' ')); });
setTimeout(function(){ if (!process.exitCode) console.log('OK'); }, 60);
`
	tmp := filepath.Join(t.TempDir(), "fields_source.js")
	if err := os.WriteFile(tmp, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the form's fields read:\n%s", out)
	}
}
