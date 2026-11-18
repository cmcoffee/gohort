// Package appcheck is an app's checks: tests that live with it, written as
// each feature is built and run on every save, so a change that breaks what
// used to work is caught by the change (docs/design/app-checks.md).
//
// Two kinds. A script check runs one data source or action with records and
// inputs of its own and judges what it prints. A page check drives the page
// in a browser, by what a person sees: fill a field, press a button, see a
// row.
//
// A subpackage rather than more of core: it speaks no core type, and core is
// at its export ceiling.
package appcheck

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Reference is what a check is, for whoever writes one: Builder's app_def
// help and an Anvil app project's standing notes. A test holds it to every
// field the types here take.
const Reference = `checks: a list of what the app is supposed to do, each a test that runs on
every save (at most 20). Three kinds:

script check: {name, script, records?, shared?, inputs?, expect}
  name     what it checks, as a person would ("adding a plant lists it")
  script   a data source or action of the app, by name
  records  the app's records the run sees, in place of anyone's (a list)
  shared   the shared collections the run sees, {"<collection>": [records]};
           unset, each reads empty. Give each record its "id" and "by" (who
           wrote it) when the script looks them up
  inputs   {name: value}: the query params (data source) or body fields
           (action), each an env var. Two are the check's own: caller (who
           the run is for, any short name: "sam" here and "by": "sam" on a
           shared record are one person; the owner when unset) and
           caller_role (owner, manager or user; unset when not given). A
           real run sets both itself, so name them to test what differs by
           person. The script sees caller, and each shared record's by, as
           the app's alias for that name (p_ and 16 hex digits), as it sees
           real people: expect what the script decides (a branch, a count,
           a message), not the name echoed back
  expect   [{path, equals | contains | count | exists}], each with exactly
           one test. path is a dot path into the printed JSON
           ("rows.0.total"); empty is the whole value. contains ignores case
  A check's run writes nothing: what an action saves, shares or sends is
  judged from what it printed, and nothing lands anywhere. So it sees a
  shared record before the host stamps it: saved for real, every shared
  record an action prints gets by set to whoever ran the action (and a new
  updated), whatever the script put there. A manager editing Sam's record
  becomes its by; when the author must survive someone else's edit, keep
  them in a field of the app's own (author) and check that field.

page check: {name, records?, shared?, steps}
  drives the page as served, in a browser, on the check's own records.
  Each step is exactly one of:
  {fill: "<field label or placeholder>", with: "<text>"}
  {click: "<button or link text>"}
  {choose: "<select label>", option: "<option text>"}
  {see: "<text>", in?: "<section title>"}
  {not_see: "<text>", in?: "<section title>"}
  {rows: <n>, in: "<table or list title>"}

agent check: {name, agent, ask, expect}
  puts ask to the app's agent (agent: its name or id) and tests the reply,
  as JSON when the reply is JSON, else as its text. expect is
  [{path?, contains | lacks | equals | exists}]: lacks is text the reply must
  not contain ("refund" for an agent that must never promise one). The agent
  answers without its tools, so it writes nothing. Runs at verify and test,
  not on every save (it costs a model call). This is what tests a change to
  the agent's prompt, which no script or page check sees.

A save that breaks a check that passed before is refused, and the version
serving stays. Change a check only when its feature changed on purpose.`

// MaxChecks is how many checks one app keeps: enough to pin each feature,
// few enough that every save can run them.
const MaxChecks = 20

