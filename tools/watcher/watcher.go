// Watcher tool — LLM-facing management for polling watchers.
//
// The model: a watcher is a captured tool call that re-runs every N
// seconds. The LLM proves a tool call works (e.g. by invoking
// call_ts3_api(url=/1/clientlist) once and seeing real data come back),
// then asks for a watcher with that same tool_name + tool_args. The
// watcher inherits the tool's auth, validation, and response shape —
// nothing parallel to maintain in the watcher subsystem itself.

package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	gt := NewGroupedTool("watcher",
		"Create + manage long-running polling watchers. A watcher repeats one of your tool calls every N seconds and wakes the worker LLM when the result changes — useful for 'tell me when X happens' patterns. Watchers wrap an existing tool call, so authenticate/URL/format are handled by the tool you point them at, not by the watcher.")

	gt.AddAction("list", &GroupedToolAction{
		Description: "List all watchers you own.",
		Params:      map[string]ToolParam{},
		Caps:        []Capability{CapRead},
		Handler:     handleList,
	})

	gt.AddAction("create", &GroupedToolAction{
		Description: "Create a polling watcher around a tool call. Required: name, tool_name, tool_args, interval_seconds (>=60). Pick ONE evaluator: \"llm\"+action_prompt (worker LLM analyzes each diff), \"script\"+evaluator_script+evaluator_test (sandboxed python — zero LLM cost), or \"raw\" (current response IS the alert). Call action=\"help\" for the recommended flow + script-writing rules.",
		Params: map[string]ToolParam{
			"name":             {Type: "string", Description: "Short identifier (snake_case)."},
			"tool_name":        {Type: "string", Description: "Tool to invoke each cycle (built-in, call_<credname>, or runtime-defined)."},
			"tool_args":        {Type: "object", Description: "Args passed to tool_name every cycle."},
			"interval_seconds": {Type: "integer", Description: "Poll interval. Minimum 60."},
			"evaluator":        {Type: "string", Description: "\"llm\" (default), \"script\", or \"raw\"."},
			"action_prompt":    {Type: "string", Description: "Required for evaluator=llm. What the worker should do when a change is detected."},
			"evaluator_script": {Type: "string", Description: "Required for evaluator=script. Python reading {\"prior\":str,\"current\":str} on stdin, printing alert to stdout (empty=no alert). Network-less, read-only-FS, 10s limit. See action=\"help\" for script-writing rules."},
			"evaluator_test":   {Type: "object", Description: "Required for evaluator=script. {\"expected_fields\":[...]} — substrings that MUST appear in the probe response, verified at create time. Optional: synthetic_prior + expected_output_contains for full diff-logic verification."},
			"delivery_prefix":  {Type: "string", Description: "Optional. Override the default \"[watcher: <name>]\\n\" tag. Empty string suppresses entirely."},
		},
		Required:     []string{"name", "tool_name", "tool_args", "interval_seconds"},
		Caps:         []Capability{CapNetwork, CapWrite, CapExecute},
		NeedsConfirm: true,
		Handler:      handleCreate,
	})

	gt.AddAction("update", &GroupedToolAction{
		Description: "Update an existing watcher's action_prompt, interval_seconds, evaluator, evaluator_script, or delivery_prefix. Cannot change the captured tool_name/tool_args (changing what's being watched is structurally a different watcher — delete + create instead). Returns the updated watcher record.",
		Params: map[string]ToolParam{
			"id":               {Type: "string", Description: "Watcher ID (from list)."},
			"action_prompt":    {Type: "string", Description: "Optional. New action_prompt. Omit to leave unchanged."},
			"interval_seconds": {Type: "integer", Description: "Optional. New polling interval (>=60). Omit or 0 to leave unchanged."},
			"evaluator":        {Type: "string", Description: "Optional. Change the evaluator mode (\"llm\", \"script\", \"raw\"). When switching to \"script\" you must also pass evaluator_script."},
			"evaluator_script": {Type: "string", Description: "Optional. New python source for evaluator=\"script\". Omit to leave unchanged."},
			"delivery_prefix":  {Type: "string", Description: "Optional. New delivery prefix. Pass empty string to suppress prefix; pass the literal string \"reset\" to revert to the routing app's default."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapWrite},
		Handler:  handleUpdate,
	})

	gt.AddAction("delete", &GroupedToolAction{
		Description: "Delete a watcher by id (also cancels its pending poll).",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Watcher ID (from list)."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapWrite},
		Handler:  handleDelete,
	})

	gt.AddAction("enable", &GroupedToolAction{
		Description: "Enable a watcher and queue its next poll.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Watcher ID."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapWrite},
		Handler:  func(args map[string]any, sess *ToolSession) (string, error) { return setEnabled(args, sess, true) },
	})

	gt.AddAction("disable", &GroupedToolAction{
		Description: "Disable a watcher and cancel its pending poll. Existing results are preserved.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Watcher ID."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapWrite},
		Handler:  func(args map[string]any, sess *ToolSession) (string, error) { return setEnabled(args, sess, false) },
	})

	gt.AddAction("peek", &GroupedToolAction{
		Description: "Show the cached result the watcher is currently comparing against. Use this to debug why a watcher isn't firing: if peek shows an error response, the underlying tool call is broken; if peek shows the expected payload, the upstream truly isn't changing.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Watcher ID."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapRead},
		Handler:  handlePeek,
	})

	gt.AddAction("results", &GroupedToolAction{
		Description: "Show recent fires (trigger summary + worker reply) for one watcher. Newest first.",
		Params: map[string]ToolParam{
			"id":    {Type: "string", Description: "Watcher ID."},
			"limit": {Type: "integer", Description: "How many recent results to return. Default 10, max 50."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapRead},
		Handler:  handleResults,
	})

	RegisterChatTool(gt)
}

