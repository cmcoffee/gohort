// Watcher tool — LLM-facing management for polling/webhook watchers.
// v1 supports polling watchers + log target only. Each watcher fires
// the worker LLM with the operator's action_prompt + a diff of what
// changed; the worker's reply lands in the watcher's results log.
//
// Catalog footprint is one entry; per-action specs live behind
// action="help" so the round budget isn't burned every turn.

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
		"Create + manage long-running polling watchers. A watcher periodically fetches a URL; on change the worker LLM wakes up with the diff and your action_prompt, and writes a brief reply to the watcher's log.")

	gt.AddAction("list", &GroupedToolAction{
		Description: "List all watchers you own. Returns id, name, kind, enabled, last fire time, total fires.",
		Params:      map[string]ToolParam{},
		Caps:        []Capability{CapRead},
		Handler:     handleList,
	})

	gt.AddAction("create", &GroupedToolAction{
		Description: "Create a new polling watcher. Provide a name, the URL to poll, the interval (seconds, min 60), and the action_prompt that tells the worker what to do when something changes. Optional: method (GET/POST/etc.), headers, body. The first observation seeds the baseline and does NOT trigger the worker; subsequent changes do.",
		Params: map[string]ToolParam{
			"name":             {Type: "string", Description: "Short identifier for the watcher (snake_case recommended)."},
			"url":              {Type: "string", Description: "URL to poll."},
			"interval_seconds": {Type: "integer", Description: "How often to poll, in seconds. Minimum 60."},
			"action_prompt":    {Type: "string", Description: "What you want the worker to do when a change is detected. Be specific — the worker has no other context besides this + the diff."},
			"method":           {Type: "string", Description: "HTTP method. Default GET."},
			"headers":          {Type: "object", Description: "Optional request headers as a {key: value} object."},
			"body":             {Type: "string", Description: "Optional request body (for POST/PUT/etc)."},
		},
		Required:     []string{"name", "url", "interval_seconds", "action_prompt"},
		Caps:         []Capability{CapNetwork, CapWrite},
		NeedsConfirm: true,
		Handler:      handleCreate,
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
		fmt.Fprintf(&b, "- id=%s  name=%q  kind=%s  enabled=%t  fires=%d",
			w.ID, w.Name, w.Kind, w.Enabled, w.FireCount)
		if !w.LastFiredAt.IsZero() {
			fmt.Fprintf(&b, "  last_fired=%s", w.LastFiredAt.UTC().Format(time.RFC3339))
		}
		if w.Kind == "polling" {
			fmt.Fprintf(&b, "  url=%s  interval=%ds", w.PollingURL, w.PollingIntervalSec)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func handleCreate(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(StringArg(args, "name"))
	url := strings.TrimSpace(StringArg(args, "url"))
	prompt := strings.TrimSpace(StringArg(args, "action_prompt"))
	interval := IntArg(args, "interval_seconds")
	if interval < 60 {
		return "", fmt.Errorf("interval_seconds must be >= 60 (you asked for %d)", interval)
	}
	method := strings.ToUpper(strings.TrimSpace(StringArg(args, "method")))
	if method == "" {
		method = "GET"
	}
	body := StringArg(args, "body")

	var headers map[string]string
	if raw, ok := args["headers"]; ok && raw != nil {
		switch h := raw.(type) {
		case map[string]any:
			headers = make(map[string]string, len(h))
			for k, v := range h {
				headers[k] = fmt.Sprint(v)
			}
		case map[string]string:
			headers = h
		default:
			// Try a JSON round-trip — Qwen sometimes hands us a string.
			if s, isStr := raw.(string); isStr && strings.TrimSpace(s) != "" {
				_ = json.Unmarshal([]byte(s), &headers)
			}
		}
	}

	w := Watcher{
		Name:               name,
		Owner:              ownerFor(sess),
		Kind:               "polling",
		ActionPrompt:       prompt,
		Enabled:            true,
		Target:             "log",
		PollingURL:         url,
		PollingMethod:      method,
		PollingHeaders:     headers,
		PollingBody:        body,
		PollingIntervalSec: interval,
	}
	if err := SaveWatcher(w); err != nil {
		return "", err
	}
	// Re-load to grab the assigned ID, then queue the first poll.
	for _, candidate := range ListWatchers(w.Owner) {
		if candidate.Name == name && candidate.PollingURL == url {
			SchedulePollNow(candidate)
			return fmt.Sprintf("Watcher created (id=%s, name=%q, polling every %ds). First poll scheduled. The first observation seeds the baseline; you'll get worker replies on subsequent changes.",
				candidate.ID, candidate.Name, candidate.PollingIntervalSec), nil
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
	// Newest first.
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

// ownerFor extracts the watcher owner from the session. Empty username
// becomes empty owner (admin-created / unscoped); listing without an
// owner returns only unowned watchers, so unauthenticated sessions
// can't see others' work.
func ownerFor(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	return sess.Username
}

// ownsWatcher gates mutation actions: the LLM can only modify
// watchers it owns. Empty-owner watchers are accessible to any
// empty-owner session (i.e. the admin/console path).
func ownsWatcher(sess *ToolSession, w Watcher) bool {
	return ownerFor(sess) == w.Owner
}

func oneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
