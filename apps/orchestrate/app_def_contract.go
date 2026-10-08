package orchestrate

// An app as a web page and its API.
//
// The page calls its endpoints (app.data, app.action, or a relative fetch of
// data/<name> and action/<name>); the endpoints are the app's data sources
// and actions. Two ways that goes wrong were found only by the user: a page
// that called an endpoint by a name the app did not have, which failed the
// first time someone clicked, and a page that showed its endpoint's JSON as
// its content. The first is read from the page's code, the second from what
// the page actually shows in a browser.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

var (
	pageDataCallRE   = regexp.MustCompile(`app\.data\(\s*["']([^"']+)["']|["'](?:\./)?data/([A-Za-z0-9_.-]+)`)
	pageActionCallRE = regexp.MustCompile(`app\.action\(\s*["']([^"']+)["']|["'](?:\./)?action/([A-Za-z0-9_.-]+)`)
	stringifyIntoRE  = regexp.MustCompile(`(textContent|innerText|innerHTML)\s*\+?=\s*JSON\.stringify\(`)
)

// appPageCode is everything in the page that can name an endpoint: the html
// sections' markup and scripts, and every other section's fields.
func appPageCode(spec AppSpec) string {
	var v any
	if json.Unmarshal(spec.Sections, &v) != nil {
		return ""
	}
	var b strings.Builder
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			b.WriteString(t)
			b.WriteByte('\n')
		case map[string]any:
			for k, e := range t {
				if k == "kind" {
					fmt.Fprintf(&b, "kind:%v\n", e)
					continue
				}
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return b.String()
}

func appEndpointNames(list []string) map[string]bool {
	out := map[string]bool{}
	for _, n := range list {
		out[slugify(n)] = true
	}
	return out
}

// appContractProblems lists calls the page makes to endpoints the app does
// not have: each one fails the first time it runs.
func appContractProblems(spec AppSpec) []string {
	code := appPageCode(spec)
	var dsNames, actNames []string
	for _, ds := range spec.DataSources {
		dsNames = append(dsNames, ds.Name)
	}
	for _, a := range spec.Actions {
		actNames = append(actNames, a.Name)
	}
	haveDS, haveAct := appEndpointNames(dsNames), appEndpointNames(actNames)
	var out []string
	seen := map[string]bool{}
	report := func(kind string, re *regexp.Regexp, have map[string]bool, names []string) {
		for _, m := range re.FindAllStringSubmatch(code, -1) {
			name := firstNonEmptyStr(m[1], m[2])
			if name == "" || strings.ContainsAny(name, "{}+$") || seen[kind+name] {
				continue
			}
			seen[kind+name] = true
			if !have[slugify(name)] {
				it := "none"
				if len(names) > 0 {
					it = strings.Join(names, ", ")
				}
				out = append(out, fmt.Sprintf("the page calls the %s %q, and the app has no %s by that name (it has: %s): add it, or call one it has", kind, name, kind, it))
			}
		}
	}
	report("data source", pageDataCallRE, haveDS, dsNames)
	report("action", pageActionCallRE, haveAct, actNames)
	return out
}

// appUnusedEndpoints lists endpoints nothing on the page reaches: no section
// reads it, no code calls it, and (for an action) no schedule or actions row
// runs it.
func appUnusedEndpoints(spec AppSpec) []string {
	code := appPageCode(spec)
	called := map[string]bool{}
	for _, re := range []*regexp.Regexp{pageDataCallRE, pageActionCallRE} {
		for _, m := range re.FindAllStringSubmatch(code, -1) {
			called[slugify(firstNonEmptyStr(m[1], m[2]))] = true
		}
	}
	for _, sec := range appSectionList(spec) {
		for _, k := range []string{"source_script", "suggest_script"} {
			if n := mapStr(sec, k); n != "" {
				called[slugify(n)] = true
			}
		}
	}
	actionsRow := strings.Contains(code, "kind:actions\n")
	var out []string
	for _, ds := range spec.DataSources {
		if !called[slugify(ds.Name)] {
			out = append(out, fmt.Sprintf("data source %q is never called by the page: show what it computes, or remove it", ds.Name))
		}
	}
	for _, a := range spec.Actions {
		if !called[slugify(a.Name)] && !actionsRow && a.Schedule == nil {
			out = append(out, fmt.Sprintf("action %q is never called by the page and has no schedule: give it a button (app.action(%q, ...)) or remove it", a.Name, a.Name))
		}
	}
	if stringifyIntoRE.MatchString(code) {
		out = append(out, "the page writes JSON.stringify(...) into its own text: show the data as a person reads it (cards, rows, a chart, a sentence), not as JSON")
	}
	return out
}

// pageShowsRawData reports whether what the page shows reads as raw data
// instead of a page: a JSON object or list of objects in its visible text, or
// "[object Object]". Returns what it is and a snippet, or "".
func pageShowsRawData(texts []string) (string, string) {
	for _, t := range texts {
		if i := strings.Index(t, "[object Object]"); i >= 0 {
			return "[object Object]", appSnippet(t, i)
		}
		tries := 0
		for i := 0; i < len(t) && tries < 50; i++ {
			if t[i] != '{' && t[i] != '[' {
				continue
			}
			rest := strings.TrimLeft(t[i+1:], " \n\r\t")
			if rest == "" || (rest[0] != '"' && rest[0] != '{') {
				continue
			}
			tries++
			var v any
			if json.NewDecoder(strings.NewReader(t[i:])).Decode(&v) != nil {
				continue
			}
			if appLooksLikeRecord(v) {
				return "raw JSON", appSnippet(t, i)
			}
		}
	}
	return "", ""
}

func appLooksLikeRecord(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		return len(x) >= 2
	case []any:
		for _, e := range x {
			if m, ok := e.(map[string]any); ok && len(m) >= 2 {
				return true
			}
		}
	}
	return false
}

func appSnippet(t string, i int) string {
	s := strings.Join(strings.Fields(t[i:]), " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// appContractReport is the contract's lines for a check report, and how many
// of them fail.
func appContractReport(spec AppSpec) (string, int) {
	probs := appContractProblems(spec)
	sort.Strings(probs)
	var b strings.Builder
	for _, p := range probs {
		fmt.Fprintf(&b, "FAIL page: %s\n", p)
	}
	return b.String(), len(probs)
}

// appShowsJSONOnPurpose is an app whose job is JSON (a viewer, a formatter),
// for which showing it is the point.
func appShowsJSONOnPurpose(spec AppSpec) bool {
	return strings.Contains(strings.ToLower(spec.Name+" "+spec.Desc), "json")
}
