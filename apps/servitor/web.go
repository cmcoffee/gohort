package servitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

func init() {
	RegisterApp(new(Servitor))
	RegisterRouteStage(RouteStage{
		Key:     "app.servitor",
		Label:   "Servitor (worker — runs SSH commands)",
		Default: "worker",
		Group:   "Servitor",
		Private: true,
	})
	RegisterRouteStage(RouteStage{
		Key:           "app.servitor.orchestrator",
		Label:         "Servitor: Orchestrator",
		Default:       "worker (thinking)",
		Group:         "Servitor",
		Private:       true,
	})
}

// orchestratorThinkOpts returns a WithThinkBudget option using the configured
// budget for the orchestrator route stage, falling back to the stage default.
func orchestratorThinkOpts() []ChatOption {
	if b := RouteThinkBudget("app.servitor.orchestrator"); b != nil {
		return []ChatOption{WithThinkBudget(*b)}
	}
	return nil
}

const applianceTable = "ssh_appliances"

// ansiEscape matches ANSI/VT100 terminal escape sequences so they can be
// stripped from PTY output before handing text to the LLM.
var ansiEscape = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])`)

func stripANSI(s string) string {
	s = ansiEscape.ReplaceAllString(s, "")
	// Normalise Windows-style line endings left by PTY output.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

// normalizeTask reduces a probe-tool task description to a content-token
// signature: lowercase, strip punctuation, drop short and stop words, sort,
// join. Two paraphrased delegations of the same task ("list mysql tables",
// "show tables in mysql") collapse to the same signature so the cache
// catches them as duplicates and the orchestrator stops grinding the
// same area through paraphrase.
func normalizeTask(s string) string {
	stop := map[string]bool{
		"the": true, "and": true, "for": true, "with": true,
		"that": true, "this": true, "from": true, "into": true,
		"are": true, "you": true, "your": true, "any": true,
		"all": true, "use": true, "via": true, "show": true,
		"list": true, "find": true, "get": true, "see": true,
		"check": true, "look": true, "what": true, "which": true,
		"where": true, "when": true, "how": true,
	}
	s = strings.ToLower(s)
	var words []string
	for _, w := range strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if len(w) < 3 || stop[w] {
			continue
		}
		words = append(words, w)
	}
	sort.Strings(words)
	return strings.Join(words, " ")
}

// LogEntry describes a single log file discovered on the remote appliance.
type LogEntry struct {
	Service string `json:"service"`
	Path    string `json:"path"`
	Desc    string `json:"desc"`
}

// Appliance is a saved remote host with connection params and cached system knowledge.
type Appliance struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "ssh" (default) | "command"
	Name string `json:"name"`
	// SSH fields
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	// Command fields (Type == "command")
	Command string   `json:"command"`  // local command name or path
	WorkDir string   `json:"work_dir"` // optional working directory
	EnvVars []string `json:"env_vars"` // optional KEY=VALUE env overrides
	// Shared
	Instructions  string     `json:"instructions"`   // freeform notes injected into every session
	PersonaName   string     `json:"persona_name"`   // short label shown in the UI (e.g. "Support", "QA")
	PersonaPrompt string     `json:"persona_prompt"` // shapes how the agent approaches this appliance
	Profile       string     `json:"profile"`        // full system profile / CLI map markdown
	LogMap       []LogEntry `json:"log_map"`      // structured list of discovered log files
	Scanned      string     `json:"scanned"`      // RFC3339 timestamp of last map run
}

// probeEvent is one event emitted by a running session goroutine.
type probeEvent struct {
	Kind   string     `json:"kind"`             // status | cmd | output | message | confirm | reply | error | done | watch | notes_consumed | intent | plan_set | plan_step
	Text   string     `json:"text,omitempty"`
	Reason string     `json:"reason,omitempty"` // destructive reason for confirm events
	IDs    []string   `json:"ids,omitempty"`    // notes_consumed: which queued notes the orchestrator just drained
	Plan   []PlanStep `json:"plan,omitempty"`   // plan_set / plan_step: snapshot of the current plan for the UI to render
}

// toInt extracts an int from an arbitrary JSON-decoded value. JSON numbers
// arrive as float64 in Go's stdlib; tool-arg maps may also have int or
// strings depending on the LLM's serialization. Returns the parsed value
// and true on success.
func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case string:
		var n int
		_, err := fmt.Sscanf(x, "%d", &n)
		return n, err == nil
	}
	return 0, false
}

const alwaysAllowTable = "ssh_always_allow"
const notesTable = "ssh_notes"

var (
	probeSessions      = NewLiveSessionMap[probeEvent](0)
	confirmChans       sync.Map // session_id -> chan bool
	pendingCmds        sync.Map // session_id -> command string currently awaiting confirmation
	termWriters        sync.Map // "userID:applianceID" -> func([]byte); writes raw bytes to active terminal WebSocket
	injectionQueues    sync.Map // session_id -> *injectionQueue (mid-flight user notes for the chat orchestrator)
	sessionAppliances  sync.Map // session_id -> applianceID (for building resume URLs in the dashboard live-sessions panel)
)

// maxInjectionQueueDepth caps how many user notes may pile up between
// orchestrator decision points. Beyond this, the oldest note is dropped
// and the user is told via a status event.
const maxInjectionQueueDepth = 5

// injectionNote is a single mid-flight user note awaiting orchestrator pickup.
// Each note has a stable ID so the user can edit or delete it before it's
// drained. EditLocked notes are held out of Drain — the user is actively
// editing them and the orchestrator must not consume the note until the
// user finishes (commit via Update, or cancel via Unlock).
type injectionNote struct {
	ID         string
	Text       string
	EditLocked bool
}

// injectionQueue holds mid-flight user notes for a single chat session.
// Drained at the top of each orchestrator round; workers never see it.
type injectionQueue struct {
	mu    sync.Mutex
	notes []injectionNote
}

// Push appends a note and returns its ID. The bool indicates whether the
// note was added cleanly (true) or whether the oldest entry was dropped to
// make room because the queue was at capacity (false).
func (q *injectionQueue) Push(text string) (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := injectionNote{ID: UUIDv4(), Text: text}
	dropped := false
	if len(q.notes) >= maxInjectionQueueDepth {
		q.notes = q.notes[1:]
		dropped = true
	}
	q.notes = append(q.notes, n)
	return n.ID, !dropped
}

// Update replaces the text of a queued note and clears its edit lock (commit
// path). Returns false if the note has already been drained or never existed.
func (q *injectionQueue) Update(id, text string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.notes {
		if q.notes[i].ID == id {
			q.notes[i].Text = text
			q.notes[i].EditLocked = false
			return true
		}
	}
	return false
}

// Lock marks a note as edit-locked so Drain skips it. Use when the user
// opens the inline editor on a queued note.
func (q *injectionQueue) Lock(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.notes {
		if q.notes[i].ID == id {
			q.notes[i].EditLocked = true
			return true
		}
	}
	return false
}

// Unlock clears the edit lock without changing the text. Use when the user
// cancels an in-progress edit so the note becomes drainable again.
func (q *injectionQueue) Unlock(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.notes {
		if q.notes[i].ID == id {
			q.notes[i].EditLocked = false
			return true
		}
	}
	return false
}

// Delete removes a queued note. Returns false if the note has already been
// drained or never existed.
func (q *injectionQueue) Delete(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, n := range q.notes {
		if n.ID == id {
			q.notes = append(q.notes[:i], q.notes[i+1:]...)
			return true
		}
	}
	return false
}

// Drain returns all unlocked notes and removes them from the queue.
// Edit-locked notes stay queued — the orchestrator picks them up only after
// the user commits or cancels the edit.
func (q *injectionQueue) Drain() []injectionNote {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.notes) == 0 {
		return nil
	}
	var taken []injectionNote
	var kept []injectionNote
	for _, n := range q.notes {
		if n.EditLocked {
			kept = append(kept, n)
		} else {
			taken = append(taken, n)
		}
	}
	q.notes = kept
	return taken
}

// mirrorToTerm writes raw terminal bytes to the active WebSocket terminal for a user+appliance,
// if one is connected. Used to route worker command output into the visible terminal pane.
func mirrorToTerm(userID, applianceID string, data []byte) {
	if v, ok := termWriters.Load(userID + ":" + applianceID); ok {
		v.(func([]byte))(data)
	}
}

// terminalPrompt returns a short colored prompt string for the given appliance.
// SSH: green hostname (first label only) followed by $. Command: plain $.
func terminalPrompt(appliance Appliance) string {
	user := appliance.User
	if user == "" {
		user = "root"
	}
	if user == "root" {
		return "\x1b[31m#$\x1b[0m "
	}
	return "\x1b[32m#$\x1b[0m "
}

// extractLogMap parses the structured ## Log Files JSON block from a profile.
func extractLogMap(profile string) []LogEntry {
	const header = "## Log Files"
	idx := strings.Index(profile, header)
	if idx < 0 {
		return nil
	}
	rest := profile[idx+len(header):]
	jsonStart := strings.Index(rest, "```json")
	if jsonStart < 0 {
		return nil
	}
	jsonStart += len("```json")
	jsonEnd := strings.Index(rest[jsonStart:], "```")
	if jsonEnd < 0 {
		return nil
	}
	var entries []LogEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(rest[jsonStart:jsonStart+jsonEnd])), &entries); err != nil {
		return nil
	}
	return entries
}

// mergeLogMap merges newly discovered log entries with the existing set.
// Entries with the same path are updated; entries only in old are preserved.
func mergeLogMap(old, fresh []LogEntry) []LogEntry {
	seen := make(map[string]bool, len(fresh))
	result := make([]LogEntry, 0, len(fresh)+len(old))
	for _, e := range fresh {
		result = append(result, e)
		seen[e.Path] = true
	}
	for _, e := range old {
		if !seen[e.Path] {
			result = append(result, e)
		}
	}
	return result
}

func (T *Servitor) WebPath() string { return "/servitor" }
func (T *Servitor) WebName() string { return "Servitor" }
func (T *Servitor) WebDesc() string {
	return "Dispatch AI agents to remote appliances (SSH) or local commands to investigate, map, and operate systems."
}

func (T *Servitor) RegisterRoutes(mux *http.ServeMux, prefix string) {
	// Bucket migration: if the "servitor" bucket is empty, fall back to the
	// previous bucket name ("sysprobe"), and then to the even older "ssh_probe".
	// Bucket() on a substore navigates to the sibling via the underlying store.
	if T.DB != nil && len(T.DB.Tables()) == 0 {
		for _, name := range []string{"sysprobe", "ssh_probe"} {
			older := T.DB.Bucket(name)
			if len(older.Tables()) > 0 {
				T.DB = older
				break
			}
		}
	}

	sub := NewWebUI(T, prefix, AppUIAssets{
		BodyHTML: sshBody,
		AppCSS:   sshCSS,
		AppJS:    sshJS,
		HeadHTML: sshHeadHTML,
	})
	sub.HandleFunc("/api/appliances", T.handleAppliances)
	sub.HandleFunc("/api/appliance/", T.handleAppliance)
	sub.HandleFunc("/api/chat", T.handleChat)
	sub.HandleFunc("/api/inject", T.handleInject)
	sub.HandleFunc("/api/map", T.handleMap)
	sub.HandleFunc("/api/mapapp", T.handleMapApp)
	sub.HandleFunc("/api/run", T.handleRun)
	sub.HandleFunc("/api/terminal", T.handleTerminal)
	sub.HandleFunc("/api/facts", T.handleFacts)
	sub.HandleFunc("/api/memory/clear", T.handleMemoryClear)
	sub.HandleFunc("/api/disconnect", T.handleDisconnect)
	sub.HandleFunc("/api/events", probeSessions.HandleReconnect())
	sub.HandleFunc("/api/confirm", T.handleConfirm)
	sub.HandleFunc("/api/cancel", probeSessions.HandleCancel("servitor"))
	sub.HandleFunc("/api/save_destinations", T.handleSaveDestinations)
	sub.HandleFunc("/api/save_article", T.handleSaveArticle)
	sub.HandleFunc("/api/save_snippet", T.handleSaveSnippet)
	sub.HandleFunc("/api/rules", T.handleRules)
	sub.HandleFunc("/api/rules/", T.handleRuleDelete)
	sub.HandleFunc("/api/workspace/create", T.handleWorkspaceCreate)
	sub.HandleFunc("/api/workspace/save", T.handleWorkspaceSave)
	sub.HandleFunc("/api/workspace/list", T.handleWorkspaceList)
	sub.HandleFunc("/api/workspace/draft", T.handleWorkspaceDraft)
	sub.HandleFunc("/api/workspace/revisions", T.handleWorkspaceRevisions)
	sub.HandleFunc("/api/workspace/revert", T.handleWorkspaceRevert)
	sub.HandleFunc("/api/workspace/synthesize", T.handleWorkspaceSynthesize)
	sub.HandleFunc("/api/workspace/supplement/add", T.handleSupplementAdd)
	sub.HandleFunc("/api/workspace/supplement/delete", T.handleSupplementDelete)
	sub.HandleFunc("/api/workspace/supplement/prompt", T.handleSupplementPrompt)
	sub.HandleFunc("/api/workspace/view", T.handleWorkspaceView)
	sub.HandleFunc("/api/workspace/", T.handleWorkspace)
	MountSubMux(mux, prefix, sub)
	go T.runWatchLoop(AppContext())
	RegisterLiveProvider(func() []LiveEntry {
		entries := probeSessions.ActiveSessions()
		for i := range entries {
			entries[i].App = "Servitor"
			entries[i].Path = prefix
			// The servitor page resumes a session via ?run=<applianceID>&session=<sessionID>.
			// Without ?run, loadAppliances() can't pick the right appliance to attach to.
			if v, ok := sessionAppliances.Load(entries[i].ID); ok {
				if applianceID, ok := v.(string); ok && applianceID != "" {
					entries[i].URL = fmt.Sprintf("%s?run=%s&session=%s",
						prefix,
						url.QueryEscape(applianceID),
						url.QueryEscape(entries[i].ID),
					)
				}
			}
		}
		return entries
	})
}

// --- Appliance CRUD ---

func (T *Servitor) handleAppliances(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if udb != nil {
			cleanDanglingRecords(udb)
		}
		var items []Appliance
		if udb != nil {
			for _, key := range udb.Keys(applianceTable) {
				var a Appliance
				if udb.Get(applianceTable, key, &a) {
					a.Password = ""
					items = append(items, a)
				}
			}
		}
		if items == nil {
			items = []Appliance{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)

	case http.MethodPost:
		if udb == nil {
			http.Error(w, "no database", http.StatusInternalServerError)
			return
		}
		var req Appliance
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if req.Type == "" {
			req.Type = "ssh"
		}
		if req.Type == "command" {
			if req.Name == "" || req.Command == "" {
				http.Error(w, "name and command required", http.StatusBadRequest)
				return
			}
		} else {
			req.Type = "ssh"
			if req.Name == "" || req.Host == "" {
				http.Error(w, "name and host required", http.StatusBadRequest)
				return
			}
			if req.Port == 0 {
				req.Port = 22
			}
			if req.User == "" {
				req.User = "root"
			}
		}
		// Preserve sensitive/derived fields when updating without re-supplying them.
		if req.ID != "" {
			var existing Appliance
			if udb.Get(applianceTable, req.ID, &existing) {
				if req.Type == "ssh" && req.Password == "" {
					req.Password = existing.Password
				}
				req.Profile = existing.Profile
				req.LogMap = existing.LogMap
				req.Scanned = existing.Scanned
			}
		}
		if req.ID == "" {
			req.ID = UUIDv4()
		}
		udb.Set(applianceTable, req.ID, req)
		dropConn(userID, req.ID) // force reconnect with new credentials
		resp := req
		resp.Password = ""
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *Servitor) handleAppliance(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/appliance/")
	if id == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		var a Appliance
		if !udb.Get(applianceTable, id, &a) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		a.Password = ""
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a)
	case http.MethodDelete:
		purgeAppliance(userID, udb, id)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// cleanDanglingRecords removes facts, knowledge docs, notes, and techniques that
// reference appliance IDs no longer present in the appliances table.
func cleanDanglingRecords(udb Database) {
	if udb == nil {
		return
	}
	valid := make(map[string]bool)
	for _, k := range udb.Keys(applianceTable) {
		valid[k] = true
	}
	// Facts, knowledge docs, and discoveries are keyed "applianceID:subkey".
	for _, tbl := range []string{factsTable, knowledgeTable, discoveriesTable} {
		for _, k := range udb.Keys(tbl) {
			if parts := strings.SplitN(k, ":", 2); len(parts) == 2 && !valid[parts[0]] {
				udb.Unset(tbl, k)
			}
		}
	}
	// Notes and techniques are keyed directly by applianceID.
	for _, tbl := range []string{notesTable, techniquesTable} {
		for _, k := range udb.Keys(tbl) {
			if !valid[k] {
				udb.Unset(tbl, k)
			}
		}
	}
}

// clearApplianceMemory wipes all learned knowledge for an appliance: facts, knowledge docs,
// notes, techniques, and the cached system profile + log map stored on the appliance record.
func clearApplianceMemory(udb Database, applianceID string) {
	// Wipe auxiliary tables keyed by "applianceID:subkey".
	prefix := applianceID + ":"
	for _, tbl := range []string{factsTable, knowledgeTable, discoveriesTable} {
		for _, k := range udb.Keys(tbl) {
			if strings.HasPrefix(k, prefix) {
				udb.Unset(tbl, k)
			}
		}
	}
	// Notes and techniques keyed directly by applianceID.
	udb.Unset(notesTable, applianceID)
	udb.Unset(techniquesTable, applianceID)
	// Clear the profile and log map stored on the appliance record itself.
	var a Appliance
	if udb.Get(applianceTable, applianceID, &a) {
		a.Profile = ""
		a.LogMap = nil
		a.Scanned = ""
		udb.Set(applianceTable, applianceID, a)
	}
}

// purgeAppliance removes all data associated with an appliance: the record itself,
// facts, knowledge docs, notes, techniques, and the pooled SSH connection.
func purgeAppliance(userID string, udb Database, applianceID string) {
	udb.Unset(applianceTable, applianceID)

	// Facts, knowledge docs, and discoveries are keyed "applianceID:subkey".
	prefix := applianceID + ":"
	for _, tbl := range []string{factsTable, knowledgeTable, discoveriesTable} {
		for _, k := range udb.Keys(tbl) {
			if strings.HasPrefix(k, prefix) {
				udb.Unset(tbl, k)
			}
		}
	}

	// Notes and techniques are keyed directly by applianceID.
	udb.Unset(notesTable, applianceID)
	udb.Unset(techniquesTable, applianceID)

	dropConn(userID, applianceID)
}

// --- Chat ---

func (T *Servitor) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ApplianceID string `json:"appliance_id"`
		WorkspaceID string `json:"workspace_id"`
		Message     string `json:"message"`
		History     []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}
	if req.ApplianceID == "" || udb == nil {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	var appliance Appliance
	if !udb.Get(applianceTable, req.ApplianceID, &appliance) {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}

	// Load supplements from the active workspace, if any.
	var supplements []WorkspaceSupplement
	if req.WorkspaceID != "" {
		if ws, ok := loadWorkspace(udb, req.WorkspaceID); ok {
			supplements = ws.Supplements
		}
	}

	label := appliance.Name + ": " + req.Message
	if len(label) > 80 {
		label = label[:80]
	}

	sid := UUIDv4()
	ctx, cancel := context.WithCancel(AppContext())
	probeSessions.Register(sid, label, cancel)
	sessionAppliances.Store(sid, appliance.ID)
	ch := make(chan bool, 1)
	confirmChans.Store(sid, ch)

	var hist []Message
	for _, h := range req.History {
		if h.Role != "user" && h.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		hist = append(hist, Message{Role: h.Role, Content: h.Content})
	}
	hist = append(hist, Message{Role: "user", Content: req.Message})

	// Pre-create the injection queue so /api/inject finds it immediately
	// (the user could plausibly inject before the worker goroutine even
	// installs OnRoundStart).
	injectionQueues.Store(sid, &injectionQueue{})

	go T.runSession(ctx, sid, userID, appliance, ch, hist, udb, false, supplements)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"session_id": sid})
}

// handleInject manages mid-flight user notes for an active chat session.
//
//	POST   {id, text}                     → queue a new note. Returns {note_id}.
//	POST   {id, note_id, action:"lock"}   → mark a queued note as being edited.
//	POST   {id, note_id, action:"unlock"} → cancel an in-progress edit.
//	PATCH  {id, note_id, text}            → commit edited text (auto-unlocks).
//	DELETE {id, note_id}                  → remove an unread note.
//
// Notes the orchestrator has already drained can no longer be edited or
// deleted — those return 410 Gone. Edit-locked notes stay in the queue but
// are skipped by Drain so the orchestrator can't grab them mid-edit.
func (T *Servitor) handleInject(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	var req struct {
		ID     string `json:"id"`
		NoteID string `json:"note_id"`
		Text   string `json:"text"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	v, ok := injectionQueues.Load(req.ID)
	if !ok {
		http.Error(w, "session not found or not interjectable", http.StatusNotFound)
		return
	}
	q := v.(*injectionQueue)

	switch r.Method {
	case http.MethodPost:
		// Lock/unlock are POST + action so we don't need separate routes.
		switch req.Action {
		case "lock":
			if req.NoteID == "" {
				http.Error(w, "note_id required", http.StatusBadRequest)
				return
			}
			if !q.Lock(req.NoteID) {
				http.Error(w, "note already delivered or not found", http.StatusGone)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "unlock":
			if req.NoteID == "" {
				http.Error(w, "note_id required", http.StatusBadRequest)
				return
			}
			if !q.Unlock(req.NoteID) {
				http.Error(w, "note already delivered or not found", http.StatusGone)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "":
			// Falls through to create-new-note path.
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		req.Text = strings.TrimSpace(req.Text)
		if req.Text == "" {
			http.Error(w, "text required", http.StatusBadRequest)
			return
		}
		noteID, clean := q.Push(req.Text)
		if !clean {
			emit(req.ID, probeEvent{Kind: "status", Text: "Note queued (oldest dropped — queue at capacity)"})
		} else {
			emit(req.ID, probeEvent{Kind: "status", Text: "Note queued — orchestrator will see it on its next decision."})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"note_id": noteID})
	case http.MethodPatch:
		req.Text = strings.TrimSpace(req.Text)
		if req.NoteID == "" || req.Text == "" {
			http.Error(w, "note_id and text required", http.StatusBadRequest)
			return
		}
		if !q.Update(req.NoteID, req.Text) {
			http.Error(w, "note already delivered or not found", http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if req.NoteID == "" {
			http.Error(w, "note_id required", http.StatusBadRequest)
			return
		}
		if !q.Delete(req.NoteID) {
			http.Error(w, "note already delivered or not found", http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Map (full scan) ---

func (T *Servitor) handleMap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ApplianceID string `json:"appliance_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ApplianceID == "" {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	if udb == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	var appliance Appliance
	if !udb.Get(applianceTable, req.ApplianceID, &appliance) {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}

	sid := UUIDv4()
	ctx, cancel := context.WithCancel(AppContext())
	probeSessions.Register(sid, "Mapping "+appliance.Name, cancel)
	sessionAppliances.Store(sid, appliance.ID)
	ch := make(chan bool, 1)
	confirmChans.Store(sid, ch)

	if appliance.Type == "command" {
		// Command-type appliances: map the command's CLI structure.
		go T.runMapAppSession(ctx, sid, userID, appliance, appliance.Command, ch, udb)
	} else {
		var mapMsg string
		if appliance.Profile != "" {
			mapMsg = fmt.Sprintf(
				"Update and extend the existing profile for the Linux appliance at %s (connected as %s). Verify key facts are still current, discover anything new, and produce a complete refreshed profile.",
				appliance.Host, appliance.User,
			)
		} else {
			mapMsg = fmt.Sprintf(
				"Perform a complete reconnaissance and profile of the Linux appliance at %s (connected as %s). Be systematic and thorough. Discover all services, configurations, and log file locations.",
				appliance.Host, appliance.User,
			)
		}
		hist := []Message{{Role: "user", Content: mapMsg}}
		go T.runSession(ctx, sid, userID, appliance, ch, hist, udb, true, nil)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"session_id": sid})
}

func (T *Servitor) handleMapApp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ApplianceID string `json:"appliance_id"`
		Command     string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ApplianceID == "" || req.Command == "" {
		http.Error(w, "appliance_id and command required", http.StatusBadRequest)
		return
	}
	if udb == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	var appliance Appliance
	if !udb.Get(applianceTable, req.ApplianceID, &appliance) {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}

	sid := UUIDv4()
	ctx, cancel := context.WithCancel(AppContext())
	probeSessions.Register(sid, "Mapping "+req.Command+" on "+appliance.Name, cancel)
	sessionAppliances.Store(sid, appliance.ID)
	ch := make(chan bool, 1)
	confirmChans.Store(sid, ch)

	go T.runMapAppSession(ctx, sid, userID, appliance, req.Command, ch, udb)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"session_id": sid})
}

// handleConfirm allows, always-allows, or denies a pending destructive command.
// allow=true → allow once; allow=always → allow and save to always-allow list; allow=false → deny.
func (T *Servitor) handleConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	allowParam := r.URL.Query().Get("allow")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	ch, ok2 := confirmChans.Load(id)
	if !ok2 {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if allowParam == "always" && udb != nil {
		if cmd, loaded := pendingCmds.Load(id); loaded {
			udb.Set(alwaysAllowTable, cmd.(string), true)
		}
	}
	allow := allowParam == "true" || allowParam == "always"
	select {
	case ch.(chan bool) <- allow:
	default:
	}
	w.WriteHeader(http.StatusOK)
}