// ----------------------------------------------------------------------
// handlers
// ----------------------------------------------------------------------

func handleList(args map[string]any, sess *ToolSession) (string, error) {
	owner := ownerFor(sess)
	ws := ListWatchers(owner)
	if len(ws) == 0 {
		return "No watchers defined.", nil
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].Name < ws[j].Name })
	var b strings.Builder
	for _, w := range ws {
		eval := w.Evaluator
		if eval == "" {
			eval = "llm"
		}
		fmt.Fprintf(&b, "- id=%s  name=%q  enabled=%t  fires=%d  tool=%s  interval=%ds  evaluator=%s",
			w.ID, w.Name, w.Enabled, w.FireCount, w.ToolName, w.IntervalSec, eval)
		if !w.LastFiredAt.IsZero() {
			fmt.Fprintf(&b, "  last_fired=%s", w.LastFiredAt.UTC().Format(time.RFC3339))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func handleCreate(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(StringArg(args, "name"))
	toolName := strings.TrimSpace(StringArg(args, "tool_name"))
	prompt := strings.TrimSpace(StringArg(args, "action_prompt"))
	interval := IntArg(args, "interval_seconds")
	if interval < 60 {
		return "", fmt.Errorf("interval_seconds must be >= 60 (you asked for %d)", interval)
	}

	evaluator := strings.ToLower(strings.TrimSpace(StringArg(args, "evaluator")))
	if evaluator == "" {
		evaluator = "llm"
	}
	script := StringArg(args, "evaluator_script")
	switch evaluator {
	case "llm":
		if prompt == "" {
			return "", fmt.Errorf("evaluator=\"llm\" requires action_prompt — describe what you want the worker to do when a change is detected")
		}
	case "script":
		if strings.TrimSpace(script) == "" {
			return "", fmt.Errorf("evaluator=\"script\" requires evaluator_script — the python source that processes prior/current and prints alert text")
		}
	case "raw":
		// no extras required
	default:
		return "", fmt.Errorf("evaluator must be one of \"llm\", \"script\", \"raw\" (you passed %q)", evaluator)
	}

	toolArgs, err := coerceArgsObject(args["tool_args"])
	if err != nil {
		return "", fmt.Errorf("tool_args: %w", err)
	}

	// Validate the tool exists by attempting the dispatch path. Both
	// the static registry path and the call_<name> credential path
	// surface clear errors that we want the LLM to see at create time.
	if strings.HasPrefix(toolName, "call_") {
		credName := strings.TrimPrefix(toolName, "call_")
		if _, ok := Secure().Load(credName); !ok {
			return "", fmt.Errorf("tool_name %q references credential %q which is not registered", toolName, credName)
		}
	} else {
		if _, ok := LookupChatTool(toolName); !ok {
			return "", fmt.Errorf("tool_name %q is not a registered chat tool", toolName)
		}
	}

	// Probe the call once to confirm it actually works. This becomes
	// the baseline-seeding poll if successful — saves a 60s wait
	// before the operator finds out the tool call is broken.
	probe, probeErr := InvokeWatcherTool(toolName, toolArgs)
	if probeErr != nil {
		return "", fmt.Errorf("test invocation of %q failed: %w — fix the call (run it directly to confirm it works) before creating the watcher", toolName, probeErr)
	}
	if looksLikeErrorBody(probe) {
		return "", fmt.Errorf("test invocation of %q returned what looks like an error response (%d bytes): %s — fix the call before creating the watcher (otherwise it will never detect change since the error body is byte-identical every poll)", toolName, len(probe), truncate(probe, 300))
	}

	// Script-mode validation: expected_fields verification + auto dry-
	// run + optional synthetic-diff test. Forces the LLM to look at
	// the actual response shape before committing — catches the
	// camelCase-vs-snake_case / forgot-to-strip-HTTP-prefix class of
	// bugs at create time instead of an hour later when "no alert
	// fires" gets reported.
	if evaluator == "script" {
		if err := validateScriptAgainstProbe(args, script, probe); err != nil {
			return "", err
		}
	}

	// The probe response IS the baseline — no point waiting for the
	// first scheduled poll to seed something we already have.
	probeBody := probe
	if len(probeBody) > 4096 {
		probeBody = probeBody[:4096]
	}
	w := Watcher{
		Name:            name,
		Owner:           ownerFor(sess),
		Kind:            "polling",
		ActionPrompt:    prompt,
		Enabled:         true,
		Target:          targetFor(sess),
		ToolName:        toolName,
		ToolArgs:        toolArgs,
		IntervalSec:     interval,
		Evaluator:       evaluator,
		EvaluatorScript: script,
		LastResultHash:  HashWatcherBody(probe),
		LastResultBody:  probeBody,
		FireCount:       1, // probe counts as the seed; next change triggers the worker
	}
	// delivery_prefix is opt-in. The "Set" sentinel distinguishes
	// "use the routing app's default" (false) from "use this exact
	// value, even if empty" (true) so an LLM that wants to suppress
	// the prefix entirely with delivery_prefix="" persists correctly
	// across save/load.
	if raw, ok := args["delivery_prefix"]; ok && raw != nil {
		if s, isStr := raw.(string); isStr {
			w.DeliveryPrefixSet = true
			w.DeliveryPrefix = s
		}
	}
	if err := SaveWatcher(w); err != nil {
		return "", err
	}
	for _, candidate := range ListWatchers(w.Owner) {
		if candidate.Name == name && candidate.ToolName == toolName {
			SchedulePollNow(candidate)
			Debug("[watcher] created %s (id=%s, tool=%s, interval=%ds, evaluator=%s, owner=%q)",
				candidate.Name, candidate.ID, candidate.ToolName, candidate.IntervalSec, evaluator, candidate.Owner)
			return fmt.Sprintf("Watcher created (id=%s, name=%q, polls %s every %ds, evaluator=%s). Test invocation returned %d bytes — review the response to confirm it's what you expected; if not, delete the watcher and recreate with corrected args.\n\n--- test response ---\n%s\n--- end test response ---",
				candidate.ID, candidate.Name, candidate.ToolName, candidate.IntervalSec, evaluator,
				len(probe), truncate(probe, 800)), nil
		}
	}
	return "Watcher created.", nil
}

func handleDelete(args map[string]any, sess *ToolSession) (string, error) {
	id := strings.TrimSpace(StringArg(args, "id"))
	w, ok := LoadWatcher(id)
	if !ok {
		return "", fmt.Errorf("watcher %q not found", id)
	}
	if !ownsWatcher(sess, w) {
		return "", fmt.Errorf("watcher %q is not yours", id)
	}
	if err := DeleteWatcher(id); err != nil {
		return "", err
	}
	return fmt.Sprintf("Watcher %q deleted.", w.Name), nil
}

func setEnabled(args map[string]any, sess *ToolSession, enabled bool) (string, error) {
	id := strings.TrimSpace(StringArg(args, "id"))
	w, ok := LoadWatcher(id)
	if !ok {
		return "", fmt.Errorf("watcher %q not found", id)
	}
	if !ownsWatcher(sess, w) {
		return "", fmt.Errorf("watcher %q is not yours", id)
	}
	if err := SetWatcherEnabled(id, enabled); err != nil {
		return "", err
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	return fmt.Sprintf("Watcher %q %s.", w.Name, state), nil
}

func handleUpdate(args map[string]any, sess *ToolSession) (string, error) {
	id := strings.TrimSpace(StringArg(args, "id"))
	w, ok := LoadWatcher(id)
	if !ok {
		return "", fmt.Errorf("watcher %q not found", id)
	}
	if !ownsWatcher(sess, w) {
		return "", fmt.Errorf("watcher %q is not yours", id)
	}

	changed := []string{}

	if v := strings.TrimSpace(StringArg(args, "action_prompt")); v != "" {
		w.ActionPrompt = v
		changed = append(changed, "action_prompt")
	}

	if interval := IntArg(args, "interval_seconds"); interval > 0 {
		if interval < 60 {
			return "", fmt.Errorf("interval_seconds must be >= 60 (you asked for %d)", interval)
		}
		if interval != w.IntervalSec {
			w.IntervalSec = interval
			changed = append(changed, "interval_seconds")
		}
	}

	if v := strings.ToLower(strings.TrimSpace(StringArg(args, "evaluator"))); v != "" {
		if v != "llm" && v != "script" && v != "raw" {
			return "", fmt.Errorf("evaluator must be one of \"llm\", \"script\", \"raw\" (you passed %q)", v)
		}
		w.Evaluator = v
		changed = append(changed, "evaluator")
	}
	if raw, present := args["evaluator_script"]; present && raw != nil {
		if s, isStr := raw.(string); isStr {
			w.EvaluatorScript = s
			changed = append(changed, "evaluator_script")
		}
	}
	// Cross-validate after applying both: switching to "script" needs
	// a non-empty script; "llm" needs an action_prompt.
	switch w.Evaluator {
	case "script":
		if strings.TrimSpace(w.EvaluatorScript) == "" {
			return "", fmt.Errorf("evaluator=\"script\" requires evaluator_script — pass the python source in the same update call")
		}
	case "llm":
		if strings.TrimSpace(w.ActionPrompt) == "" {
			return "", fmt.Errorf("evaluator=\"llm\" requires action_prompt — pass it in the same update call")
		}
	}

	if raw, present := args["delivery_prefix"]; present && raw != nil {
		if s, isStr := raw.(string); isStr {
			if s == "reset" {
				w.DeliveryPrefixSet = false
				w.DeliveryPrefix = ""
				changed = append(changed, "delivery_prefix (reset to default)")
			} else {
				w.DeliveryPrefixSet = true
				w.DeliveryPrefix = s
				changed = append(changed, "delivery_prefix")
			}
		}
	}

	if len(changed) == 0 {
		return "No changes — pass at least one of action_prompt, interval_seconds, delivery_prefix.", nil
	}

	if err := SaveWatcher(w); err != nil {
		return "", err
	}
	// Re-schedule with new interval if it changed and watcher is enabled.
	if w.Enabled {
		_ = SetWatcherEnabled(w.ID, true) // forces drop+reschedule with the new interval
	}
	return fmt.Sprintf("Watcher %q updated: %s.", w.Name, strings.Join(changed, ", ")), nil
}

func handlePeek(args map[string]any, sess *ToolSession) (string, error) {
	id := strings.TrimSpace(StringArg(args, "id"))
	w, ok := LoadWatcher(id)
	if !ok {
		return "", fmt.Errorf("watcher %q not found", id)
	}
	if !ownsWatcher(sess, w) {
		return "", fmt.Errorf("watcher %q is not yours", id)
	}
	argsJSON, _ := json.Marshal(w.ToolArgs)
	var b strings.Builder
	fmt.Fprintf(&b, "Watcher %q (id=%s)\n", w.Name, w.ID)
	fmt.Fprintf(&b, "  tool: %s\n", w.ToolName)
	fmt.Fprintf(&b, "  args: %s\n", argsJSON)
	fmt.Fprintf(&b, "  interval: %ds  enabled: %t  fires: %d\n", w.IntervalSec, w.Enabled, w.FireCount)
	eval := w.Evaluator
	if eval == "" {
		eval = "llm"
	}
	fmt.Fprintf(&b, "  evaluator: %s\n", eval)
	if w.EvaluatorScript != "" {
		fmt.Fprintf(&b, "  evaluator_script (%d chars):\n---\n%s\n---\n", len(w.EvaluatorScript), w.EvaluatorScript)
	}
	if !w.LastFiredAt.IsZero() {
		fmt.Fprintf(&b, "  last_fired: %s\n", w.LastFiredAt.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "  hash: %s\n", w.LastResultHash)
	fmt.Fprintf(&b, "  cached_result (%d bytes):\n---\n%s\n---\n",
		len(w.LastResultBody), w.LastResultBody)
	return b.String(), nil
}

func handleResults(args map[string]any, sess *ToolSession) (string, error) {
	id := strings.TrimSpace(StringArg(args, "id"))
	limit := IntArg(args, "limit")
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	w, ok := LoadWatcher(id)
	if !ok {
		return "", fmt.Errorf("watcher %q not found", id)
	}
	if !ownsWatcher(sess, w) {
		return "", fmt.Errorf("watcher %q is not yours", id)
	}
	if len(w.Results) == 0 {
		return fmt.Sprintf("No fires yet for watcher %q.", w.Name), nil
	}
	results := append([]WatcherResult(nil), w.Results...)
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
	if len(results) > limit {
		results = results[:limit]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Recent fires for watcher %q (%d total, showing %d):\n\n",
		w.Name, w.FireCount, len(results))
	for _, r := range results {
		fmt.Fprintf(&b, "[%s]\n", r.Timestamp.UTC().Format(time.RFC3339))
		if r.Error != "" {
			fmt.Fprintf(&b, "  ERROR: %s\n", r.Error)
		} else {
			fmt.Fprintf(&b, "  reply: %s\n", oneLine(r.Reply, 400))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// ----------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------

func ownerFor(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	return sess.Username
}

func ownsWatcher(sess *ToolSession, w Watcher) bool {
	return ownerFor(sess) == w.Owner
}

// targetFor reads the host app's stamped routing target verbatim,
// falling back to "log" when the session was constructed without one
// (CLI tools, tests). The watcher tool deliberately knows nothing
// about specific apps — phantom, chat, or any future Discord/Slack
// integration sets sess.RoutingTarget at construction time and
// registers a matching WatcherResultRouter; the wiring works without
// any change here.
func targetFor(sess *ToolSession) string {
	if sess == nil || sess.RoutingTarget == "" {
		return "log"
	}
	return sess.RoutingTarget
}

// coerceArgsObject normalizes whatever the LLM passed for tool_args
// into a map[string]any. Qwen sometimes hands us a JSON string instead
// of a parsed object; tolerate both shapes.
func coerceArgsObject(raw any) (map[string]any, error) {
	if raw == nil {
		return nil, fmt.Errorf("tool_args is required (pass the args object you'd give to tool_name)")
	}
	switch v := raw.(type) {
	case map[string]any:
		return v, nil
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil, fmt.Errorf("tool_args was empty")
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return nil, fmt.Errorf("could not parse tool_args as JSON object: %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("tool_args must be an object, got %T", raw)
	}
}

// validateScriptAgainstProbe enforces the "look before you leap" gate
// for evaluator=script watchers. Verifies (in order):
//
//   1. evaluator_test was provided (with at least expected_fields).
//   2. Every string in expected_fields appears in the probe response —
//      catches wrong field-name casing / typos at create time.
//   3. Auto dry-run with prior="" + current=probe should produce
//      non-empty output (because everything is "new" relative to the
//      empty prior). Empty output here means the script can't extract
//      anything from the response, regardless of its diff logic.
//   4. Optional synthetic test: if synthetic_prior + expected_output_
//      contains are provided, run the script with them and verify the
//      output contains the expected substring. Catches diff-logic
//      bugs (e.g. comparing wrong field set, off-by-one in slicing).
//
// All failures include the actual probe response in the error so the
// LLM can see exactly what shape it's working with.
func validateScriptAgainstProbe(args map[string]any, script, probe string) error {
	testRaw, present := args["evaluator_test"]
	if !present || testRaw == nil {
		return fmt.Errorf("evaluator=\"script\" requires evaluator_test — at minimum {\"expected_fields\": [\"<field_name>\", ...]} listing substrings that must appear in the probe response. Probe was: %s", truncate(probe, 600))
	}
	test, err := coerceArgsObject(testRaw)
	if err != nil {
		return fmt.Errorf("evaluator_test must be an object: %w", err)
	}

	// 1. expected_fields verification.
	fieldsRaw, hasFields := test["expected_fields"]
	if !hasFields || fieldsRaw == nil {
		return fmt.Errorf("evaluator_test.expected_fields is required — array of substrings that must appear in the probe response. Probe was: %s", truncate(probe, 600))
	}
	fields, err := coerceStringArray(fieldsRaw)
	if err != nil {
		return fmt.Errorf("evaluator_test.expected_fields: %w", err)
	}
	if len(fields) == 0 {
		return fmt.Errorf("evaluator_test.expected_fields is empty — list at least one substring that must appear in the probe response")
	}
	var missing []string
	for _, f := range fields {
		if f == "" {
			continue
		}
		if !strings.Contains(probe, f) {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("evaluator_test.expected_fields not found in probe response: %v. Likely cause: wrong field name casing (camelCase vs snake_case) or typo. Adjust the script to use the actual field names. Probe was: %s",
			missing, truncate(probe, 600))
	}

	// 2. Auto dry-run: prior="" + current=probe. Output should be
	//    non-empty since every observed item is "new" relative to
	//    the empty prior.
	dryOut, dryErr := runProbeScript(script, "", probe)
	if dryErr != nil {
		return fmt.Errorf("dry-run failed (prior=\"\", current=probe): %w. Script must execute cleanly. Probe was: %s",
			dryErr, truncate(probe, 600))
	}
	if strings.TrimSpace(dryOut) == "" {
		return fmt.Errorf("dry-run produced no output when called with prior=\"\" and current=probe — script can't extract anything from the response. Common causes: wrong field names, forgot to strip an HTTP status prefix, JSON parse failure swallowed by bare except. Probe was: %s",
			truncate(probe, 600))
	}

	// 3. Optional synthetic-diff test.
	synthPrior := strings.TrimSpace(StringArg(test, "synthetic_prior"))
	expectContains := strings.TrimSpace(StringArg(test, "expected_output_contains"))
	if synthPrior != "" && expectContains != "" {
		synthOut, synthErr := runProbeScript(script, synthPrior, probe)
		if synthErr != nil {
			return fmt.Errorf("synthetic-diff test failed: %w", synthErr)
		}
		if !strings.Contains(synthOut, expectContains) {
			return fmt.Errorf("synthetic-diff test: expected output to contain %q, got %q. Diff logic isn't firing for the change you described. Adjust the script's comparison logic.",
				expectContains, truncate(synthOut, 300))
		}
	}

	return nil
}

// runProbeScript runs the watcher's script with the given prior +
// current strings, mirroring the runtime invocation. Used at create
// time for validation tests; never persists state.
func runProbeScript(script, prior, current string) (string, error) {
	payload, err := json.Marshal(map[string]string{
		"prior":   prior,
		"current": current,
	})
	if err != nil {
		return "", fmt.Errorf("encode payload: %w", err)
	}
	scriptCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := RunSandboxedScript(scriptCtx, "python3", script, string(payload))
	if res.TimedOut {
		return "", fmt.Errorf("timed out after 10s")
	}
	if res.Err != nil {
		stderr := strings.TrimSpace(res.Stderr)
		if stderr == "" {
			stderr = res.Err.Error()
		}
		return "", fmt.Errorf("script error: %s", stderr)
	}
	return res.Stdout, nil
}

// coerceStringArray normalizes whatever the LLM passed for an array-
// of-strings field into a []string. Tolerates JSON-encoded string
// inputs (Qwen sometimes hands us the array as a stringified blob).
func coerceStringArray(raw any) ([]string, error) {
	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			} else {
				out = append(out, fmt.Sprint(x))
			}
		}
		return out, nil
	case []string:
		return v, nil
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil, nil
		}
		var out []string
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return nil, fmt.Errorf("could not parse as JSON array of strings: %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected array of strings, got %T", raw)
	}
}

// looksLikeErrorBody mirrors core/watcher.go's looksLikeErrorBaseline
// for use at create time — we want to fail fast if the test invocation
// returned something that's clearly an error response, since that
// would mean the watcher polls a constant error and never fires.
func looksLikeErrorBody(body string) bool {
	if len(body) < 50 {
		return true
	}
	lower := strings.ToLower(body)
	for _, marker := range []string{
		`"error"`, `"err":`, `"status":{"code":`,
		"unauthorized", "forbidden", "not found",
		"insufficient", "invalid api key", "missing apikey",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func oneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
