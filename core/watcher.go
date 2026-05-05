// Watchers — long-running observers that wake up the worker LLM when
// "something happens." A watcher is a captured tool call: every
// interval, the watcher re-invokes the same registered tool with the
// same args, hashes the result, and on change spawns a worker run with
// the diff + the LLM's action_prompt as context.
//
// Why "captured tool call" instead of "URL"? Because the LLM already
// knows how to call tools correctly — it has descriptions, allowed URL
// patterns, credential auth, response shape. By capturing the tool
// invocation the LLM has already proven works, the watcher inherits all
// that correctness for free. Watching a TS3 endpoint becomes:
//   1. LLM calls call_ts3_api(url=/1/clientlist) — proves the call works.
//   2. LLM creates a watcher with tool_name="call_ts3_api" and
//      tool_args={"url":"/1/clientlist"} — same call, repeated.
// No URL guessing, no parallel auth path, no credential routing in the
// watcher itself. The only thing the watcher knows how to do is "invoke
// a registered tool, hash the result, dispatch on change."
//
// Cost posture: every fire uses the worker LLM only. No per-watcher
// daily cap needed (worker LLM is typically a local/cheap model).

package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)


const (
	watcherTable        = "watchers"
	watcherScheduleKind = "watcher.poll"
	watcherMaxResults   = 50
)

// Watcher is the persistent record. The captured tool call (ToolName +
// ToolArgs) is the heart of it; everything else is metadata + history.
type Watcher struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner string `json:"owner"` // scopes which user owns the watcher
	Kind  string `json:"kind"`  // "polling" | "webhook" (future)

	// ToolName is the registered tool to invoke each cycle. Both
	// statically-registered tools (web_search, fetch_url, watcher,
	// memory, etc.) and dynamically-built secure-API call_<name>
	// tools are supported — the dispatcher routes on the name prefix.
	ToolName string `json:"tool_name"`
	// ToolArgs are the args passed to the tool every invocation,
	// captured at create time from a known-good invocation.
	ToolArgs map[string]any `json:"tool_args,omitempty"`
	// IntervalSec controls the polling cadence. Floor is 60 to keep
	// watchers from hammering APIs.
	IntervalSec int `json:"interval_sec,omitempty"`

	ActionPrompt string `json:"action_prompt"`
	Enabled      bool   `json:"enabled"`
	Target       string `json:"target"` // "log" (default) — others added later

	// Evaluator controls how a detected change becomes an alert message.
	//   "llm"    — worker LLM analyzes the diff with ActionPrompt (default
	//              when unset, preserves original behavior). Flexible but
	//              expensive: every fire pays an LLM round-trip.
	//   "script" — EvaluatorScript runs sandboxed (bwrap + no network +
	//              read-only fs). stdin = JSON {"prior":...,"current":...},
	//              stdout = alert text (empty stdout = no alert this fire).
	//              Deterministic, milliseconds, zero LLM cost.
	//   "raw"    — current response body is the alert verbatim, no
	//              evaluation. Cheapest, no analysis at all.
	Evaluator       string `json:"evaluator,omitempty"`
	EvaluatorScript string `json:"evaluator_script,omitempty"` // python source for evaluator="script"

	// DeliveryPrefix is prepended to the worker's reply text on
	// delivery (only matters when Target routes to a user-visible
	// channel like phantom). The routing app supplies a default
	// (phantom uses "[watcher: <name>]\n") which is used when
	// DeliveryPrefixSet is false. When DeliveryPrefixSet is true,
	// the LLM has explicitly chosen a value — including the empty
	// string, which means "no prefix at all."
	//
	// Two fields rather than *string because gob (kvlite's encoder)
	// can't distinguish "nil pointer" from "non-nil pointer to empty
	// string" on a roundtrip — both decode as nil. The bool sentinel
	// makes "set to empty" persist correctly.
	DeliveryPrefixSet bool   `json:"delivery_prefix_set,omitempty"`
	DeliveryPrefix    string `json:"delivery_prefix,omitempty"`

	// Last-result cache for change detection.
	LastResultHash string `json:"last_result_hash,omitempty"`
	LastResultBody string `json:"last_result_body,omitempty"` // size-capped, for diff

	// Common timestamps + counters.
	CreatedAt   time.Time `json:"created_at"`
	LastFiredAt time.Time `json:"last_fired_at,omitempty"`
	FireCount   int       `json:"fire_count"`

	// Per-watcher result history. Newest-last; FIFO trimmed at
	// watcherMaxResults. Persists alongside the metadata so the
	// operator + the LLM can both inspect prior fires.
	Results []WatcherResult `json:"results,omitempty"`
}