// handleRun executes a single ad-hoc command on the selected appliance.
// Destructive commands require force=true; the first call without force
// returns a confirmation prompt so the browser can ask the user.
func (T *Servitor) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ApplianceID string `json:"appliance_id"`
		Command     string `json:"command"`
		Force       bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Command == "" {
		http.Error(w, "command required", http.StatusBadRequest)
		return
	}
	if req.ApplianceID == "" || udb == nil {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	var appliance Appliance
	if !udb.Get(applianceTable, req.ApplianceID, &appliance) {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}

	// Check for destructive commands before connecting.
	if destructive, reason := is_destructive(req.Command); destructive && !req.Force {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"needs_confirm": true,
			"reason":        reason,
		})
		return
	}

	var output string
	var runErr error
	if appliance.Type == "command" {
		a := &Servitor{}
		output, runErr = a.exec_local_ctx(r.Context(), req.Command, appliance.WorkDir, appliance.EnvVars)
	} else {
		a := &Servitor{}
		a.input.host = appliance.Host
		a.input.port = appliance.Port
		if a.input.port == 0 {
			a.input.port = 22
		}
		a.input.user = appliance.User
		if a.input.user == "" {
			a.input.user = "root"
		}
		a.input.password = appliance.Password
		if err := a.connect(); err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"error": "connection failed: " + err.Error()})
			return
		}
		defer a.conn.Close()
		output, runErr = a.exec_command_ctx(r.Context(), req.Command)
	}
	resp := map[string]any{"output": output}
	if runErr != nil {
		resp["error"] = runErr.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleFacts returns all stored facts for a given appliance (GET)
// or deletes a single fact by its DB key (DELETE ?key=<id>).
func (T *Servitor) handleFacts(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		id := r.URL.Query().Get("id")
		if id == "" || udb == nil {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		facts := factsForAppliance(udb, id)
		if facts == nil {
			facts = []SshFact{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(facts)
	case http.MethodPost:
		var req struct {
			ApplianceID string   `json:"appliance_id"`
			Key         string   `json:"key"`
			Value       string   `json:"value"`
			Tags        []string `json:"tags,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ApplianceID == "" || req.Key == "" || req.Value == "" {
			http.Error(w, "appliance_id, key, and value required", http.StatusBadRequest)
			return
		}
		var appliance Appliance
		if udb == nil || !udb.Get(applianceTable, req.ApplianceID, &appliance) {
			http.Error(w, "appliance not found", http.StatusNotFound)
			return
		}
		storeFact(udb, req.ApplianceID, appliance.Name, req.Key, req.Value, "long", req.Tags)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		key := r.URL.Query().Get("key")
		if key == "" || udb == nil {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		udb.Unset(factsTable, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleMemoryClear wipes all learned memory for an appliance (facts, knowledge docs,
// notes, techniques) without deleting the appliance record or its system profile.
func (T *Servitor) handleMemoryClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ApplianceID string `json:"appliance_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ApplianceID == "" {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	if udb == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	clearApplianceMemory(udb, req.ApplianceID)
	w.WriteHeader(http.StatusOK)
}

// handleDisconnect closes and removes the pooled SSH connection for an appliance.
func (T *Servitor) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ApplianceID string `json:"appliance_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ApplianceID == "" {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	dropConn(userID, req.ApplianceID)
	w.WriteHeader(http.StatusOK)
}

// --- SSH connection pool ---
// Connections are keyed by (userID, applianceID) and reused across chat/map/terminal
// calls. A connection is only closed on explicit disconnect or when it is found dead.

type sshPoolEntry struct {
	mu   sync.Mutex
	conn *ssh.Client
}

var sshConnPool sync.Map // "userID:applianceID" → *sshPoolEntry

// acquireConn returns a live *ssh.Client for the given user+appliance, reusing
// an existing pooled connection if still alive, or dialling a fresh one.
func acquireConn(userID string, appliance Appliance) (*ssh.Client, error) {
	key := userID + ":" + appliance.ID
	if v, ok := sshConnPool.Load(key); ok {
		e := v.(*sshPoolEntry)
		e.mu.Lock()
		if connIsAlive(e.conn) {
			c := e.conn
			e.mu.Unlock()
			return c, nil
		}
		e.conn.Close()
		e.conn = nil
		e.mu.Unlock()
		sshConnPool.Delete(key)
	}

	a := &Servitor{}
	a.input.host = appliance.Host
	a.input.port = appliance.Port
	if a.input.port == 0 {
		a.input.port = 22
	}
	a.input.user = appliance.User
	if a.input.user == "" {
		a.input.user = "root"
	}
	a.input.password = appliance.Password
	if err := a.connect(); err != nil {
		return nil, err
	}
	e := &sshPoolEntry{conn: a.conn}
	sshConnPool.Store(key, e)
	return a.conn, nil
}

// dropConn closes and removes the pooled connection for a user+appliance.
func dropConn(userID, applianceID string) {
	key := userID + ":" + applianceID
	if v, loaded := sshConnPool.LoadAndDelete(key); loaded {
		v.(*sshPoolEntry).conn.Close()
	}
}

// connIsAlive probes an SSH client by opening and immediately closing a session.
func connIsAlive(c *ssh.Client) bool {
	if c == nil {
		return false
	}
	sess, err := c.NewSession()
	if err != nil {
		return false
	}
	sess.Close()
	return true
}

// emit adds an event to a session's buffer.
func emit(id string, ev probeEvent) {
	probeSessions.AppendEvent(id, ev, false)
}

var wsUpgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	ReadBufferSize:   4096,
	WriteBufferSize:  4096,
	CheckOrigin:      func(r *http.Request) bool { return true },
}

// handleTerminal upgrades to a WebSocket and registers it as the passive agent
// viewer for this appliance. The terminal receives command output mirrored from
// active agent sessions via mirrorToTerm — it is read-only and requires no SSH
// connection of its own.
func (T *Servitor) handleTerminal(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" || udb == nil {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	var appliance Appliance
	if !udb.Get(applianceTable, id, &appliance) {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}

	ws, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	var wsMu sync.Mutex
	wsWrite := func(data []byte) {
		wsMu.Lock()
		defer wsMu.Unlock()
		ws.WriteMessage(websocket.BinaryMessage, data)
	}

	termKey := userID + ":" + id
	termWriters.Store(termKey, wsWrite)
	defer termWriters.Delete(termKey)

	wsWrite([]byte("\x1b[2m── " + appliance.Name + " — waiting for agent activity ──\x1b[0m\r\n" + terminalPrompt(appliance)))

	// Keep the connection open until the client disconnects.
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			return
		}
	}
}

// writeInstructions appends the appliance's custom instructions block when present.
func writeInstructions(b *strings.Builder, appliance Appliance) {
	if strings.TrimSpace(appliance.Instructions) == "" {
		return
	}
	b.WriteString("## Custom Instructions for This Appliance\n\n")
	b.WriteString(strings.TrimSpace(appliance.Instructions))
	b.WriteString("\n\n")
}

// writePersona injects the appliance persona near the top of a system prompt.
// It should be called early so the persona shapes the model's entire approach.
func writePersona(b *strings.Builder, appliance Appliance) {
	prompt := strings.TrimSpace(appliance.PersonaPrompt)
	if prompt == "" {
		return
	}
	name := strings.TrimSpace(appliance.PersonaName)
	if name != "" {
		b.WriteString("## Role: ")
		b.WriteString(name)
		b.WriteString("\n\n")
	} else {
		b.WriteString("## Role\n\n")
	}
	b.WriteString(prompt)
	b.WriteString("\n\n")
}

// buildCommandChatSystemPrompt constructs the system prompt for command-type appliances.
// The worker operates a specific local CLI tool instead of SSH.
func buildCommandChatSystemPrompt(appliance Appliance, udb Database) string {
	now := time.Now()
	var b strings.Builder
	writePersona(&b, appliance)
	b.WriteString(fmt.Sprintf("Current time: %s\n\n", now.Format("2006-01-02 15:04 MST")))
	b.WriteString(fmt.Sprintf("You are an agent operating `%s` locally", appliance.Command))
	if appliance.WorkDir != "" {
		b.WriteString(fmt.Sprintf(" from `%s`", appliance.WorkDir))
	}
	b.WriteString(fmt.Sprintf(
		". Use `%s` subcommands and any supporting shell commands to accomplish the user's request.\n\n",
		appliance.Command,
	))
	if appliance.Instructions != "" {
		b.WriteString("## Instructions\n\n")
		b.WriteString(appliance.Instructions)
		b.WriteString("\n\n")
	}
	if udb != nil {
		if maps := cliMapsForAppliance(udb, appliance.ID); len(maps) > 0 {
			b.WriteString("## Command Reference\n\n")
			b.WriteString("Use this reference to pick the correct subcommand and flags without guessing:\n\n")
			for cmd, content := range maps {
				b.WriteString(fmt.Sprintf("### %s\n\n%s\n\n", cmd, content))
			}
		}
	}
	b.WriteString("## Rules\n\n")
	b.WriteString(fmt.Sprintf("1. Prefer the Command Reference above when available. If a subcommand is not covered, run `%s help <subcommand>` or `%s <subcommand> --help` to discover the correct syntax.\n", appliance.Command, appliance.Command))
	b.WriteString("2. Never guess flags or argument order — check help output first.\n")
	b.WriteString("3. Work to completion without pausing. The user cannot interact mid-run.\n")
	b.WriteString("4. Call `store_fact` immediately after any command that returns a concrete value (resource name, config path, status, version).\n")
	b.WriteString("5. Call `record_technique` when you figure out the correct non-obvious way to do something (working auth, right flag combination, correct resource name format).\n")
	b.WriteString("6. Call `note_lesson` when a command fails due to a non-obvious quirk specific to this environment.\n")
	b.WriteString("7. **Temporary file cleanup** — delete any scripts or temporary files you create (e.g. `/tmp/check.sh`, helper scripts) with `rm` before producing your final response. Do not leave artifacts behind.\n")
	b.WriteString("8. **Saving work** — when the user asks to save a query or script for later, call `save_to_codewriter`. When the user asks to document findings, save a report, or create a runbook, call `save_to_techwriter`. You can save AND run in the same response.\n\n")
	b.WriteString("## Execution Protocol\n\n")
	b.WriteString("Treat every command like an expect script: know what you're looking for, run it, read the output, branch on what you got.\n\n")
	b.WriteString("- **Relevant output** → extract facts, call `store_fact`, proceed.\n")
	b.WriteString("- **`[exit code N]`** → nonzero exit. Read the error carefully and apply the matching fix. Do not move on without resolving it.\n")
	b.WriteString("- **`command not found`** → verify the command name and PATH, then retry.\n")
	b.WriteString("- **`permission denied`** / **`forbidden`** → check required credentials or context, then retry.\n")
	b.WriteString("- **Empty output** → do NOT stop. Try an alternative flag or different subcommand before concluding the resource is absent.\n\n")
	b.WriteString("**Script self-correction** — when a script fails, read the exact error, fix the specific problem, re-run. Up to 3 fix attempts. If still failing: `note_lesson` and try a different approach.\n\n")
	b.WriteString("**No-repeat rule**: never run the same command twice if it already returned the same output. A third identical call triggers LOOP DETECTED.\n\n")
	b.WriteString("**Simplest path first**: always look for the most direct way to access a resource before attempting anything complex. If a credential is in a config or .env file, use it. If the app connects via a unix socket, use that socket. Do NOT attempt to reconstruct encryption, decode tokens, or reverse-engineer auth flows when a simpler path exists. The running application has already solved the access problem — find its method and reuse it.\n\n")
	b.WriteString("**Completion gate**: do not produce a final response until the task goal is either answered with real output or confirmed impossible after at least 2 different approaches.\n\n")
	b.WriteString("**Status envelope** — end every response with exactly this block:\n")
	b.WriteString("```\n---\nSTATUS: found|partial|not_found\nFACTS_SAVED: N\n---\n```\n\n")
	return b.String()
}

// buildChatSystemPrompt constructs the system prompt for interactive Q&A sessions.
// It does NOT use the full recon base prompt — that is only for map mode. Chat mode
// answers questions from the cached profile, only running commands when the cache
// cannot answer or the user explicitly asks for live data.
func buildChatSystemPrompt(appliance Appliance) string {
	now := time.Now()
	var b strings.Builder
	writePersona(&b, appliance)
	b.WriteString(fmt.Sprintf("Current time: %s\n\n", now.Format("2006-01-02 15:04 MST")))
	if appliance.Profile != "" {
		b.WriteString(fmt.Sprintf(
			"You are a Linux systems assistant with SSH access to %s (%s). A full profile of this system is cached below — use it to answer questions directly without re-running commands. Only issue SSH commands when the cached data cannot answer the question, the user explicitly requests fresh data, or the fact is volatile (see rule 1).",
			appliance.Name, appliance.Host,
		))
	} else {
		b.WriteString(fmt.Sprintf(
			"You are a Linux systems assistant with SSH access to %s (%s). No profile has been built yet — you can run SSH commands to investigate specific questions. For a complete system overview, suggest the user clicks 'Map System'.",
			appliance.Name, appliance.Host,
		))
	}
	b.WriteString("\n\n")
	writeInstructions(&b, appliance)
	b.WriteString("## Rules\n\n")
	b.WriteString("1. **Freshness awareness** — Not all facts age the same way:\n")
	b.WriteString("   - **Always re-run** (changes every session): who is currently logged in, active SSH sessions.\n")
	b.WriteString("   - **Short TTL — re-run if older than 30 minutes**: user accounts, running services, open ports, active connections, disk usage, recent security events.\n")
	b.WriteString("   - **Long TTL — trust for 24 hours**: installed software versions, config file values, hardware specs, log file paths, network interface addresses.\n")
	b.WriteString("   Facts in the 'What We Already Know' section are annotated with their age — use that to decide whether to re-verify.\n")
	b.WriteString("2. Never guess or invent facts. If the cache doesn't have the answer and it matters, run a targeted SSH command to find out.\n")
	b.WriteString("3. Report command errors and empty output exactly as observed — do not speculate.\n")
	b.WriteString("4. Work to completion without pausing to ask permission to continue. The user cannot interact mid-run.\n")
	b.WriteString("5. Use `run_pty` for anything requiring a TTY: password prompts (su, sudo, mysql -p), interactive programs (python3, psql, irb).\n")
	b.WriteString("6. Call `note_lesson` when a command fails due to a non-obvious system quirk so future sessions avoid the same mistake.\n")
	b.WriteString("7. Call `record_technique` when you figure out the correct way to do something non-obvious: working auth method, correct binary path, right command syntax for this system. Future sessions will use it directly without re-discovering.\n")
	b.WriteString("8. **DATABASE ACCESS — check before every attempt**: Before running any database command (mysql, psql, mongosh, redis-cli, sqlite3, etc.), look in 'Known Techniques' and 'What We Already Know' for a stored connection method for that engine. If one exists, use it AS-IS as your FIRST and ONLY attempt — do not try alternative methods unless the stored one fails. Stored keys: `mysql_auth`, `postgres_auth`, `redis_auth`, `mongo_auth`, `sqlite_path`.\n")
	b.WriteString("9. Before running any SSH command, check the 'Known Techniques' and 'What We Already Know' sections — if the answer is there and fresh enough, use it without running the command.\n")
	b.WriteString("10. After every SSH command that returns a concrete value (version string, port number, file path, config value, service status), immediately call `store_fact` to save it. Do not wait until the end. One call per fact. This makes you smarter for every future session.\n")
	b.WriteString("11. **Large output** — output is capped at 10,000 characters:\n")
	b.WriteString("   - Filter first: pipe through `| grep KEYWORD` or `| awk '{print $field}'` to narrow before reading.\n")
	b.WriteString("   - For files: use `count_lines` to check size, then `read_range` in chunks of up to 300 lines.\n")
	b.WriteString("   - When output is truncated, the message includes the exact `sed -n` command for the next page — use it.\n")
	b.WriteString("   - For user/group lists: `getent passwd | wc -l` first, then `awk -F: '$3>=1000'` to narrow.\n")
	b.WriteString("12. **Temporary file cleanup** — delete any scripts or temporary files you created (e.g. `/tmp/check.sh`, `/tmp/probe.py`) with `rm` before producing your final response. Do not leave artifacts on the system.\n")
	b.WriteString("13. **Saving work** — when the user asks to save a query or script for later reuse, call `save_to_codewriter`. When the user asks to document findings, save a report, or write a runbook, call `save_to_techwriter`. You can save AND run in the same response.\n\n")
	b.WriteString("## Execution Protocol\n\n")
	b.WriteString("Treat every command like an expect script: know what you're looking for, run it, read the output, branch on what you got.\n\n")
	b.WriteString("**After each command output, ask: did I get what I needed?**\n\n")
	b.WriteString("- **Relevant output** → extract the facts, call `store_fact`, proceed to the next step.\n")
	b.WriteString("- **Empty output** → do NOT stop. Possible causes: (a) thing doesn't exist — confirm with an alternative check; (b) wrong syntax — try the alternative form; (c) needs sudo — retry with `sudo`. Try at least 2 approaches before concluding something is absent.\n")
	b.WriteString("- **`command not found`** → locate the binary: `which <cmd>` or `find /usr /opt /bin /sbin -name '<cmd>' 2>/dev/null | head -5`, then retry with the full path. If still not found after one search, accept it as absent and move on.\n")
	b.WriteString("- **`permission denied`** → retry once with `sudo`. If that also fails, note it via `note_lesson` and move on — do not keep trying.\n")
	b.WriteString("- **`no such file or directory`** → search for the correct path once, then retry. If the path cannot be found, accept it as absent.\n")
	b.WriteString("- **`connection refused` / `can't connect`** → the service is likely down. Check `systemctl status <svc>` or `ss -tlnp` once, then report the status and move on.\n")
	b.WriteString("- **`[exit code N]`** → read the error carefully and apply one targeted fix. If it fails again, pivot to a different approach or accept the result.\n")
	b.WriteString("- **Truncated output** → always follow the sed hint or use `read_range` to get the rest. Never summarize from a truncated view.\n\n")
	b.WriteString("**Script self-correction** — when you write a shell script (bash/python/etc.) and it fails:\n")
	b.WriteString("1. Read the exact error line and message from the output.\n")
	b.WriteString("2. Identify and fix the specific problem (syntax error, wrong path, missing dependency, wrong shell).\n")
	b.WriteString("3. Write the corrected script and re-run it. Repeat up to 3 times.\n")
	b.WriteString("4. If still failing after 3 fixes: note the blocker via `note_lesson`, then try a different approach (inline commands, different language, simpler logic).\n\n")
	b.WriteString("**No-repeat rule**: Never run the same command twice if it already returned the same output. If you need different information, use a different command or different arguments. Running an identical command a third time will trigger a LOOP DETECTED error and waste a round.\n\n")
	b.WriteString("**Pivot rule** — recognize a dead end and change direction:\n")
	b.WriteString("- After **2 failures from the same binary** (e.g. `mysql`, `docker`, `systemctl`), stop using that binary. Switch to a completely different tool or investigation angle.\n")
	b.WriteString("- After **3 failures** the binary will be automatically blocked — you will not be able to use it again this session.\n")
	b.WriteString("- A real pivot means: different binary, different subsystem, or a different way of answering the question. Changing flags or adding sudo to something that already failed with sudo is NOT a pivot.\n")
	b.WriteString("- When something is confirmed absent after 2 different approaches, stop trying and report `STATUS: not_found`. Continuing past that wastes budget.\n\n")
	b.WriteString("**Indirect access** — if direct access to a resource fails, extrapolate how the running process itself accesses it and use that method instead. Look at config files, application source code, service unit files, compose files, and process environment variables to discover the credentials, socket path, or connection method the app already uses successfully. If the app can reach it, you can too — find out how.\n\n")
	b.WriteString("**Completion gate** — do not produce a final response until:\n")
	b.WriteString("1. At least one command returned relevant, non-empty output that directly addresses the task, OR\n")
	b.WriteString("2. You tried 2 meaningfully different approaches and confirmed the thing is genuinely absent — that IS a valid result, report it.\n\n")
	b.WriteString("**Status envelope** — end every response with exactly this block (no extra text after it):\n")
	b.WriteString("```\n---\nSTATUS: found|partial|not_found\nFACTS_SAVED: N\n---\n```\n")
	b.WriteString("- `found`: task goal answered with real command output\n")
	b.WriteString("- `partial`: some goals answered, others blocked (permission, service down, etc.)\n")
	b.WriteString("- `not_found`: confirmed absent after multiple approaches\n")
	b.WriteString("- `FACTS_SAVED`: exact count of `store_fact` calls made this response\n\n")
	b.WriteString("**PTY sessions (`run_pty`)** — always plan the full interaction before calling:\n")
	b.WriteString("- Know the exact prompt string to expect (e.g. `mysql>`, `Password:`, `postgres=#`).\n")
	b.WriteString("- Send all needed input lines in the `input` field (newline-separated) rather than making multiple calls.\n")
	b.WriteString("- If the session produced an unexpected interactive prompt, note the exact text, then call `run_pty` again with the correct response.\n")
	b.WriteString("- Always end interactive sessions cleanly: append `exit` or `\\q` or `\\c` as the last input line.\n")
	b.WriteString("- Use `timeout_sec=30` for slow operations (DB queries, compilations). If timeout is hit, the agent sends Ctrl+C — check the partial output and retry with a lighter query.\n\n")
	if appliance.Profile != "" {
		b.WriteString("## Cached System Profile\n\n")
		b.WriteString(appliance.Profile)
		if len(appliance.LogMap) > 0 {
			b.WriteString("\n\n## Known Log Files\n\n")
			b.WriteString("Use `read_log` or `search_logs` directly with these paths — do not re-discover them.\n\n")
			for _, e := range appliance.LogMap {
				b.WriteString(fmt.Sprintf("- **%s** `%s` — %s\n", e.Service, e.Path, e.Desc))
			}
		}
	}
	return b.String()
}

// probeWorkerProtocol is injected into investigator-dispatched probes.
// It is stricter than mapExecutionProtocol: one attempt per search path, then stop.
// The investigator decides whether a different angle is worth a new probe.
const probeWorkerProtocol = `## Execution Protocol

Treat every command like an expect script: know what you're looking for, run it, read the output, act on what you see.

After each command:
- Relevant output → extract facts, call store_fact, proceed.
- Empty output or "no such file or directory" → **stop immediately**. Do NOT try alternative paths or sudo variations. Report STATUS: not_found. The investigator will decide if another approach is worth a new probe.
- "command not found" → check once with which/find. If still absent, report STATUS: not_found and stop.
- "permission denied" → retry once with sudo. If that also fails, report what was blocked and stop.
- "connection refused" / "can't connect" → service is likely down. Check systemctl/ss once, report, stop.

No-repeat rule: never run the same command twice. Running an identical command triggers a LOOP DETECTED error.

Simplest path first: use the most direct method available. If a credential is in a .env file, use it. Never reconstruct encryption or reverse-engineer auth when a simpler path exists.

One command at a time: submit exactly one tool call per message.

Record wins immediately: call store_fact after any command that returns a concrete value. Call record_technique when you confirm a working auth method or non-obvious command.

Completion gate: stop as soon as the task is answered OR confirmed absent after ONE attempt. The investigator directs all follow-up — do not explore beyond the task.

Status envelope: end every response with exactly this block:
---
STATUS: found|partial|not_found
LEAD: <one-line description of the most promising next pointer, or "none">
FACTS_SAVED: N
---
found = task answered with real output; partial = some goals blocked; not_found = confirmed absent after the attempt.
LEAD = the single most actionable next pointer the investigator should pursue. Write "none" if there is nothing further.
FACTS_SAVED = exact count of store_fact calls made.
`

// mapExecutionProtocol is injected into both map prompt branches. It gives the worker
// explicit expect-like execution mechanics so it adapts on failure instead of stopping.
const mapExecutionProtocol = `## Execution Protocol

Treat every command like an expect script: know what you're looking for, run it, read the output, branch on what you see.

After each command:
- Relevant output → extract facts, call store_fact, proceed to the next step.
- Empty output → try one alternative approach (different syntax, sudo, different path). If that also returns nothing, accept it as absent and move on.
- "command not found" → locate the binary once: which <cmd> or find /usr /opt /bin /sbin -name '<cmd>' 2>/dev/null | head -5. If not found, accept it as absent.
- "permission denied" → retry once with sudo. If that also fails, note via note_lesson and move on.
- "no such file or directory" → search for the correct path once, retry, then accept the result.
- "connection refused" / "can't connect" → service is likely down. Check systemctl status or ss -tlnp once, report the status, move on.
- "[exit code N]" → apply one targeted fix. If it fails again, pivot to a different approach or accept the result.
- Truncated output → always follow the sed hint or use read_range to get the rest. Never summarize from a truncated view.

Script self-correction: when a shell/python script fails, read the exact error line, fix the specific problem, and re-run. Up to 3 fix attempts. If still failing after 3: note via note_lesson and try a different approach (inline commands, different language, simpler logic).

No-repeat rule: never run the same command twice if it already returned the same output. Running an identical command a third time triggers a LOOP DETECTED error. Use a different command or different arguments instead.

Pivot rule: after 2 failures from the same binary (e.g. mysql, docker, systemctl), stop using that binary and switch to a completely different tool or angle. After 3 failures from the same binary it will be automatically blocked for the rest of the session. Changing flags or adding sudo to something that already failed with sudo is not a pivot — use a different binary entirely. When something is confirmed absent after 2 different approaches, report STATUS: not_found and stop.

Simplest path first: always look for the most direct way to access a resource before attempting anything complex. If a credential is in a .env file, use it. If the app connects via a unix socket, connect via the same socket. If a config file has the DSN, use that DSN. Do NOT attempt to reconstruct encryption, decode tokens, reverse-engineer auth flows, or write complex scripts when a simpler path exists. The running application has already solved the access problem — find its method and reuse it.

Indirect access: if direct access to a resource fails, extrapolate how the running process itself accesses it and use that method instead. Look at config files, application source code, service unit files, compose files, and process environment variables to discover the credentials, socket path, or connection method the app already uses successfully. If the app can reach it, you can too — find out how.

One command at a time: submit exactly one tool call per message. Never batch multiple commands in a single response. Each result must be observed before deciding what to run next.

Record wins immediately: when a command succeeds after prior failures on the same binary, call record_technique BEFORE proceeding. Do not continue without recording what worked. The tool result will prompt you when this is required — treat it as a mandatory step, not a suggestion.

Record breakthroughs as discoveries: when you successfully access a database and see its contents, fully trace a request routing chain, find working credentials, or answer a major investigation goal — call record_discovery with the full narrative, evidence, and exact values found. Discoveries are the highest-tier knowledge and appear at the top of every future session. Do not summarize — include the actual output, schema names, route patterns, credential values, etc.

Completion gate: do not stop until every goal has been answered with real output OR confirmed absent after 2 meaningfully different approaches — confirmed absence is a valid result, not a reason to keep trying.

Status envelope: end every response with exactly this block:
---
STATUS: found|partial|not_found
LEAD: <one-line description of the most promising next pointer, or "none">
FACTS_SAVED: N
---
found = task answered with real output; partial = some goals blocked; not_found = confirmed absent after multiple attempts.
LEAD = the single most actionable next pointer from your findings — a specific file path, service name, credential location, socket path, or database handle that the orchestrator should pursue next. Write "none" if there is nothing further to investigate. This field is mandatory.
FACTS_SAVED = exact count of store_fact calls made.

PTY sessions (run_pty): plan the full interaction before calling. Include all needed input lines (exit/\q/\c as the last line). Use timeout_sec=30 for slow operations. If an unexpected prompt appears, note the exact text and call run_pty again with the correct response.

Temporary file cleanup: any scripts, temp files, or working files you create during this session (e.g. /tmp/check.sh, /tmp/probe.py) must be deleted with rm before you finish. Do not leave artifacts on the system.`

// buildSynthesisSystemPrompt is the system prompt for the profile synthesis pass.
// The synthesis agent has no SSH access — it only reads accumulated facts and summaries.
func buildSynthesisSystemPrompt(appliance Appliance) string {
	var b strings.Builder
	writePersona(&b, appliance)
	b.WriteString(fmt.Sprintf(
		"You are a Linux system profile synthesizer for **%s** (%s).\n\n"+
			"You have been given stored facts, discoveries, and investigator findings. "+
			"Your ONLY job: produce a single complete structured Markdown profile from this data. "+
			"Do NOT run any commands — no SSH access is available in this pass.\n\n"+
			"## Required Profile Sections\n\n"+
			"1. System Identity & Purpose — hostname, OS, kernel, hardware, virtualization type, primary role\n"+
			"2. Installed Software — key runtime versions, notable packages, recently installed\n"+
			"3. User Accounts & Privileges — all interactive users, sudo rules, privilege escalation paths\n"+
			"4. SSH & Remote Access — auth config, authorized keys, other remote access services\n"+
			"5. Network Configuration — all interfaces, routing, DNS, VPNs, bonds/bridges\n"+
			"6. Firewall & Security Policy — full ruleset, SELinux/AppArmor, certificates and expiry\n"+
			"7. Running Services & Ports — all services (running + failed), listening ports, process tree highlights\n"+
			"8. Scheduled Jobs & Automation — cron, timers, CM tools, deploy scripts\n"+
			"9. Service Communication Map — which services talk to which via what mechanism (HTTP, socket, queue, shared DB). Present as a dependency table.\n"+
			"10. Containers & Orchestration — every container, network, volume, compose topology\n"+
			"11. Databases — every engine: schemas, tables, users, grants, config, data size\n"+
			"12. Database Access Map — for each DB: which apps connect, exact credentials, connection method, read-only vs read-write\n"+
			"13. Applications & Frameworks — each app: language, framework, entry point, run user, deploy method\n"+
			"14. Application Dependency Graph — for each app: databases, caches, queues, internal APIs, external APIs\n"+
			"15. Application Source Structure — for each app: root directory, git repo URL/branch, entry point files, project layout, key source files, recent commits\n"+
			"16. Infrastructure & Deployment Config — docker-compose topology (full env vars, volumes, networks), k8s manifests, CI/CD pipelines, IaC summary, environment variable inventory per process\n"+
			"17. Code: Database Access Patterns — for each app: DB driver, DSN/connection-string pattern, ORM library and model files, connection pool settings, whether credentials are env-loaded or hardcoded, any raw SQL patterns found, read/write replica split\n"+
			"18. Code: Routing & Request Handling — for each app: full URL route map with handler names, middleware chain (auth, rate-limit, CORS, logging), authentication mechanism with exact implementation, HTTP server config (TLS, port, timeouts), WebSocket support, error response format\n"+
			"19. File System & Secrets — mounts, disk usage, notable config/credential files found\n"+
			"20. Logging & Monitoring — log locations, shipping config, monitoring agents\n"+
			"21. Recent Activity & Changes — recent package installs, modified files, git commits, auth events\n"+
			"22. Summary and Notable Findings — key architecture insights, security observations, anything unusual\n\n"+
			"Populate every section from the supplied facts and summaries. Be specific — include version numbers, paths, ports, and credentials where discovered. "+
			"Sections 9, 12, 14, 17, and 18 are the most valuable — fill them with full detail. "+
			"If data is genuinely absent for a section, write 'Not investigated in this scan.' — do not fabricate.",
		appliance.Name, appliance.Host,
	))
	return b.String()
}

// buildSynthesisMessage assembles investigator narrative and stored facts for the synthesis pass.
func buildSynthesisMessage(invNarrative string, storedFacts, techniques, notes, discoveries string) string {
	var b strings.Builder
	if discoveries != "" {
		b.WriteString("## Key Discoveries (breakthrough findings — highest confidence)\n\n")
		b.WriteString(discoveries)
		b.WriteString("\n\n")
	}
	if storedFacts != "" {
		b.WriteString("## Stored Facts (verified during investigation)\n\n")
		b.WriteString(storedFacts)
		b.WriteString("\n\n")
	}
	if techniques != "" {
		b.WriteString("## Known Techniques\n\n")
		b.WriteString(techniques)
		b.WriteString("\n\n")
	}
	if notes != "" {
		b.WriteString("## Lessons Learned\n\n")
		b.WriteString(notes)
		b.WriteString("\n\n")
	}
	if invNarrative != "" {
		b.WriteString("## Investigator Narrative\n\n")
		b.WriteString(invNarrative)
		b.WriteString("\n\n")
	}
	b.WriteString("Produce the complete structured Markdown profile now.")
	return b.String()
}

// runQuickSnapshot runs a fixed set of enumeration commands and returns formatted markdown.
// No LLM — just captures the system's fingerprint so the investigator can decide what to probe.
func runQuickSnapshot(ctx context.Context, execFn func(string) (string, error)) string {
	type snap struct{ label, cmd string }
	snaps := []snap{
		{"OS & Identity", `uname -a; hostname; cat /etc/os-release 2>/dev/null | grep -E '^(NAME|VERSION|PRETTY_NAME)' | head -5`},
		{"Resources", `nproc; free -h; df -h --output=target,size,avail 2>/dev/null | head -10`},
		{"Running Services", `systemctl list-units --type=service --state=running --no-pager --no-legend 2>/dev/null | awk '{print $1}' | head -40`},
		{"Listening Ports", `ss -tlnp 2>/dev/null | head -30`},
		{"Key Directories", `ls /opt/ /srv/ /var/www/ /home/ /app/ 2>/dev/null; ls /etc/nginx /etc/apache2 /etc/httpd /etc/php* 2>/dev/null`},
		{"App Config Files", `find /opt /srv /var/www /home -maxdepth 5 \( -name 'docker-compose.yml' -o -name '.env' -o -name 'package.json' -o -name 'go.mod' -o -name 'requirements.txt' -o -name 'Gemfile' \) 2>/dev/null | head -25`},
		{"Databases", `which mysql psql redis-cli mongosh mongo sqlite3 2>/dev/null; ss -tlnp 2>/dev/null | grep -E ':3306|:5432|:6379|:27017|:9200'`},
		{"Containers", `docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Ports}}\t{{.Status}}' 2>/dev/null`},
		{"Process Tree", `ps auxf 2>/dev/null | head -50`},
	}
	var out strings.Builder
	for _, s := range snaps {
		if ctx.Err() != nil {
			break
		}
		result, err := execFn(s.cmd)
		if err != nil || strings.TrimSpace(result) == "" {
			continue
		}
		out.WriteString(fmt.Sprintf("### %s\n```\n%s\n```\n\n", s.label, strings.TrimSpace(result)))
	}
	return out.String()
}

// buildInvestigatorSystemPrompt is the system prompt for the investigator agent in mapping mode.
// The investigator orchestrates targeted probes, records discoveries, and develops a complete
// operational picture — thinking like a security researcher, not a checklist executor.
func buildInvestigatorSystemPrompt(appliance Appliance) string {
	var b strings.Builder
	writePersona(&b, appliance)
	b.WriteString(fmt.Sprintf(
		"You are a skilled systems investigator with SSH access to **%s** (%s). "+
			"Your goal: build a complete operational picture of this system through targeted, hypothesis-driven investigation.\n\n",
		appliance.Name, appliance.Host,
	))
	writeInstructions(&b, appliance)
	b.WriteString("## Investigator Mindset\n\n")
	b.WriteString("Think like a security researcher doing reconnaissance:\n")
	b.WriteString("- **Follow the chain**: nginx proxies to port 3000 → what's on 3000 → read its config → find its DB connection → verify the credentials\n")
	b.WriteString("- **Understand, don't enumerate**: don't confirm services exist — understand how they're configured, who they trust, how they communicate\n")
	b.WriteString("- **Operational focus**: record how to deploy the app, restart a service, connect to the database — not just that they exist\n")
	b.WriteString("- **Specific probes**: 'show /etc/nginx/sites-enabled/myapp.conf and identify upstream' not 'investigate nginx'\n")
	b.WriteString("- **Dead ends are data**: if something is blocked, record it and redirect\n")
	b.WriteString("- **`[OUTCOME: not_found]` means done**: accept the result — do not probe the same target again with different arguments. Issue a new probe only if a completely different angle (different tool, different config path) is genuinely warranted\n\n")
	b.WriteString("## What to Record\n\n")
	b.WriteString("`record_discovery` is your primary output — use it for insights:\n")
	b.WriteString("- **Architecture**: how services connect and depend on each other (nginx:443 → node:3000 → postgres:5432 with Redis sessions)\n")
	b.WriteString("- **Database access**: exact working connection method, credentials, schemas, key tables\n")
	b.WriteString("- **Operations**: how to deploy, restart, tail logs, connect to databases on THIS system specifically\n")
	b.WriteString("- **Configuration**: where configs live, key settings, non-standard paths\n")
	b.WriteString("- **Security posture**: firewall state, sudo rules, exposed credentials, certificate expiry\n\n")
	b.WriteString("`store_fact` for atomic lookups: version strings, port numbers, file paths.\n")
	b.WriteString("`record_technique` for confirmed working commands: exact auth methods, non-standard binary paths.\n\n")
	b.WriteString("## Workflow\n\n")
	b.WriteString("1. Orient yourself from the system snapshot — identify the primary role and main services\n")
	b.WriteString("2. For each significant service: probe its config, its connections, its auth — follow the chain\n")
	b.WriteString("3. Each `probe` call has ONE clear goal; the investigator (you) decides the next step based on what it returns\n")
	b.WriteString("4. Stop when you have: the system's purpose, its full architecture, working DB access for every engine, operational procedures\n\n")
	b.WriteString("Quality over quantity: 15 focused probes that reveal real configuration beat 40 broad sweeps.\n\n")
	b.WriteString("## Acronyms\n\n")
	b.WriteString("Internal acronyms have org-specific meanings that rarely match training-data priors. Treat any acronym as an opaque label until you have verified its meaning from the system itself — a README, comment, config-file annotation, log message, or explicit statement in documentation. If you only know the letters, use the letters. Writing 'GMS (Game Management System)' when nothing on this system explained what GMS stands for is fabrication, even when the expansion 'sounds plausible'. Probe to find the meaning, or leave it unexpanded.\n\n")
	b.WriteString("## Completion\n\n")
	b.WriteString("When done, write a concise narrative of your key findings. The structured profile is built from your stored discoveries and facts — focus on recording those.\n")
	return b.String()
}

// buildProbeWorkerPrompt is the system prompt for a focused worker dispatched by the investigator.
// The worker executes a specific task with a limited command budget and reports findings clearly.
func buildProbeWorkerPrompt(appliance Appliance) string {
	var b strings.Builder
	writePersona(&b, appliance)
	b.WriteString(fmt.Sprintf(
		"You are a focused SSH executor on **%s** (%s). "+
			"An investigator has sent you a specific task — execute it precisely.\n\n",
		appliance.Name, appliance.Host,
	))
	b.WriteString("## Rules\n\n")
	b.WriteString("- Run only the commands needed to complete the task — **maximum 10 tool calls**\n")
	b.WriteString("- Call `store_fact` immediately after any command that returns a concrete value\n")
	b.WriteString("- Call `record_technique` when you find a working auth method or non-obvious command\n")
	b.WriteString("- Call `note_lesson` when you hit a dead end future workers should avoid\n")
	b.WriteString("- Do NOT explore beyond the task — the investigator directs all follow-up\n")
	b.WriteString("- If a path is blocked (permission denied, not found) after one attempt, report it and stop\n\n")
	b.WriteString("## Acronyms\n\n")
	b.WriteString("Do NOT expand acronyms. Internal/organizational acronyms have org-specific meanings that rarely match what your training data suggests. Treat acronyms as opaque labels — quote them character-for-character from the source. Only state an acronym's expansion if you actually saw it spelled out in a comment, README, log message, or explicit documentation on this system. Writing 'GMS (Game Management System)' when you only saw 'GMS' in a path is fabrication. The investigator will probe specifically if an expansion matters.\n\n")
	b.WriteString("## Report Format\n\n")
	b.WriteString("After your commands, write a clear findings report:\n")
	b.WriteString("1. **Found**: exact values, output snippets, what the investigation revealed\n")
	b.WriteString("2. **Saved**: which facts and techniques were recorded\n")
	b.WriteString("3. **Blocked**: any access failures with the exact error message\n\n")
	b.WriteString(probeWorkerProtocol)
	return b.String()
}

// parseProbeOutcome strips the status envelope from a worker response and prepends
// a machine-readable [OUTCOME: ...] prefix so the investigator can act on it.
func parseProbeOutcome(result string) string {
	idx := strings.LastIndex(result, "\n---\n")
	if idx < 0 {
		return strings.TrimSpace(result)
	}
	envelope := result[idx+5:]
	body := strings.TrimSpace(result[:idx])
	status := "found"
	lead := ""
	for _, line := range strings.Split(envelope, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "STATUS:") {
			status = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(t, "STATUS:")))
		} else if strings.HasPrefix(t, "LEAD:") {
			v := strings.TrimSpace(strings.TrimPrefix(t, "LEAD:"))
			if v != "" && strings.ToLower(v) != "none" {
				lead = v
			}
		}
	}
	var prefix string
	switch status {
	case "not_found":
		prefix = "[OUTCOME: not_found — confirmed absent. Do not probe this topic again.]"
	case "partial":
		prefix = "[OUTCOME: partial — some goals blocked; further investigation may be needed.]"
	default:
		prefix = "[OUTCOME: found — task answered.]"
	}
	if lead != "" {
		prefix += "\n[LEAD: " + lead + "]"
	}
	return prefix + "\n\n" + body
}

// buildMapSystemPrompt constructs the system prompt for a map/update run.
// When a profile already exists the LLM is instructed to UPDATE it: run targeted
// checks, merge new findings with previous knowledge, and produce a complete
// refreshed document — not a diff and not a replacement that discards prior data.
func buildMapSystemPrompt(base string, appliance Appliance) string {
	if appliance.Profile == "" {
		var b strings.Builder
		writePersona(&b, appliance)
		b.WriteString(base)
		b.WriteString("\n\n")
		writeInstructions(&b, appliance)
		b.WriteString("As you map this system, work through all ten investigation phases completely. " +
			"Do not skip a phase because you think the system is simple — a minimal system with few services still deserves full log discovery, security posture review, and scheduled job enumeration. " +
			"After every command that returns a concrete value, call `store_fact` immediately — one fact per call. " +
			"Work autonomously to completion without pausing.\n\n" +
			"DATABASE AUTH RULE (mandatory): The first time you successfully authenticate to any database engine, you MUST immediately call BOTH:\n" +
			"  • `record_technique` — exact working command, e.g. 'MySQL root: mysql -u root (no password, unix socket)'\n" +
			"  • `store_fact` — key: `mysql_auth` / `postgres_auth` / `redis_auth` / `mongo_auth`, value: exact working command\n" +
			"This is not optional. These are consulted before every future database access attempt.\n\n" +
			mapExecutionProtocol)
		return b.String()
	}
	var b strings.Builder
	writePersona(&b, appliance)
	b.WriteString(base)
	b.WriteString("\n\n")
	writeInstructions(&b, appliance)
	b.WriteString("## Existing Profile (last scanned: ")
	b.WriteString(appliance.Scanned)
	b.WriteString(")\n\n")
	b.WriteString("An existing profile is stored for this appliance. Your task is to UPDATE and EXTEND it:\n\n")
	b.WriteString("1. Verify key facts are still current (hostname, running services, IPs, open ports).\n")
	b.WriteString("2. Run targeted checks for anything that may have changed since the last scan.\n")
	b.WriteString("3. Discover any new services, packages, users, or log files not in the existing profile.\n")
	b.WriteString("4. PRESERVE all existing knowledge — do not drop sections or entries that are still valid.\n")
	b.WriteString("5. Note any meaningful changes (e.g. a service that was running is now stopped).\n")
	b.WriteString("6. As you work through each step, call `store_fact` immediately after each command returns a concrete value — version strings, listening ports, config paths, service statuses, user accounts, network topology. One call per fact, right when you discover it. These facts are pre-injected into every future chat session so questions can be answered without re-running commands.\n\n")
	b.WriteString("Produce a single complete updated Markdown profile. Do NOT produce a diff — produce the full document.\n")
	b.WriteString("For any service, config, or log path you did not deeply investigate last time, investigate it fully now. " +
		"Follow every lead. After each command returning a concrete value, call `store_fact` immediately. " +
		"Work autonomously to completion without pausing.\n\n" +
		"DATABASE AUTH RULE (mandatory): When you verify or re-discover a working database auth method, call BOTH " +
		"`record_technique` (exact command) AND `store_fact` (key: `mysql_auth`/`postgres_auth`/`redis_auth`/`mongo_auth`). " +
		"These are consulted before every future database access attempt so nothing is re-discovered unnecessarily.\n\n" +
		mapExecutionProtocol + "\n\n")
	b.WriteString(appliance.Profile)
	if len(appliance.LogMap) > 0 {
		b.WriteString("\n\n## Previously Discovered Log Files\n\n")
		b.WriteString("These log files were found in the previous scan. Verify they still exist and add any new ones you find.\n\n")
		for _, e := range appliance.LogMap {
			b.WriteString(fmt.Sprintf("- **%s** `%s` — %s\n", e.Service, e.Path, e.Desc))
		}
	}
	return b.String()
}

// buildLeadSystemPrompt constructs the prompt for the lead Knowledge Manager LLM.
// The lead maintains structured knowledge docs about the system and dispatches the worker
// on precise, context-rich investigations. It never runs SSH commands directly.
func buildLeadSystemPrompt(udb Database, appliance Appliance, docs map[string]string, cachedFacts, cachedNotes, cachedTechniques, cachedRules, cachedDiscoveries, supplementContext string) string {
	var b strings.Builder
	writePersona(&b, appliance)
	b.WriteString(fmt.Sprintf("Current time: %s\n\n", time.Now().Format("2006-01-02 15:04 MST")))
	b.WriteString(fmt.Sprintf(
		"You are the Knowledge Manager for **%s** (%s).\n\n"+
			"Your job is to answer the user's questions with verified, specific facts — not estimates, not training knowledge, not guesses. "+
			"You maintain a structured knowledge base about this system and dispatch a worker agent to retrieve anything you cannot answer from verified records. "+
			"The worker has full SSH access and executes commands on your behalf. "+
			"If you do not have a verified answer, your only acceptable response is to dispatch the worker to get one.\n\n",
		appliance.Name, appliance.Host,
	))
	writeInstructions(&b, appliance)

	b.WriteString("## Your Knowledge Base\n\n")
	b.WriteString("You maintain five structured documents about this system. Use `read_doc` to fetch one by name:\n\n")
	b.WriteString("- **overview** — OS, hostname, IP, system purpose, installed services, hardware\n")
	b.WriteString("- **databases** — all database engines, schemas, connection strings, credentials, access users\n")
	b.WriteString("- **filesystem** — key directories, config file paths, log file paths, data directories\n")
	b.WriteString("- **services** — running services, ports, inter-service dependencies, process owners\n")
	b.WriteString("- **apps** — application frameworks, entry points, routing, ORM models, external integrations\n\n")
	b.WriteString("Use `update_doc` to persist new findings after any investigation.\n\n")

	cliMaps := cliMapsForAppliance(udb, appliance.ID)
	if len(cliMaps) > 0 {
		b.WriteString("## Mapped CLI Applications\n\n")
		b.WriteString("The following CLI tools have been explored and mapped. Use `read_doc cli:<name>` to fetch the full command reference before dispatching the worker on tasks involving that tool:\n\n")
		for cmd := range cliMaps {
			b.WriteString(fmt.Sprintf("- **%s** — use `read_doc cli:%s`\n", cmd, cmd))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Investigation Approach\n\n")
	b.WriteString("You are an investigator. Answer the user's question with verified facts — adapt your investigation to what you find.\n\n")
	b.WriteString("1. **Start from what you know**: check knowledge docs and stored facts first — if the question is fully answered with verified values, respond directly without probing.\n")
	b.WriteString("2. **Formulate a hypothesis**: what do you need to find to answer the question? What is the most direct path to that answer?\n")
	b.WriteString("3. **Probe specifically**: each `probe` call has ONE clear goal. 'Show MySQL connection string from /etc/app/config.yml' not 'investigate databases'.\n")
	b.WriteString("4. **Follow leads immediately**: when a probe reveals a promising pointer (file path, service name, credential), follow it now before moving to anything else.\n")
	b.WriteString("5. **Update your docs**: call `update_doc` after each probe that yields new information.\n")
	b.WriteString("6. **Synthesize when answered**: the moment the question is fully answered with verified values, write your response. Don't keep probing once the answer is in hand.\n")
	b.WriteString("7. **Try different angles**: if initial probes don't answer it, try a different service, config location, or access method.\n\n")
	b.WriteString("If a probe returns nothing useful, pivot immediately — different path, different service, different approach.\n\n")
	b.WriteString("**REQUIRED FINAL STEP**: Every session MUST end with a plain-text response to the user. Never end on a tool call.\n\n")
	b.WriteString("## Mid-Investigation User Notes\n\n")
	b.WriteString("The user may inject a message during a running investigation. It will appear in your message history prefixed with `[USER NOTE — submitted mid-investigation]`. Workers in flight when the note arrived have already completed their current task without seeing it — only you see it, and only between rounds. When you encounter such a note:\n")
	b.WriteString("- If the note is a clear directive (e.g. \"also check /var/log/foo\", \"focus on the database, ignore the web tier\"), incorporate it into your remaining plan and continue with the appropriate next probe. Briefly acknowledge it in your eventual final response.\n")
	b.WriteString("- If the note is ambiguous or could meaningfully change direction, end your current response with a short clarifying question to the user and STOP probing. The user will reply on the next turn and you'll resume with the answer in hand.\n")
	b.WriteString("- Never ignore a user note. Treat it as authoritative — the user has context you don't.\n\n")

	b.WriteString("## Rules\n\n")
	b.WriteString("- **No fabrication, ever.** Every specific value you write in a response — a path, IP address, port number, username, database name, table name, service name, version string, config value, or file name — MUST be a character-for-character copy from your knowledge docs, stored facts, or worker output from this session. Do not retype it from memory, do not normalize it, do not fix its capitalization or punctuation — find it in the text in front of you and copy it exactly. If you cannot find it in the text, do not write it.\n")
	b.WriteString("- **No examples from imagination.** Do not write 'for example', 'e.g.', 'such as', or 'like' followed by a specific value you invented. If you need an example, probe the worker to get a real one.\n")
	b.WriteString("- **No gap-filling.** If a doc has a gap — a missing field, an unknown value, an unanswered question — the correct response is to probe, not to fill it with a plausible-sounding value.\n")
	b.WriteString("- **Never assume. Never guess.** The words 'likely', 'probably', 'typically', 'should be', 'usually', 'often', 'I assume', 'I believe', 'I expect', and 'I think' are forbidden in final responses. If you are not certain, you do not know — go find out.\n")
	b.WriteString("- **Do not expand acronyms.** Internal/organizational acronyms have org-specific meanings that rarely match training-data priors. Treat any acronym as an opaque label until you have verified its meaning from the system itself — a README, comment, config-file annotation, log message, or explicit statement in documentation. If you only know the letters, use the letters. Writing 'GMS (Game Management System)' when you saw nothing on this system explaining what GMS stands for is fabrication, even when the expansion 'sounds plausible.' Probe to find the meaning, or leave it unexpanded.\n")
	b.WriteString("- **Never answer 'I don't know'** without first having probed. If the docs are empty and no probe has run, you do not yet know — go find out.\n")
	b.WriteString("- Never answer from training knowledge — only from probe findings or your knowledge docs.\n")
	b.WriteString("- The richer the `context` you give the probe, the more precise its findings will be.\n")
	b.WriteString("- For live state questions (running processes, logged-in users, open ports, disk usage), always probe — docs alone are never sufficient.\n")
	b.WriteString("- `update_doc` is MANDATORY after every `probe` call that yields new information — call it even if findings are sparse.\n")
	b.WriteString("- Synthesize clearly — do not dump raw command output at the user. Exact verified values only.\n\n")

	b.WriteString("## Diagrams\n\n")
	b.WriteString("When the user asks for a diagram, or when a diagram would clarify architecture, connections, or data flow, produce a plain ASCII diagram inside a ` ```text ` code block — never Mermaid.\n\n")
	b.WriteString("**Style:**\n")
	b.WriteString("- Use `+--...--+` boxes for hosts/services, `[ ]` for inline labels, `-->` / `<--` / `<-->` arrows for directed flow.\n")
	b.WriteString("- Label arrows with the protocol or port: `--:3306-->` or `--HTTP:8080-->`.\n")
	b.WriteString("- Group nodes into zones with a surrounding box or dashed border and a label in the top-left corner.\n")
	b.WriteString("- Align columns so the diagram reads cleanly at a fixed-width font.\n\n")
	b.WriteString("**What to avoid:**\n")
	b.WriteString("- Mermaid syntax — it will not render correctly in this context.\n")
	b.WriteString("- Overly dense diagrams — split into multiple focused diagrams (one per tier or function) for complex topologies.\n\n")

	if len(docs) > 0 {
		b.WriteString("## Current Knowledge Base\n\n")
		for _, name := range knowledgeDocNames {
			if content, ok := docs[name]; ok {
				b.WriteString(fmt.Sprintf("### %s\n\n%s\n\n", name, content))
			}
		}
	} else if appliance.Profile != "" {
		b.WriteString("## Uncategorized System Profile (no structured docs yet — use this to build them)\n\n")
		b.WriteString(appliance.Profile)
		b.WriteString("\n\n")
	}

	if cachedDiscoveries != "" {
		b.WriteString("## Key Discoveries (pre-established breakthroughs — do not re-investigate)\n\n")
		b.WriteString(cachedDiscoveries)
		b.WriteString("\n")
	}
	if cachedFacts != "" {
		b.WriteString("## Stored Facts (pre-verified values from prior sessions)\n\n")
		b.WriteString("Use these as authoritative context when dispatching the worker — no need to re-discover them.\n\n")
		b.WriteString(cachedFacts)
		b.WriteString("\n")
	}
	if cachedNotes != "" {
		b.WriteString("## Lessons Learned (mistakes to avoid — include relevant ones in worker context)\n\n")
		b.WriteString(cachedNotes)
		b.WriteString("\n")
	}
	if cachedTechniques != "" {
		b.WriteString("## Known Techniques (confirmed working approaches — include in worker context)\n\n")
		b.WriteString(cachedTechniques)
		b.WriteString("\n")
	}
	if cachedRules != "" {
		b.WriteString("## Standing Instructions (set by the user — always follow these)\n\n")
		b.WriteString(cachedRules)
		b.WriteString("\n")
	}
	if supplementContext != "" {
		b.WriteString("## Reference Documents (attached by user — follow each document's usage instruction)\n\n")
		b.WriteString(supplementContext)
		b.WriteString("\n")
	}
	return b.String()
}

// buildConsolidationPrompt returns the system prompt for the background knowledge
// consolidation agent that runs after each chat turn to catch missed facts/techniques.
func buildConsolidationPrompt(appliance Appliance) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(
		"You are a knowledge persistence agent for %s (%s).\n\n"+
			"Your ONLY job is to persist new findings from the exchange below into the structured knowledge base. "+
			"Do NOT write any text response — only call tools, then stop.\n\n",
		appliance.Name, appliance.Host,
	))
	b.WriteString("## Persistence Rules\n\n")
	b.WriteString("1. Only persist information explicitly stated in the exchange — never infer or invent.\n")
	b.WriteString("2. Call `read_doc` before `update_doc` — append new findings to existing content rather than replacing it wholesale.\n")
	b.WriteString("3. Call `store_fact` for every concrete value in the worker output: version strings, port numbers, file paths, config values, service statuses, account names.\n")
	b.WriteString("4. Call `record_technique` for every confirmed working approach: exact command syntax, successful auth method, non-standard binary path.\n")
	b.WriteString("5. Do not duplicate — check `read_doc` content before updating a doc.\n")
	b.WriteString("6. If nothing new was found beyond what is already stored, call no tools.\n")
	b.WriteString("7. Do NOT produce any text response. Your output must be tool calls only.\n")
	return b.String()
}


// buildMapAppSystemPrompt returns the system prompt for a CLI application mapping session.
func buildMapAppSystemPrompt(appliance Appliance, command string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("You are a CLI exploration agent connected to %s (%s).\n\n", appliance.Name, appliance.Host))
	b.WriteString(fmt.Sprintf("Your task: systematically enumerate all capabilities of the `%s` command and produce a structured reference document.\n\n", command))
	b.WriteString("## Exploration Protocol\n\n")
	b.WriteString(fmt.Sprintf("1. Locate the binary: `which %s` — if not found, try `find /usr /opt /usr/local/bin /bin /sbin -name '%s' 2>/dev/null | head -5`\n", command, command))
	b.WriteString(fmt.Sprintf("2. Get the version: `%s --version 2>&1 || %s version 2>&1 | head -3`\n", command, command))
	b.WriteString(fmt.Sprintf("3. Get root-level help: `%s --help 2>&1 || %s -h 2>&1 || %s help 2>&1 || %s 2>&1 | head -80`\n", command, command, command, command))
	b.WriteString("4. Parse the help output to identify all top-level subcommands (or flags if no subcommands)\n")
	b.WriteString("5. For each top-level subcommand (up to 20), run `<cmd> <sub> --help 2>&1`\n")
	b.WriteString("6. For any depth-1 subcommand that itself has subcommands, run `<cmd> <sub> <subsub> --help 2>&1` for each (up to 10 per subcommand) — this is the maximum depth\n")
	b.WriteString("7. Stop at depth 2 from root — do not recurse deeper\n\n")
	b.WriteString("## Rules\n\n")
	b.WriteString("- Use actual help text — do not invent descriptions from training knowledge\n")
	b.WriteString("- If `--help` fails, try `-h`, then `help` as a subcommand, then bare invocation with no args\n")
	b.WriteString("- If the binary is not found, say so clearly and stop\n")
	b.WriteString("- Cap subcommands per level at 20; if more exist, note the count and explore the most common ones\n")
	b.WriteString("- Call `note_lesson` if you discover non-standard help flags or quirks specific to this system\n\n")
	b.WriteString("## Output Format\n\n")
	b.WriteString("Produce a structured Markdown reference with these sections:\n\n")
	b.WriteString("- **Binary**: full path and version string\n")
	b.WriteString("- **Description**: what this tool does (from help text)\n")
	b.WriteString("- **Global Flags**: flags that apply to all subcommands\n")
	b.WriteString("- **Subcommands**: for each subcommand — description, flags, required args, and any nested subcommands indented below\n\n")
	b.WriteString("This document will be saved as the CLI reference for this appliance and injected into future sessions automatically.\n")
	writeInstructions(&b, appliance)
	return b.String()
}