// Check is one thing an app is supposed to do.
type Check struct {
	// Name says what it checks, as a person would ("adding an item lists
	// it"). It is the check's identity: a save is judged check by check.
	Name string `json:"name"`

	// A script check: Script names a data source or action of the app.
	Script string `json:"script,omitempty"`
	// Records are the app's records the run sees, in place of anyone's.
	Records []map[string]any `json:"records,omitempty"`
	// Shared are the app's shared collections the run sees, by name, in
	// place of what they hold. Unset, every collection reads empty: an app
	// whose actions work on shared records had no way to test them, and
	// Builder wrote a second path through its records just for its checks.
	Shared map[string][]map[string]any `json:"shared,omitempty"`
	// Inputs are the run's query params (a data source) or body fields (an
	// action), each as an env var. Two are the check's own: caller, who the
	// run is for (the owner when unset), and caller_role,
	// what they are to the app (owner, manager or user; unset when not
	// given). A real run sets both itself and no param can change them; a
	// check names them to test what differs by person.
	Inputs map[string]string `json:"inputs,omitempty"`
	// Expect is what the printed JSON must hold.
	Expect []Expect `json:"expect,omitempty"`

	// A page check: the steps, in order.
	Steps []Step `json:"steps,omitempty"`

	// An agent check: Ask is put to the app's agent named by Agent, and
	// Expect is what its reply must hold (and, with lacks, must not). The
	// reply is read as JSON when it is JSON, else as its text. A prompt
	// change could pass every script and page check and make the agent
	// worse; this is what tests it. Run at verify, not on every save: it
	// costs a model call. The agent answers without its tools, so a check
	// can never write.
	Agent string `json:"agent,omitempty"`
	Ask   string `json:"ask,omitempty"`

	// Stored is the check as JSON, which is how the app store keeps it
	// (Pack): the store writes with gob, which leaves out zero values, so
	// {"count": 0}, {"exists": false}, {"rows": 0} and an empty equals came
	// back with no test at all, and the next save refused the app's own
	// check. Unpack restores the check from it. Never part of the check as
	// anyone reads it.
	Stored string `json:"-"`
}

// Pack is checks as the app store keeps them: each as its name and itself as
// JSON in Stored, which gob keeps whole. The other fields are left to
// Stored: gob cannot write a list inside equals at all ([] came back null).
func Pack(checks []Check) []Check {
	if len(checks) == 0 {
		return checks
	}
	out := make([]Check, len(checks))
	for i, c := range checks {
		c.Stored = ""
		b, err := json.Marshal(c)
		if err != nil {
			out[i] = c
			continue
		}
		out[i] = Check{Name: c.Name, Stored: string(b)}
	}
	return out
}

// Unpack is checks as the app store returned them, each restored from
// Stored where it has one; a check saved before Stored existed is as it was.
func Unpack(checks []Check) []Check {
	if len(checks) == 0 {
		return checks
	}
	out := make([]Check, len(checks))
	for i, c := range checks {
		if c.Stored != "" {
			var whole Check
			if json.Unmarshal([]byte(c.Stored), &whole) == nil {
				c = whole
			}
		}
		c.Stored = ""
		out[i] = c
	}
	return out
}

// Expect is one test of a script's printed JSON.
type Expect struct {
	// Path is a dot path into it ("rows.0.total"); empty is the whole value.
	Path string `json:"path,omitempty"`
	// Exactly one of these.
	Equals   any    `json:"equals,omitempty"`
	Contains string `json:"contains,omitempty"`
	Count    *int   `json:"count,omitempty"`
	Exists   *bool  `json:"exists,omitempty"`
	// Lacks is text the value must not contain (an agent's reply that must
	// not promise a refund, say).
	Lacks string `json:"lacks,omitempty"`
}

// Step is one thing a page check does or sees, named by what a person sees:
// a field's label, a button's text, a section's title. Exactly one of Fill,
// Click, Choose, See, NotSee or Rows is set.
type Step struct {
	Fill   string `json:"fill,omitempty"`   // a field, by label or placeholder
	With   string `json:"with,omitempty"`   // what to type into it
	Click  string `json:"click,omitempty"`  // a button or link, by its text
	Choose string `json:"choose,omitempty"` // a select, by label
	Option string `json:"option,omitempty"` // the option to pick, by its text
	See    string `json:"see,omitempty"`    // text that appears
	NotSee string `json:"not_see,omitempty"`
	In     string `json:"in,omitempty"` // a section title that See / NotSee / Rows look within
	Rows   *int   `json:"rows,omitempty"`
}

// IsPage reports a page check.
func (c Check) IsPage() bool { return len(c.Steps) > 0 }

// IsAgent reports an agent check.
func (c Check) IsAgent() bool { return strings.TrimSpace(c.Agent) != "" }

