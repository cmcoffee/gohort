package orchestrate

// An app used only through its tools has no page of its own (app_def with no
// sections). It is given one section, a centered note saying it works
// through its tools and naming them, so the page an app is everywhere else
// (the build, verify, its folder in Anvil) needs no case of its own.

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// appSectionsMissing reports an app_def call that gives the app no page: a
// create with no sections, or any call with an empty sections array. An
// update that leaves sections out keeps the page it has.
func appSectionsMissing(args map[string]any, isUpdate bool) bool {
	raw, given := args["sections"]
	if !given || raw == nil {
		return !isUpdate
	}
	arr, ok := raw.([]any)
	return ok && len(arr) == 0
}

// appExposesTools reports whether spec offers anything beyond a page: an
// action or data source marked expose, or its pipeline.
func appExposesTools(spec AppSpec) bool {
	for _, a := range spec.Actions {
		if a.Expose {
			return true
		}
	}
	for _, d := range spec.DataSources {
		if d.Expose {
			return true
		}
	}
	return spec.ExposePipeline && strings.TrimSpace(spec.PipelineID) != ""
}

// toolsOnlySection is the page of an app used only through its tools.
func toolsOnlySection(spec AppSpec) map[string]any {
	var what []string
	for _, a := range spec.Actions {
		if a.Expose {
			what = append(what, firstNonEmptyStr(a.Label, a.Name))
		}
	}
	for _, d := range spec.DataSources {
		if d.Expose {
			what = append(what, d.Name)
		}
	}
	if spec.ExposePipeline && strings.TrimSpace(spec.PipelineID) != "" {
		what = append(what, "its pipeline")
	}
	return map[string]any{
		"id":    "tools",
		"kind":  "empty",
		"icon":  "🔧",
		"title": "Used through its tools",
		"hint": "This app has no page of its own. It offers " + strings.Join(what, ", ") +
			": tick them on one of your agents (its Tools), or call them from an MCP client.",
	}
}