// WatcherResult captures one fire of a watcher: what triggered it,
// what the worker said, and any error. Stored on the watcher record
// directly (small ring buffer; no separate table).
type WatcherResult struct {
	Timestamp time.Time `json:"timestamp"`
	Trigger   string    `json:"trigger"` // diff summary
	Reply     string    `json:"reply"`
	Error     string    `json:"error,omitempty"`
}

// Watchers fire using the process-wide SharedWorkerLLM (registered by
// the app at startup via SetSharedLLMs). Same tier as delegate's
// workers and shell-sim — no separate setter needed.

// ----------------------------------------------------------------------
// Result routing
// ----------------------------------------------------------------------

// WatcherResultRouter is invoked after a watcher fires and the worker
// produces a reply. The target is the watcher's Target field (e.g.
// "phantom:iMessage;-;+14155551234"); the router knows how to interpret
// the part after the prefix and dispatch (enqueue an outbox item, post
// to a chat session, POST to a webhook URL, etc.).
//
// Routers are registered by the app that owns the destination. Phantom
// registers a "phantom" router; chat registers "chat"; webhook handling
// could register "https"/"http". Errors are logged but not surfaced to
// the watcher subsystem — routing failures don't unschedule the watcher.
type WatcherResultRouter func(target string, w Watcher, reply string, runErr error)

var (
	watcherRouters   = map[string]WatcherResultRouter{}
	watcherRoutersMu sync.Mutex
)

// RegisterWatcherResultRouter associates a router with a target prefix.
// Call from an app's startup. Last-wins if called twice with the same
// prefix.
func RegisterWatcherResultRouter(prefix string, fn WatcherResultRouter) {
	watcherRoutersMu.Lock()
	defer watcherRoutersMu.Unlock()
	watcherRouters[prefix] = fn
}

// dispatchWatcherResult routes a worker reply to the appropriate
// destination based on the watcher's Target. The "log" target (or
// empty) is a no-op here — the result is already in w.Results.
func dispatchWatcherResult(w Watcher, reply string, runErr error) {
	target := w.Target
	if target == "" || target == "log" {
		return
	}
	prefix := target
	if i := strings.Index(target, ":"); i > 0 {
		prefix = target[:i]
	}
	watcherRoutersMu.Lock()
	fn := watcherRouters[prefix]
	watcherRoutersMu.Unlock()
	if fn == nil {
		Log("[watcher] no router registered for target %q (watcher %s) — falling back to log", target, w.Name)
		return
	}
	fn(target, w, reply, runErr)
}

// ----------------------------------------------------------------------
// Storage
// ----------------------------------------------------------------------

func watcherDB() Database {
	if AuthDB == nil {
		return nil
	}
	return AuthDB()
}

// SaveWatcher upserts a watcher record.
func SaveWatcher(w Watcher) error {
	db := watcherDB()
	if db == nil {
		return fmt.Errorf("watcher store not initialized")
	}
	if w.ID == "" {
		w.ID = UUIDv4()
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = time.Now()
	}
	if w.Target == "" {
		w.Target = "log"
	}
	db.Set(watcherTable, w.ID, w)
	return nil
}

// LoadWatcher fetches a watcher by ID.
func LoadWatcher(id string) (Watcher, bool) {
	var w Watcher
	db := watcherDB()
	if db == nil || id == "" {
		return w, false
	}
	ok := db.Get(watcherTable, id, &w)
	return w, ok
}

// ListWatchers returns all watchers, optionally filtered by owner.
// Empty owner returns all (admin view).
func ListWatchers(owner string) []Watcher {
	db := watcherDB()
	if db == nil {
		return nil
	}
	var out []Watcher
	for _, k := range db.Keys(watcherTable) {
		var w Watcher
		if !db.Get(watcherTable, k, &w) {
			continue
		}
		if owner != "" && w.Owner != owner {
			continue
		}
		out = append(out, w)
	}
	return out
}