// Validate refuses a list of checks that cannot run: too many, unnamed or
// named twice, neither kind or both, a script the app does not have, an
// expectation or a step that says nothing. scripts is the app's data sources
// and actions, by name.
func Validate(checks []Check, scripts map[string]bool) error {
	if len(checks) > MaxChecks {
		return fmt.Errorf("an app keeps at most %d checks, this has %d: pin each feature once, the way it is used", MaxChecks, len(checks))
	}
	seen := map[string]bool{}
	for i, c := range checks {
		label := fmt.Sprintf("check %d", i+1)
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return fmt.Errorf("%s has no name: say what it checks, as a person would (\"adding an item lists it\")", label)
		}
		label = fmt.Sprintf("check %q", name)
		if seen[strings.ToLower(name)] {
			return fmt.Errorf("%s is named twice: each check needs its own name", label)
		}
		seen[strings.ToLower(name)] = true
		script := strings.TrimSpace(c.Script)
		kinds := 0
		for _, is := range []bool{script != "", c.IsPage(), c.IsAgent()} {
			if is {
				kinds++
			}
		}
		switch {
		case kinds > 1:
			return fmt.Errorf("%s is more than one kind: a check runs a script, takes steps on the page, or asks the app's agent, one of them", label)
		case kinds == 0:
			return fmt.Errorf("%s has neither script nor steps: name a data source or action to run, or the steps a person takes", label)
		case c.IsAgent():
			if strings.TrimSpace(c.Ask) == "" {
				return fmt.Errorf("%s asks the agent nothing: give ask, what a person would say to it", label)
			}
			if len(c.Expect) == 0 {
				return fmt.Errorf("%s expects nothing: say what the reply must hold (expect: [{contains | lacks | equals | exists, path?}])", label)
			}
			for j, e := range c.Expect {
				if n := e.tests(); n != 1 {
					return fmt.Errorf("%s, expectation %d: give exactly one of equals, contains, lacks, count or exists (it has %d)", label, j+1, n)
				}
			}
		case script != "":
			if !scripts[script] {
				return fmt.Errorf("%s runs %q, which is not one of the app's data sources or actions", label, script)
			}
			if len(c.Expect) == 0 {
				return fmt.Errorf("%s expects nothing: say what its output must hold (expect: [{path, equals | contains | count | exists}])", label)
			}
			for j, e := range c.Expect {
				if e.Lacks != "" {
					return fmt.Errorf("%s, expectation %d: lacks is for an agent check's reply; a script's output is tested with equals, contains, count or exists", label, j+1)
				}
				if n := e.tests(); n != 1 {
					return fmt.Errorf("%s, expectation %d: give exactly one of equals, contains, count or exists (it has %d)", label, j+1, n)
				}
			}
		default:
			for j, s := range c.Steps {
				if err := s.valid(); err != nil {
					return fmt.Errorf("%s, step %d: %v", label, j+1, err)
				}
			}
		}
	}
	return nil
}

func (e Expect) tests() int {
	n := 0
	if e.Equals != nil {
		n++
	}
	if e.Contains != "" {
		n++
	}
	if e.Count != nil {
		n++
	}
	if e.Exists != nil {
		n++
	}
	if e.Lacks != "" {
		n++
	}
	return n
}

func (s Step) valid() error {
	n := 0
	for _, v := range []string{s.Fill, s.Click, s.Choose, s.See, s.NotSee} {
		if strings.TrimSpace(v) != "" {
			n++
		}
	}
	if s.Rows != nil {
		n++
	}
	switch {
	case n != 1:
		return fmt.Errorf("give exactly one of fill, click, choose, see, not_see or rows (it has %d)", n)
	case s.Fill != "" && s.With == "":
		return fmt.Errorf("fill %q needs with: what to type", s.Fill)
	case s.Choose != "" && s.Option == "":
		return fmt.Errorf("choose %q needs option: which one to pick", s.Choose)
	case s.Rows != nil && strings.TrimSpace(s.In) == "":
		return fmt.Errorf("rows needs in: the title of the table or list to count")
	}
	return nil
}

// Judge tests a script's printed output against the check's expectations.
// It returns "" when every one holds, or what the first that did not saw.
func (c Check) Judge(output string) string {
	reply, _, ok := ScriptReply(output)
	var v any
	if !ok || json.Unmarshal([]byte(reply), &v) != nil {
		return "it did not print valid JSON: " + clip(output)
	}
	for _, e := range c.Expect {
		if why := e.judge(v); why != "" {
			return why
		}
	}
	return ""
}

// JudgeReply tests an agent's reply against the check's expectations: as
// JSON when the reply is JSON, else as its text. "" when every one holds.
func (c Check) JudgeReply(reply string) string {
	var v any
	if json.Unmarshal([]byte(strings.TrimSpace(reply)), &v) != nil {
		v = reply
	}
	for _, e := range c.Expect {
		if why := e.judge(v); why != "" {
			return strings.Replace(why, "the output", "the reply", 1)
		}
	}
	return ""
}

