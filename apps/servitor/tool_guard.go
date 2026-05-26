package servitor

import (
	"fmt"
	"sort"

	. "github.com/cmcoffee/gohort/core"
)

// servitorWorkerToolAllowList is the exhaustive set of tool names that
// the servitor worker may expose to the LLM. Servitor handles sensitive
// system data (SSH credentials, log contents, runtime facts) and is
// pinned to the local worker tier via Private:true on its route stages.
// To preserve that posture, every tool the worker can call MUST be a
// local-only operation — SSH exec against the user-configured appliance,
// local DB writes, or local file reads. Tools that reach third-party
// services (web_search, fetch_url, browse_page, generate_image,
// call_<credname>, etc.) are explicitly NOT allowed here.
//
// To extend: add the tool name only after confirming it never makes a
// network call to anything other than the user's own SSH appliance or
// the local llama.cpp inference server.
var servitorWorkerToolAllowList = map[string]bool{
	"run_command":         true, // SSH exec on the user's appliance
	"run_pty":             true, // SSH pty on the user's appliance
	"read_log":            true, // local: read log file from local fs / kvlite
	"search_logs":         true, // local: grep over stored logs
	"count_lines":         true, // local: bounded line count via SSH or local
	"read_range":          true, // local: bounded file read
	"note_lesson":         true, // local: kvlite write
	"record_technique":    true, // local: kvlite write
	"record_discovery":    true, // local: kvlite write
	"store_fact":          true, // local: kvlite write
	"store_rule":          true, // local: kvlite write
	"search_facts":        true, // local: kvlite read
	"watch_condition":     true, // local: watcher setup against the same appliance
	"list_watches":        true, // local: watcher state
	"save_to_codewriter":  true, // local: gohort CodeWriter DB write
	"save_to_techwriter":  true, // local: gohort TechWriter DB write
}

// servitorOrchestratorToolAllowList is the corresponding set for the
// orchestrator (investigator) loop. Smaller than the worker set because
// the orchestrator delegates execution to workers via probe_tool — it
// itself only plans and records. Same external-call posture: nothing
// here may reach a third-party service.
var servitorOrchestratorToolAllowList = map[string]bool{
	"probe":                  true, // delegate to a worker (internal)
	"read_doc":               true, // local: doc state read
	"update_doc":             true, // local: doc state write
	"store_fact":             true, // local: kvlite write
	"record_discovery":       true, // local: kvlite write
	"record_technique":       true, // local: kvlite write
	"note_lesson":            true, // local: kvlite write
	"set_plan":               true, // local: session plan state
	"mark_step_in_progress":  true, // local: session plan state
	"record_step_findings":   true, // local: session plan state
	"mark_step_blocked":      true, // local: session plan state
	"revise_plan":            true, // local: session plan state
	"report_gaps":            true, // local: session plan state
}

// assertOnlyAllowedTools panics if any tool in tools has a name not
// present in allowed. Invoked at servitor request setup so a future
// "let's just add fetch_url here" can't sneak past code review without
// also editing the allow-list above. Panic (not return error) is
// deliberate: a leaked tool name is a privacy invariant break, and
// hard-failing at startup of the request is louder than logging.
func assertOnlyAllowedTools(label string, tools []AgentToolDef, allowed map[string]bool) {
	var bad []string
	for _, td := range tools {
		if !allowed[td.Tool.Name] {
			bad = append(bad, td.Tool.Name)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		panic(fmt.Sprintf("servitor tool guard: %s contains disallowed tools %v — update servitor/tool_guard.go allow-list only after confirming they make no third-party network calls", label, bad))
	}
}