// withHeartbeat runs fn in the foreground while a background goroutine emits a
// "still working" status event every 25 seconds. This gives the user a signal
// that long LLM calls (reasoning models, large context) are still progressing.
func withHeartbeat(ctx context.Context, id, label string, fn func()) {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		start := time.Now()
		for {
			select {
			case <-ticker.C:
				elapsed := time.Since(start).Round(time.Second)
				emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("%s… (%s)", label, elapsed)})
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	fn()
	close(done)
}

// runMapAppSession connects to the appliance and runs a focused CLI exploration pass.
// The agent enumerates the given command's subcommands and flags, then the reply is
// persisted as a knowledge doc under "cli:<command>" for injection into future sessions.
func (T *Servitor) runMapAppSession(ctx context.Context, id, userID string, appliance Appliance, command string, confirm chan bool, udb Database) {
	defer func() {
		confirmChans.Delete(id)
		pendingCmds.Delete(id)
		sessionAppliances.Delete(id)
		probeSessions.AppendEvent(id, probeEvent{Kind: "done"}, true)
		probeSessions.ScheduleCleanup(id)
	}()

	a := &Servitor{}
	a.AppCore = T.AppCore

	var execFn func(string) (string, error)
	if appliance.Type == "command" {
		emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Running locally: %s", appliance.Command)})
		execFn = func(cmd string) (string, error) {
			return a.exec_local_ctx(ctx, cmd, appliance.WorkDir, appliance.EnvVars)
		}
	} else {
		client, err := acquireConn(userID, appliance)
		if err != nil {
			probeSessions.AppendEvent(id, probeEvent{Kind: "error", Text: "Connection failed: " + err.Error()}, true)
			return
		}
		a.input.host = appliance.Host
		a.input.port = appliance.Port
		if a.input.port == 0 {
			a.input.port = 22
		}
		a.input.user = appliance.User
		if a.input.user == "" {
			a.input.user = "root"
		}
		a.input.password = appliance.Password
		a.conn = client
		execFn = func(cmd string) (string, error) {
			return a.exec_command_ctx(ctx, cmd)
		}
		emit(id, probeEvent{Kind: "status", Text: "Connected."})
	}

	termPrompt := terminalPrompt(appliance)
	termEcho := func(label, output string) {
		var buf strings.Builder
		buf.WriteString(strings.ReplaceAll(label, "\n", " "))
		buf.WriteString("\r\n")
		if output != "" {
			buf.WriteString(strings.ReplaceAll(output, "\n", "\r\n"))
			if !strings.HasSuffix(output, "\n") {
				buf.WriteString("\r\n")
			}
		}
		buf.WriteString(termPrompt)
		mirrorToTerm(userID, appliance.ID, []byte(buf.String()))
	}

	cmdCount := make(map[string]int)

	run_tool := AgentToolDef{
		Tool: Tool{
			Name:        "run_command",
			Description: "Execute a shell command and return combined stdout+stderr. Output is capped at 10,000 characters.",
			Parameters: map[string]ToolParam{
				"command": {Type: "string", Description: "Shell command to run."},
			},
			Required: []string{"command"},
		},
		Handler: func(args map[string]any) (string, error) {
			cmd, _ := args["command"].(string)
			if cmd == "" {
				return "", fmt.Errorf("command is required")
			}
			cmdCount[cmd]++
			if cmdCount[cmd] > 3 {
				return fmt.Sprintf("[LOOP DETECTED] run_command(%q) called %d times — try a different approach.", cmd, cmdCount[cmd]-1), nil
			}
			emit(id, probeEvent{Kind: "cmd", Text: cmd})
			result, err := execFn(cmd)
			if stripANSI(result) != "" {
				emit(id, probeEvent{Kind: "output", Text: result})
			}
			termEcho(cmd, result)
			return result, err
		},
		NeedsConfirm: false,
	}

	note_lesson_tool := AgentToolDef{
		Tool: Tool{
			Name:        "note_lesson",
			Description: "Record a system-specific quirk about this CLI tool for future sessions.",
			Parameters: map[string]ToolParam{
				"lesson": {Type: "string", Description: "What was non-obvious or system-specific."},
			},
			Required: []string{"lesson"},
		},
		Handler: func(args map[string]any) (string, error) {
			lesson, _ := args["lesson"].(string)
			if lesson == "" {
				return "", fmt.Errorf("lesson is required")
			}
			if udb != nil {
				var existing string
				udb.Get(notesTable, appliance.ID, &existing)
				udb.Set(notesTable, appliance.ID, existing+"\n- "+strings.TrimSpace(lesson))
			}
			emit(id, probeEvent{Kind: "status", Text: "Note: " + lesson})
			return "noted", nil
		},
		NeedsConfirm: false,
	}

	taskMsg := fmt.Sprintf(
		"Map the `%s` command on this system. Enumerate all subcommands to depth 2, their flags and arguments, and produce a complete structured reference document a future session can use to operate this tool correctly without guessing.",
		command,
	)

	emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Mapping %s…", command)})

	var resp *Response
	var err error
	withHeartbeat(ctx, id, fmt.Sprintf("Mapping %s", command), func() {
		resp, _, err = a.RunAgentLoop(ctx,
			[]Message{{Role: "user", Content: taskMsg}},
			AgentLoopConfig{
				SystemPrompt:    buildMapAppSystemPrompt(appliance, command),
				Tools:           []AgentToolDef{run_tool, note_lesson_tool},
				MaxRounds:       60,
					RouteKey:        "app.servitor",
				MaskDebugOutput: true,
				ChatOptions:     []ChatOption{WithThink(false)},
			},
		)
	})

	if err != nil && ctx.Err() == nil {
		emit(id, probeEvent{Kind: "error", Text: err.Error()})
		return
	}

	var reply string
	if resp != nil {
		reply = strings.TrimSpace(resp.Content)
	}
	if reply == "" {
		return
	}

	emit(id, probeEvent{Kind: "reply", Text: reply})

	if udb != nil {
		writeDoc(udb, appliance.ID, "cli:"+command, reply)
		emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("CLI map saved: %s", command)})
	}
}