func (e Expect) judge(root any) string {
	where := "the output"
	if e.Path != "" {
		where = strconv.Quote(e.Path)
	}
	got, found := walk(root, e.Path)
	switch {
	case e.Lacks != "":
		if !found {
			return ""
		}
		if s := marshal(got); strings.Contains(strings.ToLower(text(got, s)), strings.ToLower(e.Lacks)) {
			return fmt.Sprintf("%s contains %q, and must not: it is %s", where, e.Lacks, clip(s))
		}
	case e.Exists != nil:
		if found != *e.Exists {
			if found {
				return where + " is there, and should not be"
			}
			return where + " is not there"
		}
	case !found:
		return where + " is not there (the output: " + clip(marshal(root)) + ")"
	case e.Count != nil:
		n := -1
		switch t := got.(type) {
		case []any:
			n = len(t)
		case map[string]any:
			n = len(t)
		}
		if n < 0 {
			return where + " is not a list, so it has no count: it is " + clip(marshal(got))
		}
		if n != *e.Count {
			return fmt.Sprintf("%s has %d item(s), want %d", where, n, *e.Count)
		}
	case e.Contains != "":
		if s := marshal(got); !strings.Contains(strings.ToLower(text(got, s)), strings.ToLower(e.Contains)) {
			return fmt.Sprintf("%s does not contain %q: it is %s", where, e.Contains, clip(s))
		}
	default:
		if !sameJSON(got, e.Equals) {
			return fmt.Sprintf("%s is %s, want %s", where, clip(marshal(got)), clip(marshal(e.Equals)))
		}
	}
	return ""
}

// walk follows a dot path ("rows.0.total") into a decoded JSON value.
func walk(v any, path string) (any, bool) {
	if strings.TrimSpace(path) == "" {
		return v, true
	}
	for _, part := range strings.Split(path, ".") {
		switch t := v.(type) {
		case map[string]any:
			next, ok := t[part]
			if !ok {
				return nil, false
			}
			v = next
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(t) {
				return nil, false
			}
			v = t[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// sameJSON compares two values as JSON: 7 equals 7.0, and key order does
// not matter.
func sameJSON(a, b any) bool {
	norm := func(x any) string {
		var back any
		_ = json.Unmarshal([]byte(marshal(x)), &back)
		return marshal(back)
	}
	return norm(a) == norm(b)
}

func marshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// text is a string value as itself, anything else as its JSON.
func text(v any, asJSON string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return asJSON
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// StepsProbePrefix marks a page check's steps handed to the browser through
// the page check's probe argument (core.CheckPageAsUser), which the browser
// runs itself, in every frame, rather than evaluating as script: an html
// section renders in a sandboxed frame no script on the page can reach.
const StepsProbePrefix = "appcheck-steps:"

// StepsProbe is steps as the probe the browser runs.
func StepsProbe(steps []Step) string {
	b, _ := json.Marshal(steps)
	return StepsProbePrefix + string(b)
}

// DecodeStepsProbe reads the steps back out of a probe; false for a probe
// that is not one.
func DecodeStepsProbe(probe string) ([]Step, bool) {
	rest, ok := strings.CutPrefix(probe, StepsProbePrefix)
	if !ok {
		return nil, false
	}
	var steps []Step
	if json.Unmarshal([]byte(rest), &steps) != nil {
		return nil, false
	}
	return steps, true
}

// StepsResult is how a page check's steps went: OK, or the step (from 1)
// that did not, and what the browser saw there.
type StepsResult struct {
	OK   bool   `json:"ok"`
	Step int    `json:"step,omitempty"`
	Saw  string `json:"saw,omitempty"`
}

// Describe words a step for a report: what it does, as a person would say it.
func (s Step) Describe() string {
	in := ""
	if s.In != "" {
		in = " in " + strconv.Quote(s.In)
	}
	switch {
	case s.Fill != "":
		return fmt.Sprintf("fill %q with %q", s.Fill, s.With)
	case s.Click != "":
		return fmt.Sprintf("click %q", s.Click)
	case s.Choose != "":
		return fmt.Sprintf("choose %q in %q", s.Option, s.Choose)
	case s.See != "":
		return fmt.Sprintf("see %q%s", s.See, in)
	case s.NotSee != "":
		return fmt.Sprintf("not see %q%s", s.NotSee, in)
	case s.Rows != nil:
		return fmt.Sprintf("%d row(s)%s", *s.Rows, in)
	}
	return "a step that does nothing"
}
