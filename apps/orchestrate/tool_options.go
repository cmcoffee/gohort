package orchestrate

import (
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// availableWorkerToolOptions returns the worker tool catalog as
// SelectOptions for the Tools modal in the agent editor / chat
// toolbar.
//
// Sources merged:
//
//   1. Globally-registered ChatTools minus BlockedTools, filtered to
//      capabilities {Read, Network}.
//   2. The given user's persistent temp tools — surfaces user-defined
//      tools (vapi-style API wrappers, scripts, etc.) so admin groups
//      that include them produce visible section headers in the modal.
//      Empty user → no temp tools surfaced (e.g. unauthenticated
//      preview path).
//
// keep_going is dropped because the agent loop auto-loads it; users
// shouldn't have to remember to enable it, and unchecking it would
// just silently get re-added.
//
// Sorting: capability buckets first (Network, Network+Read, Read,
// Other), then toolbox sections alphabetically. Within each section,
// tools sort by name. Tied priority breaks on group name so distinct
// toolbox sections never interleave.
// frameworkInfrastructureTools lists names of tools the framework
// auto-includes turn-bound (knowledge_save / store_fact / etc.)
// that aren't tagged via the FrameworkTool interface — typically
// because they're per-turn closures built by chatTurn rather than
// global registry entries. Most of these aren't in
// RegisteredChatTools() at all, so the picker doesn't see them
// anyway; this map is defense in depth for the few that ARE
// globally registered but framework-managed via different paths.
//
// New framework tools should implement the FrameworkTool
// interface (IsFrameworkTool() bool) rather than being added
// here — that hides them from every picker in one shot.
var frameworkInfrastructureTools = map[string]bool{
	"knowledge_save":     true,
	"knowledge_search":   true,
	"forget_knowledge":   true,
	"store_fact":         true,
	"forget_fact":        true,
	"list_facts":         true,
	"agents":             true,
	"present_build_plan": true,
	"mark_step_done":     true,
}

func availableWorkerToolOptions(user string) []ui.SelectOption {
	pool := FilterChatTools(BlockedTools)
	defs := make([]AgentToolDef, 0, len(pool))
	for _, t := range pool {
		// Framework-tagged tools hide via the interface — single
		// source of truth across every picker in the codebase.
		if IsFrameworkTool(t) {
			continue
		}
		if frameworkInfrastructureTools[t.Name()] {
			continue
		}
		defs = append(defs, ChatToolToAgentToolDefWithSession(t, nil))
	}
	defs = FilterToolsByCaps(defs, []Capability{CapRead, CapNetwork})

	// Surface this user's persistent temp tools too — admin groups
	// often target these (vapi wrappers, etc.) and the modal needs
	// to see them for the group's section header to appear. Caps
	// gating doesn't apply here (these are admin-approved tools the
	// user already has access to at runtime).
	if user != "" {
		for _, p := range LoadPersistentTempTools(AuthDB(), user) {
			defs = append(defs, AgentToolDef{
				Tool: Tool{
					Name:        p.Tool.Name,
					Description: p.Tool.Description,
				},
			})
		}
	}

	// Use group membership to drive the section header so admins see
	// toolbox organization without losing per-tool toggle granularity.
	// A tool in agent_management_toolbox shows up under the "Agent
	// Management" header; tools in no group fall back to their
	// capability label ("Network", "Read", …).
	memberToGroup := map[string]string{}
	for _, g := range LoadToolGroups(AuthDB()) {
		for _, m := range g.Members {
			if _, already := memberToGroup[m]; already {
				continue // first group wins on overlap
			}
			memberToGroup[m] = g.Name
		}
	}

	opts := make([]ui.SelectOption, 0, len(defs))
	for _, d := range defs {
		if d.Tool.Name == "keep_going" {
			continue
		}
		group := capGroupLabel(d.Tool.Caps)
		if gName, ok := memberToGroup[d.Tool.Name]; ok {
			group = gName
		}
		opts = append(opts, ui.SelectOption{
			Value: d.Tool.Name,
			Label: d.Tool.Name,
			Help:  firstLine(d.Tool.Description),
			Group: group,
		})
	}
	sort.Slice(opts, func(i, j int) bool {
		if opts[i].Group != opts[j].Group {
			oi := capGroupOrder(opts[i].Group)
			oj := capGroupOrder(opts[j].Group)
			if oi != oj {
				return oi < oj
			}
			// Same priority bucket (e.g. multiple toolbox names all
			// returning the same constant) — break the tie by group
			// name so the comparator obeys strict ordering. Without
			// this, sort.Slice sees equal priority for distinct group
			// strings and interleaves the rows.
			return opts[i].Group < opts[j].Group
		}
		return opts[i].Value < opts[j].Value
	})
	return opts
}

// availableWorkerToolNames returns just the names from the default
// pool — REGISTERED ChatTool names only, excluding persistent temp
// tools (which the modal displays via availableWorkerToolOptions but
// which the runner must NOT route through GetAgentToolsWithSession,
// since that lookup only knows about registered tools). Temp tools
// reach the runner separately, via temptool.BuildAgentToolDefs in
// runPlan/runWorkerStep. Passing user="" skips the temp-tool merge
// the options function would do — we don't want those names here.
func availableWorkerToolNames() []string {
	// Pass user="" so the options builder skips the temp-tool merge.
	// The result is purely the registered ChatTool surface filtered
	// by caps + blocklist, which is exactly what the runner's
	// AllowedTools intersection should match against.
	opts := availableWorkerToolOptions("")
	names := make([]string, 0, len(opts))
	for _, o := range opts {
		names = append(names, o.Value)
	}
	return names
}

// internetWorkerToolNames returns the names of worker tools that
// contact the network (implement InternetTool with IsInternetTool()
// returning true). Used by the chat page to ship a small filter list
// to the browser so the Tools button count can subtract these when
// Private mode is enabled — matching the runtime filter the agent
// loop applies in private mode (see resolveWorkerTools in runner.go).
func internetWorkerToolNames() []string {
	pool := FilterChatTools(BlockedTools)
	var out []string
	for _, t := range pool {
		if it, ok := t.(InternetTool); ok && it.IsInternetTool() {
			out = append(out, t.Name())
		}
	}
	sort.Strings(out)
	return out
}

// capGroupLabel maps a tool's capability set to a human-readable
// checklist group header. Tools with no caps fall under "Other" so
// they don't get silently bucketed with Network tools.
func capGroupLabel(caps []Capability) string {
	hasRead, hasNet := false, false
	for _, c := range caps {
		switch c {
		case CapRead:
			hasRead = true
		case CapNetwork:
			hasNet = true
		}
	}
	switch {
	case hasNet && hasRead:
		return "Network + Read"
	case hasNet:
		return "Network"
	case hasRead:
		return "Read"
	default:
		return "Other"
	}
}

// capGroupOrder sorts section headers in the checklist. Capability
// groups land first in their familiar order (Network, Network+Read,
// Read, Other); anything else — i.e. a toolbox display name — sorts
// alphabetically below them by returning a high constant. Toolbox
// sections then cluster together, ordered by name within the
// alpha-sort below.
func capGroupOrder(g string) int {
	switch g {
	case "Network":
		return 0
	case "Network + Read":
		return 1
	case "Read":
		return 2
	case "Other":
		return 3
	default:
		// Toolbox display name — sort after the capability groups.
		return 100
	}
}

// firstLine returns the first non-empty line of s, clipped to keep
// the checklist help text scannable. Tool descriptions sometimes run
// for paragraphs — we only want the lede.
func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if len(ln) > 140 {
			ln = ln[:140] + "…"
		}
		return ln
	}
	return ""
}
