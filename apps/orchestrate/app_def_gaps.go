package orchestrate

// What a first build left out that its user will go looking for.
//
// Builds did what was literally asked and stopped: a weather app saved the
// city typed into it and never showed it again, its page was blank for a new
// user with nothing saved, its icons pointed at files that did not exist, and
// its forecast code was a copy of a tool the owner already had. None of that
// is a failure a check can refuse, and each was found by the user. These are
// the gaps the framework can SEE in the app itself, listed after every check
// as things to add, so they do not depend on the model remembering to look.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// appGapInput is what the check saw while running the scripts: each data
// source's output, and its output with no records saved (the first visit).
type appGapInput struct {
	outputs map[string]string
	empty   map[string]string
}

// appBuildGaps lists what to add, as a block for the end of a report, or "".
func appBuildGaps(user string, spec AppSpec, in appGapInput) string {
	var gaps []string
	gaps = append(gaps, appGapSavedNotShown(spec)...)
	gaps = append(gaps, appGapFirstVisit(in)...)
	gaps = append(gaps, appGapAssets(user, spec, in)...)
	gaps = append(gaps, appGapCopiedTool(user, spec)...)
	if len(gaps) == 0 {
		return ""
	}
	return "\n\nWORTH ADDING (found in the app, not failures; leave one out only if the owner asked for it that way):\n- " + strings.Join(gaps, "\n- ") + "\n"
}

func appSectionList(spec AppSpec) []map[string]any {
	var secs []map[string]any
	_ = json.Unmarshal(spec.Sections, &secs)
	return secs
}

// A form saves what a person enters; if no section lists the records, they
// cannot see what they saved, change it, or take it back.
func appGapSavedNotShown(spec AppSpec) []string {
	hasForm, shown := false, false
	for _, sec := range appSectionList(spec) {
		kind := strings.ToLower(mapStr(sec, "kind"))
		switch {
		case kind == "form":
			hasForm = true
		case (kind == "table" || kind == "display") && strings.TrimSpace(mapStr(sec, "source_script")) == "":
			shown = true
		case kind == "workbench":
			shown = true
		}
	}
	if hasForm && !shown {
		return []string{"what the form saves is never shown back: add a table over the records (deletable, and editable where it makes sense) so a person can see, correct and remove what they entered"}
	}
	return nil
}

// With nothing saved yet, a source that prints nothing leaves a new user a
// blank page and no idea what to do.
func appGapFirstVisit(in appGapInput) []string {
	var out []string
	names := make([]string, 0, len(in.empty))
	for n := range in.empty {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		var v any
		if json.Unmarshal([]byte(strings.TrimSpace(in.empty[n])), &v) == nil && appOutputEmpty(v) {
			out = append(out, fmt.Sprintf("with nothing saved yet, data/%s prints an empty result, so a first visit shows a blank page: print something that says what to do (\"Add a city to see its forecast\"), in the shape its section reads", n))
		}
	}
	return out
}

var assetRefRE = regexp.MustCompile(`["'(=\s](/?assets/[^"'\s)>]+)`)

// Pictures the page asks for that the app does not have, or asks for by a
// path that cannot resolve.
func appGapAssets(user string, spec AppSpec, in appGapInput) []string {
	var texts []string
	for _, sec := range appSectionList(spec) {
		texts = append(texts, mapStr(sec, "html"))
	}
	for _, o := range in.outputs {
		texts = append(texts, o)
	}
	for _, ds := range spec.DataSources {
		texts = append(texts, ds.Script)
	}
	have := map[string]bool{}
	if names, err := ListAppAssets(spec.Owner, spec.Slug); err == nil {
		for _, n := range names {
			have[n] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		for _, m := range assetRefRE.FindAllStringSubmatch(" "+t, -1) {
			ref := strings.TrimRight(m[1], `\`)
			if seen[ref] {
				continue
			}
			seen[ref] = true
			name := strings.TrimPrefix(strings.TrimPrefix(ref, "/"), "assets/")
			switch {
			case strings.HasPrefix(ref, "/"):
				out = append(out, fmt.Sprintf("%s starts at the server's root, not the app: write assets/%s", ref, name))
			case strings.Contains(name, "/"):
				out = append(out, fmt.Sprintf("%s names a folder; asset names are flat (assets/%s)", ref, strings.ReplaceAll(name, "/", "-")))
			case strings.ContainsAny(name, "{}%"):
				// A name built at run time; the outputs above carry the real ones.
			case !have[name]:
				out = append(out, fmt.Sprintf("%s is shown but the app has no such asset: add it (make it, find it with fetch_url save_to, or generate it with add_asset prompt=) or draw it inline", ref))
			}
		}
	}
	return out
}

var hostRE = regexp.MustCompile(`https?://([A-Za-z0-9.-]+)`)

// A script that reads the same service one of the owner's tools does,
// without calling the tool, is a second copy of that tool to keep in step.
func appGapCopiedTool(user string, spec AppSpec) []string {
	tools := LoadPersistentTempTools(RootDB, user)
	if len(tools) == 0 {
		return nil
	}
	toolHosts := map[string][]string{}
	for _, p := range tools {
		for _, m := range hostRE.FindAllStringSubmatch(p.Tool.ScriptBody+" "+p.Tool.CommandTemplate, -1) {
			toolHosts[strings.ToLower(m[1])] = append(toolHosts[strings.ToLower(m[1])], p.Tool.Name)
		}
	}
	var out []string
	check := func(kind, name, script string) {
		if strings.Contains(script, "call_tool") {
			return
		}
		said := map[string]bool{}
		for _, m := range hostRE.FindAllStringSubmatch(script, -1) {
			for _, tool := range toolHosts[strings.ToLower(m[1])] {
				if said[tool] {
					continue
				}
				said[tool] = true
				if _, err := temptool.ScriptCallableTool(RootDB, user, tool); err != nil {
					continue
				}
				out = append(out, fmt.Sprintf("%s %q reads %s, which the owner's tool %s already does: call it (capabilities tool:%s, then call_tool(%q, ...)) instead of keeping a copy of its code", kind, name, m[1], tool, tool, tool))
			}
		}
	}
	for _, ds := range spec.DataSources {
		check("data source", ds.Name, ds.Script)
	}
	for _, act := range spec.Actions {
		check("action", act.Name, act.Script)
	}
	return out
}