// DeleteWatcher removes a watcher by ID. Also unschedules any pending
// scheduler tasks for it.
func DeleteWatcher(id string) error {
	db := watcherDB()
	if db == nil || id == "" {
		return fmt.Errorf("invalid")
	}
	db.Unset(watcherTable, id)
	for _, t := range ListScheduledTasks(watcherScheduleKind) {
		var p watcherPollPayload
		if json.Unmarshal(t.Payload, &p) != nil {
			continue
		}
		if p.WatcherID == id {
			UnscheduleTask(t.ID)
		}
	}
	return nil
}

// SetWatcherEnabled toggles the enabled flag and (re-)schedules a
// poll task accordingly. Disabling cancels the next poll; enabling
// schedules a fresh one.
func SetWatcherEnabled(id string, enabled bool) error {
	w, ok := LoadWatcher(id)
	if !ok {
		return fmt.Errorf("watcher %q not found", id)
	}
	w.Enabled = enabled
	if err := SaveWatcher(w); err != nil {
		return err
	}
	for _, t := range ListScheduledTasks(watcherScheduleKind) {
		var p watcherPollPayload
		if json.Unmarshal(t.Payload, &p) != nil {
			continue
		}
		if p.WatcherID == id {
			UnscheduleTask(t.ID)
		}
	}
	if enabled && w.Kind == "polling" {
		schedulePollNow(w)
	}
	return nil
}

// ----------------------------------------------------------------------
// Tool invocation
// ----------------------------------------------------------------------

// InvokeWatcherTool runs the captured tool call. Routes by tool-name
// prefix: call_<name> goes through the secure-API dispatcher (so
// per-credential auth + URL allowlist + method allowlist + daily cap
// all apply); everything else is looked up in the static chat-tool
// registry. Exported so non-watcher code (e.g. tests, admin "fire
// now") can re-use the same dispatch path.
func InvokeWatcherTool(toolName string, toolArgs map[string]any) (string, error) {
	if toolName == "" {
		return "", fmt.Errorf("empty tool name")
	}
	if strings.HasPrefix(toolName, "call_") {
		credName := strings.TrimPrefix(toolName, "call_")
		urlStr := StringArg(toolArgs, "url")
		method := StringArg(toolArgs, "method")
		body := StringArg(toolArgs, "body")
		return Secure().DispatchToolCall(nil, credName, urlStr, method, body)
	}
	t, ok := LookupChatTool(toolName)
	if !ok {
		return "", fmt.Errorf("tool %q is not registered", toolName)
	}
	if st, ok := t.(SessionChatTool); ok {
		return st.RunWithSession(toolArgs, nil)
	}
	return t.Run(toolArgs)
}

// ----------------------------------------------------------------------
// Polling scheduler integration
// ----------------------------------------------------------------------

type watcherPollPayload struct {
	WatcherID string `json:"watcher_id"`
}

// SchedulePollNow queues an immediate poll for a polling-kind watcher.
func SchedulePollNow(w Watcher) {
	schedulePollNow(w)
}

func schedulePollNow(w Watcher) {
	if w.Kind != "polling" {
		return
	}
	when := time.Now().Add(time.Duration(w.IntervalSec) * time.Second)
	if w.IntervalSec <= 0 {
		when = time.Now().Add(60 * time.Second) // safety floor
	}
	if _, err := ScheduleTask(watcherScheduleKind, watcherPollPayload{WatcherID: w.ID}, when); err != nil {
		Log("[watcher] schedule failed for %s: %v", w.Name, err)
	}
}

func init() {
	// kvlite encodes Watcher records via gob; ToolArgs is map[string]any
	// and gob refuses to encode unregistered concrete types inside an
	// interface field. Register the shapes the LLM actually passes in
	// tool args so save round-trips work.
	gob.Register(map[string]any{})
	gob.Register([]any{})
	gob.Register(map[string]string{})
	gob.Register([]string{})

	RegisterScheduleHandler(watcherScheduleKind, func(ctx context.Context, raw json.RawMessage) {
		var p watcherPollPayload
		if json.Unmarshal(raw, &p) != nil {
			return
		}
		w, ok := LoadWatcher(p.WatcherID)
		if !ok || !w.Enabled {
			return
		}
		fireWatcherPoll(ctx, w)
	})
}