// runSession acquires a pooled SSH connection, runs the agent loop, and streams events.
// The connection is NOT closed on return — it stays pooled for the next request.
// saveProfile=true writes the final LLM reply and extracted log map back to the appliance record.
func (T *Servitor) runSession(ctx context.Context, id, userID string, appliance Appliance, confirm chan bool, messages []Message, udb Database, saveProfile bool, supplements []WorkspaceSupplement) {
	defer func() {
		confirmChans.Delete(id)
		pendingCmds.Delete(id)
		injectionQueues.Delete(id)
		sessionAppliances.Delete(id)
		probeSessions.AppendEvent(id, probeEvent{Kind: "done"}, true)
		probeSessions.ScheduleCleanup(id)
	}()

	a := &Servitor{}
	a.AppCore = T.AppCore

	if appliance.Type == "command" {
		emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Running locally: %s", appliance.Command)})
	} else {
		client, err := acquireConn(userID, appliance)
		if err != nil {
			probeSessions.AppendEvent(id, probeEvent{Kind: "error", Text: "Connection failed: " + err.Error()}, true)
			probeSessions.ScheduleCleanup(id)
			return
		}
		a.input.host = appliance.Host
		a.input.port = appliance.Port
		if a.input.port == 0 {
			a.input.port = 22
		}
		a.input.user = appliance.User
		if a.input.user == "" {
			a.input.user = "root"
		}
		a.input.password = appliance.Password
		a.conn = client
		emit(id, probeEvent{Kind: "status", Text: "Connected."})
	}

	// termEcho mirrors a worker command label and its output into the active terminal pane.
	termPrompt := terminalPrompt(appliance)
	termEcho := func(label, output string) {
		var buf strings.Builder
		buf.WriteString(strings.ReplaceAll(label, "\n", " "))
		buf.WriteString("\r\n")
		if output != "" {
			buf.WriteString(strings.ReplaceAll(output, "\n", "\r\n"))
			if !strings.HasSuffix(output, "\n") {
				buf.WriteString("\r\n")
			}
		}
		buf.WriteString(termPrompt)
		mirrorToTerm(userID, appliance.ID, []byte(buf.String()))
	}

	// sshExec executes a command via the appropriate exec path: local for command-type
	// appliances, SSH with transparent reconnect for ssh-type appliances. ctx is the
	// session context so a cancelled session aborts in-flight commands.
	sshExec := func(cmd string) (string, error) {
		if appliance.Type == "command" {
			return a.exec_local_ctx(ctx, cmd, appliance.WorkDir, appliance.EnvVars)
		}
		result, err := a.exec_command_ctx(ctx, cmd)
		if err == nil {
			return result, nil
		}
		msg := err.Error()
		isConnErr := strings.Contains(msg, "EOF") ||
			strings.Contains(msg, "connection reset") ||
			strings.Contains(msg, "broken pipe") ||
			strings.Contains(msg, "new SSH session")
		if !isConnErr {
			return result, err
		}
		emit(id, probeEvent{Kind: "status", Text: "SSH connection lost — reconnecting…"})
		dropConn(userID, appliance.ID)
		newClient, rerr := acquireConn(userID, appliance)
		if rerr != nil {
			reconnMsg := fmt.Sprintf("[SSH DISCONNECTED — reconnect failed: %v. Stop issuing SSH commands; the session must be restarted.]", rerr)
			emit(id, probeEvent{Kind: "error", Text: "Reconnect failed: " + rerr.Error()})
			return reconnMsg, nil
		}
		a.conn = newClient
		emit(id, probeEvent{Kind: "status", Text: "SSH reconnected — retrying command…"})
		return a.exec_command_ctx(ctx, cmd)
	}

	// sessionFailures collects commands that exited nonzero during this session.
	// Emitted as a summary before the final reply so the user can see what the agent
	// tried and couldn't complete.
	type sessionFailure struct {
		Cmd    string
		Reason string // first non-empty line of the output
	}
	var sessionFailures []sessionFailure

	const loopLimit      = 3
	// Failure budget removed — sessionFailures still collected for the
	// post-session summary, but no hard block on binary or global
	// failure counts. Loop/topic-exhaustion guards (LOOP DETECTED,
	// probeLoopSignalCount, probeTopicCount) handle runaway behavior;
	// command failures are signal for the model to interpret, not a
	// reason to short-circuit the worker.

	// cmdBinary returns the effective binary from a shell command string,
	// skipping sudo, env, nohup, and env-var assignments so that
	// "sudo mysql -u root" and "mysql -u root -p" both map to "mysql".
	cmdBinary := func(cmd string) string {
		skip := map[string]bool{"sudo": true, "env": true, "nohup": true, "nice": true, "time": true, "ionice": true}
		for _, f := range strings.Fields(cmd) {
			if strings.Contains(f, "=") {
				continue // env var assignment
			}
			if skip[f] {
				continue
			}
			if i := strings.LastIndex(f, "/"); i >= 0 {
				return f[i+1:]
			}
			return f
		}
		return cmd
	}

	// Probe-session shared state for loop / failure detection.
	// Previously these maps lived inside newRunTool() so each invocation
	// (each orchestrator delegation that spawned a worker session)
	// started with a fresh slate. That defeated cross-delegation loop
	// detection: orchestrator could re-delegate "query the database"
	// 10 times, each worker would run the same `mysql -e ...` once,
	// and the per-session loopLimit=3 check never fired across the
	// boundary. Lifting to probe-session scope means after 3 cumulative
	// runs of the same command — across ANY worker session in this
	// probe — the LOOP DETECTED message fires and forces the
	// orchestrator to pivot.
	//
	// Trade: phase-boundary "reset" semantics from before are gone.
	// If you ever want per-phase budgets, wire a reset hook from the
	// orchestrator side rather than reverting to per-tool maps.
	cmdCount := make(map[string]int)
	failCount := make(map[string]int)
	var failMu sync.Mutex

	// newRunTool returns a run_command tool wired to the probe-session
	// shared counters above. The tool struct itself is created fresh
	// per delegation (cheap); the counters persist across delegations.
	newRunTool := func() AgentToolDef {
		return AgentToolDef{
			Tool: Tool{
				Name:        "run_command",
				Description: "Execute a shell command on the remote Linux system via SSH and return combined stdout+stderr. Output is capped at 10,000 characters.",
				Parameters: map[string]ToolParam{
					"command": {Type: "string", Description: "The shell command to run on the remote host."},
				},
				Required: []string{"command"},
			},
			Handler: func(args map[string]any) (string, error) {
				cmd, _ := args["command"].(string)
				if cmd == "" {
					return "", fmt.Errorf("command is required")
				}
				cmdCount[cmd]++
				if cmdCount[cmd] > loopLimit {
					msg := fmt.Sprintf("[LOOP DETECTED] run_command(%q) has been called %d times in this session. Running it again will not produce a different result. Stop. Choose a different command, different arguments, or a different investigation strategy.", cmd, cmdCount[cmd]-1)
					emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Loop detected: %q (%dx)", cmd, cmdCount[cmd]-1)})
					return msg, nil
				}
				bin := cmdBinary(cmd)
				emit(id, probeEvent{Kind: "cmd", Text: cmd})
				destructive, reason := is_destructive(cmd)
				if destructive {
					// Check always-allow list before prompting.
					if udb != nil {
						var alwaysOK bool
						if udb.Get(alwaysAllowTable, cmd, &alwaysOK) && alwaysOK {
							emit(id, probeEvent{Kind: "status", Text: "Auto-allowed: " + cmd})
							destructive = false
						}
					}
				}
				if destructive {
					pendingCmds.Store(id, cmd)
					emit(id, probeEvent{Kind: "confirm", Text: cmd, Reason: reason})
					select {
					case allowed := <-confirm:
						if !allowed {
							emit(id, probeEvent{Kind: "status", Text: "Command denied."})
							return "", fmt.Errorf("command denied by user")
						}
					case <-time.After(5 * time.Minute):
						return "", fmt.Errorf("confirmation timed out")
					case <-ctx.Done():
						return "", ctx.Err()
					}
					pendingCmds.Delete(id)
				}
				result, err := sshExec(cmd)
				if stripANSI(result) != "" {
					emit(id, probeEvent{Kind: "output", Text: result})
				}
				termEcho(cmd, result)
				if strings.Contains(result, "[exit code ") {
					// Record nonzero exits for the failure summary surfaced
					// at session end. No hard block — the model decides
					// when to pivot off a failing approach based on the
					// error text.
					reason := result
					if nl := strings.Index(reason, "\n"); nl > 0 {
						reason = reason[:nl]
					}
					if len(reason) > 120 {
						reason = reason[:120] + "…"
					}
					sessionFailures = append(sessionFailures, sessionFailure{Cmd: cmd, Reason: reason})
					failMu.Lock()
					failCount[bin]++
					failMu.Unlock()
				} else {
					// Command succeeded — if this binary had prior failures this phase,
					// the agent just found a working approach after trying multiple things.
					// Force it to record the technique NOW before moving on.
					failMu.Lock()
					priorFails := failCount[bin]
					failMu.Unlock()
					if priorFails > 0 {
						result += fmt.Sprintf("\n\n[TECHNIQUE FOUND] '%s' succeeded after %d failure(s) this phase. You MUST call record_technique NOW with the exact working command and why it worked, before doing anything else. Do not skip this step.", bin, priorFails)
					}
				}
				return result, err
			},
			NeedsConfirm: false,
		}
	}

	// read_log — safe, targeted log reader.
	read_log_tool := AgentToolDef{
		Tool: Tool{
			Name:        "read_log",
			Description: "Read the last N lines from a log file on the remote system, with optional grep filter. Safer and faster than run_command for log inspection.",
			Parameters: map[string]ToolParam{
				"path":   {Type: "string", Description: "Absolute path to the log file."},
				"lines":  {Type: "integer", Description: "Number of lines to read from the end (default 100, max 500)."},
				"filter": {Type: "string", Description: "Optional grep pattern to filter output (case-insensitive)."},
			},
			Required: []string{"path"},
		},
		Handler: func(args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}
			lines := 100
			if n, ok := args["lines"].(float64); ok && n > 0 {
				lines = int(n)
				if lines > 500 {
					lines = 500
				}
			}
			filter, _ := args["filter"].(string)

			var label string
			var cmd string
			if filter != "" {
				label = fmt.Sprintf("read_log %s (last %d lines, filter: %s)", path, lines, filter)
				cmd = fmt.Sprintf("tail -n %d %s 2>/dev/null | grep -i %s 2>/dev/null", lines, shellQuote(path), shellQuote(filter))
			} else {
				label = fmt.Sprintf("read_log %s (last %d lines)", path, lines)
				cmd = fmt.Sprintf("tail -n %d %s 2>/dev/null", lines, shellQuote(path))
			}
			emit(id, probeEvent{Kind: "cmd", Text: label})
			result, err := sshExec(cmd)
			if stripANSI(result) != "" {
				emit(id, probeEvent{Kind: "output", Text: result})
			}
			termEcho(label, result)
			return result, err
		},
		NeedsConfirm: false,
	}

	// search_logs — cross-file pattern search.
	search_logs_tool := AgentToolDef{
		Tool: Tool{
			Name:        "search_logs",
			Description: "Search one or more log files for a pattern. Returns matching lines with surrounding context.",
			Parameters: map[string]ToolParam{
				"pattern": {Type: "string", Description: "grep pattern to search for (case-insensitive)."},
				"paths": {
					Type:        "array",
					Description: "List of absolute log file paths to search. If empty, searches /var/log/ recursively.",
					Items:       &ToolParam{Type: "string"},
				},
				"context_lines": {Type: "integer", Description: "Lines of context around each match (default 2, max 5)."},
			},
			Required: []string{"pattern"},
		},
		Handler: func(args map[string]any) (string, error) {
			pattern, _ := args["pattern"].(string)
			if pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}
			ctx_lines := 2
			if n, ok := args["context_lines"].(float64); ok && n >= 0 {
				ctx_lines = int(n)
				if ctx_lines > 5 {
					ctx_lines = 5
				}
			}
			var pathArgs string
			if raw, ok := args["paths"]; ok {
				if arr, ok := raw.([]any); ok && len(arr) > 0 {
					var parts []string
					for _, p := range arr {
						if s, ok := p.(string); ok && s != "" {
							parts = append(parts, shellQuote(s))
						}
					}
					pathArgs = strings.Join(parts, " ")
				}
			}
			if pathArgs == "" {
				pathArgs = "/var/log/"
			}

			label := fmt.Sprintf("search_logs %q in %s", pattern, pathArgs)
			cmd := fmt.Sprintf("grep -r -i -C %d %s %s 2>/dev/null | head -300",
				ctx_lines, shellQuote(pattern), pathArgs)
			emit(id, probeEvent{Kind: "cmd", Text: label})
			result, err := sshExec(cmd)
			if stripANSI(result) != "" {
				emit(id, probeEvent{Kind: "output", Text: result})
			}
			termEcho(label, result)
			return result, err
		},
		NeedsConfirm: false,
	}

	// Probe-session shared PTY loop counter — same rationale as
	// cmdCount above. Per-tool isolation defeated cross-delegation
	// detection.
	ptyCount := make(map[string]int)

	// newRunPtyTool returns a run_pty tool wired to the probe-session
	// shared ptyCount. Tool struct cheap to recreate; counter persists.
	newRunPtyTool := func() AgentToolDef {
		return AgentToolDef{
		Tool: Tool{
			Name:        "run_pty",
			Description: "Run a command via a PTY (pseudo-terminal) on the remote system. Use this for commands that require a TTY: password prompts (su, sudo, mysql -p), interactive programs (python3, irb, psql), or anything that checks isatty(). Output is captured with ANSI codes stripped. Provide the 'input' parameter to send responses to prompts (newline-separated).",
			Parameters: map[string]ToolParam{
				"command":     {Type: "string", Description: "The command to run on the remote host."},
				"input":       {Type: "string", Description: "Optional lines to send to stdin after the command starts (newline-separated). Use for passwords, menu selections, shell commands inside an interactive session, etc."},
				"timeout_sec": {Type: "integer", Description: "Seconds to wait for the command to finish (default 15, max 60)."},
			},
			Required: []string{"command"},
		},
		Handler: func(args map[string]any) (string, error) {
			cmd, _ := args["command"].(string)
			if cmd == "" {
				return "", fmt.Errorf("command is required")
			}
			ptyCount[cmd]++
			if ptyCount[cmd] > loopLimit {
				msg := fmt.Sprintf("[LOOP DETECTED] run_pty(%q) has been called %d times in this session. Stop. Use a different command or approach.", cmd, ptyCount[cmd]-1)
				emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Loop detected (pty): %q (%dx)", cmd, ptyCount[cmd]-1)})
				return msg, nil
			}
			inputText, _ := args["input"].(string)
			timeout := 15
			if t, ok := args["timeout_sec"].(float64); ok && t > 0 {
				timeout = int(t)
				if timeout > 60 {
					timeout = 60
				}
			}

			emit(id, probeEvent{Kind: "cmd", Text: "pty: " + cmd})

			sess, err := a.conn.NewSession()
			if err != nil {
				// Attempt reconnect on connection-level errors.
				errMsg := err.Error()
				isConnErr := strings.Contains(errMsg, "EOF") ||
					strings.Contains(errMsg, "connection reset") ||
					strings.Contains(errMsg, "broken pipe") ||
					strings.Contains(errMsg, "new SSH session")
				if isConnErr {
					emit(id, probeEvent{Kind: "status", Text: "SSH connection lost — reconnecting…"})
					dropConn(userID, appliance.ID)
					newClient, rerr := acquireConn(userID, appliance)
					if rerr != nil {
						return fmt.Sprintf("[SSH DISCONNECTED — reconnect failed: %v. Stop issuing SSH commands; the session must be restarted.]", rerr), nil
					}
					a.conn = newClient
					emit(id, probeEvent{Kind: "status", Text: "SSH reconnected."})
					sess, err = a.conn.NewSession()
				}
				if err != nil {
					return "", fmt.Errorf("new SSH session: %w", err)
				}
			}
			defer sess.Close()

			modes := ssh.TerminalModes{
				ssh.ECHO:          0,
				ssh.TTY_OP_ISPEED: 14400,
				ssh.TTY_OP_OSPEED: 14400,
			}
			if err := sess.RequestPty("xterm", 50, 220, modes); err != nil {
				return "", fmt.Errorf("PTY request failed: %w", err)
			}

			stdinPipe, err := sess.StdinPipe()
			if err != nil {
				return "", fmt.Errorf("stdin pipe: %w", err)
			}

			var outBuf bytes.Buffer
			sess.Stdout = &outBuf
			sess.Stderr = &outBuf

			if err := sess.Start(cmd); err != nil {
				return "", fmt.Errorf("start: %w", err)
			}

			// Send input lines with a short delay between each to let prompts appear.
			if inputText != "" {
				time.Sleep(400 * time.Millisecond)
				for _, line := range strings.Split(inputText, "\n") {
					fmt.Fprintln(stdinPipe, line)
					time.Sleep(200 * time.Millisecond)
				}
			}

			done := make(chan error, 1)
			go func() { done <- sess.Wait() }()

			select {
			case <-done:
			case <-time.After(time.Duration(timeout) * time.Second):
				stdinPipe.Write([]byte{3}) // Ctrl+C
				time.Sleep(200 * time.Millisecond)
				stdinPipe.Write([]byte{4}) // Ctrl+D
				select {
				case <-done:
				case <-time.After(2 * time.Second):
				}
			case <-ctx.Done():
				sess.Close()
				return "", ctx.Err()
			}
			stdinPipe.Close()

			result := stripANSI(outBuf.String())
			if len(result) > max_output {
				result = result[:max_output] + fmt.Sprintf("\n... [truncated — %d chars total]", len(result))
			}
			if result != "" {
				emit(id, probeEvent{Kind: "output", Text: result})
			}
			termEcho("pty: "+cmd, result)
			// If this PTY session succeeded after prior attempts, force technique recording.
			if ptyCount[cmd] > 1 {
				result += fmt.Sprintf("\n\n[TECHNIQUE FOUND] run_pty(%q) succeeded after %d attempt(s). You MUST call record_technique NOW with the exact working command and input sequence, before doing anything else.", cmd, ptyCount[cmd]-1)
			}
			return result, nil
		},
		NeedsConfirm: false,
		}
	}

	// withFreshRunTool clones a tools slice, replacing run_command and run_pty
	// entries with fresh instances so each invocation gets isolated counters.
	withFreshRunTool := func(base []AgentToolDef) []AgentToolDef {
		result := make([]AgentToolDef, len(base))
		copy(result, base)
		for i, t := range result {
			switch t.Tool.Name {
			case "run_command":
				result[i] = newRunTool()
			case "run_pty":
				result[i] = newRunPtyTool()
			}
		}
		return result
	}

	// note_lesson — append a correction or lesson to the persistent notes for this appliance.
	note_lesson_tool := AgentToolDef{
		Tool: Tool{
			Name:        "note_lesson",
			Description: "Append a lesson or correction to the persistent notes for this appliance. Call this after discovering a mistake, a wrong assumption, or a non-obvious quirk about this system (e.g. 'sudo is not installed', 'mysql uses socket /tmp/mysql.sock not /var/run', 'journalctl requires sudo'). Notes are re-injected into every future session so the same mistake is not repeated.",
			Parameters: map[string]ToolParam{
				"note": {Type: "string", Description: "The lesson to record. Be concise and specific — one sentence per call."},
			},
			Required: []string{"note"},
		},
		Handler: func(args map[string]any) (string, error) {
			note, _ := args["note"].(string)
			if note == "" {
				return "", fmt.Errorf("note is required")
			}
			if udb == nil {
				return "", fmt.Errorf("no database")
			}
			var existing string
			udb.Get(notesTable, appliance.ID, &existing)
			entry := fmt.Sprintf("- %s (%s)\n", note, time.Now().Format("2006-01-02"))
			udb.Set(notesTable, appliance.ID, existing+entry)
			emit(id, probeEvent{Kind: "status", Text: "Noted: " + note})
			return "noted", nil
		},
		NeedsConfirm: false,
	}

	// record_technique — save a successful approach for future sessions.
	record_technique_tool := AgentToolDef{
		Tool: Tool{
			Name: "record_technique",
			Description: "Record a technique that worked on this system — a successful approach, correct command syntax, " +
				"working auth method, or non-obvious way to accomplish something. " +
				"Call this whenever you figure out HOW to do something that wasn't obvious: " +
				"e.g. 'MySQL root login works without a password via unix socket: mysql -u root', " +
				"'PostgreSQL uses peer auth — connect as postgres user: sudo -u postgres psql', " +
				"'Redis requires AUTH token found in /etc/redis/redis.conf', " +
				"'Python app uses venv at /opt/app/venv/bin/python'. " +
				"Techniques are injected at the start of every future session so you know exactly how to access things. " +
				"DATABASE AUTH IS MANDATORY: the moment any database login succeeds, record_technique MUST be called with the exact working command — this prevents re-discovery on every future session.",
			Parameters: map[string]ToolParam{
				"technique": {Type: "string", Description: "Concise description of what works and exactly how. Include the specific command or path."},
			},
			Required: []string{"technique"},
		},
		Handler: func(args map[string]any) (string, error) {
			technique, _ := args["technique"].(string)
			if technique == "" {
				return "", fmt.Errorf("technique is required")
			}
			if udb == nil {
				return "", fmt.Errorf("no database")
			}
			// Before appending, audit existing techniques against the new one.
			// If any existing entry covers the same topic but with outdated or
			// contradicted information, remove it so stale knowledge doesn't linger.
			if existing := techniquesFor(udb, appliance.ID); existing != "" {
				auditPrompt := "You are auditing a list of stored techniques for a specific system. " +
					"A new technique is about to be added. Identify any existing techniques that the new one " +
					"supersedes, contradicts, or makes redundant (e.g. an old auth method that is now wrong, " +
					"a path that has changed, an approach that the new one replaces). " +
					"Reply with ONLY the exact lines to remove, one per line. " +
					"If nothing should be removed, reply with exactly: NONE"
				auditMsg := fmt.Sprintf("Existing techniques:\n%s\n\nNew technique being added:\n- %s", existing, technique)
				auditResp, auditErr := a.WorkerChat(ctx, []Message{{Role: "user", Content: auditMsg}},
					WithSystemPrompt(auditPrompt), WithMaxTokens(512))
				if auditErr == nil && auditResp != nil {
					removal := strings.TrimSpace(auditResp.Content)
					if removal != "" && removal != "NONE" {
						pruned := existing
						for _, line := range strings.Split(removal, "\n") {
							line = strings.TrimSpace(line)
							if line != "" {
								pruned = strings.ReplaceAll(pruned, line+"\n", "")
								pruned = strings.ReplaceAll(pruned, line, "")
							}
						}
						udb.Set(techniquesTable, appliance.ID, strings.TrimSpace(pruned))
					}
				}
			}
			recordTechnique(udb, appliance.ID, technique)
			emit(id, probeEvent{Kind: "status", Text: "Technique saved: " + technique})
			return "technique recorded", nil
		},
		NeedsConfirm: false,
	}

	// record_discovery — capture a key breakthrough finding.
	record_discovery_tool := AgentToolDef{
		Tool: Tool{
			Name: "record_discovery",
			Description: "Record a key breakthrough that directly solves a goal or constitutes a major finding. " +
				"Call this when you: successfully authenticated to a database or service and confirmed what's inside, " +
				"fully traced a request routing chain, found credentials or secrets that unlock further access, " +
				"identified how the application accesses a resource (DB driver, ORM setup, connection method), " +
				"or confirmed any significant security or architectural finding. " +
				"Discoveries are surfaced at the TOP of every future session as pre-established knowledge — " +
				"anything recorded here will not be re-investigated. " +
				"This is NOT for routine facts or techniques. Only call it when you have answered a significant goal with real evidence. " +
				"DATABASE ACCESS: when you successfully enter a database and see its schemas/tables, call record_discovery with the full access path, credentials, and what you found inside.",
			Parameters: map[string]ToolParam{
				"title":    {Type: "string", Description: "One-line summary, e.g. 'Production PostgreSQL access confirmed' or 'Full request routing chain mapped'."},
				"finding":  {Type: "string", Description: "Full narrative: what you found, where, exact values (credentials, paths, ports, schema names, route patterns), and why it matters. Include the evidence — commands run and their output."},
				"category": {Type: "string", Description: "One of: database | credentials | routing | service | code | security | config | general"},
			},
			Required: []string{"title", "finding"},
		},
		Handler: func(args map[string]any) (string, error) {
			title, _ := args["title"].(string)
			finding, _ := args["finding"].(string)
			category, _ := args["category"].(string)
			if strings.TrimSpace(title) == "" || strings.TrimSpace(finding) == "" {
				return "", fmt.Errorf("title and finding are required")
			}
			if udb == nil {
				return "", fmt.Errorf("no database")
			}
			storeDiscovery(udb, appliance.ID, title, finding, category)
			emit(id, probeEvent{Kind: "discovery", Text: "★ " + strings.TrimSpace(title)})
			return "discovery recorded", nil
		},
		NeedsConfirm: false,
	}

	// store_fact — persist a discrete observation about this appliance.
	store_fact_tool := AgentToolDef{
		Tool: Tool{
			Name:        "store_fact",
			Description: "Save a fact you just discovered so future sessions can answer the same question without running SSH commands again. Call this immediately after any command returns a concrete value: a version, a port, a path, a config value, a service status, a username. Key should be short and descriptive (e.g. 'nginx_version', 'mysql_port', 'web_root'). Facts overwrite on the same key so re-mapping keeps them current. For database auth methods use standard keys: 'mysql_auth', 'postgres_auth', 'redis_auth', 'mongo_auth' — value should be the exact working command. Set ttl='short' for volatile facts (who is logged in, running services, open ports, disk usage); use the default 'long' for stable facts (versions, config paths, hardware).",
			Parameters: map[string]ToolParam{
				"key":   {Type: "string", Description: "Short descriptive key, e.g. 'mysql_version', 'admin_user', 'web_root'."},
				"value": {Type: "string", Description: "The fact value."},
				"ttl":   {Type: "string", Description: "Freshness window: 'short' (re-verify after 30 min, for volatile state) or 'long' (trust for 24h, for stable config/versions). Default: 'long'."},
				"tags": {
					Type:        "array",
					Description: "Optional labels for cross-appliance search, e.g. 'database', 'security', 'network'.",
					Items:       &ToolParam{Type: "string"},
				},
			},
			Required: []string{"key", "value"},
		},
		Handler: func(args map[string]any) (string, error) {
			key, _ := args["key"].(string)
			value, _ := args["value"].(string)
			if key == "" || value == "" {
				return "", fmt.Errorf("key and value are required")
			}
			ttl, _ := args["ttl"].(string)
			var tags []string
			if raw, ok := args["tags"]; ok {
				if arr, ok := raw.([]any); ok {
					for _, t := range arr {
						if s, ok := t.(string); ok && s != "" {
							tags = append(tags, s)
						}
					}
				}
			}
			storeFact(udb, appliance.ID, appliance.Name, key, value, ttl, tags)
			emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Stored fact: %s = %s", key, value)})
			return "fact stored", nil
		},
		NeedsConfirm: false,
	}

	// store_rule — persist a standing instruction the user has established.
	store_rule_tool := AgentToolDef{
		Tool: Tool{
			Name:        "store_rule",
			Description: "Save a standing instruction or preference the user has expressed about how to work with this system. Call this when the user states a rule, preference, or convention they want followed in all future sessions — e.g. 'always check staging before production', 'never restart the web server without warning', 'use sudo for all service commands'. Rules persist across sessions and are injected into every future prompt.",
			Parameters: map[string]ToolParam{
				"rule": {Type: "string", Description: "The standing instruction to remember, written as a clear directive."},
			},
			Required: []string{"rule"},
		},
		Handler: func(args map[string]any) (string, error) {
			rule, _ := args["rule"].(string)
			if strings.TrimSpace(rule) == "" {
				return "", fmt.Errorf("rule is required")
			}
			if udb == nil {
				return "", fmt.Errorf("no database")
			}
			storeRule(udb, appliance.ID, rule)
			preview := rule
			if len(preview) > 80 {
				preview = preview[:80] + "…"
			}
			emit(id, probeEvent{Kind: "status", Text: "Rule saved: " + preview})
			return "rule saved", nil
		},
	}

	// count_lines — check file size before deciding how to read it.
	count_lines_tool := AgentToolDef{
		Tool: Tool{
			Name:        "count_lines",
			Description: "Return the total number of lines in a file on the remote system. Use this before read_range or before catting a file to know whether it will fit in one read or needs pagination.",
			Parameters: map[string]ToolParam{
				"path": {Type: "string", Description: "Absolute path to the file."},
			},
			Required: []string{"path"},
		},
		Handler: func(args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}
			emit(id, probeEvent{Kind: "cmd", Text: "count_lines " + path})
			result, err := sshExec(fmt.Sprintf("wc -l %s 2>/dev/null", shellQuote(path)))
			termEcho("wc -l "+path, result)
			return result, err
		},
		NeedsConfirm: false,
	}

	// read_range — read a specific line range from a file; avoids re-running expensive commands.
	read_range_tool := AgentToolDef{
		Tool: Tool{
			Name: "read_range",
			Description: "Read a specific range of lines from a file on the remote system. " +
				"Use this to page through large files without re-running the original command. " +
				"Call count_lines first to know total line count, then page through in chunks up to 300 lines at a time.",
			Parameters: map[string]ToolParam{
				"path":       {Type: "string", Description: "Absolute path to the file."},
				"start_line": {Type: "integer", Description: "First line to return (1-indexed)."},
				"end_line":   {Type: "integer", Description: "Last line to return (inclusive). Maximum 300 lines per call."},
			},
			Required: []string{"path", "start_line", "end_line"},
		},
		Handler: func(args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}
			start := 1
			if v, ok := args["start_line"].(float64); ok && v >= 1 {
				start = int(v)
			}
			end := start + 99
			if v, ok := args["end_line"].(float64); ok && v >= 1 {
				end = int(v)
			}
			if end-start > 299 {
				end = start + 299
			}
			if end < start {
				end = start
			}
			label := fmt.Sprintf("read_range %s lines %d–%d", path, start, end)
			cmd := fmt.Sprintf("awk 'NR>=%d && NR<=%d' %s 2>/dev/null", start, end, shellQuote(path))
			emit(id, probeEvent{Kind: "cmd", Text: label})
			result, err := sshExec(cmd)
			if stripANSI(result) != "" {
				emit(id, probeEvent{Kind: "output", Text: result})
			}
			termEcho(label, result)
			return result, err
		},
		NeedsConfirm: false,
	}

	// search_facts — retrieve facts from the persistent knowledge base.
	search_facts_tool := AgentToolDef{
		Tool: Tool{
			Name:        "search_facts",
			Description: "Search stored facts across all appliances by keyword. Checks fact keys, values, and tags. Call this before running SSH commands — the answer may already be in persistent memory.",
			Parameters: map[string]ToolParam{
				"query":     {Type: "string", Description: "Substring to search in fact keys, values, and tags."},
				"appliance": {Type: "string", Description: "Optional appliance name or ID filter."},
			},
			Required: []string{"query"},
		},
		Handler: func(args map[string]any) (string, error) {
			query, _ := args["query"].(string)
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			appFilter, _ := args["appliance"].(string)
			results := searchFacts(udb, query, appFilter)
			emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("search_facts %q: %d result(s)", query, len(results))})
			if len(results) == 0 {
				return "no facts found", nil
			}
			return formatFacts(results), nil
		},
		NeedsConfirm: false,
	}

	// Build the worker base prompt and inject what the worker needs directly.
	workerPrompt := func() string {
		if appliance.Type == "command" {
			return buildCommandChatSystemPrompt(appliance, udb)
		}
		if saveProfile {
			return buildMapSystemPrompt(T.SystemPrompt(), appliance)
		}
		return buildChatSystemPrompt(appliance)
	}()

	var cachedFacts, cachedNotes, cachedTechniques, cachedRules, cachedDiscoveries string
	if udb != nil {
		now := time.Now()
		if disc := discoveriesFor(udb, appliance.ID); len(disc) > 0 {
			cachedDiscoveries = formatDiscoveries(disc)
			workerPrompt += "\n\n## Key Discoveries (pre-established — do not re-investigate)\n\n"
			workerPrompt += cachedDiscoveries + "\n"
		}
		if facts := factsForAppliance(udb, appliance.ID); len(facts) > 0 {
			cachedFacts = formatFactsWithAge(facts, now)
			workerPrompt += "\n\n## What We Already Know About This System\n\n"
			workerPrompt += fmt.Sprintf("Current time: %s\n\n", now.Format("2006-01-02 15:04 MST"))
			workerPrompt += "Facts are annotated with age. Re-run for anything about current state older than 30 minutes. " +
				"Trust config, versions, hardware, and paths for 24 hours.\n\n"
			workerPrompt += cachedFacts
		}
		var notes string
		if udb.Get(notesTable, appliance.ID, &notes) && strings.TrimSpace(notes) != "" {
			cachedNotes = strings.TrimSpace(notes)
			workerPrompt += "\n\n## Lessons Learned (do not repeat these)\n\n" + cachedNotes + "\n"
		}
		if t := techniquesFor(udb, appliance.ID); t != "" {
			cachedTechniques = t
			workerPrompt += "\n\n## Known Techniques (use directly — do not re-discover)\n\n" + cachedTechniques + "\n"
		}
		if rules := rulesForAppliance(udb, appliance.ID); len(rules) > 0 {
			cachedRules = formatRules(rules)
			workerPrompt += "\n\n## Standing Instructions (set by the user — always follow these)\n\n" + cachedRules + "\n"
		}
	}

	// watch_condition — register a 1-minute expect-style poll until a condition is met.
	watch_condition_tool := AgentToolDef{
		Tool: Tool{
			Name: "watch_condition",
			Description: "Register an expect-style watch: runs the given command every minute until " +
				"the output contains the success pattern, then stores the result. " +
				"Use this when you've started something that takes time — a backup, a service restart, " +
				"a migration — and want to know when it finishes without blocking. " +
				"The watch fires silently in the background; the result is in stored facts on next session.",
			Parameters: map[string]ToolParam{
				"task":            {Type: "string", Description: "What you are waiting for (human description)."},
				"command":         {Type: "string", Description: "SSH command to run each minute to check the condition."},
				"success_pattern": {Type: "string", Description: "Substring that must appear in command output for the condition to be considered met."},
				"timeout_minutes": {Type: "string", Description: "Give up after this many minutes if the condition never matches. Default 60."},
			},
			Required: []string{"task", "command", "success_pattern"},
		},
		Handler: func(args map[string]any) (string, error) {
			task, _ := args["task"].(string)
			command, _ := args["command"].(string)
			pattern, _ := args["success_pattern"].(string)
			if task == "" || command == "" || pattern == "" {
				return "", fmt.Errorf("task, command, and success_pattern are required")
			}
			timeoutMin := 60
			if s, _ := args["timeout_minutes"].(string); s != "" {
				var n int
				if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
					timeoutMin = n
				}
			}
			now := time.Now()
			w := ScheduledWatch{
				ID:          UUIDv4(),
				ApplianceID: appliance.ID,
				UserID:      userID,
				Task:        task,
				Command:     command,
				Pattern:     pattern,
				TimeoutAt:   now.Add(time.Duration(timeoutMin) * time.Minute).Format(time.RFC3339),
				NextRunAt:   now.Add(60 * time.Second).Format(time.RFC3339),
				Created:     now.Format(time.RFC3339),
			}
			storeWatch(T.DB, w)
			emit(id, probeEvent{Kind: "watch", Text: fmt.Sprintf("Watching: %s (every 60s, up to %d min)", task, timeoutMin)})
			return fmt.Sprintf("Watch registered (id: %s). Will check every 60 seconds for up to %d minutes for pattern %q in: %s", w.ID[:8], timeoutMin, pattern, command), nil
		},
		NeedsConfirm: false,
	}

	// list_watches — show active watches for this appliance.
	list_watches_tool := AgentToolDef{
		Tool: Tool{
			Name:        "list_watches",
			Description: "List active watches registered for this appliance.",
			Parameters:  map[string]ToolParam{},
		},
		Handler: func(args map[string]any) (string, error) {
			watches := listWatchesForAppliance(T.DB, appliance.ID)
			if len(watches) == 0 {
				return "No active watches.", nil
			}
			var b strings.Builder
			for _, w := range watches {
				b.WriteString(fmt.Sprintf("- [%s] %s — checking: %s (pattern: %q, timeout: %s)\n",
					w.ID[:8], w.Task, w.Command, w.Pattern, w.TimeoutAt))
			}
			return b.String(), nil
		},
		NeedsConfirm: false,
	}

	save_to_codewriter_tool := AgentToolDef{
		Tool: Tool{
			Name:        "save_to_codewriter",
			Description: "Save a SQL query, shell script, or code snippet to the user's CodeWriter library in gohort. This is a local save action — do NOT run anything on the appliance. Use this when the user asks to save the script/query for later reuse rather than (or in addition to) running it immediately.",
			Parameters: map[string]ToolParam{
				"name": {Type: "string", Description: "Short descriptive name for the snippet (e.g. 'Active connections by database')."},
				"lang": {Type: "string", Description: "Language or type: 'sql', 'bash', 'python', 'go', 'javascript', 'text', etc."},
				"code": {Type: "string", Description: "The full script or query text to save."},
			},
			Required: []string{"name", "lang", "code"},
		},
		Handler: func(args map[string]any) (string, error) {
			if SaveSnippetFunc == nil {
				return "", fmt.Errorf("CodeWriter is not available")
			}
			name, _ := args["name"].(string)
			lang, _ := args["lang"].(string)
			code, _ := args["code"].(string)
			if name == "" || code == "" {
				return "", fmt.Errorf("name and code are required")
			}
			id, err := SaveSnippetFunc(userID, name, lang, code)
			if err != nil {
				return "", fmt.Errorf("save failed: %w", err)
			}
			return fmt.Sprintf("Saved to CodeWriter as %q (id: %s).", name, id), nil
		},
		NeedsConfirm: false,
	}

	save_to_techwriter_tool := AgentToolDef{
		Tool: Tool{
			Name:        "save_to_techwriter",
			Description: "Save a report, runbook, findings summary, or any prose document to the user's TechWriter library in gohort. This is a local save action — do NOT run anything on the appliance or search for TechWriter on the remote system. Use this when the user asks to document findings, save a report, or create a runbook from the session results.",
			Parameters: map[string]ToolParam{
				"subject": {Type: "string", Description: "Title or subject of the document (e.g. 'Disk usage report – web01', 'MySQL slow query runbook')."},
				"body":    {Type: "string", Description: "Full document body in markdown."},
			},
			Required: []string{"subject", "body"},
		},
		Handler: func(args map[string]any) (string, error) {
			if SaveArticleFunc == nil {
				return "", fmt.Errorf("TechWriter is not available")
			}
			subject, _ := args["subject"].(string)
			body, _ := args["body"].(string)
			if subject == "" || body == "" {
				return "", fmt.Errorf("subject and body are required")
			}
			id, err := SaveArticleFunc(userID, subject, body)
			if err != nil {
				return "", fmt.Errorf("save failed: %w", err)
			}
			return fmt.Sprintf("Saved to TechWriter as %q (id: %s).", subject, id), nil
		},
		NeedsConfirm: false,
	}

	// workerTools holds a placeholder run_command entry. Every call site must use
	// withFreshRunTool(workerTools) so each invocation gets isolated counters.
	var workerTools []AgentToolDef
	if appliance.Type == "command" {
		workerTools = []AgentToolDef{
			newRunTool(), read_log_tool, search_logs_tool,
			note_lesson_tool, record_technique_tool, record_discovery_tool, store_fact_tool, store_rule_tool, search_facts_tool,
			count_lines_tool, read_range_tool, save_to_codewriter_tool, save_to_techwriter_tool,
		}
	} else {
		workerTools = []AgentToolDef{
			newRunTool(), read_log_tool, search_logs_tool, newRunPtyTool(),
			note_lesson_tool, record_technique_tool, record_discovery_tool, store_fact_tool, store_rule_tool, search_facts_tool,
			count_lines_tool, read_range_tool,
			watch_condition_tool, list_watches_tool, save_to_codewriter_tool, save_to_techwriter_tool,
		}
	}
	// Enforced sanity check — servitor handles sensitive system data and
	// must never call out to third-party services. assertOnlyAllowedTools
	// panics if anything outside the local-only allow-list sneaks in.
	assertOnlyAllowedTools("servitor.worker", workerTools, servitorWorkerToolAllowList)

	var reply string
	var consolidateFn func() // set by chat mode, fired after reply is emitted

	if saveProfile {
		if appliance.Profile == "" {
			// === New investigator-driven mapping ===
			//
			// Phase 1: Quick snapshot (no LLM) — gives the investigator a starting point.
			emit(id, probeEvent{Kind: "status", Text: "Taking system snapshot…"})
			snapshot := runQuickSnapshot(ctx, sshExec)
			if ctx.Err() != nil {
				return
			}
			if snapshot != "" {
				emit(id, probeEvent{Kind: "output", Text: "## System Snapshot\n\n" + snapshot})
			}

			// Phase 2: Investigator loop — the investigator decides what to probe,
			// follows leads, and records discoveries. Workers execute specific tasks.
			//
			// probeCache: results keyed by aggressively-normalized task (sorted
			// content tokens, stop words dropped) so paraphrased re-delegations
			// hit the cache. "list mysql tables" and "show tables in mysql"
			// normalize to the same key.
			//
			// probeTopicCount: tracks how many times the orchestrator has
			// delegated tasks sharing a content-token set with prior tasks.
			// When the same topic gets tried 3+ ways, escalate the response
			// from "[ALREADY PROBED]" to "[ENOUGH — pivot to a different
			// topic entirely]" so the orchestrator stops grinding the same
			// area through paraphrase.
			probeCache := make(map[string]string)
			probeTopicCount := make(map[string]int)
			const probeTopicLimit = 3

			// plan: session-scoped investigation plan. The investigator's
			// system prompt requires set_plan as the first tool call;
			// subsequent step lifecycle tools (mark_step_in_progress,
			// record_step_findings, mark_step_blocked) operate on this
			// state. Plan events stream to the UI for the left-pane
			// checklist render.
			plan := &Plan{}
			set_plan_tool := AgentToolDef{
				Tool: Tool{
					Name: "set_plan",
					Description: "REQUIRED FIRST CALL — emit a structured investigation plan before any other tool. " +
						"Each step has a short title (5–10 words) and a what_to_find description (1–3 sentences) explaining what success looks like for that step. " +
						"Order steps by dependency: foundation/discovery first, then deeper investigation that builds on it. " +
						"Typically 5–12 steps for a generic system; scale up to 15+ for complex appliances (Kubernetes hosts, multi-tenant DB servers, hosts running many distinct services). " +
						"Err toward more steps with narrower scopes rather than fewer steps with sprawling scopes — narrow steps produce sharper findings and clearer gap reports. " +
						"You can revise step status as you go, but the initial plan is your contract — use revise_plan only if findings reveal a step you couldn't have known to include.",
					Parameters: map[string]ToolParam{
						"steps": {
							Type:        "array",
							Description: "Ordered list of plan steps. Each item must be an object with 'title' (string) and 'what_to_find' (string).",
						},
					},
					Required: []string{"steps"},
				},
				Handler: func(args map[string]any) (string, error) {
					if plan.IsSet() {
						return "[PLAN ALREADY SET] You may revise specific steps via mark_step_in_progress / record_step_findings / mark_step_blocked. To replace the entire plan, that's not currently supported in this phase.", nil
					}
					raw, ok := args["steps"].([]any)
					if !ok || len(raw) == 0 {
						return "", fmt.Errorf("steps must be a non-empty array of {title, what_to_find}")
					}
					var titles, finds []string
					for i, s := range raw {
						m, ok := s.(map[string]any)
						if !ok {
							return "", fmt.Errorf("step %d: must be an object with 'title' and 'what_to_find'", i+1)
						}
						title, _ := m["title"].(string)
						find, _ := m["what_to_find"].(string)
						if strings.TrimSpace(title) == "" {
							return "", fmt.Errorf("step %d: 'title' is required", i+1)
						}
						if strings.TrimSpace(find) == "" {
							return "", fmt.Errorf("step %d: 'what_to_find' is required", i+1)
						}
						titles = append(titles, strings.TrimSpace(title))
						finds = append(finds, strings.TrimSpace(find))
					}
					if err := plan.SetSteps(titles, finds); err != nil {
						return "", err
					}
					emit(id, probeEvent{Kind: "plan_set", Plan: plan.Snapshot()})
					return fmt.Sprintf("Plan set with %d steps. Begin step 1: mark_step_in_progress with id=1, then probe to investigate, then record_step_findings or mark_step_blocked.", len(titles)), nil
				},
			}
			mark_step_in_progress_tool := AgentToolDef{
				Tool: Tool{
					Name:        "mark_step_in_progress",
					Description: "Mark a plan step as the one you're currently working on. Surfaces in the user-visible plan as the active step. Call before delegating probe(s) for that step.",
					Parameters: map[string]ToolParam{
						"step_id": {Type: "integer", Description: "The step ID to mark in_progress (from set_plan)."},
					},
					Required: []string{"step_id"},
				},
				Handler: func(args map[string]any) (string, error) {
					if !plan.IsSet() {
						return "[NO PLAN] Call set_plan first.", nil
					}
					stepID, _ := toInt(args["step_id"])
					if err := plan.SetStatus(stepID, PlanStepInProgress); err != nil {
						return "", err
					}
					emit(id, probeEvent{Kind: "plan_step", Plan: plan.Snapshot()})
					return fmt.Sprintf("Step %d marked in_progress. Delegate probe(s) to investigate.", stepID), nil
				},
			}
			record_step_findings_tool := AgentToolDef{
				Tool: Tool{
					Name:        "record_step_findings",
					Description: "Attach findings to a plan step and mark it done. Findings should be a 1–3 sentence summary of what was learned for this step (NOT the raw worker output — your synthesis of it). Call after the probe(s) for this step return useful results.",
					Parameters: map[string]ToolParam{
						"step_id":  {Type: "integer", Description: "The step ID to record findings for."},
						"findings": {Type: "string", Description: "1–3 sentence summary of what was learned for this step."},
					},
					Required: []string{"step_id", "findings"},
				},
				Handler: func(args map[string]any) (string, error) {
					if !plan.IsSet() {
						return "[NO PLAN] Call set_plan first.", nil
					}
					stepID, _ := toInt(args["step_id"])
					findings, _ := args["findings"].(string)
					if strings.TrimSpace(findings) == "" {
						return "", fmt.Errorf("findings is required")
					}
					if err := plan.RecordFindings(stepID, strings.TrimSpace(findings)); err != nil {
						return "", err
					}
					emit(id, probeEvent{Kind: "plan_step", Plan: plan.Snapshot()})
					return fmt.Sprintf("Step %d marked done. Move to the next pending step or call mark_step_blocked if no more steps can be completed.", stepID), nil
				},
			}
			mark_step_blocked_tool := AgentToolDef{
				Tool: Tool{
					Name:        "mark_step_blocked",
					Description: "Mark a plan step as blocked when it can't be completed. Use for genuine obstacles: no access (permission denied), required tool missing on the system, target service not running and can't be started, etc. Do NOT use to hide difficulty — only when external constraints prevent the step. The reason will appear in the final report's gap section.",
					Parameters: map[string]ToolParam{
						"step_id": {Type: "integer", Description: "The step ID to mark blocked."},
						"reason":  {Type: "string", Description: "Why the step couldn't be completed (e.g. 'sudo not available', 'mysql user lacks SHOW GRANTS permission', 'logfile rotated, no archive)."},
					},
					Required: []string{"step_id", "reason"},
				},
				Handler: func(args map[string]any) (string, error) {
					if !plan.IsSet() {
						return "[NO PLAN] Call set_plan first.", nil
					}
					stepID, _ := toInt(args["step_id"])
					reason, _ := args["reason"].(string)
					if strings.TrimSpace(reason) == "" {
						return "", fmt.Errorf("reason is required")
					}
					if err := plan.MarkBlocked(stepID, strings.TrimSpace(reason)); err != nil {
						return "", err
					}
					emit(id, probeEvent{Kind: "plan_step", Plan: plan.Snapshot()})
					return fmt.Sprintf("Step %d marked blocked: %s. Move to the next pending step.", stepID, reason), nil
				},
			}
			revise_plan_tool := AgentToolDef{
				Tool: Tool{
					Name: "revise_plan",
					Description: fmt.Sprintf(
						"Revise the plan when findings reveal something you couldn't have known to plan for. Three operations, all optional and combinable: add (new steps appended with fresh IDs), remove (drop pending steps that are no longer relevant — done/blocked/in_progress steps are durable history and refused), reorder (full new ordering of remaining step IDs). Capped at %d revisions per session — use deliberately, not reflexively. The plan is your contract; revise only when reality contradicts the contract, not because you'd write it differently in hindsight.",
						PlanRevisionLimit,
					),
					Parameters: map[string]ToolParam{
						"add": {
							Type:        "array",
							Description: "Optional. New steps to append. Each item is an object with 'title' and 'what_to_find', same shape as set_plan.",
						},
						"remove": {
							Type:        "array",
							Description: "Optional. Step IDs (integers) to drop. Only pending steps can be removed.",
						},
						"reorder": {
							Type:        "array",
							Description: "Optional. Full new ordering of step IDs (integers). Must be a permutation of all remaining step IDs after add+remove are applied.",
						},
						"reason": {
							Type:        "string",
							Description: "Brief one-sentence explanation of why this revision is needed. Surfaced in the UI alongside the plan changes.",
						},
					},
					Required: []string{"reason"},
				},
				Handler: func(args map[string]any) (string, error) {
					if !plan.IsSet() {
						return "[NO PLAN] Call set_plan first.", nil
					}
					reason, _ := args["reason"].(string)
					if strings.TrimSpace(reason) == "" {
						return "", fmt.Errorf("reason is required to document the revision")
					}
					count, atCap := plan.IncrRevision()
					if atCap && count > PlanRevisionLimit {
						return fmt.Sprintf("[REVISION LIMIT REACHED] You have already revised the plan %d times this session, the maximum allowed. Work the existing plan to completion; if a step you wanted is missing, mark related blocked steps and call out the gap in your final report.", PlanRevisionLimit), nil
					}
					var feedback strings.Builder
					fmt.Fprintf(&feedback, "Revision %d/%d: %s\n", count, PlanRevisionLimit, reason)
					// Process remove first so reorder operates on the
					// final set, then add (appends), then reorder.
					if rawRem, ok := args["remove"].([]any); ok && len(rawRem) > 0 {
						var ids []int
						for _, v := range rawRem {
							if n, ok := toInt(v); ok {
								ids = append(ids, n)
							}
						}
						if len(ids) > 0 {
							removed, refused, _ := plan.RemoveSteps(ids)
							if len(removed) > 0 {
								fmt.Fprintf(&feedback, "Removed steps: %v\n", removed)
							}
							if len(refused) > 0 {
								fmt.Fprintf(&feedback, "Refused to remove (status not pending): %v\n", refused)
							}
						}
					}
					if rawAdd, ok := args["add"].([]any); ok && len(rawAdd) > 0 {
						var titles, finds []string
						for i, s := range rawAdd {
							m, ok := s.(map[string]any)
							if !ok {
								return "", fmt.Errorf("add[%d]: must be an object with 'title' and 'what_to_find'", i)
							}
							title, _ := m["title"].(string)
							find, _ := m["what_to_find"].(string)
							if strings.TrimSpace(title) == "" || strings.TrimSpace(find) == "" {
								return "", fmt.Errorf("add[%d]: title and what_to_find are both required", i)
							}
							titles = append(titles, strings.TrimSpace(title))
							finds = append(finds, strings.TrimSpace(find))
						}
						added, err := plan.AddSteps(titles, finds)
						if err != nil {
							return "", err
						}
						fmt.Fprintf(&feedback, "Added steps: %v\n", added)
					}
					if rawOrd, ok := args["reorder"].([]any); ok && len(rawOrd) > 0 {
						var order []int
						for _, v := range rawOrd {
							if n, ok := toInt(v); ok {
								order = append(order, n)
							}
						}
						if err := plan.ReorderSteps(order); err != nil {
							return "", err
						}
						feedback.WriteString("Reordered.\n")
					}
					emit(id, probeEvent{Kind: "plan_step", Plan: plan.Snapshot(), Reason: reason})
					return strings.TrimSpace(feedback.String()), nil
				},
			}
			report_gaps_tool := AgentToolDef{
				Tool: Tool{
					Name: "report_gaps",
					Description: "REQUIRED before you write your final answer. Returns a structured summary of every plan step that's blocked or never completed, plus the reasons. You MUST incorporate this into the final report under a clearly-labeled 'What I Couldn't Determine' section so the user sees the gaps explicitly rather than getting an overconfident report that quietly omits unverified things. The user trusts the report only if you're honest about what you couldn't see.",
					Parameters:  map[string]ToolParam{},
				},
				Handler: func(args map[string]any) (string, error) {
					if !plan.IsSet() {
						return "[NO PLAN] Call set_plan first.", nil
					}
					gaps := plan.MarkGapsReported()
					emit(id, probeEvent{Kind: "plan_step", Plan: plan.Snapshot()})
					if len(gaps.Blocked) == 0 && len(gaps.Skipped) == 0 {
						return "No gaps. Every plan step was completed with findings. Write your final answer now — no 'What I Couldn't Determine' section needed.", nil
					}
					var b strings.Builder
					b.WriteString("Gap report — incorporate the following into a 'What I Couldn't Determine' section in your final answer:\n\n")
					if len(gaps.Blocked) > 0 {
						b.WriteString("Blocked:\n")
						for _, g := range gaps.Blocked {
							fmt.Fprintf(&b, "  - %s (step %d): %s\n", g.Title, g.ID, g.Reason)
						}
					}
					if len(gaps.Skipped) > 0 {
						b.WriteString("\nNever completed:\n")
						for _, g := range gaps.Skipped {
							fmt.Fprintf(&b, "  - %s (step %d): %s\n", g.Title, g.ID, g.Reason)
						}
					}
					return strings.TrimSpace(b.String()), nil
				},
			}

			// probeLoopSignalCount tracks how many delegations have ended
			// with a worker [LOOP DETECTED] message. The orchestrator is
			// supposed to read these messages and pivot, but in practice
			// it often ignores them and re-delegates with paraphrased
			// task descriptions. Counting at the orchestrator's tool-call
			// boundary lets us refuse the (N+1)th delegation outright
			// once the orchestrator has demonstrated it's not reading the
			// signal — forcing model attention via tool-call refusal
			// instead of relying on prompt-level guidance.
			probeLoopSignalCount := 0
			const probeLoopSignalLimit = 3
			probe_tool := AgentToolDef{
				Tool: Tool{
					Name: "probe",
					Description: "Execute a specific SSH investigation task on the target system. " +
						"Be precise: 'show /etc/nginx/sites-enabled/myapp.conf and identify its upstream' not 'investigate nginx'. " +
						"Pass rich context so the worker uses what you already know without re-discovering it.",
					Parameters: map[string]ToolParam{
						"task": {
							Type:        "string",
							Description: "Single clear goal: find X, read Y, verify Z. One objective per probe.",
						},
						"context": {
							Type:        "string",
							Description: "What you know so far that's relevant: paths, ports, credentials, service names.",
						},
					},
					Required: []string{"task"},
				},
				Handler: func(args map[string]any) (string, error) {
					task, _ := args["task"].(string)
					if task == "" {
						return "", fmt.Errorf("task is required")
					}
					// Hard refusal: orchestrator has accumulated too many
					// worker [LOOP DETECTED] signals across prior delegations
					// without effectively pivoting. Refuse the new delegation
					// outright before spawning a worker — forces model
					// attention via tool-call rejection rather than relying
					// on the orchestrator to read [LOOP DETECTED] strings
					// it's been ignoring.
					if probeLoopSignalCount >= probeLoopSignalLimit {
						emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Probe refused: orchestrator hit loop-signal limit (%d signals)", probeLoopSignalCount)})
						return fmt.Sprintf("[DELEGATION REFUSED — your prior %d delegations have triggered worker LOOP DETECTED responses. Your current investigation strategy is not converging. STOP delegating new probes. Write your final report based on what you have already learned. Acknowledge what you could not determine and why. Do not call probe again in this session.]", probeLoopSignalCount), nil
					}
					cacheKey := normalizeTask(task)
					if cached, ok := probeCache[cacheKey]; ok {
						probeTopicCount[cacheKey]++
						if probeTopicCount[cacheKey] >= probeTopicLimit {
							emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Topic exhausted: %q (%dx)", task, probeTopicCount[cacheKey])})
							return fmt.Sprintf("[TOPIC EXHAUSTED — you have re-delegated this topic %d times now. Stop probing this area entirely. Pivot to a fundamentally different domain (different service, different layer, different angle on the original goal). Re-delegating the same topic in different words will not produce new information.]\n\nLast result for reference:\n\n%s", probeTopicCount[cacheKey], cached), nil
						}
						return "[ALREADY PROBED — result below. Do not probe this topic again; move to a different area.]\n\n" + cached, nil
					}
					context, _ := args["context"].(string)
					var msg strings.Builder
					if context != "" {
						msg.WriteString("## Known Context\n\n")
						msg.WriteString(context)
						msg.WriteString("\n\n")
					}
					if udb != nil {
						if t := techniquesFor(udb, appliance.ID); t != "" {
							msg.WriteString("## Known Techniques (use directly)\n\n")
							msg.WriteString(t)
							msg.WriteString("\n\n")
						}
						if facts := factsForAppliance(udb, appliance.ID); len(facts) > 0 {
							msg.WriteString("## Stored Facts\n\n")
							msg.WriteString(formatFacts(facts))
							msg.WriteString("\n\n")
						}
					}
					msg.WriteString("## Task\n\n")
					msg.WriteString(task)
					short := task
					if len(short) > 80 {
						short = short[:80] + "…"
					}
					emit(id, probeEvent{Kind: "intent", Text: task, Reason: context})
					var workerResp *Response
					var workerErr error
					withHeartbeat(ctx, id, "Probe: "+short, func() {
						workerResp, _, workerErr = a.RunAgentLoop(ctx,
							[]Message{{Role: "user", Content: msg.String()}},
							AgentLoopConfig{
								SystemPrompt:    buildProbeWorkerPrompt(appliance),
								Tools:           withFreshRunTool(workerTools),
								MaxRounds:       12,
													RouteKey:        "app.servitor",
								MaskDebugOutput: true,
								ChatOptions:     []ChatOption{WithTemperature(0.2), WithThink(false)},
								SerialTools:     true,
							},
						)
					})
					if workerErr != nil {
						return "", workerErr
					}
					if workerResp == nil {
						return "No findings.", nil
					}
					result := strings.TrimSpace(workerResp.Content)
					result = parseProbeOutcome(result)
					probeCache[cacheKey] = result
					// If the worker hit the cmd loop limit during this
					// delegation, propagate the signal up to the
					// orchestrator-level counter so we can refuse future
					// delegations once the orchestrator has demonstrated
					// it's not pivoting in response.
					if strings.Contains(result, "[LOOP DETECTED]") {
						probeLoopSignalCount++
						emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Worker loop signal received (%d/%d)", probeLoopSignalCount, probeLoopSignalLimit)})
					}
					if len(result) > 12000 {
						result = result[:12000] + "\n… [truncated]"
					}
					emit(id, probeEvent{Kind: "output", Text: result})
					return result, nil
				},
				NeedsConfirm: false,
			}

			var invMsg strings.Builder
			invMsg.WriteString("## System Snapshot\n\n")
			if snapshot != "" {
				invMsg.WriteString(snapshot)
			} else {
				invMsg.WriteString("(snapshot unavailable)\n")
			}
			invMsg.WriteString("\n\n")
			if udb != nil {
				if disc := discoveriesFor(udb, appliance.ID); len(disc) > 0 {
					invMsg.WriteString("## Prior Discoveries (already established)\n\n")
					invMsg.WriteString(formatDiscoveries(disc))
					invMsg.WriteString("\n\n")
				}
				if cachedFacts != "" {
					invMsg.WriteString("## Prior Facts\n\n")
					invMsg.WriteString(cachedFacts)
					invMsg.WriteString("\n\n")
				}
				if cachedTechniques != "" {
					invMsg.WriteString("## Prior Techniques\n\n")
					invMsg.WriteString(cachedTechniques)
					invMsg.WriteString("\n\n")
				}
			}
			invMsg.WriteString("Begin your investigation.\n\n")
			invMsg.WriteString("REQUIRED FIRST CALL: `set_plan` with ordered steps — typically 5–12, scale higher (15+) for complex appliances. Err toward more steps with narrower scopes rather than fewer with sprawling scopes; narrow steps produce sharper findings. Each step needs a short title and a what_to_find description. Foundation/discovery steps come first; deeper investigation later builds on what they find.\n\n")
			invMsg.WriteString("After the plan is set, work the steps one at a time:\n")
			invMsg.WriteString("  1. mark_step_in_progress (step_id)\n")
			invMsg.WriteString("  2. probe (delegate worker investigation for that step — may call multiple times)\n")
			invMsg.WriteString("  3. record_step_findings (step_id, 1–3 sentence summary) — OR mark_step_blocked (step_id, reason) if you can't complete it\n")
			invMsg.WriteString("  4. Move to the next pending step\n\n")
			invMsg.WriteString(fmt.Sprintf("If findings reveal something you couldn't have planned for, call `revise_plan` to add/remove/reorder steps (max %d revisions per session — use deliberately, not reflexively).\n\n", PlanRevisionLimit))
			invMsg.WriteString("BEFORE WRITING YOUR FINAL ANSWER: call `report_gaps`. It returns a structured summary of every blocked or skipped step. You MUST incorporate that into a 'What I Couldn't Determine' section in your final answer — the user trusts the report only when you're explicit about what you couldn't see. If the gap report is empty (everything completed), no such section is needed.\n\n")
			invMsg.WriteString("Use store_fact / record_discovery / record_technique alongside step work for durable knowledge that survives the session. When all steps are done or blocked AND report_gaps has been called, write your final answer.")

			emit(id, probeEvent{Kind: "status", Text: "Investigator starting…"})
			var invResp *Response
			var invErr error
			investigatorTools := []AgentToolDef{
				set_plan_tool, mark_step_in_progress_tool, record_step_findings_tool, mark_step_blocked_tool,
				revise_plan_tool, report_gaps_tool,
				probe_tool, store_fact_tool, record_discovery_tool, record_technique_tool, note_lesson_tool,
			}
			assertOnlyAllowedTools("servitor.investigator", investigatorTools, servitorOrchestratorToolAllowList)
			withHeartbeat(ctx, id, "Investigator", func() {
				invResp, _, invErr = a.RunAgentLoop(ctx,
					[]Message{{Role: "user", Content: invMsg.String()}},
					AgentLoopConfig{
						SystemPrompt:    buildInvestigatorSystemPrompt(appliance),
						Tools:           investigatorTools,
						MaxRounds:       75,
									RouteKey:        "app.servitor",
						MaskDebugOutput: true,
						SerialTools:     true,
						ChatOptions:     append([]ChatOption{WithTemperature(0.3), WithThink(true)}, orchestratorThinkOpts()...),
					},
				)
			})
			if invErr != nil && ctx.Err() == nil {
				emit(id, probeEvent{Kind: "error", Text: "Investigator error: " + invErr.Error()})
				// Don't return — synthesize what was gathered.
			}
			if ctx.Err() != nil {
				return
			}

			// Phase 3: Synthesis — structured profile from accumulated discoveries + facts.
			emit(id, probeEvent{Kind: "status", Text: "Synthesizing profile from investigation findings…"})
			now := time.Now()
			finalFacts := ""
			if facts := factsForAppliance(udb, appliance.ID); len(facts) > 0 {
				finalFacts = formatFactsWithAge(facts, now)
			}
			var finalNotes string
			if udb != nil {
				udb.Get(notesTable, appliance.ID, &finalNotes)
			}
			finalTechniques := ""
			if udb != nil {
				finalTechniques = techniquesFor(udb, appliance.ID)
			}
			finalDiscoveries := ""
			if udb != nil {
				finalDiscoveries = formatDiscoveries(discoveriesFor(udb, appliance.ID))
			}
			invNarrative := ""
			if invResp != nil {
				invNarrative = strings.TrimSpace(invResp.Content)
			}
			synthMsg := buildSynthesisMessage(invNarrative, finalFacts, finalTechniques, finalNotes, finalDiscoveries)
			var synthResp *Response
			var synthErr error
			withHeartbeat(ctx, id, "Synthesizing profile", func() {
				synthResp, _, synthErr = a.RunAgentLoop(ctx,
					[]Message{{Role: "user", Content: synthMsg}},
					AgentLoopConfig{
						SystemPrompt:    buildSynthesisSystemPrompt(appliance),
						Tools:           nil,
						MaxRounds:       1,
									RouteKey:        "app.servitor",
						MaskDebugOutput: true,
						ChatOptions:     []ChatOption{WithThink(false)},
					},
				)
			})
			if synthErr != nil && ctx.Err() == nil {
				emit(id, probeEvent{Kind: "error", Text: "Synthesis error: " + synthErr.Error()})
			}
			if synthResp != nil && strings.TrimSpace(synthResp.Content) != "" {
				reply = strings.TrimSpace(synthResp.Content)
			}
			if reply == "" && invNarrative != "" {
				reply = invNarrative
			}
		} else {
			// Existing profile: single-worker update pass.
			var resp *Response
			var err error
			withHeartbeat(ctx, id, "Updating profile", func() {
				resp, _, err = a.RunAgentLoop(ctx, messages, AgentLoopConfig{
					SystemPrompt:    workerPrompt,
					Tools:           withFreshRunTool(workerTools),
					MaxRounds:       100,
							RouteKey:        "app.servitor",
					MaskDebugOutput: true,
					SerialTools:     true,
					ChatOptions:     []ChatOption{WithThink(false)},
				})
			})
			if err != nil && ctx.Err() == nil {
				emit(id, probeEvent{Kind: "error", Text: err.Error()})
				return
			}
			if resp != nil {
				reply = strings.TrimSpace(resp.Content)
			}
		}
	} else {
		// Chat mode: investigator loop using probe tool — no pre-planned task list.

		// read_doc — fetch a structured knowledge document.
		read_doc_tool := AgentToolDef{
			Tool: Tool{
				Name:        "read_doc",
				Description: "Read a structured knowledge document about this system. System docs: overview, databases, filesystem, services, apps. CLI maps: cli:<command> (e.g. cli:kubectl, cli:docker).",
				Parameters: map[string]ToolParam{
					"doc": {Type: "string", Description: "Document name: overview, databases, filesystem, services, apps, or cli:<command>."},
				},
				Required: []string{"doc"},
			},
			Handler: func(args map[string]any) (string, error) {
				doc, _ := args["doc"].(string)
				if doc == "" {
					return "", fmt.Errorf("doc is required")
				}
				emit(id, probeEvent{Kind: "status", Text: "Reading " + doc + " knowledge..."})
				content, age := readDocWithAge(udb, appliance.ID, doc, time.Now())
				if content == "" {
					if strings.HasPrefix(doc, "cli:") {
						return fmt.Sprintf("No CLI map found for %q. Ask the user to run 'Map App' for this command first.", strings.TrimPrefix(doc, "cli:")), nil
					}
					return fmt.Sprintf("No %s document found. Probe the system to build it.", doc), nil
				}
				if age != "" {
					return fmt.Sprintf("[Last updated: %s]\n\n%s", age, content), nil
				}
				return content, nil
			},
			NeedsConfirm: false,
		}

		// update_doc — persist a structured knowledge document.
		update_doc_tool := AgentToolDef{
			Tool: Tool{
				Name:        "update_doc",
				Description: "Write or replace a structured knowledge document with new findings. Call this after every probe that yields new information. These docs are the investigator's persistent memory across sessions.",
				Parameters: map[string]ToolParam{
					"doc":     {Type: "string", Description: "Document name: overview, databases, filesystem, services, or apps."},
					"content": {Type: "string", Description: "Full markdown content for this document."},
				},
				Required: []string{"doc", "content"},
			},
			Handler: func(args map[string]any) (string, error) {
				doc, _ := args["doc"].(string)
				content, _ := args["content"].(string)
				if doc == "" || content == "" {
					return "", fmt.Errorf("doc and content are required")
				}
				writeDoc(udb, appliance.ID, doc, content)
				emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Knowledge updated: %s", doc)})
				return "saved", nil
			},
			NeedsConfirm: false,
		}

		var lastProbeResult string
		var allProbeResults []string
		qaProbeCache := make(map[string]string) // normalized task → last result
		qaTopicCount := make(map[string]int)
		const qaTopicLimit = 3

		// probe_tool — targeted investigation at the investigator's direction.
		probe_tool := AgentToolDef{
			Tool: Tool{
				Name: "probe",
				Description: "Execute a specific SSH investigation task on the target system. " +
					"Be precise — one clear goal per probe. Pass rich context so the worker " +
					"uses what you already know.",
				Parameters: map[string]ToolParam{
					"task":    {Type: "string", Description: "Single clear goal: find X, read Y, verify Z."},
					"context": {Type: "string", Description: "Relevant context you already know: paths, ports, credentials."},
				},
				Required: []string{"task"},
			},
			Handler: func(args map[string]any) (string, error) {
				task, _ := args["task"].(string)
				if task == "" {
					return "", fmt.Errorf("task is required")
				}
				cacheKey := normalizeTask(task)
				if cached, ok := qaProbeCache[cacheKey]; ok {
					qaTopicCount[cacheKey]++
					if qaTopicCount[cacheKey] >= qaTopicLimit {
						emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Topic exhausted: %q (%dx)", task, qaTopicCount[cacheKey])})
						return fmt.Sprintf("[TOPIC EXHAUSTED — you have re-delegated this topic %d times now. Stop probing this area entirely. Pivot to a fundamentally different domain. Re-delegating the same topic in different words will not produce new information.]\n\nLast result for reference:\n\n%s", qaTopicCount[cacheKey], cached), nil
					}
					return "[ALREADY PROBED — result below. Do not probe this topic again; move to a different area.]\n\n" + cached, nil
				}
				context, _ := args["context"].(string)
				var msg strings.Builder
				if context != "" {
					msg.WriteString("## Known Context\n\n")
					msg.WriteString(context)
					msg.WriteString("\n\n")
				}
				if udb != nil {
					if disc := discoveriesFor(udb, appliance.ID); len(disc) > 0 {
						msg.WriteString("## Key Discoveries (pre-established — do not re-investigate)\n\n")
						msg.WriteString(formatDiscoveries(disc))
						msg.WriteString("\n\n")
					}
					if t := techniquesFor(udb, appliance.ID); t != "" {
						msg.WriteString("## Known Techniques (use directly)\n\n")
						msg.WriteString(t)
						msg.WriteString("\n\n")
					}
					if facts := factsForAppliance(udb, appliance.ID); len(facts) > 0 {
						msg.WriteString("## Stored Facts\n\n")
						msg.WriteString(formatFacts(facts))
						msg.WriteString("\n\n")
					}
				}
				msg.WriteString("## Task\n\n")
				msg.WriteString(task)
				// Surface the orchestrator's intent for this round as a
				// prominent event so the user can see "what is the brain
				// trying to do" without needing to read raw reasoning.
				// task is the orchestrator's user-facing summary; context
				// is the supporting briefing it passed along.
				emit(id, probeEvent{Kind: "intent", Text: task, Reason: context})
				var workerResp *Response
				var err error
				withHeartbeat(ctx, id, "Worker: investigating", func() {
					workerResp, _, err = a.RunAgentLoop(ctx,
						[]Message{{Role: "user", Content: msg.String()}},
						AgentLoopConfig{
							SystemPrompt:    buildProbeWorkerPrompt(appliance),
							Tools:           withFreshRunTool(workerTools),
							MaxRounds:       15,
											RouteKey:        "app.servitor",
							MaskDebugOutput: true,
							ChatOptions:     []ChatOption{WithTemperature(0.2), WithThink(false)},
							SerialTools:     true,
						},
					)
				})
				if err != nil {
					return "", err
				}
				if workerResp == nil {
					return "Worker returned no findings.", nil
				}
				result := strings.TrimSpace(workerResp.Content)
				result = parseProbeOutcome(result)
				if result != "" {
					qaProbeCache[cacheKey] = result
					lastProbeResult = result
					allProbeResults = append(allProbeResults, result)
				}
				if len(result) > 14000 {
					result = result[:14000] + "\n… [truncated]"
				}
				emit(id, probeEvent{Kind: "status", Text: "Worker complete — reviewing findings."})
				return result, nil
			},
			NeedsConfirm: false,
		}

		docs := allDocs(udb, appliance.ID)
		supplementContext := buildSupplementContext(ctx, udb, supplements, messages, a.WorkerContextSize())
		leadPrompt := buildLeadSystemPrompt(udb, appliance, docs, cachedFacts, cachedNotes, cachedTechniques, cachedRules, cachedDiscoveries, supplementContext)
		emit(id, probeEvent{Kind: "status", Text: "Investigator analyzing…"})

		// Resolve the per-session injection queue so the orchestrator picks
		// up mid-flight user notes between rounds. Workers don't get the hook
		// — they finish their current task before the orchestrator sees the note.
		var injQ *injectionQueue
		if v, ok := injectionQueues.Load(id); ok {
			injQ = v.(*injectionQueue)
		}
		drainInjections := func() []Message {
			if injQ == nil {
				return nil
			}
			notes := injQ.Drain()
			if len(notes) == 0 {
				return nil
			}
			out := make([]Message, 0, len(notes))
			ids := make([]string, 0, len(notes))
			for _, n := range notes {
				out = append(out, Message{Role: "user", Content: "[USER NOTE — submitted mid-investigation] " + n.Text})
				ids = append(ids, n.ID)
			}
			emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Orchestrator picked up %d user note(s).", len(notes))})
			// Tell the UI which notes are now locked from edit/delete.
			emit(id, probeEvent{Kind: "notes_consumed", IDs: ids})
			return out
		}

		var invResp *Response
		var invHistory []Message
		var err error
		docInvestigatorTools := []AgentToolDef{read_doc_tool, update_doc_tool, probe_tool}
		assertOnlyAllowedTools("servitor.doc_investigator", docInvestigatorTools, servitorOrchestratorToolAllowList)
		withHeartbeat(ctx, id, "Investigator: working", func() {
			invResp, invHistory, err = a.RunAgentLoop(ctx, messages, AgentLoopConfig{
				SystemPrompt:    leadPrompt,
				Tools:           docInvestigatorTools,
				MaxRounds:       50,
					RouteKey:        "app.servitor",
				MaskDebugOutput: true,
				SerialTools:     true,
				ChatOptions:     append([]ChatOption{WithTemperature(0.2), WithThink(true)}, orchestratorThinkOpts()...),
				OnRoundStart:    drainInjections,
				OnStep: func(step StepInfo) {
					if step.Done && strings.TrimSpace(step.Content) != "" {
						emit(id, probeEvent{Kind: "status", Text: "Investigator: synthesizing answer…"})
					}
				},
			})
		})
		_ = invHistory
		if err != nil && ctx.Err() == nil {
			emit(id, probeEvent{Kind: "error", Text: err.Error()})
			return
		}
		if invResp != nil {
			reply = strings.TrimSpace(invResp.Content)
		}
		if reply == "" && lastProbeResult != "" {
			reply = lastProbeResult
		}

		// Consolidation and verification use allProbeResults / lastProbeResult.
		if lastProbeResult != "" && udb != nil {
			workerOut := lastProbeResult
			userQuestion := ""
			if n := len(messages); n > 0 && messages[n-1].Role == "user" {
				userQuestion = messages[n-1].Content
			}
			consolidateFn = func() {
				leadAnswer := reply
				bgCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				defer cancel()
				var cMsg strings.Builder
				cMsg.WriteString(fmt.Sprintf("Consolidate knowledge for %s.\n\n", appliance.Name))
				if userQuestion != "" {
					cMsg.WriteString(fmt.Sprintf("## User Question\n\n%s\n\n", userQuestion))
				}
				cMsg.WriteString(fmt.Sprintf("## Worker Findings\n\n%s\n\n", workerOut))
				if leadAnswer != "" {
					cMsg.WriteString(fmt.Sprintf("## Investigator Summary\n\n%s\n", leadAnswer))
				}
				a.RunAgentLoop(bgCtx, []Message{{Role: "user", Content: cMsg.String()}}, AgentLoopConfig{
					SystemPrompt:    buildConsolidationPrompt(appliance),
					Tools:           []AgentToolDef{read_doc_tool, update_doc_tool, store_fact_tool, record_technique_tool},
					MaxRounds:       10,
					RouteKey:        "app.servitor",
					MaskDebugOutput: true,
					ChatOptions:     []ChatOption{WithThink(false)},
				})
				emit(id, probeEvent{Kind: "status", Text: "Background: knowledge consolidated."})
			}
		}

		// Verification pass.
		if reply != "" && len(allProbeResults) > 0 && a.LLM != nil {
			emit(id, probeEvent{Kind: "status", Text: "Verifying names and identifiers…"})
			rawFindings := strings.Join(allProbeResults, "\n\n---\n\n")
			if len(rawFindings) > 24000 {
				rawFindings = rawFindings[:24000] + "\n... [truncated]"
			}
			verifyPrompt := "You are a fact-checker. Your only job is to verify that every specific identifier in a response — table names, service names, file paths, usernames, database names, column names, IP addresses, port numbers, and version strings — appears character-for-character in the raw worker findings below.\n\n" +
				"If all identifiers match exactly: respond with only the word OK.\n\n" +
				"If any identifier was altered (even one character — wrong underscore, wrong prefix, wrong suffix, wrong capitalization): respond with the corrected version of the full response, with all incorrect identifiers replaced by the exact strings from the findings. Do not change anything else.\n\n" +
				"## Raw Worker Findings\n\n" + rawFindings
			verifyResp, verifyErr := a.LLM.Chat(ctx,
				[]Message{
					{Role: "user", Content: "## Response to verify\n\n" + reply},
				},
				WithSystemPrompt(verifyPrompt),
				WithTemperature(0.0),
				WithRouteKey("app.servitor"),
				WithThink(false),
			)
			if verifyErr == nil && verifyResp != nil {
				corrected := strings.TrimSpace(verifyResp.Content)
				if corrected != "OK" && corrected != "" && corrected != reply {
					Debug("[servitor] verification corrected reply")
					reply = corrected
				}
			}
		}

	}

	if reply == "" {
		return
	}
	if len(sessionFailures) > 0 { // chat mode only
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d command(s) failed this session:\n", len(sessionFailures))
		for _, f := range sessionFailures {
			fmt.Fprintf(&sb, "• %s\n  → %s\n", f.Cmd, f.Reason)
		}
		emit(id, probeEvent{Kind: "status", Text: strings.TrimSpace(sb.String())})
	}
	emit(id, probeEvent{Kind: "reply", Text: reply})

	if consolidateFn != nil {
		emit(id, probeEvent{Kind: "status", Text: "Background: consolidating knowledge..."})
		go consolidateFn()
	}

	if saveProfile && udb != nil {
		var existing Appliance
		if udb.Get(applianceTable, appliance.ID, &existing) {
			existing.Profile = reply
			existing.Scanned = time.Now().Format(time.RFC3339)
			if fresh := extractLogMap(reply); len(fresh) > 0 {
				existing.LogMap = mergeLogMap(existing.LogMap, fresh)
			}
			udb.Set(applianceTable, appliance.ID, existing)
			extractDocsFromProfile(udb, appliance.ID, reply)
		}
	}
}

