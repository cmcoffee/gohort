package appcheck

import (
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