// fireWatcherPoll executes one polling cycle. Invokes the captured
// tool, hashes the result, compares to LastResultHash. On change,
// spawns the worker; either way, re-schedules.
func fireWatcherPoll(ctx context.Context, w Watcher) {
	defer func() {
		if reloaded, ok := LoadWatcher(w.ID); ok && reloaded.Enabled {
			schedulePollNow(reloaded)
		}
	}()

	body, err := InvokeWatcherTool(w.ToolName, w.ToolArgs)
	if err != nil {
		Debug("[watcher] %s: tool %q failed: %v", w.Name, w.ToolName, err)
		appendWatcherResult(w.ID, WatcherResult{
			Timestamp: time.Now(),
			Error:     fmt.Sprintf("tool %q failed: %v", w.ToolName, err),
		})
		return
	}
	hash := sha256Sum(body)
	Debug("[watcher] %s: invoked %s, %d bytes, hash=%s",
		w.Name, w.ToolName, len(body), hash[:12])
	Trace("[watcher] %s: response body=%q", w.Name, truncateForTrigger(body))
	if hash == w.LastResultHash {
		Debug("[watcher] %s: no change (hash matches), nothing to do", w.Name)
		return
	}

	// Capture the prior body BEFORE we overwrite it on the watcher
	// record — the evaluator (especially script mode) needs the
	// actual prior state, not the post-update copy. Earlier code
	// passed w.LastResultBody after the assignment below, which
	// silently fed (current, current) to the script and produced
	// false-empty alerts.
	priorBody := w.LastResultBody
	trigger := buildTriggerContext(priorBody, body)

	// Update hash + cached body BEFORE running the worker so a slow
	// worker call doesn't cause us to re-fire on the same change.
	w.LastResultHash = hash
	if len(body) > 4096 {
		w.LastResultBody = body[:4096]
	} else {
		w.LastResultBody = body
	}
	w.LastFiredAt = time.Now()
	w.FireCount++
	_ = SaveWatcher(w)

	Debug("[watcher] %s: change detected, dispatching evaluator=%s (fire #%d)",
		w.Name, evaluatorOrDefault(w), w.FireCount)

	reply, runErr := runWatcherEvaluator(ctx, w, priorBody, body, trigger)

	// Empty reply with no error means the evaluator decided this change
	// isn't worth surfacing (script returned nothing, raw mode skipped,
	// LLM produced empty output). Skip both result-log and routing —
	// no point recording "nothing happened" entries.
	if runErr == nil && strings.TrimSpace(reply) == "" {
		Debug("[watcher] %s: evaluator returned empty — no alert this fire", w.Name)
		return
	}

	res := WatcherResult{
		Timestamp: time.Now(),
		Trigger:   trigger,
		Reply:     reply,
	}
	if runErr != nil {
		res.Error = runErr.Error()
	}
	appendWatcherResult(w.ID, res)
	dispatchWatcherResult(w, reply, runErr)
}

// evaluatorOrDefault returns the watcher's evaluator mode, treating
// empty as "llm" so existing watchers continue to behave as before.
func evaluatorOrDefault(w Watcher) string {
	if w.Evaluator == "" {
		return "llm"
	}
	return w.Evaluator
}

// runWatcherEvaluator dispatches to the configured evaluator and returns
// the alert text (empty = "no alert"). The prior + current bodies are
// the raw responses; trigger is a pre-built diff context useful for the
// LLM evaluator. Script and raw evaluators ignore trigger and use the
// raw bodies directly.
func runWatcherEvaluator(ctx context.Context, w Watcher, prior, current, trigger string) (string, error) {
	switch evaluatorOrDefault(w) {
	case "raw":
		return current, nil
	case "script":
		return runWatcherScript(ctx, w, prior, current)
	case "llm":
		return runWatcherWorker(ctx, w, trigger)
	default:
		return "", fmt.Errorf("watcher %q: unknown evaluator mode %q", w.Name, w.Evaluator)
	}
}

