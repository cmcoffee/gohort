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
	// Inputs are the run's query params (a data source) or body fields (an
	// action), each as an env var.
	Inputs map[string]string `json:"inputs,omitempty"`
	// Expect is what the printed JSON must hold.
	Expect []Expect `json:"expect,omitempty"`

	// A page check: the steps, in order.
	Steps []Step `json:"steps,omitempty"`
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
		switch {
		case script != "" && c.IsPage():
			return fmt.Errorf("%s has both script and steps: a check is one or the other", label)
		case script == "" && !c.IsPage():
			return fmt.Errorf("%s has neither script nor steps: name a data source or action to run, or the steps a person takes", label)
		case script != "":
			if !scripts[script] {
				return fmt.Errorf("%s runs %q, which is not one of the app's data sources or actions", label, script)
			}
			if len(c.Expect) == 0 {
				return fmt.Errorf("%s expects nothing: say what its output must hold (expect: [{path, equals | contains | count | exists}])", label)
			}
			for j, e := range c.Expect {
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
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &v); err != nil {
		return "it did not print valid JSON: " + clip(output)
	}
	for _, e := range c.Expect {
		if why := e.judge(v); why != "" {
			return why
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
