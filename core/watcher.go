// Watchers — long-running observers that wake up the worker LLM when
// "something happens." v1 supports polling: the watcher periodically
// fetches a URL, hashes the response, and on change spawns a worker
// run with the diff + the LLM's action_prompt as context. Result is
// appended to a per-watcher log.
//
// Coming next: webhook receivers (push-style trigger) + non-log
// targets (post to chat session, phantom outbox, downstream webhook).
//
// Cost posture: every fire uses the worker LLM only (no LeadLLM).
// Same tier as delegate's workers — bounded, cheap, no per-watcher
// daily cap needed (the worker LLM is typically a local/cheap model).

package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	watcherTable          = "watchers"
	watcherScheduleKind   = "watcher.poll"
	watcherMaxResults     = 50
	watcherDefaultTimeout = 30 * time.Second
)

// Watcher is the persistent record. Polling-specific fields are
// populated when Kind == "polling"; webhook-specific fields when
// Kind == "webhook" (next slice).
type Watcher struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Owner        string `json:"owner"` // scopes which user owns the watcher
	Kind         string `json:"kind"`  // "polling" | "webhook" (future)
	ActionPrompt string `json:"action_prompt"`
	Enabled      bool   `json:"enabled"`
	Target       string `json:"target"` // "log" (default) — others added later

	// Polling-specific.
	PollingURL         string            `json:"polling_url,omitempty"`
	PollingMethod     string             `json:"polling_method,omitempty"` // GET / POST / etc.
	PollingHeaders    map[string]string  `json:"polling_headers,omitempty"`
	PollingBody       string             `json:"polling_body,omitempty"`
	PollingIntervalSec int               `json:"polling_interval_sec,omitempty"`
	LastResponseHash  string             `json:"last_response_hash,omitempty"`
	LastResponseBody  string             `json:"last_response_body,omitempty"` // size-capped, for diff

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
	Trigger   string    `json:"trigger"` // diff summary or webhook body excerpt
	Reply     string    `json:"reply"`
	Error     string    `json:"error,omitempty"`
}

// Watcher fires use the process-wide SharedWorkerLLM (registered by
// the app at startup via SetSharedLLMs). Same tier as delegate's
// workers and shell-sim — no separate setter needed.

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
	// Unschedule any pending poll tasks. The scheduler stores tasks
	// keyed by their own UUIDs; we walk and match by payload.
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
	// Drop existing pending polls for this watcher.
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
// Polling scheduler integration
// ----------------------------------------------------------------------

type watcherPollPayload struct {
	WatcherID string `json:"watcher_id"`
}

// SchedulePollNow queues an immediate poll for a polling-kind watcher.
// Used at watcher creation + enable-toggle time. After each fire the
// handler re-schedules itself for the next interval.
func SchedulePollNow(w Watcher) {
	schedulePollNow(w)
}

func schedulePollNow(w Watcher) {
	if w.Kind != "polling" {
		return
	}
	when := time.Now().Add(time.Duration(w.PollingIntervalSec) * time.Second)
	if w.PollingIntervalSec <= 0 {
		when = time.Now().Add(60 * time.Second) // safety floor
	}
	if _, err := ScheduleTask(watcherScheduleKind, watcherPollPayload{WatcherID: w.ID}, when); err != nil {
		Log("[watcher] schedule failed for %s: %v", w.Name, err)
	}
}

// init registers the scheduler kind at package load. The handler
// fetches the watcher, polls, runs the worker on change, and
// re-schedules.
func init() {
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

// fireWatcherPoll executes one polling cycle. Fetches the URL, hashes
// the body, compares to LastResponseHash. On change, spawns the
// worker; either way, re-schedules.
func fireWatcherPoll(ctx context.Context, w Watcher) {
	defer func() {
		// Always re-schedule unless explicitly disabled mid-fire.
		if reloaded, ok := LoadWatcher(w.ID); ok && reloaded.Enabled {
			schedulePollNow(reloaded)
		}
	}()

	body, err := fetchWatcherURL(ctx, w)
	if err != nil {
		appendWatcherResult(w.ID, WatcherResult{
			Timestamp: time.Now(),
			Error:     "fetch failed: " + err.Error(),
		})
		return
	}
	hash := sha256Sum(body)
	if hash == w.LastResponseHash {
		// No change — nothing to do this cycle.
		return
	}

	// Build a small diff context: prior body (truncated) + new body
	// (truncated) so the worker has both to compare.
	trigger := buildTriggerContext(w.LastResponseBody, body)

	// Update hash + cached body BEFORE running the worker so a slow
	// worker call doesn't cause us to re-fire on the same change.
	w.LastResponseHash = hash
	if len(body) > 4096 {
		w.LastResponseBody = body[:4096]
	} else {
		w.LastResponseBody = body
	}
	w.LastFiredAt = time.Now()
	w.FireCount++
	_ = SaveWatcher(w)

	// First-fire seed: skip running the worker on the initial
	// observation. Otherwise every brand-new watcher fires once
	// immediately ("look! it's set to 42!") which is rarely useful.
	if w.FireCount == 1 {
		return
	}

	reply, runErr := runWatcherWorker(ctx, w, trigger)
	res := WatcherResult{
		Timestamp: time.Now(),
		Trigger:   trigger,
		Reply:     reply,
	}
	if runErr != nil {
		res.Error = runErr.Error()
	}
	appendWatcherResult(w.ID, res)
}

// fetchWatcherURL performs the HTTP request configured on the watcher.
func fetchWatcherURL(ctx context.Context, w Watcher) (string, error) {
	method := w.PollingMethod
	if method == "" {
		method = "GET"
	}
	reqCtx, cancel := context.WithTimeout(ctx, watcherDefaultTimeout)
	defer cancel()
	var bodyReader io.Reader
	if w.PollingBody != "" {
		bodyReader = strings.NewReader(w.PollingBody)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, w.PollingURL, bodyReader)
	if err != nil {
		return "", err
	}
	for k, v := range w.PollingHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", "gohort/watcher")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 1<<20) // 1MB cap on watch payloads
	b, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	return string(b), nil
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
// catalog — the worker just analyzes and replies. Tools come later.
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

// randomToken returns a URL-safe random token. Used by webhook
// watchers (next slice) for unique URLs.
func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