// runWatcherScript runs the watcher's python evaluator under bwrap with
// {"prior":...,"current":...} on stdin. stdout is the alert text;
// empty stdout means "no alert this fire." Errors and stderr are
// surfaced so a broken script is visible in the result log without
// silently dropping fires.
func runWatcherScript(ctx context.Context, w Watcher, prior, current string) (string, error) {
	if strings.TrimSpace(w.EvaluatorScript) == "" {
		return "", fmt.Errorf("evaluator=script but evaluator_script is empty")
	}
	payload, err := json.Marshal(map[string]string{
		"prior":   prior,
		"current": current,
	})
	if err != nil {
		return "", fmt.Errorf("encode payload: %w", err)
	}
	Trace("[watcher] %s: script stdin payload=%s", w.Name, truncateForTrigger(string(payload)))
	scriptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res := RunSandboxedScript(scriptCtx, "python3", w.EvaluatorScript, string(payload))
	Trace("[watcher] %s: script result sandbox=%t exit_err=%v stdout=%q stderr=%q",
		w.Name, res.Sandbox, res.Err, res.Stdout, res.Stderr)
	if res.TimedOut {
		return "", fmt.Errorf("evaluator script timed out after 10s")
	}
	if res.Err != nil {
		stderr := strings.TrimSpace(res.Stderr)
		if stderr == "" {
			stderr = res.Err.Error()
		}
		return "", fmt.Errorf("evaluator script failed: %s", stderr)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" && strings.TrimSpace(res.Stderr) != "" {
		// Script ran cleanly but printed nothing on stdout while
		// emitting stderr — usually a debug-print or a logic path
		// the author didn't realize wasn't surfacing as an alert.
		// Worth an explicit Debug so "why didn't I get alerted" is
		// visible without enabling Trace.
		Debug("[watcher] %s: script returned empty stdout but wrote to stderr: %q",
			w.Name, truncateForTrigger(res.Stderr))
	}
	return out, nil
}

// buildTriggerContext formats prior + current body for the worker so
// it can describe what changed without needing to fetch separately.
func buildTriggerContext(prior, current string) string {
	if prior == "" {
		return "Initial observation:\n" + truncateForTrigger(current)
	}
	return "Previous response:\n" + truncateForTrigger(prior) +
		"\n\nCurrent response:\n" + truncateForTrigger(current)
}

func truncateForTrigger(s string) string {
	if len(s) <= 2048 {
		return s
	}
	return s[:2048] + "\n... [truncated]"
}

// runWatcherWorker spawns a single-pass worker LLM call with the
// watcher's action_prompt + the trigger context. v1 has no tool
// catalog — the worker just analyzes and replies.
func runWatcherWorker(ctx context.Context, w Watcher, trigger string) (string, error) {
	llm := SharedWorkerLLM()
	if llm == nil {
		return "", fmt.Errorf("watcher worker LLM not configured (SetSharedLLMs not called)")
	}
	sys := "You are a watcher worker. You wake up when a watcher detects activity. " +
		"Read the trigger context, follow the operator's action prompt, and produce a concise text reply. " +
		"No tool calls — just analysis. Keep replies under 500 words."
	userMsg := fmt.Sprintf("[WATCHER FIRED — %s]\n%s\n\n%s",
		w.Name, w.ActionPrompt, trigger)
	f := false
	resp, err := llm.Chat(ctx,
		[]Message{{Role: "user", Content: userMsg}},
		WithSystemPrompt(sys), WithThink(f), WithMaxTokens(2048),
	)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("nil worker response")
	}
	return strings.TrimSpace(resp.Content), nil
}

func appendWatcherResult(id string, r WatcherResult) {
	w, ok := LoadWatcher(id)
	if !ok {
		return
	}
	w.Results = append(w.Results, r)
	if len(w.Results) > watcherMaxResults {
		w.Results = w.Results[len(w.Results)-watcherMaxResults:]
	}
	_ = SaveWatcher(w)
}

// ----------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------

func sha256Sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// HashWatcherBody is the exported wrapper around the same hash
// function fireWatcherPoll uses for change detection. Callers that
// pre-seed a watcher's baseline (e.g. the watcher tool's create
// handler, which uses its probe response as the seed) need to compute
// the hash the same way the polling loop will.
func HashWatcherBody(s string) string { return sha256Sum(s) }

// randomToken returns a URL-safe random token. Used by webhook
// watchers (next slice) for unique URLs.
func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
