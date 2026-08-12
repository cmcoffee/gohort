package servitor

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// servitorWorkerToolAllowList is the set of tool names ANY servitor worker may
// expose to the LLM. Servitor handles sensitive system data (SSH credentials,
// log contents, runtime facts) and is pinned to the local worker tier via
// Private:true on its route stages. To preserve that posture, every tool on
// THIS list must be a local-only operation — SSH exec against the
// user-configured appliance, local DB writes, or local file reads. Tools that
// reach third-party services (web_search, fetch_url, browse_page,
// generate_image, call_<credname>, etc.) are not allowed here.
//
// To extend THIS list: add the name only after confirming it never makes a
// network call to anything other than the user's own SSH appliance or the local
// llama.cpp inference server.
//
// ONE TYPE IS DIFFERENT. A "toolset" appliance is investigated through tools the
// owner explicitly BOUND to that one target, and those may reach the service
// they were authored against — that is the entire point of the type. They are
// sanctioned per appliance rather than globally: see assertAllowedWithBindings
// below, and toolset.go for why a curated, fingerprinted, per-target list is a
// NARROWER permission than the risk-category grant `run_command` already
// carries. Nothing on this list changes for that type; the bound set is checked
// alongside it.
var servitorWorkerToolAllowList = map[string]bool{
	"map_find":           true, // local: read the appliance's scoped graph
	"map_neighbors":      true, // local: traverse the appliance's scoped graph
	"map_path":           true, // local: shortest recorded chain in the scoped graph
	"run_command":        true, // SSH exec on the user's appliance
	"run_pty":            true, // SSH pty on the user's appliance
	"search_code":        true, // local: substring search over the encrypted repo store
	"read_file":          true, // local: read a file from the encrypted repo store
	"list_dir":           true, // local: list a directory in the encrypted repo store
	"bundle_summary":     true, // local: read the bundle index in the encrypted evidence store
	"list_bundle":        true, // local: list files in the encrypted evidence store
	"search_bundle":      true, // local: regex scan over the encrypted evidence store
	"read_bundle_file":   true, // local: bounded line-range read from the evidence store
	"bundle_timeline":    true, // local: merge evidence-store lines by timestamp
	"read_log":           true, // local: read log file from local fs / kvlite
	"search_logs":        true, // local: grep over stored logs
	"count_lines":        true, // local: bounded line count via SSH or local
	"read_range":         true, // local: bounded file read
	"note_lesson":        true, // local: kvlite write
	"record_technique":   true, // local: kvlite write
	"record_discovery":   true, // local: kvlite write
	"store_fact":         true, // local: kvlite write
	"link_entities":      true, // local: scoped graph write
	"store_rule":         true, // local: kvlite write
	"search_facts":       true, // local: kvlite read
	"search_knowledge":   true, // local: vector search over owner's linked collections (embeds via local llama.cpp)
	"watch_condition":    true, // local: watcher setup against the same appliance
	"list_watches":       true, // local: watcher state
	"save_to_codewriter": true, // local: gohort CodeWriter DB write
	"save_to_techwriter": true, // local: gohort TechWriter DB write
	"record_finding":     true, // local: queues a finding in the Guides inbox (via core FindingTarget)
	"push_to_guide":      true, // local: gohort Guides DB write (via core DocumentTarget)
	"list_guides":        true, // local: gohort Guides read
}

