package appcheck

import (
	"bytes"
	"encoding/gob"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func n(i int) *int   { return &i }
func b(v bool) *bool { return &v }

// A list that cannot run is refused before it is saved, each case naming
// what to fix.
func TestValidateRefusesChecksThatCannotRun(t *testing.T) {
	scripts := map[string]bool{"total": true}
	good := Check{Name: "totals", Script: "total", Expect: []Expect{{Path: "total", Equals: 7}}}
	if err := Validate([]Check{good}, scripts); err != nil {
		t.Fatalf("a good check was refused: %v", err)
	}
	cases := map[string][]Check{
		"no name":         {{Script: "total", Expect: good.Expect}},
		"named twice":     {good, good},
		"neither kind":    {{Name: "x"}},
		"both kinds":      {{Name: "x", Script: "total", Expect: good.Expect, Steps: []Step{{See: "a"}}}},
		"unknown script":  {{Name: "x", Script: "nope", Expect: good.Expect}},
		"expects nothing": {{Name: "x", Script: "total"}},
		"two tests":       {{Name: "x", Script: "total", Expect: []Expect{{Equals: 1, Contains: "a"}}}},
		"empty step":      {{Name: "x", Steps: []Step{{}}}},
		"fill no with":    {{Name: "x", Steps: []Step{{Fill: "Item"}}}},
		"rows no in":      {{Name: "x", Steps: []Step{{Rows: n(2)}}}},
	}
	for name, cs := range cases {
		if err := Validate(cs, scripts); err == nil {
			t.Errorf("%s: not refused", name)
		}
	}
	var many []Check
	for i := 0; i <= MaxChecks; i++ {
		many = append(many, Check{Name: strings.Repeat("x", i+1), Script: "total", Expect: good.Expect})
	}
	if err := Validate(many, scripts); err == nil {
		t.Errorf("more than %d checks were kept", MaxChecks)
	}
}

// What a script printed is judged by path: equals as JSON (7 is 7.0, key
// order does not matter), contains without case, count of a list, exists.
func TestJudgeReadsTheOutputByPath(t *testing.T) {
	out := `{"total": 7.0, "rows": [{"day": "Mon"}, {"day": "Tue"}], "note": "All Good"}`
	pass := []Expect{
		{Path: "total", Equals: 7},
		{Path: "rows.1.day", Equals: "Tue"},
		{Path: "rows", Count: n(2)},
		{Path: "note", Contains: "all good"},
		{Path: "missing", Exists: b(false)},
		{Path: "rows.0", Equals: map[string]any{"day": "Mon"}},
	}
	for _, e := range pass {
		if why := (Check{Expect: []Expect{e}}).Judge(out); why != "" {
			t.Errorf("%+v should pass: %s", e, why)
		}
	}
	fail := []Expect{
		{Path: "total", Equals: 8},
		{Path: "rows", Count: n(3)},
		{Path: "note", Contains: "bad"},
		{Path: "rows.5.day", Equals: "x"},
		{Path: "total", Exists: b(false)},
	}
	for _, e := range fail {
		if why := (Check{Expect: []Expect{e}}).Judge(out); why == "" {
			t.Errorf("%+v should fail", e)
		}
	}
	if why := (Check{Expect: []Expect{{Equals: 1}}}).Judge("not json"); !strings.Contains(why, "valid JSON") {
		t.Errorf("unparseable output: %q", why)
	}
}

// The reference an author reads names every field a check takes: one added
// to the types and left out of it is one nobody is told about.
func TestTheReferenceNamesEveryField(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(Check{}), reflect.TypeOf(Expect{}), reflect.TypeOf(Step{})} {
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if tag == "" || tag == "-" {
				continue
			}
			if !regexp.MustCompile(`\b` + tag + `\b`).MatchString(Reference) {
				t.Errorf("%s.%s (%q) is not in Reference", typ.Name(), typ.Field(i).Name, tag)
			}
		}
	}
}

// A check keeps its zero values through the app store, which writes with gob:
// gob leaves zero values out, so {"count": 0} came back with no test and the
// next save refused the app's own check. Pack and Unpack carry the check
// whole; a check stored before Stored existed reads as it was.
func TestAChecksZeroValuesSurviveTheStore(t *testing.T) {
	checks := []Check{{Name: "nothing yet", Script: "s", Expect: []Expect{
		{Path: "total", Equals: 0.0}, {Path: "rows", Count: n(0)}, {Path: "error", Exists: b(false)},
		{Path: "list", Equals: []any{}}, {Path: "on", Equals: false},
	}}, {Name: "empty table", Steps: []Step{{Rows: n(0), In: "Books"}}}}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(Pack(checks)); err != nil {
		t.Fatal(err)
	}
	var back []Check
	if err := gob.NewDecoder(&buf).Decode(&back); err != nil {
		t.Fatal(err)
	}
	back = Unpack(back)
	if !reflect.DeepEqual(back, checks) {
		t.Errorf("after the store:\n%+v\nwant\n%+v", back, checks)
	}
	if err := Validate(back, map[string]bool{"s": true}); err != nil {
		t.Errorf("the stored checks no longer validate: %v", err)
	}
	if got := Unpack([]Check{{Name: "old", Script: "s"}}); got[0].Name != "old" || got[0].Stored != "" {
		t.Errorf("a check stored without Stored: %+v", got)
	}
}

// An agent check asks the app's agent and tests its reply: as JSON when it
// is JSON, else as its text, with lacks for what it must not say. It is
// exactly one kind, needs a question and an expectation, and lacks is for a
// reply, not a script's output.
func TestAnAgentCheckTestsTheReply(t *testing.T) {
	c := Check{Name: "never promises a refund", Agent: "Helper", Ask: "Can I get my money back?",
		Expect: []Expect{{Contains: "policy"}, {Lacks: "refund"}}}
	if err := Validate([]Check{c}, nil); err != nil {
		t.Fatalf("a good agent check: %v", err)
	}
	if why := c.JudgeReply("Our policy covers exchanges within 30 days."); why != "" {
		t.Errorf("a good reply: %s", why)
	}
	if why := c.JudgeReply("Our policy: yes, a full Refund."); !strings.Contains(why, "must not") {
		t.Errorf("a reply saying what it must not: %q", why)
	}
	j := Check{Name: "j", Agent: "Helper", Ask: "status?", Expect: []Expect{{Path: "ok", Equals: true}}}
	if why := j.JudgeReply(`{"ok": true}`); why != "" {
		t.Errorf("a JSON reply: %s", why)
	}
	for name, bad := range map[string]Check{
		"no question":       {Name: "x", Agent: "Helper", Expect: []Expect{{Contains: "a"}}},
		"no expectation":    {Name: "x", Agent: "Helper", Ask: "hi"},
		"two kinds":         {Name: "x", Agent: "Helper", Ask: "hi", Script: "s", Expect: []Expect{{Contains: "a"}}},
		"lacks on a script": {Name: "x", Script: "s", Expect: []Expect{{Lacks: "a"}}},
	} {
		if err := Validate([]Check{bad}, map[string]bool{"s": true}); err == nil {
			t.Errorf("%s was not refused", name)
		}
	}
}