// handleRules handles GET (list) and POST (create) for appliance rules.
func (T *Servitor) handleRules(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	applianceID := r.URL.Query().Get("appliance_id")
	switch r.Method {
	case http.MethodGet:
		if applianceID == "" {
			http.Error(w, "appliance_id required", http.StatusBadRequest)
			return
		}
		rules := rulesForAppliance(udb, applianceID)
		if rules == nil {
			rules = []ApplianceRule{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rules)
	case http.MethodPost:
		var req struct {
			ApplianceID string `json:"appliance_id"`
			Rule        string `json:"rule"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ApplianceID == "" || strings.TrimSpace(req.Rule) == "" {
			http.Error(w, "appliance_id and rule required", http.StatusBadRequest)
			return
		}
		id := storeRule(udb, req.ApplianceID, req.Rule)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRuleDelete handles DELETE /api/rules/<id>.
func (T *Servitor) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/rules/")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	deleteRule(udb, id)
	w.WriteHeader(http.StatusNoContent)
}

// handleSaveDestinations returns which save targets are available.
func (T *Servitor) handleSaveDestinations(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{
		"techwriter": SaveArticleFunc != nil,
		"codewriter": SaveSnippetFunc != nil,
	})
}

// handleSaveArticle reformats the given assistant response for TechWriter and saves it.
func (T *Servitor) handleSaveArticle(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if SaveArticleFunc == nil {
		http.Error(w, "TechWriter not available", http.StatusServiceUnavailable)
		return
	}
	if T.LLM == nil {
		http.Error(w, "LLM not configured", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	sysPrompt := "You are a technical writer. Format the provided content as a polished technical document. " +
		"Respond with a JSON object only — no other text — with two fields: " +
		`"subject" (a concise document title) and "body" (full markdown content).`
	userMsg := "Format this for TechWriter:\n\n" + req.Text
	resp, err := T.LLM.Chat(r.Context(),
		[]Message{{Role: "user", Content: userMsg}},
		WithSystemPrompt(sysPrompt),
		WithJSONMode(),
		WithRouteKey("app.servitor"),
		WithThink(false),
	)
	if err != nil {
		http.Error(w, "LLM error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var out struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &out); err != nil || out.Subject == "" {
		// Fall back: use first line as subject, full text as body.
		lines := strings.SplitN(strings.TrimSpace(req.Text), "\n", 2)
		out.Subject = strings.TrimPrefix(strings.TrimSpace(lines[0]), "# ")
		if out.Subject == "" {
			out.Subject = "Untitled"
		}
		out.Body = req.Text
	}
	id, err := SaveArticleFunc(userID, out.Subject, out.Body)
	if err != nil {
		http.Error(w, "save error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id, "subject": out.Subject})
}

// handleSaveSnippet reformats the given assistant response for CodeWriter and saves it.
func (T *Servitor) handleSaveSnippet(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if SaveSnippetFunc == nil {
		http.Error(w, "CodeWriter not available", http.StatusServiceUnavailable)
		return
	}
	if T.LLM == nil {
		http.Error(w, "LLM not configured", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	sysPrompt := "You are a code assistant. Extract the primary code snippet from the provided content. " +
		"Respond with a JSON object only — no other text — with three fields: " +
		`"name" (a short descriptive name for the snippet), "lang" (language, e.g. sql/bash/python/go), and "code" (the code only, no markdown fences).`
	userMsg := "Extract the code snippet:\n\n" + req.Text
	resp, err := T.LLM.Chat(r.Context(),
		[]Message{{Role: "user", Content: userMsg}},
		WithSystemPrompt(sysPrompt),
		WithJSONMode(),
		WithRouteKey("app.servitor"),
		WithThink(false),
	)
	if err != nil {
		http.Error(w, "LLM error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var out struct {
		Name string `json:"name"`
		Lang string `json:"lang"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &out); err != nil || out.Code == "" {
		out.Name = "Snippet"
		out.Lang = "text"
		out.Code = req.Text
	}
	id, err := SaveSnippetFunc(userID, out.Name, out.Lang, out.Code)
	if err != nil {
		http.Error(w, "save error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id, "name": out.Name})
}

// --- HTML / CSS / JS ---

const sshHeadHTML = `<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css">
<script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js"></script>
<script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js"></script>
<script src="https://cdn.jsdelivr.net/npm/marked@11.2.0/marked.min.js"></script>`

const sshCSS = `
body { height: 100vh; display: flex; flex-direction: column; }

#toolbar {
  display: flex; align-items: center; gap: 0.5rem; padding: 0.5rem 1rem;
  background: var(--bg-1); border-bottom: 1px solid var(--border); flex-shrink: 0; flex-wrap: wrap;
}
#toolbar .app-title { font-weight: 700; color: var(--text-hi); margin-right: 0.25rem; }
#appliance-select {
  flex: 1; min-width: 0; max-width: 280px;
  background: var(--bg-0); border: 1px solid var(--border); color: var(--text);
  padding: 0.4rem 0.6rem; border-radius: 6px; font-size: 0.85rem;
}
#toolbar button {
  background: var(--accent); color: #fff; border: none; border-radius: 6px;
  padding: 0.4rem 0.8rem; cursor: pointer; font-size: 0.8rem; white-space: nowrap;
}
#toolbar button.secondary {
  background: transparent; border: 1px solid var(--border); color: var(--text-mute);
}
#toolbar button:hover { opacity: 0.9; }
#toolbar button:disabled { opacity: 0.35; cursor: default; }

#main { display: flex; flex: 1; overflow: hidden; }

/* --- Chat pane (left) --- */
#chat-pane {
  width: 50%; display: flex; flex-direction: column; background: var(--bg-1); flex-shrink: 0;
  border-right: 1px solid var(--border);
}
#chat-header {
  padding: 0.5rem 1rem; border-bottom: 1px solid var(--border); font-size: 0.85rem;
  color: var(--text-mute); font-weight: 600; display: flex; justify-content: space-between; align-items: flex-start;
}
#chat-header .hdr-info { flex: 1; min-width: 0; }
#chat-header .hdr-title { color: var(--text-hi); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
#chat-header .hdr-meta { font-size: 0.72rem; color: var(--text-mute); font-weight: 400; margin-top: 0.1rem; }
.persona-badge { display:inline-block; font-size:0.65rem; font-weight:600; padding:0.1rem 0.35rem; border-radius:10px; background:var(--accent-dim,rgba(99,102,241,0.18)); color:var(--accent); border:1px solid var(--accent); margin-left:0.4rem; vertical-align:middle; letter-spacing:0.02em; }
.preset-chip.active { color:var(--accent); border-color:var(--accent); background:var(--accent-dim,rgba(99,102,241,0.1)); }
#chat-header .hdr-btns { display: flex; gap: 0.3rem; flex-shrink: 0; margin-left: 0.5rem; }
#chat-header .hdr-btns button {
  background: none; border: none; color: var(--text-mute); cursor: pointer; font-size: 0.72rem; padding: 0 0.3rem;
}
#chat-header .hdr-btns button:hover { color: var(--text-hi); }

/* Log file list below header */
#log-list {
  border-bottom: 1px solid var(--border); max-height: 120px; overflow-y: auto;
  display: none;
}
#log-list-toggle {
  display: flex; align-items: center; gap: 0.4rem; padding: 0.3rem 1rem;
  background: var(--bg-1); border-bottom: 1px solid var(--border); cursor: pointer;
  font-size: 0.78rem; color: var(--text-mute); user-select: none;
}
#log-list-toggle:hover { color: var(--text-hi); }
#log-list-toggle .arrow { font-size: 0.55rem; transition: transform 0.15s; }
#log-list-toggle .arrow.open { transform: rotate(90deg); }
.log-entry {
  display: flex; align-items: center; gap: 0.5rem; padding: 0.25rem 1rem 0.25rem 1.5rem;
  font-size: 0.78rem; cursor: pointer;
}
.log-entry:hover { background: var(--bg-0); }
.log-entry .log-svc { color: var(--accent); font-weight: 600; min-width: 70px; flex-shrink: 0; }
.log-entry .log-path { color: var(--text-mute); font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 0.75rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

#chat-messages { flex: 1; overflow-y: auto; padding: 0.75rem; }
.chat-msg { margin-bottom: 0.75rem; padding: 0.6rem 0.8rem; border-radius: 8px; font-size: 0.85rem; line-height: 1.5; }
.chat-msg.user { background: var(--accent); color: #fff; margin-left: 2rem; }
.chat-msg.assistant { background: var(--bg-0); color: var(--text); border: 1px solid var(--border); }
.chat-msg.system { background: transparent; color: var(--text-mute); font-size: 0.8rem; font-style: italic; padding: 0.2rem 0; border: none; }
.chat-msg pre {
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 4px;
  padding: 0.5rem; margin: 0.4rem 0; overflow-x: auto; font-size: 0.8rem;
  font-family: ui-monospace, Menlo, Consolas, monospace; white-space: pre; word-break: normal;
}
.chat-msg code { background: var(--bg-1); padding: 0.1rem 0.25rem; border-radius: 3px; font-size: 0.85em; }
.chat-msg pre code { background: none; padding: 0; }
.chat-save-actions { display: flex; gap: 0.4rem; margin-top: 0.5rem; flex-wrap: wrap; }
.chat-save-btn { font-size: 0.72rem; padding: 0.2rem 0.55rem; border-radius: 4px; border: 1px solid var(--border); background: var(--bg-1); color: var(--text-mute); cursor: pointer; transition: color 0.15s, border-color 0.15s; }
.chat-save-btn:hover { color: var(--text); border-color: var(--accent); }
.chat-save-btn.saving { opacity: 0.5; cursor: default; }
.chat-save-btn.saved { color: var(--accent); border-color: var(--accent); }
.chat-msg.interjection { background: rgba(88, 166, 255, 0.18); border: 1px dashed var(--accent); }
.chat-msg.interjection.interjection-locked { opacity: 0.78; border-style: solid; }
.chat-msg.interjection.interjection-failed { background: rgba(220, 90, 90, 0.18); border-color: #b85a5a; }
.interjection-meta { display: flex; align-items: center; justify-content: space-between; margin-top: 0.4rem; gap: 0.5rem; flex-wrap: wrap; }
.interjection-actions button { font-size: 0.7rem; padding: 0.15rem 0.5rem; border-radius: 3px; border: 1px solid rgba(255,255,255,0.25); background: transparent; color: #fff; cursor: pointer; margin-left: 0.25rem; }
.interjection-actions button:hover { background: rgba(255,255,255,0.12); }
#chat-input-area {
  display: flex; gap: 0.5rem; padding: 0.5rem 0.75rem 1.25rem; border-top: 1px solid var(--border);
  align-items: flex-end;
}
#chat-input {
  flex: 1; background: var(--bg-0); border: 1px solid var(--border); color: var(--text);
  padding: 0.4rem 0.6rem; border-radius: 6px; font-size: 0.85rem;
  font-family: inherit; resize: vertical; min-height: 60px; max-height: 200px;
}
#chat-input:focus { border-color: var(--accent); outline: none; }
#chat-send {
  background: var(--accent); color: #fff; border: none; border-radius: 6px;
  padding: 0.4rem 0.8rem; cursor: pointer; font-size: 0.8rem;
}
#chat-send:disabled { opacity: 0.35; cursor: default; }
#chat-cancel {
  background: var(--danger); color: #fff; border: none; border-radius: 6px;
  padding: 0.4rem 0.8rem; cursor: pointer; font-size: 0.8rem; display: none;
}
#chat-cancel:hover { opacity: 0.85; }

/* --- Resizer --- */
#chat-resizer { width: 5px; background: var(--border); cursor: col-resize; flex-shrink: 0; }
#chat-resizer:hover, #chat-resizer.dragging { background: var(--accent); }

/* --- Activity pane (right) --- */
#activity-pane { flex: 1; display: flex; flex-direction: column; min-width: 0; min-height: 0; }
#activity-header {
  padding: 0.5rem 1rem; border-bottom: 1px solid var(--border); font-size: 0.85rem;
  color: var(--text-mute); font-weight: 600; display: flex; justify-content: space-between; align-items: center;
}
#activity-header button {
  background: none; border: none; color: var(--text-mute); cursor: pointer; font-size: 0.72rem;
}
#activity-header button:hover { color: var(--text-hi); }
#activity-log {
  flex: 1; overflow-y: auto; padding: 0.75rem; min-height: 60px;
  font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 0.8rem; line-height: 1.5;
}
.ev-status { color: var(--text-mute); padding: 0.1rem 0; }
.ev-cmd { color: var(--accent); padding: 0.2rem 0; display: flex; gap: 0.4rem; align-items: baseline; }
.ev-cmd::before { content: "$"; color: var(--text-mute); flex-shrink: 0; }
.ev-output {
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 4px;
  padding: 0.35rem 0.6rem; margin: 0.1rem 0 0.4rem; white-space: pre-wrap; word-break: break-all;
  color: var(--text); max-height: 280px; overflow-y: auto; cursor: pointer;
}
.ev-output.collapsed { max-height: 3.4em; overflow: hidden; }
.ev-output.collapsed::after { content: " … (click to expand)"; color: var(--text-mute); font-style: italic; }
.ev-message {
  background: var(--bg-0); border-left: 2px solid var(--accent); border-radius: 0 4px 4px 0;
  padding: 0.4rem 0.7rem; margin: 0.2rem 0 0.4rem; font-size: 0.83rem; line-height: 1.5; color: var(--text);
  white-space: pre-wrap; word-break: break-word;
}
.chat-msg.plan {
  background: var(--bg-2); border: 1px solid var(--border); border-left: 3px solid var(--accent);
  border-radius: 0 6px 6px 0; padding: 0.6rem 0.85rem;
}
.chat-msg.plan .plan-label { color: var(--accent); font-weight: 700; font-size: 0.74rem; letter-spacing: 0.04em; text-transform: uppercase; margin-bottom: 0.4rem; }
.chat-msg.plan .plan-steps { list-style: none; margin: 0; padding: 0; }
.chat-msg.plan .plan-steps li { padding: 0.35rem 0; display: grid; grid-template-columns: 1.2rem 1fr; gap: 0.4rem; align-items: start; border-bottom: 1px dotted var(--border); }
.chat-msg.plan .plan-steps li:last-child { border-bottom: none; }
.chat-msg.plan .plan-icon { color: var(--text-mute); text-align: center; line-height: 1.45; }
.chat-msg.plan .plan-title { color: var(--text-hi); font-weight: 500; line-height: 1.4; }
.chat-msg.plan .plan-detail { grid-column: 2; color: var(--text-mute); font-size: 0.78rem; line-height: 1.4; margin-top: 0.15rem; }
.chat-msg.plan .plan-findings { grid-column: 2; color: var(--text); font-size: 0.78rem; line-height: 1.4; margin-top: 0.2rem; padding-left: 0.4rem; border-left: 2px solid var(--bg-1); }
.chat-msg.plan .plan-blocked-reason { grid-column: 2; color: var(--danger); font-size: 0.78rem; line-height: 1.4; margin-top: 0.2rem; font-style: italic; }
.chat-msg.plan .plan-active .plan-icon { color: var(--accent); }
.chat-msg.plan .plan-active .plan-title { color: var(--accent); font-weight: 600; }
.chat-msg.plan .plan-done .plan-icon { color: #5fb37c; }
.chat-msg.plan .plan-done .plan-title { color: var(--text); }
.chat-msg.plan .plan-blocked .plan-icon { color: var(--danger); }
.chat-msg.plan .plan-blocked .plan-title { color: var(--text); text-decoration: line-through; opacity: 0.7; }
.chat-msg.intent {
  background: var(--bg-2); border: 1px solid var(--border); border-left: 3px solid var(--accent);
  border-radius: 0 6px 6px 0;
}
.chat-msg.intent .intent-label { color: var(--accent); font-weight: 700; font-size: 0.74rem; letter-spacing: 0.04em; text-transform: uppercase; margin-bottom: 0.25rem; }
.chat-msg.intent .intent-task { color: var(--text-hi); font-weight: 500; }
.chat-msg.intent .intent-context { color: var(--text-mute); font-size: 0.78rem; margin-top: 0.3rem; font-style: italic; white-space: pre-wrap; word-break: break-word; }
.ev-error { color: var(--danger); padding: 0.2rem 0; }
.ev-watch { color: #b45309; padding: 0.2rem 0; }
.ev-confirm-technique { background:var(--bg-2); border:1px solid var(--accent); border-radius:5px; padding:0.5rem 0.65rem; margin:0.2rem 0; font-size:0.82rem; }
.ev-confirm-technique .tech-label { color:var(--accent); font-weight:700; font-size:0.78rem; margin-bottom:0.25rem; }
.ev-confirm-technique .tech-text { color:var(--text-hi); margin:0.1rem 0 0.35rem; white-space:pre-wrap; }
.ev-confirm-technique .confirm-btns { display:flex; gap:0.5rem; }
.ev-confirm-technique .confirm-btns button { font-size:0.78rem; padding:0.2rem 0.6rem; border-radius:4px; border:1px solid var(--border); background:var(--bg-1); color:var(--text); cursor:pointer; }
.ev-confirm-technique .confirm-btns button:hover { background:var(--bg-2); }
.ev-confirm-technique .confirm-save { border-color:var(--accent) !important; color:var(--accent) !important; }
.ev-confirm {
  border: 1px solid var(--danger); border-radius: 6px; padding: 0.5rem 0.7rem; margin: 0.3rem 0;
  background: rgba(220,50,47,0.07);
}
.ev-confirm .warn-label { color: var(--danger); font-weight: 700; font-size: 0.78rem; margin-bottom: 0.25rem; }
.ev-confirm .warn-cmd { color: var(--text-hi); margin: 0.1rem 0; }
.ev-confirm .warn-reason { color: var(--text-mute); font-size: 0.75rem; margin-bottom: 0.35rem; }
.ev-confirm .confirm-btns { display: flex; gap: 0.5rem; }
.ev-confirm .confirm-btns button {
  border: none; border-radius: 4px; padding: 0.25rem 0.7rem; cursor: pointer; font-size: 0.78rem;
}
.confirm-allow { background: var(--danger); color: #fff; }
.confirm-always { background: #b45309; color: #fff; }
.confirm-deny { background: var(--bg-2); color: var(--text); border: 1px solid var(--border) !important; }
.confirm-allow:disabled, .confirm-always:disabled, .confirm-deny:disabled { opacity: 0.4; cursor: default; }

/* --- Modals --- */
#overlay {
  display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%;
  background: rgba(0,0,0,0.5); z-index: 99;
}
#appliance-modal {
  display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%,-50%);
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 1.25rem; width: 90%; max-width: 480px; z-index: 100;
}
#appliance-modal h3 { margin: 0 0 0.75rem; font-size: 1rem; color: var(--text-hi); }
#appliance-modal label { display: block; font-size: 0.8rem; color: var(--text-mute); margin: 0.5rem 0 0.2rem; }
#appliance-modal input, #appliance-modal textarea {
  width: 100%; padding: 0.4rem 0.5rem; background: var(--bg-0); border: 1px solid var(--border);
  border-radius: 4px; color: var(--text); font-size: 0.85rem; box-sizing: border-box;
}
#appliance-modal input:focus, #appliance-modal textarea:focus { border-color: var(--accent); outline: none; }
#appliance-modal textarea { resize: vertical; font-family: inherit; font-size: 0.82rem; }
#appliance-modal .hint { font-size: 0.72rem; color: var(--text-mute); margin-top: 0.2rem; }
#appliance-modal .btns { display: flex; gap: 0.5rem; margin-top: 1rem; justify-content: space-between; align-items: center; }
#appliance-modal .btns .right { display: flex; gap: 0.5rem; }
#appliance-modal .btns button { padding: 0.35rem 1rem; border-radius: 4px; cursor: pointer; font-size: 0.85rem; }
#appliance-modal .del-btn { color: var(--danger); border-color: var(--danger) !important; background: transparent; }
.a-type-tabs { display: flex; gap: 0; margin-bottom: 0.75rem; border: 1px solid var(--border); border-radius: 4px; overflow: hidden; }
.a-type-tab { flex: 1; padding: 0.3rem 0; font-size: 0.82rem; cursor: pointer; background: var(--bg-0); color: var(--text-mute); border: none; border-radius: 0; transition: background 0.15s; }
.a-type-tab.active { background: var(--accent); color: #fff; }
.a-type-section { display: none; }
.a-type-section.active { display: block; }

#profile-panel {
  display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%,-50%);
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 1.25rem; width: 92%; max-width: 800px; max-height: 82vh; overflow-y: auto; z-index: 100;
}
#profile-panel h3 { margin: 0 0 0.75rem; }
#profile-content { white-space: pre-wrap; font-size: 0.82rem; line-height: 1.6; color: var(--text); }
#profile-panel .btns { display: flex; gap: 0.5rem; margin-top: 1rem; justify-content: flex-end; }
#profile-panel .btns button { padding: 0.35rem 1rem; border-radius: 4px; cursor: pointer; font-size: 0.85rem; }

#facts-panel {
  display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%,-50%);
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 1.25rem; width: 92%; max-width: 760px; max-height: 82vh; overflow-y: auto; z-index: 100;
}
#facts-panel h3 { margin: 0 0 0.75rem; }
#facts-panel .btns { display: flex; gap: 0.5rem; margin-top: 1rem; justify-content: flex-end; }
#facts-panel .btns button { padding: 0.35rem 1rem; border-radius: 4px; cursor: pointer; font-size: 0.85rem; }
.facts-list { display: flex; flex-direction: column; gap: 0; }
.fact-card { padding: 0.5rem 0.6rem; border-bottom: 1px solid var(--border); }
.fact-card:last-child { border-bottom: none; }
.fact-header { display: flex; align-items: baseline; justify-content: space-between; gap: 0.5rem; margin-bottom: 0.2rem; }
.fact-key { color: var(--accent); font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 0.82rem; font-weight: 600; word-break: break-all; }
.fact-date { color: var(--text-mute); font-size: 0.7rem; white-space: nowrap; flex-shrink: 0; }
.fact-value { font-size: 0.83rem; color: var(--text); word-break: break-word; line-height: 1.4; }
.fact-tags { margin-top: 0.2rem; display: flex; flex-wrap: wrap; gap: 0.25rem; }
.fact-tag { font-size: 0.68rem; color: var(--text-mute); background: var(--bg-1); border: 1px solid var(--border); border-radius: 3px; padding: 0.05rem 0.35rem; }
.fact-delete { flex-shrink: 0; background: none; border: none; color: var(--text-mute); cursor: pointer; font-size: 1rem; padding: 0 0.1rem; line-height: 1; margin-left: 0.25rem; }
.fact-delete:hover { color: var(--red, #e05c5c); }
#facts-badge {
  display: inline-block; background: var(--accent); color: #fff;
  border-radius: 8px; font-size: 0.68rem; padding: 0 0.35rem; margin-left: 0.2rem; vertical-align: middle;
}

#rules-panel {
  display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%,-50%);
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 1.25rem; width: 92%; max-width: 640px; max-height: 82vh; overflow-y: auto; z-index: 100;
}
#rules-panel h3 { margin: 0 0 0.75rem; }
#rules-panel .btns { display: flex; gap: 0.5rem; margin-top: 1rem; justify-content: flex-end; }
.rules-list { display: flex; flex-direction: column; gap: 0.4rem; margin-bottom: 0.75rem; }
.rule-row { display: flex; align-items: flex-start; gap: 0.5rem; background: var(--bg-0); border: 1px solid var(--border); border-radius: 4px; padding: 0.4rem 0.6rem; font-size: 0.83rem; }
.rule-text { flex: 1; line-height: 1.5; }
.rule-delete { flex-shrink: 0; background: none; border: none; color: var(--text-mute); cursor: pointer; font-size: 1rem; padding: 0 0.2rem; line-height: 1; }
.rule-delete:hover { color: var(--red, #e05c5c); }
#new-rule-input { width: 100%; padding: 0.4rem 0.5rem; background: var(--bg-0); border: 1px solid var(--border); border-radius: 4px; color: var(--text); font-size: 0.83rem; box-sizing: border-box; margin-bottom: 0.5rem; }

/* --- Workspace panel --- */
#workspace-panel {
  display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%,-50%);
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 1.25rem; width: 92%; max-width: 860px; max-height: 88vh; overflow-y: auto; z-index: 100;
}
#workspace-panel h3 { margin: 0 0 0.1rem; font-size: 1rem; }
#workspace-panel .panel-sub { color: var(--text-mute); font-size: 0.78rem; margin-bottom: 0.9rem; }
#workspace-panel .panel-hdr { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 0.75rem; }
#workspace-panel .panel-close { background: none; border: none; color: var(--text-mute); font-size: 1.2rem; cursor: pointer; padding: 0; line-height: 1; }
#workspace-panel .panel-close:hover { color: var(--text); }
.ws-list { display: flex; flex-direction: column; gap: 0.35rem; }
.ws-row {
  display: flex; align-items: center; gap: 0.6rem;
  padding: 0.5rem 0.7rem; border: 1px solid var(--border); border-radius: 6px;
  background: var(--bg-0); cursor: pointer; transition: border-color 0.15s;
}
.ws-row:hover { border-color: var(--accent); }
.ws-row-info { flex: 1; min-width: 0; }
.ws-row-name { font-size: 0.88rem; color: var(--text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ws-row-meta { font-size: 0.72rem; color: var(--text-mute); margin-top: 0.1rem; }
.ws-row-del { flex-shrink: 0; background: none; border: none; color: var(--text-mute); cursor: pointer; font-size: 1rem; padding: 0 0.2rem; }
.ws-row-del:hover { color: var(--red, #e05c5c); }
.ws-empty { color: var(--text-mute); font-size: 0.85rem; text-align: center; padding: 1.5rem 0; }
.ws-back { background: none; border: none; color: var(--accent); cursor: pointer; font-size: 0.82rem; padding: 0; margin-bottom: 0.75rem; display: inline-flex; align-items: center; gap: 0.3rem; }
.ws-back:hover { text-decoration: underline; }
.ws-section-label { font-size: 0.72rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; color: var(--text-mute); margin: 1rem 0 0.4rem; }
.ws-entry { border: 1px solid var(--border); border-radius: 6px; margin-bottom: 0.4rem; overflow: hidden; }
.ws-entry-hdr { display: flex; align-items: center; gap: 0.5rem; padding: 0.45rem 0.7rem; cursor: pointer; background: var(--bg-0); font-size: 0.83rem; }
.ws-entry-hdr:hover { background: var(--bg-1); }
.ws-entry-arrow { font-size: 0.65rem; color: var(--text-mute); transition: transform 0.15s; flex-shrink: 0; }
.ws-entry.open .ws-entry-arrow { transform: rotate(90deg); }
.ws-entry-q { flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; color: var(--text); }
.ws-entry-date { flex-shrink: 0; font-size: 0.7rem; color: var(--text-mute); }
.ws-entry-body { display: none; padding: 0.6rem 0.7rem; font-size: 0.82rem; line-height: 1.55; border-top: 1px solid var(--border); }
.ws-entry.open .ws-entry-body { display: block; }
.ws-entry-qlabel { font-size: 0.7rem; font-weight: 600; text-transform: uppercase; color: var(--accent); margin-bottom: 0.3rem; }
.ws-entry-alabel { font-size: 0.7rem; font-weight: 600; text-transform: uppercase; color: var(--text-mute); margin: 0.6rem 0 0.3rem; }
.ws-entry-text { white-space: pre-wrap; color: var(--text); }
#ws-draft {
  width: 100%; min-height: 180px; background: var(--bg-0); border: 1px solid var(--border);
  border-radius: 4px; color: var(--text); font-size: 0.83rem; padding: 0.5rem 0.6rem;
  font-family: ui-monospace, Menlo, Consolas, monospace; resize: vertical; box-sizing: border-box;
}
.ws-draft-btns { display: flex; gap: 0.5rem; margin-top: 0.5rem; justify-content: flex-end; }
.ws-draft-btns button { padding: 0.35rem 0.9rem; border-radius: 4px; cursor: pointer; font-size: 0.83rem; }
/* supplement docs */
.ws-supp-list { display: flex; flex-direction: column; gap: 0.4rem; margin-bottom: 0.5rem; }
.ws-supp-row {
  border: 1px solid var(--border); border-radius: 6px; overflow: hidden; background: var(--bg-0);
}
.ws-supp-hdr {
  display: flex; align-items: center; gap: 0.5rem; padding: 0.4rem 0.65rem;
  cursor: pointer; background: var(--bg-0);
}
.ws-supp-hdr:hover { background: var(--bg-1); }
.ws-supp-name { flex: 1; font-size: 0.83rem; color: var(--text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ws-supp-del { background: none; border: none; color: var(--text-mute); cursor: pointer; font-size: 0.9rem; padding: 0 0.2rem; flex-shrink: 0; }
.ws-supp-del:hover { color: var(--red, #e05c5c); }
.ws-supp-body { display: none; padding: 0.5rem 0.65rem; border-top: 1px solid var(--border); }
.ws-supp-row.open .ws-supp-body { display: block; }
.ws-supp-prompt {
  width: 100%; background: var(--bg-1); border: 1px solid var(--border); border-radius: 4px;
  color: var(--text); font-size: 0.8rem; padding: 0.35rem 0.5rem; resize: vertical;
  font-family: inherit; box-sizing: border-box; min-height: 54px;
}
.ws-supp-prompt-btns { display: flex; justify-content: flex-end; margin-top: 0.3rem; gap: 0.4rem; }
.ws-supp-upload { display: flex; flex-direction: column; gap: 0.4rem; }
.ws-supp-upload label { font-size: 0.8rem; color: var(--text-mute); }
.ws-supp-upload input[type=file] { font-size: 0.8rem; color: var(--text); }
.ws-supp-upload textarea {
  width: 100%; background: var(--bg-0); border: 1px solid var(--border); border-radius: 4px;
  color: var(--text); font-size: 0.8rem; padding: 0.35rem 0.5rem; resize: vertical;
  font-family: inherit; box-sizing: border-box; min-height: 54px;
}
.ws-supp-upload .ws-draft-btns { margin-top: 0.3rem; }

/* --- Horizontal pane resizer (activity / terminal split) --- */
#h-resizer { height: 5px; background: var(--border); cursor: row-resize; flex-shrink: 0; }
#h-resizer:hover, #h-resizer.dragging { background: var(--accent); }

/* --- Terminal container --- */
#terminal-container {
  flex: 1; overflow: hidden; background: #0d1117; padding: 6px 4px; min-height: 80px;
}
#terminal-container .xterm { height: 100%; }
#terminal-container .xterm-viewport { overflow-y: hidden !important; }


.spinner { display: inline-block; width: 12px; height: 12px; border: 2px solid var(--border); border-top-color: var(--accent); border-radius: 50%; animation: spin 0.8s linear infinite; vertical-align: middle; margin-right: 0.3rem; }
.spinner-lg { display: inline-block; width: 22px; height: 22px; border: 3px solid var(--border); border-top-color: var(--accent); border-radius: 50%; animation: spin 0.8s linear infinite; vertical-align: middle; }
@keyframes spin { to { transform: rotate(360deg); } }

@media (max-width: 720px) {
  #main { flex-direction: column; }
  #chat-pane { width: 100% !important; border-right: none; border-bottom: 1px solid var(--border); min-height: 45%; }
  #chat-resizer { display: none; }
  #terminal-container { flex: 1; height: auto !important; }
}
`

const sshBody = `
<div id="toolbar">
  <span class="app-title">Servitor</span>
  <select id="appliance-select" onchange="selectAppliance(this.value)">
    <option value="">— select appliance —</option>
  </select>
  <button class="secondary" onclick="showApplianceModal(null)">New</button>
  <button class="secondary" id="btn-edit" onclick="editCurrentAppliance()" disabled>Edit</button>
  <button class="secondary" id="btn-profile" onclick="showProfile()" disabled>Profile</button>
  <button class="secondary" id="btn-facts" onclick="showFacts()" disabled>Facts <span id="facts-badge" style="display:none"></span></button>
  <button class="secondary" id="btn-rules" onclick="showRules()" disabled>Rules</button>
  <button class="secondary" id="btn-workspaces" onclick="showWorkspaces()" disabled>Workspaces</button>
  <button class="secondary" id="btn-clear-memory" onclick="clearMemory()" disabled title="Clear all memory: profile, knowledge docs, facts, notes, and techniques">Clear Memory</button>
  <button id="btn-map" onclick="runMap()" disabled>Map System</button>
  <button class="secondary" id="btn-mapapp" onclick="showMapAppModal()" disabled>Map App</button>
</div>
<div id="main">
  <div id="chat-pane">
    <div id="chat-header">
      <div class="hdr-info">
        <div class="hdr-title" id="chat-title">No appliance selected</div>
        <div class="hdr-meta" id="chat-meta"></div>
      </div>
      <div class="hdr-btns">
        <button id="btn-ws-draft" onclick="showDraftPanel()" title="Open workspace draft" style="display:none">Draft</button>
        <button onclick="clearChat()" title="Clear chat history">Clear</button>
      </div>
    </div>
    <div id="log-list-toggle" onclick="toggleLogList()" style="display:none">
      <span class="arrow" id="log-arrow">&#9654;</span>
      <span id="log-list-label">Log Files</span>
    </div>
    <div id="log-list"></div>
    <div id="chat-messages">
      <div class="chat-msg system">Select or create an appliance to begin.</div>
    </div>
    <div id="chat-input-area">
      <textarea id="chat-input" rows="2"
        placeholder="Ask about this system… (Enter to send, Shift+Enter for newline)"
        onkeydown="if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();sendChat();}"></textarea>
      <button id="chat-send" onclick="sendChat()" disabled>Send</button>
      <button id="chat-cancel" onclick="cancelChat()">Cancel</button>
      <span id="chat-working" style="display:none;margin:0 0.6rem;align-self:center" title="Working…"><span class="spinner-lg"></span></span>
    </div>
  </div>
  <div id="chat-resizer" onmousedown="startResize(event)"></div>
  <div id="activity-pane">
    <div id="activity-header">
      <span style="font-size:0.85rem;font-weight:600;color:var(--text-hi)">Activity</span>
      <div style="display:flex;gap:0.5rem;align-items:center">
        <span id="conn-status" style="font-size:0.72rem;color:var(--text-mute)"></span>
        <button id="btn-disconnect" onclick="disconnectAppliance()" style="display:none">Disconnect</button>
        <button id="btn-clear-activity" onclick="clearActivity()">Clear</button>
      </div>
    </div>
    <div id="activity-log">
      <div class="ev-status">Commands and results will appear here.</div>
    </div>
    <div id="h-resizer" onmousedown="startHResize(event)"></div>
    <div id="terminal-container"></div>
  </div>
</div>

<div id="overlay" onclick="hideModals()"></div>

<div id="appliance-modal">
  <h3 id="modal-title">New Appliance</h3>
  <div class="a-type-tabs">
    <button class="a-type-tab active" id="tab-ssh" onclick="setApplianceType('ssh')">SSH</button>
    <button class="a-type-tab" id="tab-command" onclick="setApplianceType('command')">Local Command</button>
  </div>
  <label>Name *</label>
  <input id="a-name" type="text" placeholder="e.g. web-prod-01">
  <div class="a-type-section active" id="section-ssh">
    <label>Host *</label>
    <input id="a-host" type="text" placeholder="hostname or IP address">
    <label>Port</label>
    <input id="a-port" type="number" value="22" min="1" max="65535">
    <label>User</label>
    <input id="a-user" type="text" placeholder="root">
    <label>Password</label>
    <input id="a-pass" type="password" placeholder="leave blank to use server SSH key (~/.ssh/id_rsa)">
    <div class="hint">Password is stored in the server database. If omitted, the server's default SSH key is used.</div>
  </div>
  <div class="a-type-section" id="section-command">
    <label>Command *</label>
    <input id="a-command" type="text" placeholder="e.g. kubectl, ./manage.py, /usr/local/bin/mycli">
    <div class="hint">The command name or path the AI will invoke. Arguments can be included.</div>
    <label>Working Directory</label>
    <input id="a-workdir" type="text" placeholder="e.g. /opt/myapp (optional)">
    <label>Environment Variables</label>
    <textarea id="a-envvars" rows="3" placeholder="KEY=VALUE (one per line, optional)"></textarea>
  </div>
  <label>Custom Instructions</label>
  <textarea id="a-instructions" rows="4" placeholder="Optional. Anything you want the AI to know: app-specific CLI tools, management commands, known quirks, workflow notes…"></textarea>
  <label>Persona</label>
  <div class="preset-chips" id="persona-chips">
    <span class="preset-chip" onclick="applyPersonaPreset('Support','You are acting as a senior support engineer diagnosing issues on this appliance. When given an error or symptom, trace it to its root cause: find the error in logs, identify the component that generated it, and follow the dependency chain back to the originating cause. Be targeted and precise — do not explore beyond what is relevant to the question.')">Support</span>
    <span class="preset-chip" onclick="applyPersonaPreset('QA','You are acting as a QA engineer validating application behavior on this appliance. Focus on testing functionality, verifying outputs match expectations, checking edge cases, and documenting unexpected behavior. Prioritize observable application behavior. Be systematic — document what you test and what you find.')">QA</span>
    <span class="preset-chip" onclick="applyPersonaPreset('DevOps','You are acting as a DevOps engineer working with this appliance. Focus on service health, deployment state, configuration correctness, resource utilization, and operational reliability. Check service dependencies and identify bottlenecks or misconfigurations.')">DevOps</span>
    <span class="preset-chip" onclick="applyPersonaPreset('Security','You are acting as a security engineer auditing this appliance. Look for misconfigurations, exposed credentials, overly permissive access, outdated software, unusual network exposure, and suspicious processes. Be thorough — document findings with evidence. Note what is secure as well as what needs attention.')">Security</span>
    <span class="preset-chip" onclick="clearPersona()">Clear</span>
  </div>
  <input id="a-persona-name" type="text" placeholder="Persona name (e.g. Support, QA, DevOps)" style="margin-bottom:0.4rem">
  <textarea id="a-persona-prompt" rows="3" placeholder="Describe how the AI should approach this appliance…"></textarea>
  <div class="btns">
    <button class="del-btn secondary" id="btn-delete-appliance" style="display:none" onclick="deleteAppliance()">Delete</button>
    <div class="right">
      <button class="secondary" onclick="hideModals()">Cancel</button>
      <button onclick="saveAppliance()">Save</button>
    </div>
  </div>
</div>

<div id="profile-panel">
  <h3 id="profile-title">System Profile</h3>
  <pre id="profile-content"></pre>
  <div class="btns">
    <button class="secondary" onclick="hideModals()">Close</button>
  </div>
</div>

<div id="facts-panel">
  <h3 id="facts-title">Persistent Facts</h3>
  <div id="facts-content"></div>
  <div style="margin-top:1rem;padding-top:0.75rem;border-top:1px solid var(--border)">
    <div style="font-size:0.8rem;font-weight:600;color:var(--text-mute);margin-bottom:0.5rem">Add Fact</div>
    <div style="display:flex;gap:0.4rem;margin-bottom:0.4rem">
      <input id="fact-key-input" type="text" placeholder="key (e.g. root_password)" style="flex:1">
      <input id="fact-val-input" type="text" placeholder="value" style="flex:2">
    </div>
    <div style="display:flex;justify-content:flex-end">
      <button onclick="addFact()">Add Fact</button>
    </div>
  </div>
  <div class="btns">
    <button class="secondary" onclick="hideModals()">Close</button>
  </div>
</div>

<div id="rules-panel">
  <h3>Standing Instructions</h3>
  <div class="hint" style="margin-bottom:0.75rem;font-size:0.82rem;color:var(--text-mute)">Rules the AI follows in every session with this appliance. Add them here or tell the AI during a conversation.</div>
  <div class="rules-list" id="rules-list"></div>
  <input id="new-rule-input" type="text" placeholder="e.g. Always check staging before production" onkeydown="if(event.key==='Enter'){addRule();}">
  <div class="btns">
    <button onclick="addRule()">Add Rule</button>
    <button class="secondary" onclick="hideModals()">Close</button>
  </div>
</div>

<div id="workspace-panel">
  <div class="panel-hdr">
    <div>
      <h3 id="ws-panel-title">Workspaces</h3>
      <div class="panel-sub" id="ws-panel-sub"></div>
    </div>
    <button class="panel-close" onclick="hideModals()">&#x2715;</button>
  </div>
  <div id="ws-list-view">
    <div class="ws-list" id="ws-list"></div>
  </div>
  <div id="ws-detail-view" style="display:none">
    <button class="ws-back" onclick="showWorkspaceList()">&#9664; All Workspaces</button>
    <div class="ws-section-label">Q&amp;A Entries</div>
    <div id="ws-entries"></div>
    <div class="ws-section-label">Reference Documents</div>
    <div class="ws-supp-list" id="ws-supp-list"></div>
    <div class="ws-supp-upload" id="ws-supp-upload">
      <label>File (PDF or plain text)</label>
      <input type="file" id="ws-supp-file" accept=".pdf,.txt,.md,.log,.csv,.yaml,.yml,.json,.conf,.ini,.toml">
      <label>Usage instruction — when and how the LLM should reference this document</label>
      <textarea id="ws-supp-prompt" placeholder="e.g. Use this config reference when answering questions about service settings."></textarea>
      <div class="ws-draft-btns">
        <button onclick="attachSupplement()">Attach Document</button>
      </div>
    </div>
    <div class="ws-section-label">Draft</div>
    <textarea id="ws-draft" placeholder="Notes and documentation draft will appear here. You can edit freely."></textarea>
    <div class="ws-draft-btns">
      <button class="secondary" onclick="hideModals()">Close</button>
      <button class="secondary" onclick="openDraftView()">Open for Copy</button>
      <button class="secondary" id="btn-generate-draft" onclick="generateDraft()">Generate Draft</button>
      <button onclick="saveDraft()">Save Draft</button>
    </div>
  </div>
</div>

<div id="mapapp-modal" style="display:none">
  <h3>Map Application</h3>
  <div class="hint">Enter the CLI command name to explore. Servitor will enumerate its subcommands and flags and save the reference to the knowledge base.</div>
  <label>Command *</label>
  <input id="mapapp-cmd" type="text" placeholder="e.g. kubectl, docker, haproxycfg"
    onkeydown="if(event.key==='Enter'){event.preventDefault();runMapApp();}">
  <div class="btns">
    <div class="right">
      <button class="secondary" onclick="hideModals()">Cancel</button>
      <button onclick="runMapApp()">Map</button>
    </div>
  </div>
</div>
`

const sshJS = `
var currentAppliance = null;
var chatHistory = [];
var activeSessionId = null;
var hasTechWriter = false;
var hasCodeWriter = false;
var currentWorkspaceId = null;
var currentWorkspaceName = null;
var lastUserQuestion = '';

fetch('api/save_destinations').then(r => r.ok ? r.json() : {}).then(function(d) {
  hasTechWriter = !!d.techwriter;
  hasCodeWriter = !!d.codewriter;
}).catch(function() {});
var activeEventSource = null;
var activeEventSourceErrors = 0;
var activeSessionKind = null; // 'chat' | 'map' | 'mapapp' — only 'chat' supports mid-flight interjection
var pendingConfirmSessionId = null;
var editingApplianceId = null;
var logListOpen = false;
var activeSpinner = null; // activity-log spinner element, removed on first real event
var lastEventTime = 0;   // ms timestamp of last received SSE event, for heartbeat detection
var heartbeatTimer = null;
var heartbeatEl = null;  // persistent "still processing" indicator element
var xtermInstance = null;
var xtermFit = null;
var xtermWs = null;
var termAutoReconnect = false;
var termReconnectTimer = null;
var termReconnectDelay = 2000; // ms, doubles on each failed attempt up to 16s

// Configure marked for safe rendering (no HTML pass-through from LLM).
if (typeof marked !== 'undefined') {
  marked.setOptions({ breaks: true, gfm: true });
}

// --- Boot ---
loadAppliances();

// openTerminalWhenReady retries until the terminal container has real pixel
// dimensions (i.e. the browser has finished its flex layout pass) before
// calling openTerminal(). Without this, FitAddon.fit() gets zero dimensions.
function openTerminalWhenReady(tries) {
  var cont = document.getElementById('terminal-container');
  if (cont.offsetHeight < 20) {
    if (tries < 30) setTimeout(function() { openTerminalWhenReady(tries + 1); }, 20);
    return;
  }
  openTerminal();
}

// --- xterm.js terminal ---

function makeWsUrl(path) {
  var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  var a = document.createElement('a');
  a.href = path;
  return proto + '//' + a.host + a.pathname + (a.search || '');
}

function scheduleTerminalReconnect() {
  if (!currentAppliance || !termAutoReconnect) return;
  clearTimeout(termReconnectTimer);
  var delay = termReconnectDelay;
  termReconnectDelay = Math.min(termReconnectDelay * 2, 16000);
  termReconnectTimer = setTimeout(function() {
    if (!currentAppliance || !termAutoReconnect) return;
    if (xtermInstance) xtermInstance.write('\x1b[2m[reconnecting…]\x1b[0m\r\n');
    openTerminal();
  }, delay);
}

function openTerminal() {
  // Reuse existing connection if still open.
  if (xtermWs && xtermWs.readyState === WebSocket.OPEN) return;
  // Tear down any dead socket without disabling reconnect.
  if (xtermWs) { xtermWs.onclose = null; xtermWs.onerror = null; xtermWs.close(); xtermWs = null; }
  termAutoReconnect = true;

  var cont = document.getElementById('terminal-container');
  cont.innerHTML = '';

  xtermInstance = new Terminal({
    theme: { background: '#0d1117', foreground: '#e6edf3', cursor: '#58a6ff', selectionBackground: '#264f78' },
    fontSize: 13,
    fontFamily: 'ui-monospace, Menlo, Consolas, "Courier New", monospace',
    cursorBlink: true,
    scrollback: 3000,
  });
  xtermFit = new FitAddon.FitAddon();
  xtermInstance.loadAddon(xtermFit);
  xtermInstance.open(cont);
  xtermFit.fit();

  var ro = new ResizeObserver(function() { if (xtermFit) xtermFit.fit(); });
  ro.observe(cont);

  xtermInstance.onResize(function(sz) {
    if (xtermWs && xtermWs.readyState === WebSocket.OPEN) {
      xtermWs.send(JSON.stringify({type:'resize', cols:sz.cols, rows:sz.rows}));
    }
  });
  xtermInstance.onData(function(data) {
    if (xtermWs && xtermWs.readyState === WebSocket.OPEN) {
      xtermWs.send(data);
    }
  });

  xtermWs = new WebSocket(makeWsUrl('api/terminal?id=' + encodeURIComponent(currentAppliance.id)));
  xtermWs.binaryType = 'arraybuffer';

  xtermWs.onopen = function() {
    termReconnectDelay = 2000; // reset backoff on successful connect
    clearTimeout(termReconnectTimer);
    xtermWs.send(JSON.stringify({type:'resize', cols:xtermInstance.cols, rows:xtermInstance.rows}));
  };
  xtermWs.onmessage = function(e) {
    if (e.data instanceof ArrayBuffer) {
      xtermInstance.write(new Uint8Array(e.data));
    } else {
      xtermInstance.write(e.data);
    }
  };
  xtermWs.onclose = function() {
    if (xtermInstance) xtermInstance.write('\r\n\x1b[2m[session closed]\x1b[0m\r\n');
    scheduleTerminalReconnect();
  };
  xtermWs.onerror = function() {
    if (xtermInstance) xtermInstance.write('\r\n\x1b[31m[connection error]\x1b[0m\r\n');
  };
}

function closeTerminal() {
  termAutoReconnect = false;
  clearTimeout(termReconnectTimer);
  termReconnectTimer = null;
  termReconnectDelay = 2000;
  if (xtermWs) { xtermWs.onclose = null; xtermWs.onerror = null; xtermWs.close(); xtermWs = null; }
  if (xtermInstance) { xtermInstance.dispose(); xtermInstance = null; xtermFit = null; }
}

// --- Appliance management ---

function loadAppliances() {
  var params = new URLSearchParams(window.location.search);
  var urlAppliance = params.get('run') || params.get('appliance');
  var urlSession = params.get('session');
  fetch('api/appliances').then(function(r) { return r.json(); }).then(function(items) {
    var sel = document.getElementById('appliance-select');
    var prev = sel.value;
    sel.innerHTML = '<option value="">— select appliance —</option>';
    (items || []).sort(function(a,b){ return a.name.localeCompare(b.name); }).forEach(function(a) {
      var opt = document.createElement('option');
      opt.value = a.id;
      opt.textContent = a.name + ' (' + a.host + ')';
      sel.appendChild(opt);
    });
    var restoreId = urlAppliance || prev;
    if (restoreId && items.some(function(a) { return a.id === restoreId; })) {
      sel.value = restoreId;
      if (urlSession) {
        selectAppliance(restoreId, function() { openEventStream(urlSession); });
      }
    }
  });
}

function selectAppliance(id, afterLoad) {
  if (!id) {
    currentAppliance = null;
    closeTerminal();
    updateUI();
    return;
  }
  fetch('api/appliance/' + encodeURIComponent(id)).then(function(r) { return r.json(); }).then(function(a) {
    if (currentAppliance && currentAppliance.id !== a.id) { closeTerminal(); clearChat(); }
    currentAppliance = a;
    updateUI();
    openTerminalWhenReady(0);
    if (afterLoad) afterLoad();
  });
}

function refreshCurrentAppliance() {
  if (!currentAppliance) return;
  fetch('api/appliance/' + encodeURIComponent(currentAppliance.id))
    .then(function(r) { return r.json(); }).then(function(a) {
      currentAppliance = a;
      updateUI();
    }).catch(function(){});
}

function disconnectAppliance() {
  if (!currentAppliance) return;
  closeTerminal();
  fetch('api/disconnect', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({appliance_id: currentAppliance.id})
  }).then(function() {
    /* connection status shown in terminal — no activity entry needed */
  }).catch(function(){});
}

function updateUI() {
  var has = !!currentAppliance;
  document.getElementById('btn-edit').disabled = !has;
  document.getElementById('btn-profile').disabled = !has || !currentAppliance.profile;
  document.getElementById('btn-facts').disabled = !has;
  document.getElementById('btn-rules').disabled = !has;
  document.getElementById('btn-workspaces').disabled = !has;
  document.getElementById('btn-clear-memory').disabled = !has;
  document.getElementById('btn-map').disabled = !has;
  document.getElementById('btn-mapapp').disabled = !has;
  document.getElementById('chat-send').disabled = !has;
  document.getElementById('btn-disconnect').style.display = has ? '' : 'none';
  if (has) refreshFactsBadge(); else document.getElementById('facts-badge').style.display = 'none';

  var isCmd = has && currentAppliance.type === 'command';
  // Terminal pane is SSH-only.
  document.getElementById('terminal-container').style.display = isCmd ? 'none' : '';
  document.getElementById('h-resizer').style.display = isCmd ? 'none' : '';
  if (has) {
    var subtitle = isCmd ? 'local command' : currentAppliance.host;
    var personaBadge = currentAppliance.persona_name ? ' <span class="persona-badge">' + escapeHtml(currentAppliance.persona_name) + '</span>' : '';
    document.getElementById('chat-title').innerHTML = escapeHtml(currentAppliance.name + ' (' + subtitle + ')') + personaBadge;
    document.getElementById('btn-map').textContent = isCmd ? 'Map Command' : 'Map System';
    document.getElementById('chat-meta').textContent = currentAppliance.scanned
      ? 'Mapped ' + new Date(currentAppliance.scanned).toLocaleString()
      : isCmd ? 'Not yet mapped — click Map Command to profile this tool.'
              : 'Not yet mapped — click Map System to profile this appliance.';
    if (!isCmd) renderLogList(currentAppliance.log_map || []);
    else renderLogList([]);
  } else {
    document.getElementById('btn-map').textContent = 'Map System';
    document.getElementById('chat-title').textContent = 'No appliance selected';
    document.getElementById('chat-meta').textContent = '';
    renderLogList([]);
  }
}

function renderLogList(logs) {
  var toggle = document.getElementById('log-list-toggle');
  var list = document.getElementById('log-list');
  if (!logs || logs.length === 0) {
    toggle.style.display = 'none';
    list.style.display = 'none';
    list.innerHTML = '';
    return;
  }
  toggle.style.display = 'flex';
  document.getElementById('log-list-label').textContent = 'Log Files (' + logs.length + ')';
  var html = '';
  logs.forEach(function(e) {
    html += '<div class="log-entry" onclick="queryLog(' + JSON.stringify(e.path) + ',' + JSON.stringify(e.desc) + ')" title="Click to query this log">'
      + '<span class="log-svc">' + escapeHtml(e.service) + '</span>'
      + '<span class="log-path">' + escapeHtml(e.path) + '</span>'
      + '</div>';
  });
  list.innerHTML = html;
  if (logListOpen) list.style.display = 'block';
}

function toggleLogList() {
  logListOpen = !logListOpen;
  var list = document.getElementById('log-list');
  var arrow = document.getElementById('log-arrow');
  list.style.display = logListOpen ? 'block' : 'none';
  arrow.classList.toggle('open', logListOpen);
}

function queryLog(path, desc) {
  var input = document.getElementById('chat-input');
  input.value = 'Show recent errors and warnings from ' + path;
  input.focus();
}

function applyPersonaPreset(name, prompt) {
  document.getElementById('a-persona-name').value = name;
  document.getElementById('a-persona-prompt').value = prompt;
  document.querySelectorAll('#persona-chips .preset-chip').forEach(function(c) {
    c.classList.toggle('active', c.textContent === name);
  });
}

function clearPersona() {
  document.getElementById('a-persona-name').value = '';
  document.getElementById('a-persona-prompt').value = '';
  document.querySelectorAll('#persona-chips .preset-chip').forEach(function(c) {
    c.classList.remove('active');
  });
}

function setApplianceType(t) {
  document.getElementById('section-ssh').classList.toggle('active', t === 'ssh');
  document.getElementById('section-command').classList.toggle('active', t === 'command');
  document.getElementById('tab-ssh').classList.toggle('active', t === 'ssh');
  document.getElementById('tab-command').classList.toggle('active', t === 'command');
}

function showApplianceModal(id) {
  editingApplianceId = id;
  document.getElementById('modal-title').textContent = id ? 'Edit Appliance' : 'New Appliance';
  document.getElementById('a-name').value = '';
  document.getElementById('a-host').value = '';
  document.getElementById('a-port').value = '22';
  document.getElementById('a-user').value = 'root';
  document.getElementById('a-pass').value = '';
  document.getElementById('a-command').value = '';
  document.getElementById('a-workdir').value = '';
  document.getElementById('a-envvars').value = '';
  document.getElementById('a-instructions').value = '';
  document.getElementById('a-persona-name').value = '';
  document.getElementById('a-persona-prompt').value = '';
  document.getElementById('btn-delete-appliance').style.display = 'none';
  var atype = 'ssh';
  if (id && currentAppliance && currentAppliance.id === id) {
    atype = currentAppliance.type || 'ssh';
    document.getElementById('a-name').value = currentAppliance.name || '';
    document.getElementById('a-host').value = currentAppliance.host || '';
    document.getElementById('a-port').value = currentAppliance.port || 22;
    document.getElementById('a-user').value = currentAppliance.user || 'root';
    document.getElementById('a-command').value = currentAppliance.command || '';
    document.getElementById('a-workdir').value = currentAppliance.work_dir || '';
    document.getElementById('a-envvars').value = (currentAppliance.env_vars || []).join('\n');
    document.getElementById('a-instructions').value = currentAppliance.instructions || '';
    document.getElementById('a-persona-name').value = currentAppliance.persona_name || '';
    document.getElementById('a-persona-prompt').value = currentAppliance.persona_prompt || '';
    var pname = currentAppliance.persona_name || '';
    document.querySelectorAll('#persona-chips .preset-chip').forEach(function(c) {
      c.classList.toggle('active', c.textContent === pname);
    });
    document.getElementById('btn-delete-appliance').style.display = '';
  }
  setApplianceType(atype);
  document.getElementById('appliance-modal').style.display = 'block';
  document.getElementById('overlay').style.display = 'block';
  setTimeout(function() { document.getElementById('a-name').focus(); }, 50);
}

function editCurrentAppliance() {
  if (currentAppliance) showApplianceModal(currentAppliance.id);
}

function saveAppliance() {
  var name = document.getElementById('a-name').value.trim();
  var isCmd = document.getElementById('tab-command').classList.contains('active');
  var atype = isCmd ? 'command' : 'ssh';
  if (!name) { alert('Name is required.'); return; }
  var body = { name: name, type: atype, instructions: document.getElementById('a-instructions').value.trim(), persona_name: document.getElementById('a-persona-name').value.trim(), persona_prompt: document.getElementById('a-persona-prompt').value.trim() };
  if (isCmd) {
    var cmd = document.getElementById('a-command').value.trim();
    if (!cmd) { alert('Command is required.'); return; }
    body.command = cmd;
    body.work_dir = document.getElementById('a-workdir').value.trim();
    var envRaw = document.getElementById('a-envvars').value;
    body.env_vars = envRaw.split('\n').map(function(s){return s.trim();}).filter(function(s){return s.length > 0;});
  } else {
    var host = document.getElementById('a-host').value.trim();
    if (!host) { alert('Host is required.'); return; }
    body.host = host;
    body.port = parseInt(document.getElementById('a-port').value) || 22;
    body.user = document.getElementById('a-user').value.trim() || 'root';
    body.password = document.getElementById('a-pass').value;
  }
  if (editingApplianceId) body.id = editingApplianceId;
  fetch('api/appliances', {
    method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)
  }).then(function(r) { return r.json(); }).then(function(a) {
    hideModals();
    loadAppliances();
    currentAppliance = a;
    document.getElementById('appliance-select').value = a.id;
    updateUI();
  }).catch(function(err) { alert('Save failed: ' + err.message); });
}

function deleteAppliance() {
  if (!editingApplianceId) return;
  if (!confirm('Delete this appliance and its stored profile?')) return;
  fetch('api/appliance/' + encodeURIComponent(editingApplianceId), {method: 'DELETE'}).then(function() {
    hideModals();
    currentAppliance = null;
    loadAppliances();
    updateUI();
  });
}

function showProfile() {
  if (!currentAppliance || !currentAppliance.profile) return;
  document.getElementById('profile-title').textContent = currentAppliance.name + ' — System Profile';
  document.getElementById('profile-content').textContent = currentAppliance.profile;
  document.getElementById('profile-panel').style.display = 'block';
  document.getElementById('overlay').style.display = 'block';
}

function hideModals() {
  document.getElementById('overlay').style.display = 'none';
  document.getElementById('appliance-modal').style.display = 'none';
  document.getElementById('profile-panel').style.display = 'none';
  document.getElementById('facts-panel').style.display = 'none';
  document.getElementById('rules-panel').style.display = 'none';
  document.getElementById('mapapp-modal').style.display = 'none';
  document.getElementById('workspace-panel').style.display = 'none';
}

function showFacts() {
  if (!currentAppliance) return;
  fetch('api/facts?id=' + encodeURIComponent(currentAppliance.id))
    .then(function(r) { return r.json(); }).then(function(facts) {
      document.getElementById('facts-title').textContent = currentAppliance.name + ' — Persistent Facts';
      var el = document.getElementById('facts-content');
      if (!facts || facts.length === 0) {
        el.innerHTML = '<div style="color:var(--text-mute);font-size:0.85rem;padding:0.5rem 0">No facts stored yet. Run a system map to populate the knowledge base.</div>';
      } else {
        var html = '<div class="facts-list">';
        facts.forEach(function(f) {
          var date = f.updated ? new Date(f.updated).toLocaleString() : '';
          var tags = (f.tags || []).map(function(t) {
            return '<span class="fact-tag">' + escapeHtml(t) + '</span>';
          }).join('');
          html += '<div class="fact-card">'
            + '<div class="fact-header">'
            +   '<span class="fact-key">' + escapeHtml(f.key) + '</span>'
            +   '<span style="display:flex;align-items:center;gap:0.25rem;flex-shrink:0">'
            +     '<span class="fact-date">' + escapeHtml(date) + '</span>'
            +     '<button class="fact-delete" title="Delete fact" onclick="deleteFact(\'' + escapeHtml(f.id) + '\')">×</button>'
            +   '</span>'
            + '</div>'
            + '<div class="fact-value">' + escapeHtml(f.value) + '</div>'
            + (tags ? '<div class="fact-tags">' + tags + '</div>' : '')
            + '</div>';
        });
        html += '</div>';
        el.innerHTML = html;
      }
      document.getElementById('facts-panel').style.display = 'block';
      document.getElementById('overlay').style.display = 'block';
    }).catch(function(err) { alert('Failed to load facts: ' + err.message); });
}

function deleteFact(id) {
  fetch('api/facts?key=' + encodeURIComponent(id), {method: 'DELETE'})
    .then(function(r) {
      if (!r.ok) throw new Error('delete failed');
      showFacts();
      refreshFactsBadge();
    }).catch(function(err) { alert('Failed to delete fact: ' + err.message); });
}

function addFact() {
  if (!currentAppliance) return;
  var key = document.getElementById('fact-key-input').value.trim();
  var val = document.getElementById('fact-val-input').value.trim();
  if (!key || !val) { alert('Key and value are required.'); return; }
  fetch('api/facts', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({appliance_id: currentAppliance.id, key: key, value: val})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    document.getElementById('fact-key-input').value = '';
    document.getElementById('fact-val-input').value = '';
    showFacts();
    refreshFactsBadge();
  }).catch(function(err) { alert('Failed to add fact: ' + err.message); });
}

function showRules() {
  if (!currentAppliance) return;
  fetch('api/rules?appliance_id=' + encodeURIComponent(currentAppliance.id))
    .then(function(r) { return r.json(); }).then(function(rules) {
      renderRulesList(rules || []);
      document.getElementById('new-rule-input').value = '';
      document.getElementById('rules-panel').style.display = 'block';
      document.getElementById('overlay').style.display = 'block';
    }).catch(function(err) { alert('Failed to load rules: ' + err.message); });
}

function renderRulesList(rules) {
  var el = document.getElementById('rules-list');
  if (!rules.length) {
    el.innerHTML = '<div style="color:var(--text-mute);font-size:0.82rem;padding:0.25rem 0">No rules yet.</div>';
    return;
  }
  el.innerHTML = rules.map(function(r) {
    return '<div class="rule-row">'
      + '<span class="rule-text">' + escapeHtml(r.rule) + '</span>'
      + '<button class="rule-delete" title="Delete rule" onclick="deleteRule(\'' + escapeHtml(r.id) + '\')">×</button>'
      + '</div>';
  }).join('');
}

function addRule() {
  if (!currentAppliance) return;
  var input = document.getElementById('new-rule-input');
  var rule = input.value.trim();
  if (!rule) return;
  fetch('api/rules', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({appliance_id: currentAppliance.id, rule: rule})
  }).then(function(r) {
    if (!r.ok) throw new Error('save failed');
    input.value = '';
    return fetch('api/rules?appliance_id=' + encodeURIComponent(currentAppliance.id)).then(r => r.json());
  }).then(function(rules) {
    renderRulesList(rules || []);
  }).catch(function(err) { alert('Failed to add rule: ' + err.message); });
}

function deleteRule(id) {
  fetch('api/rules/' + encodeURIComponent(id), {method: 'DELETE'})
    .then(function() {
      return fetch('api/rules?appliance_id=' + encodeURIComponent(currentAppliance.id)).then(r => r.json());
    }).then(function(rules) {
      renderRulesList(rules || []);
    }).catch(function(err) { alert('Failed to delete rule: ' + err.message); });
}

// --- Workspaces ---

function showWorkspaces() {
  if (!currentAppliance) return;
  document.getElementById('ws-panel-title').textContent = currentAppliance.name + ' — Workspaces';
  document.getElementById('ws-panel-sub').textContent = '';
  document.getElementById('ws-list-view').style.display = 'block';
  document.getElementById('ws-detail-view').style.display = 'none';
  fetch('api/workspace/list?appliance_id=' + encodeURIComponent(currentAppliance.id))
    .then(function(r) { return r.json(); })
    .then(function(list) { renderWorkspaceList(list || []); })
    .catch(function() { renderWorkspaceList([]); });
  document.getElementById('workspace-panel').style.display = 'block';
  document.getElementById('overlay').style.display = 'block';
}

function renderWorkspaceList(list) {
  var el = document.getElementById('ws-list');
  if (!list || list.length === 0) {
    el.innerHTML = '<div class="ws-empty">No workspaces yet.<br>Use "Create Workspace" after a chat reply to start one.</div>';
    return;
  }
  el.innerHTML = '';
  list.forEach(function(ws) {
    var row = document.createElement('div');
    row.className = 'ws-row' + (ws.id === currentWorkspaceId ? ' active' : '');
    var date = ws.updated ? new Date(ws.updated).toLocaleDateString() : '';
    var entryWord = ws.entries === 1 ? 'entry' : 'entries';
    row.innerHTML = '<div class="ws-row-info"><div class="ws-row-name">' + escapeHtml(ws.name) + '</div>'
      + '<div class="ws-row-meta">' + ws.entries + ' ' + entryWord + (date ? ' &middot; ' + escapeHtml(date) : '') + '</div></div>'
      + '<button class="ws-row-del" title="Delete" onclick="event.stopPropagation();deleteWorkspaceItem(\'' + escapeHtml(ws.id) + '\')">&#x2715;</button>';
    row.onclick = function() { loadWorkspaceIntoChat(ws.id); };
    el.appendChild(row);
  });
}

// loadWorkspaceIntoChat closes the modal and replays the workspace Q&A into
// the chat pane, restoring full conversation context.
function loadWorkspaceIntoChat(id) {
  fetch('api/workspace/' + encodeURIComponent(id))
    .then(function(r) { return r.json(); })
    .then(function(ws) {
      hideModals();
      // Set workspace state.
      currentWorkspaceId = ws.id;
      currentWorkspaceName = ws.name;
      // Rebuild chat pane from workspace entries.
      chatHistory = [];
      var msgs = document.getElementById('chat-messages');
      msgs.innerHTML = '';
      if (ws.entries && ws.entries.length > 0) {
        ws.entries.forEach(function(e) {
          addChatMsg('user', escapeHtml(e.question));
          chatHistory.push({role: 'user', content: e.question});
          // rawText passed so action buttons render; lastUserQuestion set for each entry.
          lastUserQuestion = e.question;
          addChatMsg('assistant', formatChat(e.answer), e.answer);
          chatHistory.push({role: 'assistant', content: e.answer});
        });
      } else {
        msgs.innerHTML = '<div class="chat-msg system">Workspace loaded. Ask a follow-up question to continue.</div>';
      }
      // Show workspace name in header and reveal Draft button.
      document.getElementById('chat-title').textContent = ws.name;
      document.getElementById('chat-meta').textContent = currentAppliance ? currentAppliance.name : '';
      document.getElementById('btn-ws-draft').style.display = 'inline-block';
      // Store draft and supplements for the draft panel.
      document.getElementById('ws-draft').value = ws.draft || '';
      renderSupplements(ws.supplements || []);
      // Scroll to bottom.
      msgs.scrollTop = msgs.scrollHeight;
    })
    .catch(function(err) { alert('Failed to load workspace: ' + err.message); });
}

function showDraftPanel() {
  if (!currentWorkspaceId) return;
  document.getElementById('ws-panel-title').textContent = currentWorkspaceName || 'Workspace';
  document.getElementById('ws-panel-sub').textContent = 'Draft';
  document.getElementById('ws-list-view').style.display = 'none';
  document.getElementById('ws-detail-view').style.display = 'block';
  document.getElementById('workspace-panel').style.display = 'block';
  document.getElementById('overlay').style.display = 'block';
}

function deleteWorkspaceItem(id) {
  if (!confirm('Delete this workspace?')) return;
  fetch('api/workspace/' + encodeURIComponent(id), {method: 'DELETE'})
    .then(function() {
      if (id === currentWorkspaceId) {
        currentWorkspaceId = null;
        currentWorkspaceName = null;
        document.getElementById('btn-ws-draft').style.display = 'none';
        clearChat();
      }
      showWorkspaces();
    })
    .catch(function(err) { alert('Delete failed: ' + err.message); });
}

function saveDraft() {
  if (!currentWorkspaceId) return;
  var draft = document.getElementById('ws-draft').value;
  var btn = document.querySelector('#ws-detail-view .ws-draft-btns button:last-child');
  if (btn) { btn.textContent = 'Saving…'; btn.disabled = true; }
  fetch('api/workspace/draft', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({workspace_id: currentWorkspaceId, draft: draft})
  }).then(function(r) {
    if (!r.ok) throw new Error('save failed');
    if (btn) { btn.textContent = 'Saved'; setTimeout(function() { btn.textContent = 'Save Draft'; btn.disabled = false; }, 1200); }
  }).catch(function(err) {
    if (btn) { btn.textContent = 'Save Draft'; btn.disabled = false; }
    alert('Save draft failed: ' + err.message);
  });
}

// renderSupplements populates the supplements list in the draft panel.
var suppPollTimer = null;
function pollSupplemetsIfNeeded(list) {
  if (suppPollTimer) { clearTimeout(suppPollTimer); suppPollTimer = null; }
  if (!list || !list.some(function(s) { return s.processing; })) return;
  suppPollTimer = setTimeout(function() {
    if (!currentWorkspaceId) return;
    fetch('api/workspace/' + encodeURIComponent(currentWorkspaceId))
      .then(function(r) { return r.json(); })
      .then(function(ws) { renderSupplements(ws.supplements || []); })
      .catch(function() {});
  }, 5000);
}

function renderSupplements(list) {
  var el = document.getElementById('ws-supp-list');
  if (!el) return;
  if (!list || list.length === 0) { el.innerHTML = ''; return; }
  pollSupplemetsIfNeeded(list);
  var html = '';
  list.forEach(function(s) {
    var statusBadge = s.processing
      ? '<span style="font-size:0.72rem;color:var(--accent);margin-right:0.4rem"><span class="spinner"></span>Analyzing…</span>'
      : (s.content ? '<span style="font-size:0.72rem;color:var(--text-mute);margin-right:0.4rem">' + Math.round(s.content.length/1024) + ' KB</span>' : '');
    html += '<div class="ws-supp-row" id="supp-' + s.id + '">' +
      '<div class="ws-supp-hdr" onclick="toggleSupp(\'' + s.id + '\')">' +
        '<span class="ws-supp-name" title="' + escapeHtml(s.name) + '">' + escapeHtml(s.name) + '</span>' +
        statusBadge +
        '<button class="ws-supp-del" onclick="event.stopPropagation();deleteSupplement(\'' + s.id + '\')" title="Remove">&#x2715;</button>' +
      '</div>' +
      '<div class="ws-supp-body">' +
        '<label style="font-size:0.78rem;color:var(--text-mute)">Usage instruction</label>' +
        '<textarea class="ws-supp-prompt" id="supp-prompt-' + s.id + '" placeholder="How/when should the LLM reference this document?">' + escapeHtml(s.sub_prompt || '') + '</textarea>' +
        '<div class="ws-supp-prompt-btns">' +
          '<button onclick="saveSupplementPrompt(\'' + s.id + '\')">Save Instruction</button>' +
        '</div>' +
      '</div>' +
    '</div>';
  });
  el.innerHTML = html;
}

function toggleSupp(id) {
  var row = document.getElementById('supp-' + id);
  if (row) row.classList.toggle('open');
}

function attachSupplement() {
  if (!currentWorkspaceId) return;
  var fileEl = document.getElementById('ws-supp-file');
  var promptEl = document.getElementById('ws-supp-prompt');
  if (!fileEl.files || !fileEl.files.length) { alert('Select a file first.'); return; }
  var btn = document.querySelector('#ws-supp-upload .ws-draft-btns button');
  if (btn) { btn.textContent = 'Attaching…'; btn.disabled = true; }
  var fd = new FormData();
  fd.append('workspace_id', currentWorkspaceId);
  fd.append('sub_prompt', promptEl.value.trim());
  fd.append('file', fileEl.files[0]);
  fetch('api/workspace/supplement/add', {method: 'POST', body: fd})
    .then(function(r) {
      if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
      return r.json();
    })
    .then(function() {
      // Reload workspace to get updated supplement list.
      return fetch('api/workspace/' + encodeURIComponent(currentWorkspaceId)).then(function(r) { return r.json(); });
    })
    .then(function(ws) {
      renderSupplements(ws.supplements || []);
      fileEl.value = '';
      promptEl.value = '';
      if (btn) { btn.textContent = 'Attach Document'; btn.disabled = false; }
    })
    .catch(function(err) {
      if (btn) { btn.textContent = 'Attach Document'; btn.disabled = false; }
      alert('Attach failed: ' + err.message);
    });
}

function deleteSupplement(suppId) {
  if (!currentWorkspaceId || !confirm('Remove this document from the workspace?')) return;
  fetch('api/workspace/supplement/delete', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({workspace_id: currentWorkspaceId, supplement_id: suppId})
  }).then(function(r) {
    if (!r.ok) throw new Error('delete failed');
    var row = document.getElementById('supp-' + suppId);
    if (row) row.remove();
  }).catch(function(err) { alert('Remove failed: ' + err.message); });
}

function saveSupplementPrompt(suppId) {
  if (!currentWorkspaceId) return;
  var ta = document.getElementById('supp-prompt-' + suppId);
  if (!ta) return;
  var btn = ta.parentElement.querySelector('button');
  if (btn) { btn.textContent = 'Saving…'; btn.disabled = true; }
  fetch('api/workspace/supplement/prompt', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({workspace_id: currentWorkspaceId, supplement_id: suppId, sub_prompt: ta.value.trim()})
  }).then(function(r) {
    if (!r.ok) throw new Error('save failed');
    if (btn) { btn.textContent = 'Saved'; setTimeout(function() { btn.textContent = 'Save Instruction'; btn.disabled = false; }, 1200); }
  }).catch(function(err) {
    if (btn) { btn.textContent = 'Save Instruction'; btn.disabled = false; }
    alert('Save failed: ' + err.message);
  });
}

function openDraftView() {
  if (!currentWorkspaceId) return;
  window.open('api/workspace/view?id=' + encodeURIComponent(currentWorkspaceId), '_blank');
}

function generateDraft() {
  if (!currentWorkspaceId) return;
  var btn = document.getElementById('btn-generate-draft');
  if (btn) { btn.textContent = 'Generating…'; btn.disabled = true; }
  fetch('api/workspace/synthesize', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({workspace_id: currentWorkspaceId})
  }).then(function(r) {
    if (!r.ok) { return r.text().then(function(t) { throw new Error(t || r.status); }); }
    return r.json();
  }).then(function(data) {
    openEventStream(data.session_id);
  }).catch(function(err) {
    if (btn) { btn.textContent = 'Generate Draft'; btn.disabled = false; }
    alert('Synthesis failed: ' + err.message);
  });
}

function refreshFactsBadge() {
  if (!currentAppliance) return;
  fetch('api/facts?id=' + encodeURIComponent(currentAppliance.id))
    .then(function(r) { return r.json(); }).then(function(facts) {
      var badge = document.getElementById('facts-badge');
      if (facts && facts.length > 0) {
        badge.textContent = facts.length;
        badge.style.display = 'inline-block';
      } else {
        badge.style.display = 'none';
      }
    }).catch(function(){});
}

// --- Event streaming ---

function pushSessionURL(sid) {
  var params = new URLSearchParams(window.location.search);
  if (currentAppliance) params.set('run', currentAppliance.id);
  if (sid) { params.set('session', sid); } else { params.delete('session'); }
  window.history.replaceState({}, '', window.location.pathname + '?' + params.toString());
}

function openEventStream(sid) {
  closeEventStream();
  activeSessionId = sid;
  pushSessionURL(sid);
  // A live session means cancel + the working spinner should be
  // visible. The three submit handlers already do this before the
  // fetch returns; this redundancy is what gets the resume path
  // (loadAppliances → ?run=&session=) into the same UI state.
  document.getElementById('chat-cancel').style.display = 'inline-block';
  document.getElementById('chat-working').style.display = 'inline-flex';
  activeEventSourceErrors = 0;
  lastEventTime = Date.now();
  activeEventSource = new EventSource('api/events?id=' + encodeURIComponent(sid));
  activeEventSource.onopen = function() {
    activeEventSourceErrors = 0;
  };
  activeEventSource.onmessage = function(e) {
    activeEventSourceErrors = 0;
    lastEventTime = Date.now();
    dismissHeartbeat();
    try { handleEvent(JSON.parse(e.data)); } catch(err) {}
  };
  activeEventSource.onerror = function() {
    activeEventSourceErrors++;
    if (activeEventSourceErrors >= 5) { closeEventStream(); }
  };
  startHeartbeatWatch();
}

function closeEventStream() {
  if (activeEventSource) { activeEventSource.close(); activeEventSource = null; }
  activeEventSourceErrors = 0;
  stopHeartbeatWatch();
  dismissHeartbeat();
}

function startHeartbeatWatch() {
  stopHeartbeatWatch();
  heartbeatTimer = setInterval(function() {
    if (!activeSessionId) { stopHeartbeatWatch(); return; }
    var elapsed = Date.now() - lastEventTime;
    if (elapsed > 28000) {
      if (!heartbeatEl) {
        heartbeatEl = appendActivity('<div class="ev-status" id="heartbeat-indicator"><span class="spinner"></span>Still processing… (' + Math.round(elapsed/1000) + 's)</div>');
      } else {
        var inner = heartbeatEl.querySelector('.spinner');
        heartbeatEl.textContent = 'Still processing… (' + Math.round(elapsed/1000) + 's)';
        if (inner) heartbeatEl.insertBefore(inner, heartbeatEl.firstChild);
      }
    }
  }, 5000);
}

function stopHeartbeatWatch() {
  if (heartbeatTimer) { clearInterval(heartbeatTimer); heartbeatTimer = null; }
}

function dismissHeartbeat() {
  if (heartbeatEl) { heartbeatEl.remove(); heartbeatEl = null; }
}

function dismissSpinner() {
  if (activeSpinner) { activeSpinner.remove(); activeSpinner = null; }
}

function handleEvent(ev) {
  switch (ev.kind) {
    case 'status': {
      dismissSpinner();
      var t = ev.text || '';
      var isConnectMsg = t === 'Connected.' || t.startsWith('SSH reconnected');
      if (!isConnectMsg) appendActivity('<div class="ev-status">' + escapeHtml(t) + '</div>');
      break;
    }
    case 'cmd':
      dismissSpinner();
      appendActivity('<div class="ev-cmd"><span>' + escapeHtml(ev.text) + '</span></div>');
      break;
    case 'output': {
      var d = document.createElement('div');
      d.className = 'ev-output collapsed';
      d.textContent = ev.text;
      d.addEventListener('click', function() { this.classList.toggle('collapsed'); });
      document.getElementById('activity-log').appendChild(d);
      scrollActivity();
      break;
    }
    case 'message': {
      var d = document.createElement('div');
      d.className = 'ev-message';
      d.textContent = ev.text;
      document.getElementById('activity-log').appendChild(d);
      scrollActivity();
      break;
    }
    case 'plan_set':
    case 'plan_step': {
      // Investigator's plan — render or update the checklist in the
      // left pane (chat-messages). One container per session, replaced
      // on each plan event so status updates show in place rather than
      // appending duplicates.
      renderPlan(ev.plan || []);
      break;
    }
    case 'intent': {
      // Orchestrator delegating a probe — render the strategic
      // narration in the LEFT pane (chat-messages) where it's
      // visible during investigation, while detailed cmd/output
      // continues to scroll in the right activity pane. Gives the
      // user something to read during the bulk of work and turns
      // the chat history into a strategy timeline that ends with
      // the final answer.
      var html = '<div class="intent-label">&#9656; Investigating</div>'
        + '<div class="intent-task">' + escapeHtml(ev.text || '') + '</div>';
      if (ev.reason) {
        html += '<div class="intent-context">' + escapeHtml(ev.reason) + '</div>';
      }
      var msgs = document.getElementById('chat-messages');
      var div = document.createElement('div');
      div.className = 'chat-msg intent';
      div.innerHTML = html;
      msgs.appendChild(div);
      msgs.scrollTop = msgs.scrollHeight;
      break;
    }
    case 'confirm': {
      pendingConfirmSessionId = activeSessionId;
      var sid = activeSessionId;
      var html = '<div class="ev-confirm" id="confirm-' + escapeHtml(sid) + '">'
        + '<div class="warn-label">&#9888; Destructive Command</div>'
        + '<div class="warn-cmd">$ ' + escapeHtml(ev.text) + '</div>'
        + '<div class="warn-reason">Reason: ' + escapeHtml(ev.reason || '') + '</div>'
        + '<div class="confirm-btns">'
        + '<button class="confirm-allow" onclick="confirmCmd(\'allow\')">Allow</button>'
        + '<button class="confirm-always" onclick="confirmCmd(\'always\')" title="Allow now and skip this prompt for this exact command in future sessions">Always</button>'
        + '<button class="confirm-deny" onclick="confirmCmd(\'deny\')">Deny</button>'
        + '</div></div>';
      appendActivity(html);
      break;
    }
    case 'confirm_technique': {
      pendingConfirmSessionId = activeSessionId;
      var sid = activeSessionId;
      var html = '<div class="ev-confirm-technique" id="confirm-' + escapeHtml(sid) + '">'
        + '<div class="tech-label">Save technique?</div>'
        + '<div class="tech-text">' + escapeHtml(ev.text) + '</div>'
        + '<div class="confirm-btns">'
        + '<button class="confirm-save" onclick="confirmCmd(\'allow\')">Save</button>'
        + '<button onclick="confirmCmd(\'deny\')">Skip</button>'
        + '</div></div>';
      appendActivity(html);
      break;
    }
    case 'watch':
      appendActivity('<div class="ev-watch"><span class="spinner"></span>' + escapeHtml(ev.text) + '</div>');
      break;
    case 'notes_consumed':
      // Orchestrator drained these queued notes — lock them in the UI so
      // the user can no longer edit or delete them.
      if (Array.isArray(ev.ids)) {
        ev.ids.forEach(lockInterjection);
      }
      break;
    case 'reply':
      dismissSpinner();
      if (ev.text && ev.text.trim()) {
        addChatMsg('assistant', formatChat(ev.text), ev.text);
        chatHistory.push({role: 'assistant', content: ev.text});
      }
      enableInput();
      refreshCurrentAppliance();
      refreshFactsBadge();
      break;
    case 'error':
      dismissSpinner();
      appendActivity('<div class="ev-error">Error: ' + escapeHtml(ev.text) + '</div>');
      addChatMsg('assistant', 'Error: ' + escapeHtml(ev.text));
      enableInput();
      break;
    case 'draft': {
      dismissSpinner();
      var ta = document.getElementById('ws-draft');
      if (ta && ev.text) {
        ta.value = ev.text;
      }
      var btn = document.getElementById('btn-generate-draft');
      if (btn) { btn.textContent = 'Generate Draft'; btn.disabled = false; }
      closeEventStream();
      activeSessionId = null;
      break;
    }
    case 'done':
      dismissSpinner();
      closeEventStream();
      activeSessionId = null;
      pushSessionURL(null);
      enableInput();
      break;
  }
}

function enableInput() {
  document.getElementById('chat-send').disabled = !currentAppliance;
  document.getElementById('chat-send').style.display = '';
  document.getElementById('chat-cancel').style.display = 'none';
  document.getElementById('chat-working').style.display = 'none';
  document.getElementById('chat-input').disabled = false;
  document.getElementById('chat-input').placeholder = 'Ask about this system… (Enter to send, Shift+Enter for newline)';
  document.getElementById('btn-map').disabled = !currentAppliance;
  document.getElementById('btn-mapapp').disabled = !currentAppliance;
  activeSessionKind = null;
}

function cancelChat() {
  if (!activeSessionId) { enableInput(); return; }
  fetch('api/cancel?id=' + encodeURIComponent(activeSessionId), {method: 'POST'});
  closeEventStream();
  activeSessionId = null;
  pushSessionURL(null);
  enableInput();
}

function confirmCmd(action) {
  // action: 'allow' | 'always' | 'deny'
  if (!pendingConfirmSessionId) return;
  var box = document.getElementById('confirm-' + pendingConfirmSessionId);
  if (box) box.querySelectorAll('button').forEach(function(b) { b.disabled = true; });
  var sid = pendingConfirmSessionId;
  pendingConfirmSessionId = null;
  var allowVal = action === 'deny' ? 'false' : action === 'always' ? 'always' : 'true';
  fetch('api/confirm?id=' + encodeURIComponent(sid) + '&allow=' + allowVal, {method: 'POST'});
}

// --- Chat ---

// renderInterjection appends a user-interjection message to the chat with
// edit/delete affordances. The note is identified by its server-assigned ID
// (via div.dataset.noteId, set by the caller once the POST returns). When
// the orchestrator drains the note, lockInterjection removes the buttons.
function renderInterjection(noteId, text) {
  var msgs = document.getElementById('chat-messages');
  var div = document.createElement('div');
  div.className = 'chat-msg user interjection';
  if (noteId) div.dataset.noteId = noteId;
  div.dataset.text = text;
  div.innerHTML =
    '<div class="interjection-body">' + escapeHtml(text) + '</div>' +
    '<div class="interjection-meta">' +
      '<span class="interjection-status" style="opacity:0.55;font-size:0.72em;font-style:italic">(queued — orchestrator picks up between rounds)</span>' +
      '<span class="interjection-actions" style="margin-left:0.5rem">' +
        '<button class="interjection-edit" onclick="editInterjection(this)" title="Edit">Edit</button>' +
        '<button class="interjection-delete" onclick="deleteInterjection(this)" title="Delete">Delete</button>' +
      '</span>' +
    '</div>';
  msgs.appendChild(div);
  msgs.scrollTop = msgs.scrollHeight;
  return div;
}

// lockInterjection strips the edit/delete affordances on a note that the
// orchestrator has now consumed. Called from the notes_consumed event.
function lockInterjection(noteId) {
  var div = document.querySelector('.interjection[data-note-id="' + cssEscape(noteId) + '"]');
  if (!div) return;
  var actions = div.querySelector('.interjection-actions');
  if (actions) actions.remove();
  var status = div.querySelector('.interjection-status');
  if (status) status.textContent = '(delivered to orchestrator)';
  div.classList.add('interjection-locked');
}

// cssEscape is a minimal CSS attribute-value escaper for selector use.
function cssEscape(s) {
  return String(s).replace(/["\\]/g, '\\$&');
}

function editInterjection(btn) {
  var div = btn.closest('.interjection');
  if (!div || !div.dataset.noteId) return;
  var noteId = div.dataset.noteId;
  var current = div.dataset.text || '';
  // Lock server-side first so the orchestrator can't drain it mid-edit.
  fetch('api/inject', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id: activeSessionId, note_id: noteId, action: 'lock'})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    // Server confirmed lock — open inline editor.
    div.querySelector('.interjection-body').innerHTML =
      '<textarea class="interjection-editor" rows="2" style="width:100%;background:#161b22;border:1px solid #30363d;color:#c9d1d9;border-radius:4px;padding:4px 6px;font-size:0.85rem;font-family:inherit">' + escapeHtml(current) + '</textarea>';
    div.querySelector('.interjection-actions').innerHTML =
      '<button class="interjection-save" onclick="commitInterjection(this)" title="Save">Save</button>' +
      '<button class="interjection-cancel" onclick="cancelInterjection(this)" title="Cancel">Cancel</button>';
    var ta = div.querySelector('textarea');
    ta.focus();
    ta.setSelectionRange(ta.value.length, ta.value.length);
  }).catch(function(err) {
    addChatMsg('system', 'Edit failed: ' + escapeHtml(err.message));
  });
}

function commitInterjection(btn) {
  var div = btn.closest('.interjection');
  if (!div || !div.dataset.noteId) return;
  var noteId = div.dataset.noteId;
  var ta = div.querySelector('textarea');
  if (!ta) return;
  var newText = ta.value.trim();
  if (!newText) { addChatMsg('system', 'Empty note — cancel instead.'); return; }
  fetch('api/inject', {
    method: 'PATCH', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id: activeSessionId, note_id: noteId, text: newText})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    div.dataset.text = newText;
    div.querySelector('.interjection-body').innerHTML = escapeHtml(newText);
    div.querySelector('.interjection-actions').innerHTML =
      '<button class="interjection-edit" onclick="editInterjection(this)" title="Edit">Edit</button>' +
      '<button class="interjection-delete" onclick="deleteInterjection(this)" title="Delete">Delete</button>';
  }).catch(function(err) {
    addChatMsg('system', 'Save failed: ' + escapeHtml(err.message));
    // Note remains locked server-side until cancel or another save.
  });
}

function cancelInterjection(btn) {
  var div = btn.closest('.interjection');
  if (!div || !div.dataset.noteId) return;
  var noteId = div.dataset.noteId;
  fetch('api/inject', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id: activeSessionId, note_id: noteId, action: 'unlock'})
  }).then(function(r) {
    if (!r.ok && r.status !== 410) return r.text().then(function(t) { throw new Error(t); });
    // Restore original view (whether unlock succeeded or note was already drained).
    div.querySelector('.interjection-body').innerHTML = escapeHtml(div.dataset.text || '');
    div.querySelector('.interjection-actions').innerHTML =
      '<button class="interjection-edit" onclick="editInterjection(this)" title="Edit">Edit</button>' +
      '<button class="interjection-delete" onclick="deleteInterjection(this)" title="Delete">Delete</button>';
  }).catch(function(err) {
    addChatMsg('system', 'Cancel failed: ' + escapeHtml(err.message));
  });
}

function deleteInterjection(btn) {
  var div = btn.closest('.interjection');
  if (!div || !div.dataset.noteId) return;
  if (!confirm('Delete this queued note?')) return;
  var noteId = div.dataset.noteId;
  fetch('api/inject', {
    method: 'DELETE', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id: activeSessionId, note_id: noteId})
  }).then(function(r) {
    if (!r.ok && r.status !== 410) return r.text().then(function(t) { throw new Error(t); });
    div.remove();
  }).catch(function(err) {
    addChatMsg('system', 'Delete failed: ' + escapeHtml(err.message));
  });
}

function sendChat() {
  if (!currentAppliance) return;
  var input = document.getElementById('chat-input');
  var msg = input.value.trim();
  if (!msg) return;

  // Mid-flight interjection: a chat session is already running. Push the
  // note onto the orchestrator's queue instead of starting a new turn.
  // The orchestrator picks it up between rounds; in-flight workers finish
  // their current task first. Render with edit/delete affordances; the
  // note becomes locked (no edit/delete) once the orchestrator drains it.
  if (activeSessionId && activeSessionKind === 'chat') {
    input.value = '';
    var div = renderInterjection(null, msg);
    chatHistory.push({role: 'user', content: msg});
    fetch('api/inject', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({id: activeSessionId, text: msg})
    }).then(function(r) {
      if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
      return r.json();
    }).then(function(data) {
      if (data && data.note_id) {
        div.dataset.noteId = data.note_id;
      }
    }).catch(function(err) {
      div.querySelector('.interjection-status').textContent = '(failed: ' + err.message + ')';
      div.classList.add('interjection-failed');
    });
    return;
  }

  input.value = '';
  lastUserQuestion = msg;
  addChatMsg('user', escapeHtml(msg));
  chatHistory.push({role: 'user', content: msg});
  // Chat sessions stay interjectable, so leave the input enabled and the
  // send button visible — the user may type follow-on notes mid-run.
  document.getElementById('chat-cancel').style.display = 'inline-block';
  document.getElementById('chat-working').style.display = 'inline-flex';
  document.getElementById('chat-input').placeholder = 'Type to interject — orchestrator picks it up between rounds…';
  activeSpinner = appendActivity('<div class="ev-status"><span class="spinner"></span>Connecting…</div>');

  var chatPayload = {
    appliance_id: currentAppliance.id,
    message: msg,
    history: chatHistory.slice(0, -1)
  };
  if (currentWorkspaceId) chatPayload.workspace_id = currentWorkspaceId;
  fetch('api/chat', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(chatPayload)
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    return r.json();
  }).then(function(data) {
    activeSessionKind = 'chat';
    openEventStream(data.session_id);
  }).catch(function(err) {
    addChatMsg('assistant', 'Error: ' + escapeHtml(err.message));
    enableInput();
  });
}

function runMap() {
  if (!currentAppliance) return;
  if (!confirm('Run a full system map of ' + currentAppliance.name + '?\n\nThis profiles the appliance and discovers all services and log files. Results are saved to the appliance record.')) return;
  document.getElementById('btn-map').disabled = true;
  document.getElementById('chat-send').style.display = 'none';
  document.getElementById('chat-cancel').style.display = 'inline-block';
  document.getElementById('chat-working').style.display = 'inline-flex';
  document.getElementById('chat-input').disabled = true;
  addChatMsg('system', 'Starting full system map…');
  activeSpinner = appendActivity('<div class="ev-status"><span class="spinner"></span>Starting system map…</div>');

  fetch('api/map', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({appliance_id: currentAppliance.id})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    return r.json();
  }).then(function(data) {
    activeSessionKind = 'map';
    openEventStream(data.session_id);
  }).catch(function(err) {
    addChatMsg('assistant', 'Map failed: ' + escapeHtml(err.message));
    enableInput();
  });
}

function showMapAppModal() {
  if (!currentAppliance) return;
  document.getElementById('mapapp-cmd').value = '';
  document.getElementById('mapapp-modal').style.display = 'block';
  document.getElementById('overlay').style.display = 'block';
  setTimeout(function() { document.getElementById('mapapp-cmd').focus(); }, 50);
}

function runMapApp() {
  var cmd = document.getElementById('mapapp-cmd').value.trim();
  if (!cmd) { alert('Command is required.'); return; }
  if (!currentAppliance) return;
  hideModals();
  document.getElementById('btn-mapapp').disabled = true;
  document.getElementById('chat-send').style.display = 'none';
  document.getElementById('chat-cancel').style.display = 'inline-block';
  document.getElementById('chat-working').style.display = 'inline-flex';
  document.getElementById('chat-input').disabled = true;
  addChatMsg('system', 'Mapping application: ' + escapeHtml(cmd) + '…');
  activeSpinner = appendActivity('<div class="ev-status"><span class="spinner"></span>Mapping ' + escapeHtml(cmd) + '…</div>');

  fetch('api/mapapp', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({appliance_id: currentAppliance.id, command: cmd})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    return r.json();
  }).then(function(data) {
    activeSessionKind = 'mapapp';
    openEventStream(data.session_id);
  }).catch(function(err) {
    addChatMsg('assistant', 'Map App failed: ' + escapeHtml(err.message));
    enableInput();
  });
}

// --- UI helpers ---

// renderPlan upserts the investigator's plan checklist in the chat-messages
// pane. Called on plan_set (initial render) and plan_step (status update).
// One container per session — replaces in place on each event so the
// checklist updates live without appending duplicates.
function renderPlan(steps) {
  var msgs = document.getElementById('chat-messages');
  if (!msgs) return;
  var box = document.getElementById('plan-checklist');
  if (!box) {
    box = document.createElement('div');
    box.id = 'plan-checklist';
    box.className = 'chat-msg plan';
    msgs.appendChild(box);
  }
  var html = '<div class="plan-label">&#9656; Investigation Plan</div><ul class="plan-steps">';
  steps.forEach(function(s) {
    var icon, cls;
    switch (s.status) {
      case 'pending':     icon = '&#9675;'; cls = 'plan-pending'; break;     // ○
      case 'in_progress': icon = '&#9679;'; cls = 'plan-active'; break;      // ●
      case 'done':        icon = '&#10003;'; cls = 'plan-done'; break;       // ✓
      case 'blocked':     icon = '&#9888;'; cls = 'plan-blocked'; break;     // ⚠
      default:            icon = '&middot;'; cls = '';
    }
    html += '<li class="' + cls + '">'
      + '<span class="plan-icon">' + icon + '</span>'
      + '<span class="plan-title">' + escapeHtml(s.title || '') + '</span>';
    if (s.what_to_find) {
      html += '<span class="plan-detail">' + escapeHtml(s.what_to_find) + '</span>';
    }
    if (s.findings) {
      html += '<span class="plan-findings">&#8627; ' + escapeHtml(s.findings) + '</span>';
    }
    if (s.blocked_reason) {
      html += '<span class="plan-blocked-reason">blocked: ' + escapeHtml(s.blocked_reason) + '</span>';
    }
    html += '</li>';
  });
  html += '</ul>';
  box.innerHTML = html;
  msgs.scrollTop = msgs.scrollHeight;
}

function addChatMsg(role, html, rawText) {
  var div = document.createElement('div');
  div.className = 'chat-msg ' + role;
  div.innerHTML = html;
  if (role === 'assistant' && rawText) {
    var actions = document.createElement('div');
    actions.className = 'chat-save-actions';

    // Workspace button — label changes after first save.
    var btnWS = document.createElement('button');
    btnWS.className = 'chat-save-btn ws-btn';
    btnWS.textContent = currentWorkspaceId ? 'Save Workspace' : 'Create Workspace';
    (function(q, a, btn) {
      btn.onclick = function() { workspaceAction(q, a, btn); };
    })(lastUserQuestion, rawText, btnWS);
    actions.appendChild(btnWS);

    if (hasTechWriter) {
      var btnTW = document.createElement('button');
      btnTW.className = 'chat-save-btn';
      btnTW.textContent = '↗ TechWriter';
      btnTW.onclick = function() { saveToServitor(rawText, 'article', btnTW); };
      actions.appendChild(btnTW);
    }
    if (hasCodeWriter) {
      var btnCW = document.createElement('button');
      btnCW.className = 'chat-save-btn';
      btnCW.textContent = '↗ CodeWriter';
      btnCW.onclick = function() { saveToServitor(rawText, 'snippet', btnCW); };
      actions.appendChild(btnCW);
    }
    div.appendChild(actions);
  }
  var msgs = document.getElementById('chat-messages');
  msgs.appendChild(div);
  msgs.scrollTop = msgs.scrollHeight;
}

function workspaceAction(question, answer, btn) {
  if (btn.classList.contains('saving') || btn.classList.contains('saved')) return;
  btn.classList.add('saving');
  var origText = btn.textContent;
  btn.textContent = 'Saving…';

  if (currentWorkspaceId) {
    // Append to existing workspace.
    fetch('api/workspace/save', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({workspace_id: currentWorkspaceId, question: question, answer: answer})
    }).then(function(r) {
      if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
      btn.classList.remove('saving');
      btn.classList.add('saved');
      btn.textContent = '✓ Saved';
    }).catch(function(err) {
      btn.classList.remove('saving');
      btn.textContent = origText;
      alert('Save failed: ' + err.message);
    });
  } else {
    // Create new workspace named after the question.
    fetch('api/workspace/create', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        appliance_id: currentAppliance ? currentAppliance.id : '',
        question: question,
        answer: answer
      })
    }).then(function(r) {
      if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
      return r.json();
    }).then(function(d) {
      currentWorkspaceId = d.id;
      currentWorkspaceName = d.name;
      btn.classList.remove('saving');
      btn.classList.add('saved');
      btn.textContent = '✓ Workspace Created';
      // Update all future workspace buttons in the chat to show "Save Workspace".
      document.querySelectorAll('.chat-save-btn.ws-btn:not(.saved)').forEach(function(b) {
        b.textContent = 'Save Workspace';
      });
    }).catch(function(err) {
      btn.classList.remove('saving');
      btn.textContent = origText;
      alert('Create workspace failed: ' + err.message);
    });
  }
}

function appendActivity(html) {
  var log = document.getElementById('activity-log');
  var tmp = document.createElement('div');
  tmp.innerHTML = html;
  var first = tmp.firstChild;
  while (tmp.firstChild) log.appendChild(tmp.firstChild);
  scrollActivity();
  return first; // caller can keep a ref to remove it later (e.g. spinner)
}

function scrollActivity() {
  var log = document.getElementById('activity-log');
  log.scrollTop = log.scrollHeight;
}


function clearChat() {
  chatHistory = [];
  document.getElementById('chat-messages').innerHTML = '';
  currentWorkspaceId = null;
  currentWorkspaceName = null;
  lastUserQuestion = '';
  document.getElementById('btn-ws-draft').style.display = 'none';
}
function clearActivity() { document.getElementById('activity-log').innerHTML = ''; }

function clearMemory() {
  if (!currentAppliance) return;
  if (!confirm('Clear all memory for ' + currentAppliance.name + '?\n\nThis removes:\n• System profile (map data)\n• Knowledge docs\n• Stored facts\n• Notes and techniques\n\nThe appliance connection settings are kept. Run "Map System" to rebuild.\n\nThis cannot be undone.')) return;
  fetch('api/memory/clear', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({appliance_id: currentAppliance.id})
  }).then(function(r) {
    if (r.ok) {
      appendActivity('<div class="ev-status">All memory cleared for ' + escapeHtml(currentAppliance.name) + '. Run Map System to rebuild.</div>');
      refreshCurrentAppliance();
    } else {
      appendActivity('<div class="ev-status" style="color:var(--red)">Failed to clear memory.</div>');
    }
  }).catch(function() {
    appendActivity('<div class="ev-status" style="color:var(--red)">Failed to clear memory.</div>');
  });
}

function saveToServitor(text, dest, btn) {
  if (btn.classList.contains('saving') || btn.classList.contains('saved')) return;
  btn.classList.add('saving');
  var origText = btn.textContent;
  btn.textContent = 'Saving…';
  fetch('api/save_' + dest, {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({text: text, appliance_id: currentAppliance ? currentAppliance.id : ''})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    return r.json();
  }).then(function(d) {
    btn.classList.remove('saving');
    btn.classList.add('saved');
    btn.textContent = origText.replace('↗', '✓');
  }).catch(function(err) {
    btn.classList.remove('saving');
    btn.textContent = origText;
    alert('Save failed: ' + err.message);
  });
}

function escapeHtml(s) {
  return String(s == null ? '' : s)
    .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
    .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

function deLatex(text) {
  // Convert common LaTeX inline math ($\symbol$) to Unicode. LLMs frequently
  // emit these for technical diagrams; marked.js has no math renderer.
  return text
    .replace(/\$\\rightarrow\$/g, '→').replace(/\$\\leftarrow\$/g, '←')
    .replace(/\$\\Rightarrow\$/g, '⇒').replace(/\$\\Leftarrow\$/g, '⇐')
    .replace(/\$\\leftrightarrow\$/g, '↔').replace(/\$\\Leftrightarrow\$/g, '⟺')
    .replace(/\$\\to\$/g, '→').replace(/\$\\gets\$/g, '←')
    .replace(/\$\\times\$/g, '×').replace(/\$\\div\$/g, '÷')
    .replace(/\$\\pm\$/g, '±').replace(/\$\\mp\$/g, '∓')
    .replace(/\$\\leq\$/g, '≤').replace(/\$\\geq\$/g, '≥').replace(/\$\\neq\$/g, '≠')
    .replace(/\$\\approx\$/g, '≈').replace(/\$\\infty\$/g, '∞').replace(/\$\\cdot\$/g, '·')
    .replace(/\$\\in\$/g, '∈').replace(/\$\\notin\$/g, '∉')
    .replace(/\$\\subset\$/g, '⊂').replace(/\$\\supset\$/g, '⊃')
    .replace(/\$\\cup\$/g, '∪').replace(/\$\\cap\$/g, '∩');
}

function formatChat(text) {
  text = deLatex(text);
  if (typeof marked !== 'undefined') {
    return marked.parse(text);
  }
  // Fallback: basic code block and inline code handling.
  text = text.replace(/` + "```" + `(\w*)\n([\s\S]*?)` + "```" + `/g, function(m, lang, code) {
    return '<pre><code>' + escapeHtml(code.replace(/\n$/, '')) + '</code></pre>';
  });
  text = text.replace(/` + "`" + `([^` + "`" + `]+)` + "`" + `/g, function(m, code) {
    return '<code>' + escapeHtml(code) + '</code>';
  });
  return text.replace(/\n/g, '<br>');
}

function startResize(e) {
  var pane = document.getElementById('chat-pane');
  var container = document.getElementById('main');
  var resizer = document.getElementById('chat-resizer');
  var startX = e.clientX;
  var startW = pane.offsetWidth;
  resizer.classList.add('dragging');
  function onMove(e) {
    var newW = Math.max(260, Math.min(startW + (e.clientX - startX), container.offsetWidth - 280));
    pane.style.width = newW + 'px';
    if (xtermFit) xtermFit.fit();
  }
  function onUp() {
    resizer.classList.remove('dragging');
    document.removeEventListener('mousemove', onMove);
    document.removeEventListener('mouseup', onUp);
  }
  document.addEventListener('mousemove', onMove);
  document.addEventListener('mouseup', onUp);
}

function startHResize(e) {
  var actPane = document.getElementById('activity-pane');
  var termCont = document.getElementById('terminal-container');
  var resizer = document.getElementById('h-resizer');
  var startY = e.clientY;
  var startH = termCont.offsetHeight;
  resizer.classList.add('dragging');
  function onMove(e) {
    var newH = Math.max(80, Math.min(startH - (e.clientY - startY), actPane.offsetHeight - 120));
    termCont.style.flex = 'none';
    termCont.style.height = newH + 'px';
    if (xtermFit) xtermFit.fit();
  }
  function onUp() {
    resizer.classList.remove('dragging');
    document.removeEventListener('mousemove', onMove);
    document.removeEventListener('mouseup', onUp);
  }
  document.addEventListener('mousemove', onMove);
  document.addEventListener('mouseup', onUp);
}
`