// servitorOrchestratorToolAllowList is the corresponding set for the
// orchestrator (investigator) loop. Smaller than the worker set because
// the orchestrator delegates execution to workers via probe_tool — it
// itself only plans and records. Same external-call posture: nothing
// here may reach a third-party service.
var servitorOrchestratorToolAllowList = map[string]bool{
	"map_find":              true, // local: read the appliance's scoped graph
	"map_neighbors":         true, // local: traverse the appliance's scoped graph
	"map_path":              true, // local: shortest recorded chain in the scoped graph
	"probe":                 true, // delegate to a worker (internal)
	"read_doc":              true, // local: doc state read
	"update_doc":            true, // local: doc state write
	"store_fact":            true, // local: kvlite write
	"link_entities":         true, // local: scoped graph write
	"record_discovery":      true, // local: kvlite write
	"record_technique":      true, // local: kvlite write
	"note_lesson":           true, // local: kvlite write
	"set_plan":              true, // local: session plan state
	"mark_step_in_progress": true, // local: session plan state
	"record_step_findings":  true, // local: session plan state
	"mark_step_blocked":     true, // local: session plan state
	"revise_plan":           true, // local: session plan state
	"report_gaps":           true, // local: session plan state
}

// servitorWorkspaceToolAllowList is the set for the WORKSPACE coordinator — the
// cross-appliance lead. It reaches nothing itself: every tool here either
// dispatches an investigation into a member (which runs under that member's own
// worker, already bound by servitorWorkerToolAllowList) or reads a local store.
// Same posture as the others: nothing may reach a third party.
var servitorWorkspaceToolAllowList = map[string]bool{
	"investigate_member":    true, // delegate to one member's own investigator (internal)
	"investigate_cluster":   true, // same, fanned out across members (internal)
	"search_code":           true, // local: substring search over a member's encrypted repo store
	"search_evidence":       true, // local: regex scan over an evidence member's encrypted bundle store
	"search_knowledge":      true, // local: vector search over the workspace's linked collections
	"set_plan":              true, // local: session plan state
	"mark_step_in_progress": true, // local: session plan state
	"record_step_findings":  true, // local: session plan state
	"mark_step_blocked":     true, // local: session plan state
	"revise_plan":           true, // local: session plan state
	"report_gaps":           true, // local: session plan state
}

// assertOnlyAllowedTools panics if any tool in tools has a name not
// present in allowed. Invoked at servitor request setup so a future
// "let's just add fetch_url here" can't sneak past code review without
// also editing the allow-list above. Panic (not return error) is
// deliberate: a leaked tool name is a privacy invariant break, and
// hard-failing at startup of the request is louder than logging.
func assertOnlyAllowedTools(label string, tools []AgentToolDef, allowed map[string]bool) {
	assertAllowedWithBindings(label, tools, allowed, nil)
}

// assertAllowedWithBindings is the same guard for a TOOLSET appliance, where the
// sanctioned set is partly DATA: the compile-time list above, plus the tools the
// owner explicitly bound to this one target.
//
// This is a carve-out for one appliance type, not a relaxation of servitor. The
// compile-time list still governs every other type, and a bound tool is
// sanctioned only for the appliance whose record names it — which is a narrower
// permission than anything servitor grants elsewhere. Compare `run_command`,
// where the grant is a whole risk CATEGORY covering an open set of command
// strings nobody has read; here it is a finite list of tools with fingerprints,
// approved one at a time.
//
// Derived names are accepted by prefix: an expanded toolbox contributes
// `<toolbox>_<action>` entries whose names are not the bound name, and rejecting
// them would panic on a binding the owner made correctly.
func assertAllowedWithBindings(label string, tools []AgentToolDef, allowed map[string]bool, bound map[string]bool) {
	var bad []string
	for _, td := range tools {
		name := td.Tool.Name
		if allowed[name] || bound[name] || boundByPrefix(bound, name) {
			continue
		}
		bad = append(bad, name)
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		panic(fmt.Sprintf("servitor tool guard: %s contains disallowed tools %v — update servitor/tool_guard.go allow-list only after confirming they make no third-party network calls, or bind the tool to the appliance", label, bad))
	}
}

// boundByPrefix reports whether name is a derived action of a bound toolbox.
func boundByPrefix(bound map[string]bool, name string) bool {
	for b := range bound {
		if strings.HasPrefix(name, b+"_") {
			return true
		}
	}
	return false
}
