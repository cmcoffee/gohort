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
		Description: "Create a new polling watcher around a tool call you've already run successfully. Required: name, tool_name (any tool you can call — including secure-API call_<credname> tools), tool_args (the args object you want the watcher to pass each cycle), interval_seconds (>=60), action_prompt (what the worker should do when the result changes). Optional: delivery_prefix (overrides the default tag prepended to delivered messages — pass empty string to suppress the prefix entirely). The watcher invokes tool_name with tool_args every interval, hashes the result, and on change wakes the worker with the diff + your action_prompt. RECOMMENDED FLOW: call the tool once yourself first to confirm it works and returns the data you want — then mint a watcher with the same args.",
		Params: map[string]ToolParam{
			"name":             {Type: "string", Description: "Short identifier for the watcher (snake_case recommended)."},
			"tool_name":        {Type: "string", Description: "Name of the tool to invoke each cycle. Anything you can call — built-in tools (web_search, fetch_url, etc.), secure-API call_<credname> tools, or runtime-defined tools."},
			"tool_args":        {Type: "object", Description: "The args object the watcher passes to tool_name every cycle. Use the same args you used when you tested the call."},
			"interval_seconds": {Type: "integer", Description: "How often to poll, in seconds. Minimum 60."},
			"action_prompt":    {Type: "string", Description: "What you want the worker to do when a change is detected. Be specific — the worker has no other context besides this + the diff."},
			"delivery_prefix":  {Type: "string", Description: "Optional. Text prepended to the worker's reply on delivery. Default is a tag like \"[watcher: <name>]\\n\" so recipients know it's an unprompted alert. Set empty string to suppress the prefix when the message body is self-explanatory (e.g. \"AAPL just crossed $250\")."},
		},
		Required:     []string{"name", "tool_name", "tool_args", "interval_seconds", "action_prompt"},
		Caps:         []Capability{CapNetwork, CapWrite},
		NeedsConfirm: true,
		Handler:      handleCreate,
	})

	gt.AddAction("update", &GroupedToolAction{
		Description: "Update an existing watcher's action_prompt, interval_seconds, or delivery_prefix. Cannot change the captured tool_name/tool_args (changing what's being watched is structurally a different watcher — delete + create instead). Returns the updated watcher record.",
		Params: map[string]ToolParam{
			"id":               {Type: "string", Description: "Watcher ID (from list)."},
			"action_prompt":    {Type: "string", Description: "Optional. New action_prompt. Omit to leave unchanged."},
			"interval_seconds": {Type: "integer", Description: "Optional. New polling interval (>=60). Omit or 0 to leave unchanged."},
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
		fmt.Fprintf(&b, "- id=%s  name=%q  enabled=%t  fires=%d  tool=%s  interval=%ds",
			w.ID, w.Name, w.Enabled, w.FireCount, w.ToolName, w.IntervalSec)
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

	// The probe response IS the baseline — no point waiting for the
	// first scheduled poll to seed something we already have.
	probeBody := probe
	if len(probeBody) > 4096 {
		probeBody = probeBody[:4096]
	}
	w := Watcher{
		Name:           name,
		Owner:          ownerFor(sess),
		Kind:           "polling",
		ActionPrompt:   prompt,
		Enabled:        true,
		Target:         targetFor(sess),
		ToolName:       toolName,
		ToolArgs:       toolArgs,
		IntervalSec:    interval,
		LastResultHash: HashWatcherBody(probe),
		LastResultBody: probeBody,
		FireCount:      1, // probe counts as the seed; next change triggers the worker
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
			return fmt.Sprintf("Watcher created (id=%s, name=%q, polls %s every %ds). Test invocation returned %d bytes — review the response to confirm it's what you expected; if not, delete the watcher and recreate with corrected args.\n\n--- test response ---\n%s\n--- end test response ---",
				candidate.ID, candidate.Name, candidate.ToolName, candidate.IntervalSec,
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
