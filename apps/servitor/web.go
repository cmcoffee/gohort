package servitor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

func init() {
	RegisterApp(new(Servitor))
	registerServitorMCPTools()
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
	Type string `json:"type"` // "ssh" (default) | "command" | "repo" | "workspace"
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
	// Repo fields (Type == "repo") — the code is cloned into tmpfs and ingested
	// into the encrypted RepoFilesDB; nothing but derived, encrypted content
	// persists. RepoFiles/RepoCloned track ingest status.
	RepoURL    string `json:"repo_url"`              // git remote (https://github.com/owner/repo)
	RepoBranch string `json:"repo_branch,omitempty"` // branch to clone; "" = remote default
	RepoToken  string `json:"repo_token,omitempty"`  // access token for private repos; blanked on list
	RepoFiles  int    `json:"repo_files,omitempty"`  // ingested file count
	RepoCloned string `json:"repo_cloned,omitempty"` // RFC3339 of last successful ingest
	// RepoSkipDirs are extra directory names (basename match) to exclude from
	// ingest, on top of the built-in defaults (VCS/venv/build artifacts). Lets a
	// project drop its own generated trees (e.g. "dist", "target", "coverage").
	RepoSkipDirs []string `json:"repo_skip_dirs,omitempty"`
	// Workspace fields (Type == "workspace") — a master appliance that references
	// other appliances (repos and/or SSH boxes) and investigates them together.
	// It owns no store/creds of its own; each member is resolved and run in its
	// own owner's context. See docs/servitor-workspace-mvp.md.
	Members []string `json:"members,omitempty"` // member appliance IDs
	// Collections are knowledge-collection IDs linked to this appliance so the
	// investigator can draw on curated external knowledge (runbooks, vendor docs,
	// a guide) when answering — via the search_knowledge tool — alongside what it
	// gathered from the system itself. Applies to every appliance type.
	Collections []string `json:"collections,omitempty"`
	// Sharing — an appliance/repo is owned by the user who created it and lives
	// in that user's store. When Shared is set, every authenticated user can
	// discover and operate it (in the OWNER's context: same creds, same repo
	// clone, same accumulated knowledge/scoped memory) — but each user keeps
	// their OWN chat sessions. Owner is stamped on create; the shared index in
	// T.DB (see sharing.go) lets non-owners resolve it.
	Owner  string `json:"owner,omitempty"`  // username that created + owns this record
	Shared bool   `json:"shared,omitempty"` // visible to + usable by all authenticated users
	// Shared config below (persona/instructions/profile/etc.)
	Instructions  string     `json:"instructions"`   // freeform notes injected into every session
	PersonaName   string     `json:"persona_name"`   // short label shown in the UI (e.g. "Support", "QA")
	PersonaPrompt string     `json:"persona_prompt"` // shapes how the agent approaches this appliance
	Profile       string     `json:"profile"`        // full system profile / CLI map markdown
	LogMap       []LogEntry `json:"log_map"`      // structured list of discovered log files
	Scanned      string     `json:"scanned"`      // RFC3339 timestamp of last map run
}

// dedupeStrings trims, drops empties, and removes duplicates while preserving
// first-seen order. Used to normalize workspace member ID lists.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
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
	termBuffers        sync.Map // "userID:applianceID" -> *termBuffer; persistent command+output log mirrored to any connected terminal WebSocket
	sessionAppliances  sync.Map // session_id -> applianceID (for building resume URLs in the dashboard live-sessions panel)
)

// Mid-flight injection-queue types + registry were lifted into
// core/injection.go so phantom and orchestrate can share the same
// machinery. Aliases keep existing call sites in this file terse;
// the registry helpers (RegisterInjectionQueue / LookupInjectionQueue
// / ReleaseInjectionQueue) sit on top of a shared sync.Map in core.
type injectionQueue = InjectionQueue
type injectionNote = InjectionNote

// termBuffer is the per-(user+appliance) persistent terminal log.
// Every command + output mirrored from a worker session lands here
// regardless of whether anyone is watching the terminal WebSocket;
// when a viewer connects, it gets the buffer's contents replayed
// before live streaming starts. Keeps the terminal pane truthful:
// commands the user missed because they hadn't opened the pane yet
// still show up when they finally do.
//
// Buffer is a byte slice trimmed to termBufferCap bytes — keeps long
// sessions from growing memory unbounded while preserving recent
// activity (256KB ≈ a few thousand lines of normal command output,
// far more than what's interesting to scroll back through).
type termBuffer struct {
	mu      sync.Mutex
	bytes   []byte
	writers map[*termWriterEntry]struct{}
}

// termWriterEntry is one live WebSocket subscribed to a termBuffer.
// Held as a pointer so we can address-match in subscribe/unsubscribe
// without worrying about map-key equality on closure values.
type termWriterEntry struct {
	write func([]byte)
}

func termBufferCap() int { return TuneInt("tune_term_buffer_cap") }

func init() {
	RegisterTunable(TunableSpec{
		Key:      "tune_term_buffer_cap",
		Category: "Limits",
		Label:    "Terminal buffer cap (bytes)",
		Help:     "Max bytes retained per live terminal buffer before old output is trimmed.",
		Kind:     KindInt,
		Default:  262144,
		Min:      32768,
		Max:      4194304,
	})
}

// termBufferFor returns (or creates) the buffer for a user+appliance
// pair. Buffers persist for the process lifetime; that's fine — they
// trim themselves, and an idle buffer holding 256KB is negligible.
func termBufferFor(userID, applianceID string) *termBuffer {
	key := userID + ":" + applianceID
	if v, ok := termBuffers.Load(key); ok {
		return v.(*termBuffer)
	}
	tb := &termBuffer{writers: map[*termWriterEntry]struct{}{}}
	if actual, loaded := termBuffers.LoadOrStore(key, tb); loaded {
		return actual.(*termBuffer)
	}
	return tb
}

// append records data in the buffer and returns a snapshot of the
// current writer set. Caller broadcasts to each writer OUTSIDE the
// lock so a slow client can't stall other writers or future appends.
func (tb *termBuffer) append(data []byte) []*termWriterEntry {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.bytes = append(tb.bytes, data...)
	if cap := termBufferCap(); len(tb.bytes) > cap {
		// Trim from the start. Copy into a fresh slice so the dropped
		// prefix's underlying memory is reclaimable.
		fresh := make([]byte, cap)
		copy(fresh, tb.bytes[len(tb.bytes)-cap:])
		tb.bytes = fresh
	}
	writers := make([]*termWriterEntry, 0, len(tb.writers))
	for w := range tb.writers {
		writers = append(writers, w)
	}
	return writers
}

// subscribe registers a writer and returns a copy of the current
// buffer so the caller can replay it to the new subscriber before
// any future appends arrive. Locked so a concurrent append can't
// interleave (subscriber would either miss it or see it twice).
func (tb *termBuffer) subscribe(w *termWriterEntry) []byte {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.writers[w] = struct{}{}
	snap := make([]byte, len(tb.bytes))
	copy(snap, tb.bytes)
	return snap
}

func (tb *termBuffer) unsubscribe(w *termWriterEntry) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	delete(tb.writers, w)
}

// mirrorToTerm appends terminal bytes to the persistent buffer and
// fans out to every connected viewer. Worker code keeps calling this
// unchanged — the only behavior change is that writes no longer drop
// when no terminal is connected.
func mirrorToTerm(userID, applianceID string, data []byte) {
	if len(data) == 0 {
		return
	}
	tb := termBufferFor(userID, applianceID)
	for _, w := range tb.append(data) {
		w.write(data)
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

	// Expose servitor's appliances as a generic reference source so writer
	// apps can ground drafts in gathered system knowledge. T.DB is final here.
	RegisterReferenceSource(servitorSource{db: T.DB})

	sub := NewWebUI(T, prefix, AppUIAssets{})
	sub.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		T.handleChatPage(w, r)
	})
	// AgentLoopPanel-facing endpoints — translator on top of the
	// existing probeSessions queue. See chat_bridge.go / chat_page.go.
	sub.HandleFunc("/api/chat/v2/events", T.handleChatEvents)
	sub.HandleFunc("/api/chat/v2/confirm", T.handleChatConfirm)
	sub.HandleFunc("/api/profile", T.handleProfile)
	sub.HandleFunc("/manage", T.handleManagePage)
	sub.HandleFunc("/manage/", T.handleManagePage)
	sub.HandleFunc("/api/appliances", T.handleAppliances)
	sub.HandleFunc("/api/appliances/", T.handleApplianceMemory) // /api/appliances/<id>/{facts,graph,inferred,...} — shared agent-memory surface
	sub.HandleFunc("/api/appliance/", T.handleAppliance)
	sub.HandleFunc("/api/chat", T.handleChat)
	// Persisted chat sessions back the left rail (see sessions.go).
	sub.HandleFunc("/api/sessions", T.handleServitorSessionList)
	sub.HandleFunc("/api/sessions/", T.handleServitorSessionOne)
	sub.HandleFunc("/api/push-to-guide", T.handlePushToGuide)
	sub.HandleFunc("/api/inject", T.handleInject)
	sub.HandleFunc("/api/map", T.handleMap)
	sub.HandleFunc("/api/mapapp", T.handleMapApp)
	sub.HandleFunc("/api/terminal", T.handleTerminal)
	sub.HandleFunc("/api/facts", T.handleFacts)
	sub.HandleFunc("/api/knowledge/export", T.handleKnowledgeExport)
	sub.HandleFunc("/api/memory/clear", T.handleMemoryClear)
	sub.HandleFunc("/api/repo/refresh", T.handleRepoRefresh)
	sub.HandleFunc("/api/collections", T.handleCollectionsList)
	sub.HandleFunc("/api/cancel", probeSessions.HandleCancel("servitor"))
	sub.HandleFunc("/api/save_destinations", T.handleSaveDestinations)
	sub.HandleFunc("/api/save_article", T.handleSaveArticle)
	sub.HandleFunc("/api/save_snippet", T.handleSaveSnippet)
	sub.HandleFunc("/api/rules", T.handleRules)
	sub.HandleFunc("/api/rules/", T.handleRuleDelete)
	MountSubMux(mux, prefix, sub)
	go T.runWatchLoop(AppContext())
	RegisterLiveProvider(func() []LiveEntry {
		entries := probeSessions.ActiveSessions()
		for i := range entries {
			entries[i].App = "Servitor"
			entries[i].Path = prefix
			// New framework page reconnects via ?reconnect=<sid>;
			// the runtime taps the chat-events translator stream
			// for that session id on mount. The appliance picker
			// re-syncs to the saved active appliance from a
			// separate flow — no need to thread it through here.
			entries[i].URL = fmt.Sprintf("%s/?reconnect=%s",
				prefix, url.QueryEscape(entries[i].ID))
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
		items := []Appliance{}
		seen := map[string]bool{}
		// The user's OWN appliances.
		if udb != nil {
			for _, key := range udb.Keys(applianceTable) {
				var a Appliance
				if udb.Get(applianceTable, key, &a) {
					if a.Owner == "" {
						a.Owner = userID // legacy record: the holder owns it
					}
					a.Password = ""
					a.RepoToken = ""
					items = append(items, a)
					seen[a.ID] = true
				}
			}
		}
		// Shared appliances owned by OTHERS — discoverable + usable by everyone,
		// but managed only by their owner (see canManageAppliance).
		for id, owner := range T.listSharedAppliances() {
			if seen[id] || owner == userID {
				continue
			}
			if ownerUDB := UserDB(T.DB, owner); ownerUDB != nil {
				var a Appliance
				if ownerUDB.Get(applianceTable, id, &a) {
					a.Owner = owner
					a.Shared = true
					a.Password = ""
					a.RepoToken = ""
					items = append(items, a)
					seen[id] = true
				}
			}
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
		switch req.Type {
		case "command":
			if req.Name == "" || req.Command == "" {
				http.Error(w, "name and command required", http.StatusBadRequest)
				return
			}
		case "repo":
			req.RepoURL = strings.TrimSpace(req.RepoURL)
			if req.RepoURL == "" {
				http.Error(w, "repo url required", http.StatusBadRequest)
				return
			}
			if req.Name == "" {
				req.Name = repoNameFromURL(req.RepoURL)
			}
		case "workspace":
			// A workspace references other appliances; it owns no creds/store.
			req.Members = dedupeStrings(req.Members)
			if req.Name == "" {
				http.Error(w, "name required", http.StatusBadRequest)
				return
			}
			if len(req.Members) == 0 {
				http.Error(w, "select at least one member appliance", http.StatusBadRequest)
				return
			}
		default:
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
		isNew := req.ID == ""
		// The store the record lives in, and its owner. A new record is owned by
		// its creator and saved to their store; an update lands in the OWNER's
		// store (which may not be the requester's, for a shared record edited by
		// an admin).
		targetUDB := udb
		owner := userID
		// Preserve sensitive/derived fields when updating without re-supplying them.
		if !isNew {
			existing, exOwner, exUDB, found := T.resolveAppliance(userID, udb, req.ID)
			if !found {
				http.Error(w, "appliance not found", http.StatusNotFound)
				return
			}
			// Only the owner or an admin may edit / re-share a record. A non-owner
			// can USE a shared appliance but not change it.
			if !canManageAppliance(userID, existing, servitorIsAdmin(r)) {
				http.Error(w, "not allowed to edit this appliance", http.StatusForbidden)
				return
			}
			targetUDB = exUDB
			owner = exOwner
			if req.Type == "ssh" && req.Password == "" {
				req.Password = existing.Password
			}
			if req.Type == "repo" && req.RepoToken == "" {
				req.RepoToken = existing.RepoToken
			}
			req.Profile = existing.Profile
			req.LogMap = existing.LogMap
			req.Scanned = existing.Scanned
			req.RepoFiles = existing.RepoFiles
			req.RepoCloned = existing.RepoCloned
		}
		req.Owner = owner
		if req.ID == "" {
			req.ID = UUIDv4()
		}
		targetUDB.Set(applianceTable, req.ID, req)
		// Keep the global shared index in sync with the record's Shared flag.
		T.setApplianceShared(req.ID, owner, req.Shared)
		dropConn(owner, req.ID) // force reconnect with new credentials
		// Repo appliances: clone + ingest under the OWNER (one shared clone) on
		// create or when the store is empty.
		if req.Type == "repo" && (isNew || req.RepoFiles == 0) {
			go T.cloneAndIngestRepo(AppContext(), owner, targetUDB, req.ID)
		}
		resp := req
		resp.Password = ""
		resp.RepoToken = ""
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
		// Resolve own OR shared so a non-owner can load a shared appliance's
		// record (read-only; the UI gates edit/delete on can_manage).
		a, owner, _, found := T.resolveAppliance(userID, udb, id)
		if !found {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		a.Owner = owner
		a.Password = ""
		a.RepoToken = "" // never send the stored token back to the edit form
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a)
	case http.MethodDelete:
		a, owner, ownerUDB, found := T.resolveAppliance(userID, udb, id)
		if !found {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		// Only the owner or an admin may delete.
		if !canManageAppliance(userID, a, servitorIsAdmin(r)) {
			http.Error(w, "not allowed to delete this appliance", http.StatusForbidden)
			return
		}
		T.setApplianceShared(id, owner, false) // drop from the shared index first
		purgeAppliance(owner, ownerUDB, id)
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
	// Also clear the orchestrate-scoped memory so "Clear Memory" is consistent
	// across the dual-write split (legacy ssh_* AND the scope the modal reads).
	clearApplianceScopedMemory(udb, applianceID)
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

	// Drop the orchestrate-scoped memory too, so a deleted appliance leaves no
	// orphaned scope behind.
	clearApplianceScopedMemory(udb, applianceID)

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
		SessionID   string `json:"session_id"`
		WorkspaceID string `json:"workspace_id"`
		Message     string `json:"message"`
		History     []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"history"`
		// Optional image attachments — base64-encoded payloads from
		// the paperclip picker. Decoded into Message.Images bytes
		// and attached to the final user turn so the worker LLM
		// can see screenshots (e.g. system error dialogs, GUI
		// state) the user is asking about.
		Images []string `json:"images"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Message == "" && len(req.Images) == 0 {
		http.Error(w, "message or image required", http.StatusBadRequest)
		return
	}
	if req.ApplianceID == "" || udb == nil {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	// Resolve own OR shared. Sessions stay in the requester's udb; a shared
	// repo's clone/store is read under ownerUser inside runSession.
	appliance, ownerUser, _, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}

	label := appliance.Name + ": " + req.Message
	if len(label) > 80 {
		label = label[:80]
	}

	// Resolve the persisted chat session — create one (titled from this
	// message) on a fresh conversation, or continue the supplied one.
	// The session id doubles as the run id, so it's stable across turns
	// and the client's session_id / EventsURL / cancel / deep-link all
	// key off this single value. The stale-cleanup race from reusing an
	// id across runs is handled by the pointer-guard in
	// LiveSessionMap.ScheduleCleanupAfter.
	sid := ensureSession(udb, appliance.ID, req.SessionID, req.Message)
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
	// Decode any image attachments — base64 → bytes — and attach to
	// the final user message so the LLM sees text + images on the
	// same turn. Skips silently on decode failure so a corrupt
	// upload doesn't break the whole chat request.
	var userImages [][]byte
	for _, b64 := range req.Images {
		if strings.TrimSpace(b64) == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err == nil && len(raw) > 0 {
			userImages = append(userImages, raw)
		}
	}
	hist = append(hist, Message{Role: "user", Content: req.Message, Images: userImages})

	// Pre-create the injection queue so /api/inject finds it immediately
	// (the user could plausibly inject before the worker goroutine even
	// installs OnRoundStart). Servitor doesn't gate by owner today —
	// RequireUser in handleInject confirms the requester is logged in,
	// no per-queue cross-check — so the registration leaves Owner empty.
	RegisterInjectionQueue(sid, "", "")

	go T.runSession(ctx, sid, userID, ownerUser, appliance, ch, hist, udb, false)

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
	q := LookupInjectionQueue(req.ID)
	if q == nil {
		http.Error(w, "session not found or not interjectable", http.StatusNotFound)
		return
	}

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
	appliance, ownerUser, _, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}

	sid := UUIDv4()
	ctx, cancel := context.WithCancel(AppContext())
	probeSessions.Register(sid, "Refreshing "+appliance.Name, cancel)
	sessionAppliances.Store(sid, appliance.ID)
	ch := make(chan bool, 1)
	confirmChans.Store(sid, ch)

	if appliance.Type == "command" {
		// Command-type appliances: map the command's CLI structure. The
		// resulting reference doubles as the appliance profile (saveProfile).
		go T.runMapAppSession(ctx, sid, userID, ownerUser, appliance, appliance.Command, ch, udb, true)
	} else {
		// Every Map System press does a FULL re-mapping — fresh
		// reconnaissance regardless of whether a profile already
		// exists. The previous "update and extend" path on re-runs
		// produced incremental drift; users hit Map System when they
		// want a clean re-derivation. Facts (stored separately in
		// the appliance facts table) survive the re-map; only the
		// profile blob is overwritten.
		mapMsg := fmt.Sprintf(
			"Perform a complete reconnaissance and profile of the Linux appliance at %s (connected as %s). Be systematic and thorough. Discover all services, configurations, and log file locations.",
			appliance.Host, appliance.User,
		)
		if appliance.Type == "repo" {
			mapMsg = fmt.Sprintf(
				"Map the codebase in the repository %s. Be systematic and thorough: identify the language and framework, trace the architecture and major subsystems, find the data model and entry points, and record how the parts connect as a code map.",
				repoDisplayTarget(appliance),
			)
		}
		hist := []Message{{Role: "user", Content: mapMsg}}
		// Persist the map as a session so it shows in the left rail and its
		// reconnaissance summary stays reviewable, not just streamed live.
		// The run id IS the session id; runSession appends the transcript on
		// done. Each Map System run is its own rail entry (timestamped).
		saveSession(udb, appliance.ID, chatSession{ID: sid, Name: "Refresh: " + appliance.Name})
		// runSession re-clones + re-ingests repos internally when
		// saveProfile=true (the repo analogue of SSH reconnaissance), so a
		// Refresh already picks up new code without a separate pull here.
		go T.runSession(ctx, sid, userID, ownerUser, appliance, ch, hist, udb, true)
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
	appliance, _, _, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}

	sid := UUIDv4()
	ctx, cancel := context.WithCancel(AppContext())
	probeSessions.Register(sid, "Mapping "+req.Command+" on "+appliance.Name, cancel)
	sessionAppliances.Store(sid, appliance.ID)
	ch := make(chan bool, 1)
	confirmChans.Store(sid, ch)

	// saveProfile=false: an SSH appliance's profile is the system
	// reconnaissance — a single CLI's reference must not replace it.
	go T.runMapAppSession(ctx, sid, userID, "", appliance, req.Command, ch, udb, false)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"session_id": sid})
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
	// Clearing shared memory affects every user of a shared appliance, so it's
	// an owner/admin-only management action, operating on the owner's store.
	rec, ownerUser, ownerUDB, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}
	if !canManageAppliance(userID, rec, servitorIsAdmin(r)) {
		http.Error(w, "not allowed to clear this appliance's memory", http.StatusForbidden)
		return
	}
	clearApplianceMemory(ownerUDB, req.ApplianceID)
	// Repo appliances: also drop the ingested code files. Reset the clone
	// bookkeeping so the record reflects "needs re-clone". Connection settings
	// (URL/branch/token) are kept, mirroring how SSH settings survive a clear.
	if rec.Type == "repo" {
		wipeRepoFiles(ownerUser, req.ApplianceID)
		rec.RepoFiles = 0
		rec.RepoCloned = ""
		ownerUDB.Set(applianceTable, req.ApplianceID, rec)
	}
	w.WriteHeader(http.StatusOK)
}

// handleRepoRefresh re-clones a repo appliance and re-ingests its files,
// picking up new commits (or restoring files after a memory clear). The clone
// runs in the background; the client polls the appliance list for RepoFiles.
func (T *Servitor) handleRepoRefresh(w http.ResponseWriter, r *http.Request) {
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
	rec, ownerUser, ownerUDB, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}
	if rec.Type != "repo" {
		http.Error(w, "not a repository appliance", http.StatusBadRequest)
		return
	}
	// Re-cloning replaces the shared code for everyone, so gate it to owner+admin.
	if !canManageAppliance(userID, rec, servitorIsAdmin(r)) {
		http.Error(w, "not allowed to refresh this repository", http.StatusForbidden)
		return
	}
	// Run the re-clone as a live, cancelable session (mirrors handleMap) so the
	// UI shows a spinner + live status and offers Cancel, instead of a silent
	// 202. The AgentLoopPanel subscribes to the same event stream.
	sid := UUIDv4()
	ctx, cancel := context.WithCancel(AppContext())
	probeSessions.Register(sid, "Refreshing "+rec.Name, cancel)
	sessionAppliances.Store(sid, rec.ID)
	go func() {
		defer cancel()
		emit(sid, probeEvent{Kind: "status", Text: fmt.Sprintf("Re-cloning %s…", repoDisplayTarget(rec))})
		T.cloneAndIngestRepo(ctx, ownerUser, ownerUDB, rec.ID)
		if ctx.Err() != nil {
			probeSessions.AppendEvent(sid, probeEvent{Kind: "error", Text: "Refresh cancelled."}, true)
			probeSessions.ScheduleCleanup(sid)
			return
		}
		files := 0
		var updated Appliance
		if ownerUDB.Get(applianceTable, rec.ID, &updated) {
			files = updated.RepoFiles
		}
		emit(sid, probeEvent{Kind: "status", Text: fmt.Sprintf("Ingested %d files.", files)})
		// Validate the stored knowledge against the freshly-pulled code and
		// auto-correct stale docs. Runs under ctx, so Cancel stops it too.
		T.runRepoMemoryAudit(ctx, sid, ownerUser, ownerUDB, rec)
		probeSessions.AppendEvent(sid, probeEvent{Kind: "done"}, true)
		probeSessions.ScheduleCleanup(sid)
	}()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"session_id": sid})
}

// handleCollectionsList returns the caller's knowledge collections (their own +
// deployment-scoped) as [{id,name,description}], so the appliance edit form can
// render the "Linked Knowledge" picker. Read-only; the selection itself is saved
// on the appliance record via the normal appliance POST (Collections field).
func (T *Servitor) handleCollectionsList(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	type item struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}
	out := []item{}
	for _, c := range ListCollections(UserDB(CollectionsDB(), userID), userID) {
		out = append(out, item{ID: c.ID, Name: c.Name, Description: c.Description})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
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
	// Same-origin only. This terminal WS is cookie-authenticated (behind
	// AuthMiddleware) and streams a live shell, so a permissive CheckOrigin would
	// allow cross-site WebSocket hijacking — a malicious page opening the socket
	// with the victim's session cookie. It's a browser-only surface with no
	// legitimate cross-origin client, so require the handshake Origin to match the
	// host (SameOriginRequest returns true when Origin is absent, i.e. a
	// non-browser client, which still needs a valid session cookie to get here).
	CheckOrigin: SameOriginRequest,
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
	appliance, _, _, found := T.resolveAppliance(userID, udb, id)
	if !found {
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

	// Subscribe to the appliance's persistent terminal buffer. The
	// snapshot returned is everything mirrored so far (capped to
	// termBufferCap bytes); replay it first so the viewer sees prior
	// activity, then live writes pick up where the snapshot ended.
	// Subscribe is synchronized with append, so there's no gap where
	// a write could land between snapshot and live-stream.
	tb := termBufferFor(userID, id)
	entry := &termWriterEntry{write: wsWrite}
	history := tb.subscribe(entry)
	defer tb.unsubscribe(entry)

	// Force-reset the terminal to US ASCII before anything else. Some
	// SSH programs emit DEC SCS (Select Character Set) escapes — e.g.
	// "\x1b(K" for German NRCS or "\x1b(H" for Swedish — that remap
	// ASCII bracket bytes to localized letters ('[' → 'Ä', ']' → 'Å'
	// under Swedish) and never reset them. Once the G0 set is stuck in
	// an NRCS, every subsequent ASCII bracket renders wrong. Three
	// bytes restore sanity: ESC ( B (G0 = US ASCII), ESC ) B (G1 = US
	// ASCII), SI (invoke G0). Idempotent and harmless if the terminal
	// is already in US ASCII mode.
	const charsetReset = "\x1b(B\x1b)B\x0f"

	// Send a brief header so the user can tell at a glance whether
	// they're looking at backfilled history or live streaming. When
	// the buffer is empty it doubles as the "no activity yet" placeholder.
	header := charsetReset + "\x1b[2m── " + appliance.Name + " — "
	if len(history) == 0 {
		header += "waiting for agent activity"
	} else {
		header += "showing prior activity (" + fmt.Sprintf("%d bytes", len(history)) + ")"
	}
	header += " ──\x1b[0m\r\n"
	wsWrite([]byte(header))
	if len(history) > 0 {
		wsWrite(history)
	} else {
		wsWrite([]byte(terminalPrompt(appliance)))
	}

	// Keep the connection open until the client disconnects.
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			return
		}
	}
}

// asciiDiagramRule is shared diagram-formatting guidance referenced by
// every servitor prompt that may produce architecture or topology output.
// All diagrams should be plain ASCII inside ```text fenced blocks; Mermaid
// is explicitly forbidden because it renders as raw text in this UI.
const asciiDiagramRule = "## Diagrams\n\n" +
	"When the user asks for a diagram, or when a diagram would clarify architecture, network topology, service connections, application dependencies, request flow, or routing, produce a plain ASCII diagram inside a ` ```text ` code block — never Mermaid, PlantUML, or any other DSL.\n\n" +
	"**Style:**\n" +
	"- Use `+--...--+` boxes for hosts/services, `[ ]` for inline labels, `-->` / `<--` / `<-->` arrows for directed flow.\n" +
	"- Label arrows with the protocol or port: `--:3306-->` or `--HTTP:8080-->`.\n" +
	"- Group nodes into zones with a surrounding box or dashed border and a label in the top-left corner.\n" +
	"- Align columns so the diagram reads cleanly in a fixed-width font.\n\n" +
	"**What to avoid:**\n" +
	"- Mermaid / PlantUML / GraphViz syntax — they will not render correctly in this context and appear as raw text to the reader.\n" +
	"- Overly dense diagrams — split into multiple focused diagrams (one per tier or function) for complex topologies.\n\n"

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
	b.WriteString("8. **Saving work** — when the user asks to save a query or script for later, call `save_to_codewriter`. When the user asks to document findings, save a report, or create a runbook, call `save_to_techwriter`. When the user asks to add a finding into one of their guides (living multi-section docs), call `push_to_guide` — use `list_guides` first if unsure of the exact guide name. You can save AND run in the same response.\n\n")
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
			"You are a Linux systems assistant with SSH access to %s (%s). No profile has been built yet — you can run SSH commands to investigate specific questions. For a complete system overview, suggest the user clicks 'Refresh'.",
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
	b.WriteString("13. **Saving work** — when the user asks to save a query or script for later reuse, call `save_to_codewriter`. When the user asks to document findings, save a report, or write a runbook, call `save_to_techwriter`. When the user asks to add a finding into one of their guides (living multi-section docs), call `push_to_guide` — use `list_guides` first if unsure of the exact guide name. You can save AND run in the same response.\n\n")
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
			"If data is genuinely absent for a section, write 'Not investigated in this scan.' — do not fabricate.\n\n",
		appliance.Name, appliance.Host,
	))
	b.WriteString(asciiDiagramRule)
	b.WriteString("Apply the diagram rule when producing sections 5 (Network Configuration), 9 (Service Communication Map), 14 (Application Dependency Graph), 17 (Database Access Patterns), and 18 (Routing & Request Handling) — these all benefit from a small ASCII diagram in addition to prose.\n")
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
	if appliance.Type == "repo" {
		return buildRepoInvestigatorPrompt(appliance)
	}
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
	b.WriteString("`link_entities` builds the SYSTEM MAP as a graph — use it for architecture and every connection you find, one relationship per call: nginx → proxies to → node:3000; node → connects to → postgres:5432; node → uses → Redis (sessions). Each service/database/app is its OWN entity; record its version/port/path as subject_attrs. This is your primary structural output — a topology you can traverse, not flat facts on one node.\n")
	b.WriteString("`record_discovery` is for narrative INSIGHTS that don't reduce to a single relationship:\n")
	b.WriteString("- **Database access**: exact working connection method, credentials, schemas, key tables\n")
	b.WriteString("- **Operations**: how to deploy, restart, tail logs, connect to databases on THIS system specifically\n")
	b.WriteString("- **Configuration**: where configs live, key settings, non-standard paths\n")
	b.WriteString("- **Security posture**: firewall state, sudo rules, exposed credentials, certificate expiry\n\n")
	b.WriteString("`store_fact` for appliance-wide properties: os, hostname, kernel, architecture.\n")
	b.WriteString("`record_technique` for confirmed working commands: exact auth methods, non-standard binary paths.\n\n")
	b.WriteString("## Workflow\n\n")
	b.WriteString("1. Orient yourself from the system snapshot — identify the primary role and main services\n")
	b.WriteString("2. For each significant service: probe its config, its connections, its auth — follow the chain\n")
	b.WriteString("3. Each `probe` call has ONE clear goal; the investigator (you) decides the next step based on what it returns\n")
	b.WriteString("4. Stop when you have: the system's purpose, its full architecture, working DB access for every engine, operational procedures\n\n")
	b.WriteString("Quality over quantity: 15 focused probes that reveal real configuration beat 40 broad sweeps.\n\n")
	b.WriteString("## Pacing: defer, don't abandon\n\n")
	b.WriteString("If a step is slow — you've tried 2-3 angles and it isn't advancing — move on to another step rather than grinding. Mark the next step `mark_step_in_progress` and work it; the slow step stays unfinished and you revisit it later with what you learned (the answer is often hiding in a config you've now read or a service you've now mapped). Coming back fresh is usually faster than continuing to grind.\n\n")
	b.WriteString("**Running low on rounds is NOT a reason to block a step.** The investigation automatically receives additional rounds to finish any step still pending or in progress, so leaving a step unfinished is ALWAYS better than closing it out under time pressure. Never call `mark_step_blocked` with a reason like \"no time remaining\", \"ran out of rounds\", or \"out of time\" — that is invalid; just leave the step unfinished and keep working, and you'll be given the rounds to complete it. Reserve `mark_step_blocked` for GENUINE dead-ends only: no access, a required tool is missing, the target is unreachable, or every reasonable angle has been exhausted.\n\n")
	b.WriteString("## Acronyms\n\n")
	b.WriteString("Internal acronyms have org-specific meanings that rarely match training-data priors. Treat any acronym as an opaque label until you have verified its meaning from the system itself — a README, comment, config-file annotation, log message, or explicit statement in documentation. If you only know the letters, use the letters. Writing 'GMS (Game Management System)' when nothing on this system explained what GMS stands for is fabrication, even when the expansion 'sounds plausible'. Probe to find the meaning, or leave it unexpanded.\n\n")
	b.WriteString("## Completion\n\n")
	b.WriteString("When done, write a concise narrative of your key findings. The structured profile is built from your stored discoveries and facts — focus on recording those.\n")
	return b.String()
}

// buildProbeWorkerPrompt is the system prompt for a focused worker dispatched by the investigator.
// The worker executes a specific task with a limited command budget and reports findings clearly.
func buildProbeWorkerPrompt(appliance Appliance) string {
	if appliance.Type == "repo" {
		return buildRepoProbeWorkerPrompt(appliance)
	}
	var b strings.Builder
	writePersona(&b, appliance)
	b.WriteString(fmt.Sprintf(
		"You are a focused SSH executor on **%s** (%s). "+
			"An investigator has sent you a specific task — execute it precisely.\n\n",
		appliance.Name, appliance.Host,
	))
	b.WriteString("## Rules\n\n")
	b.WriteString("- Run only the commands needed to complete the task — **maximum 10 tool calls**\n")
	b.WriteString("- Record a concrete value on the RIGHT node: `store_fact` ONLY for appliance-wide properties (os, hostname, kernel, arch). A value about a specific component — a service version/port, an app's config path, a database's auth — goes in `link_entities` as `subject_attrs` on that component's entity, NOT store_fact (which would pile everything onto the appliance)\n")
	b.WriteString("- Call `link_entities` to record how parts of the system CONNECT — a service to its port, an app to its database, a process to its config file, a service to the host — with the component's details in `subject_attrs`. This builds the system map as a real topology instead of one overloaded node\n")
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
	// Every Map System run is a fresh, complete reconnaissance — the
	// previous "update and extend" branch on re-runs left the
	// investigator anchored to the prior profile (and often the prior
	// plan), which produced incremental drift instead of the
	// re-derivation the operator asked for. Facts + techniques +
	// discoveries from prior runs live in their own tables and
	// persist across re-maps; only the profile blob is rewritten.
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
		mapExecutionProtocol + "\n\n")
	b.WriteString(asciiDiagramRule)
	return b.String()
}

// buildLeadSystemPrompt constructs the prompt for the lead Knowledge Manager LLM.
// The lead maintains structured knowledge docs about the system and dispatches the worker
// on precise, context-rich investigations. It never runs SSH commands directly.
// leadStaticGuidance writes the appliance-INDEPENDENT investigator core
// (investigation approach, mid-investigation user-note handling, the
// anti-fabrication rules, and the diagram rule). Extracted so the live
// buildLeadSystemPrompt and the orchestrate template agent
// (app-servitor-investigator, see appliance_memory.go) share ONE copy of the
// guidance; the per-appliance persona, docs, facts, and rules are layered on
// separately (scoped memory + the per-run message) at the call site.
func leadStaticGuidance(b *strings.Builder) {
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

	b.WriteString(asciiDiagramRule)
}

// linkedKnowledgeNote tells the lead that curated knowledge collections are
// linked to this appliance, so it dispatches search_knowledge lookups and
// grounds answers in that reference material alongside live findings. Empty when
// nothing is linked. Shared by the SSH/command and repo lead prompts.
func linkedKnowledgeNote(a Appliance) string {
	if len(a.Collections) == 0 {
		return ""
	}
	return "## Linked reference knowledge\n\n" +
		"The owner has attached curated knowledge (runbooks, vendor docs, guides) to this appliance. When a question could be answered or corroborated by that material, dispatch a worker to call `search_knowledge` — it searches the linked collections and does NOT touch the live system — and fold what it returns into your answer. Prefer verified system evidence; use linked knowledge to fill gaps and cross-check.\n\n"
}

func buildLeadSystemPrompt(udb Database, appliance Appliance, docs map[string]string, cachedFacts, cachedNotes, cachedTechniques, cachedRules, cachedDiscoveries string, hasFreshImage bool) string {
	if appliance.Type == "repo" {
		return buildRepoLeadPrompt(appliance, docs, cachedFacts, cachedNotes, cachedTechniques, cachedRules, cachedDiscoveries)
	}
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

	leadStaticGuidance(&b)
	b.WriteString(linkedKnowledgeNote(appliance))

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
	if gb := scopedGraphBlock(appliance); gb != "" {
		b.WriteString("## System Map (components and how they connect)\n\n")
		b.WriteString("The topology recorded in prior sessions — services, databases, apps, and their relationships. Use it to target probes precisely; re-verify live state.\n\n")
		b.WriteString(gb)
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
	if hasFreshImage {
		// When the user attaches a paperclip image, the lead LLM has
		// observed it referencing UI screenshot descriptions from
		// `supplementContext` (workspace supplements processed by
		// extractPDFScreenshots) instead of the actual attached image —
		// the LLM pattern-matches "image" in the prompt context and
		// confabulates from supplement text. Make the disambiguation
		// explicit so the model knows which image to describe.
		b.WriteString("## Current Turn Has An Attached Image\n\n")
		b.WriteString("The user attached one or more IMAGES directly to their current message — those bytes are included with this turn's user message and you should be looking at them now. Describe THAT specific attached image. Do NOT substitute or reference any image described in **Reference Documents** above; those are different images uploaded earlier as workspace supplements and have no relationship to the user's current attachment. If you cannot actually see the attached image in this turn, say so plainly (\"I don't see image bytes attached to this turn\") — do not invent a description by reading from supplements.\n\n")
	}
	return b.String()
}

// buildConsolidationPrompt returns the system prompt for the background knowledge
// consolidation agent that runs after each chat turn to catch missed facts/techniques.
func buildConsolidationPrompt(appliance Appliance) string {
	if appliance.Type == "repo" {
		return buildRepoConsolidationPrompt(appliance)
	}
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
	b.WriteString("3. Call `store_fact` for APPLIANCE-WIDE values — properties of the box as a whole: os, hostname, kernel, architecture.\n")
	b.WriteString("4. Call `link_entities` for the SYSTEM TOPOLOGY — how named components connect: a service to its port, an app to its database, a process to its config file, a service to the host. Each service/database/app is its OWN entity; record its version/path/port as subject_attrs. This is the main way you build knowledge here — do it for every relationship in the findings so the map becomes a real graph, not flat facts on one node.\n")
	b.WriteString("5. Call `record_technique` for every confirmed working approach: exact command syntax, successful auth method, non-standard binary path.\n")
	b.WriteString("6. Call `record_discovery` for a narrative insight worth reusing that isn't a single relationship — a working database access method, an operational procedure, a security-posture finding.\n")
	b.WriteString("7. Call `note_lesson` for any dead end or wrong assumption the exchange revealed — a path that turned out empty, a config that wasn't where expected, an approach that failed — so future sessions don't repeat it.\n")
	b.WriteString("8. Do not duplicate — check `read_doc` content before updating a doc; graph entities auto-merge by name.\n")
	b.WriteString("9. If nothing new was found beyond what is already stored, call no tools.\n")
	b.WriteString("10. Do NOT produce any text response. Your output must be tool calls only.\n")
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
// saveProfile additionally writes the reference into appliance.Profile — set by the
// command-appliance Map path, where the CLI reference IS the profile (the Profile
// panel otherwise stays empty after a successful map). Map App runs on SSH appliances
// pass false: their profile is the system reconnaissance, not one CLI's reference.
// ownerUser routes the profile write to the appliance owner's store (shared appliances
// keep one profile regardless of who mapped); only used when saveProfile is set.
func (T *Servitor) runMapAppSession(ctx context.Context, id, userID, ownerUser string, appliance Appliance, command string, confirm chan bool, udb Database, saveProfile bool) {
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

	// cmdCount keys off the PARSED command base (first non-wrapper
	// word after stripping sudo/nice/timeout/etc.), not the verbatim
	// command string. This catches the "20 variants of grep" failure
	// mode where the LLM rapidly mutates args but keeps hammering the
	// same tool — previously the counter only triggered on byte-exact
	// repeats. baseKey returns "?" for an unparseable line; we still
	// rate-limit those by treating them as a single counter.
	cmdCount := make(map[string]int)
	var cmdMu sync.Mutex // protects cmdCount — agent may issue parallel tool calls
	baseKey := func(cmd string) string {
		segs := shell_segments(cmd)
		if len(segs) == 0 {
			return "?"
		}
		name, _ := parse_cmd(segs[0])
		if name == "" {
			return "?"
		}
		return name
	}

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
			key := baseKey(cmd)
			cmdMu.Lock()
			cmdCount[key]++
			count := cmdCount[key]
			cmdMu.Unlock()
			// Higher threshold (5) when keying by base since legit
			// exploration of one tool's surface (e.g. several grep
			// variants narrowing down a search) is common. The pivot
			// nudge in the agent loop catches genuine error streaks
			// independently.
			if count > 5 {
				return fmt.Sprintf("Note: %s has been called %d times this session. Recommending checking other vectors first before continuing — a different tool or angle may move faster than more variants of %s. If %s really is the right tool here, try narrowing the scope (smaller path, more specific pattern) and continue.", key, count-1, key, key), nil
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
		// Previously a silent return: the user watched the exploration in the
		// chat feed and then nothing saved, with no explanation. Say so.
		if ctx.Err() == nil {
			emit(id, probeEvent{Kind: "error", Text: "Mapping ended without a final reference document — nothing was saved. Re-run Map."})
		}
		return
	}

	emit(id, probeEvent{Kind: "reply", Text: reply})

	if udb != nil {
		writeDoc(udb, appliance.ID, "cli:"+command, reply)
		emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("CLI map saved: %s", command)})
	}

	// Command-appliance profile write. Same owner-store routing + thin-reply
	// guards as runSession's saveProfile block: never clobber a real profile
	// with a stub, and don't save from a cancelled run.
	if saveProfile && ctx.Err() == nil {
		ownerUDB := udb
		if ownerUser != "" && ownerUser != userID {
			if o := UserDB(T.DB, ownerUser); o != nil {
				ownerUDB = o
			}
		}
		if ownerUDB != nil {
			var existing Appliance
			if ownerUDB.Get(applianceTable, appliance.ID, &existing) {
				prior := strings.TrimSpace(existing.Profile)
				tooThin := len(reply) < minMapProfileChars ||
					(prior != "" && len(reply) < len(prior)/3)
				if tooThin {
					emit(id, probeEvent{Kind: "status", Text: "Map didn't produce a complete profile — keeping the previous one."})
					Log("[servitor.map] kept prior profile for %q (new=%d chars, prior=%d chars)",
						appliance.Name, len(reply), len(prior))
				} else {
					existing.Profile = reply
					existing.Scanned = time.Now().Format(time.RFC3339)
					ownerUDB.Set(applianceTable, appliance.ID, existing)
					emit(id, probeEvent{Kind: "status", Text: "Profile updated."})
				}
			}
		}
	}
}

// runSession acquires a pooled SSH connection, runs the agent loop, and streams events.
// The connection is NOT closed on return — it stays pooled for the next request.
// saveProfile=true writes the final LLM reply and extracted log map back to the appliance record.
// runSession runs an investigation. userID/udb are the REQUESTING user's
// (connection, terminal, chat sessions, per-user notes) while ownerUser is the
// appliance's owner — used for the shared repo clone/store so a shared repo is
// read from ONE place regardless of who opened it. For a non-shared appliance
// ownerUser == userID, so behavior is unchanged. Scoped memory is keyed by the
// appliance ID (global), so it's shared with no extra plumbing.
func (T *Servitor) runSession(ctx context.Context, id, userID, ownerUser string, appliance Appliance, confirm chan bool, messages []Message, udb Database, saveProfile bool) {
	defer func() {
		confirmChans.Delete(id)
		pendingCmds.Delete(id)
		ReleaseInjectionQueue(id)
		sessionAppliances.Delete(id)
		probeSessions.AppendEvent(id, probeEvent{Kind: "done"}, true)
		probeSessions.ScheduleCleanup(id)
	}()

	if ownerUser == "" {
		ownerUser = userID
	}
	ownerUDB := udb
	if ownerUser != userID {
		ownerUDB = UserDB(T.DB, ownerUser) // shared repo: clone/store live under the owner
	}

	a := &Servitor{}
	a.AppCore = T.AppCore

	if appliance.Type == "workspace" {
		// Slice 1: the record + member picker exist, but the cross-appliance
		// coordinator (scout-then-drill) is not built yet. Fail clearly instead
		// of falling through to the SSH path and dialing an empty host.
		// See docs/servitor-workspace-mvp.md.
		probeSessions.AppendEvent(id, probeEvent{Kind: "error",
			Text: fmt.Sprintf("Workspace %q has %d member(s), but cross-appliance investigation isn't available yet. For now, open a member appliance directly.", appliance.Name, len(appliance.Members))}, true)
		probeSessions.ScheduleCleanup(id)
		return
	}
	if appliance.Type == "command" {
		emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Running locally: %s", appliance.Command)})
	} else if appliance.Type == "repo" {
		// No connection to acquire — probes search/read the encrypted store.
		// A Map run (saveProfile) is the repo analogue of SSH reconnaissance:
		// it re-clones synchronously first so the map reflects CURRENT code and
		// self-heals an empty store (e.g. right after Clear Memory). Q&A runs
		// use whatever is already ingested.
		if saveProfile {
			emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Cloning %s…", repoDisplayTarget(appliance))})
			withHeartbeat(ctx, id, "Cloning repository", func() {
				T.cloneAndIngestRepo(ctx, ownerUser, ownerUDB, appliance.ID)
			})
			if ctx.Err() != nil {
				probeSessions.ScheduleCleanup(id)
				return
			}
		}
		if repoFileCount(ownerUser, appliance.ID) == 0 {
			msg := "Repository not ingested yet — run Refresh to clone and map it."
			if saveProfile {
				msg = "Clone failed — check the Git URL, branch, and access token, then try again. (Is git installed on the host?)"
			}
			probeSessions.AppendEvent(id, probeEvent{Kind: "error", Text: msg}, true)
			probeSessions.ScheduleCleanup(id)
			return
		}
		emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Reading repository %s", repoDisplayTarget(appliance))})
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
	var cmdMu sync.Mutex // protects cmdCount — agent may issue parallel tool calls
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
				cmdMu.Lock()
				cmdCount[cmd]++
				count := cmdCount[cmd]
				cmdMu.Unlock()
				if count > loopLimit {
					msg := fmt.Sprintf("[LOOP DETECTED] run_command(%q) has been called %d times in this session. Running it again will not produce a different result. Stop. Choose a different command, different arguments, or a different investigation strategy.", cmd, count-1)
					emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Loop detected: %q (%dx)", cmd, count-1)})
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
			recordScopedExplicit(appliance, note) // gotcha -> Explicit Memory (always-in-prompt Shortcuts layer)
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
			recordScopedExplicit(appliance, technique) // working command -> Explicit Memory (always-in-prompt Shortcuts layer)
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
			recordScopedReference(ctx, appliance, "discoveries", title, finding) // dual-write to the orchestrate scope (lead migration, slice 1)
			emit(id, probeEvent{Kind: "discovery", Text: "★ " + strings.TrimSpace(title)})
			return "discovery recorded", nil
		},
		NeedsConfirm: false,
	}

	// store_fact — persist an APPLIANCE-WIDE property (not a per-component fact).
	store_fact_tool := AgentToolDef{
		Tool: Tool{
			Name:        "store_fact",
			Description: "Save an APPLIANCE-WIDE property — a fact about the WHOLE system, not any particular service or component: os, hostname, kernel version, CPU architecture, timezone, primary role. Key should be short (e.g. 'os', 'hostname', 'kernel', 'arch'). Facts overwrite on the same key. IMPORTANT: for a fact about a SPECIFIC component — a service's version or port, an app's config path, a database's auth command — do NOT use store_fact (it would pile everything onto the appliance node). Use `link_entities` instead, putting the detail in `subject_attrs` on that component's own entity (e.g. subject='nginx', subject_attrs={'version':'1.24','port':'443'}) so it lands on the right node. Set ttl='short' for volatile appliance-wide state, default 'long' for stable properties.",
			Parameters: map[string]ToolParam{
				"key":   {Type: "string", Description: "Short appliance-wide key, e.g. 'os', 'hostname', 'kernel', 'arch'. NOT a component-specific key like 'nginx_version' — that goes in link_entities subject_attrs."},
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
			// Cutover: facts now live ONLY in the appliance scope (graph attrs);
			// ssh_facts is retired. Short-TTL (ephemeral) facts are not persisted
			// — the graph has no expiry, and live state should be re-probed.
			recordScopedApplianceFact(appliance, key, value, ttl)
			emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Stored fact: %s = %s", key, value)})
			return "fact stored", nil
		},
		NeedsConfirm: false,
	}

	// link_entities — record a RELATIONSHIP in this system's graph map so the
	// knowledge is a real topology (services, configs, dependencies) instead of
	// flat facts piled on one node. store_fact is for appliance-wide properties;
	// link_entities is for everything with structure.
	link_entities_tool := AgentToolDef{
		Tool: Tool{
			Name: "link_entities",
			Description: "Record a RELATIONSHIP between two named parts of this system — the structured graph map. State it subject-relation-object, e.g. subject='nginx' relation='proxies to' object='app on :8080', or subject='app' relation='connects to' object='postgres'. Entities auto-merge by name. Use this whenever you learn how parts connect: a service to its port, an app to its database, a process to its config file, a service to the host. Put non-relational details (version, path, port) in subject_attrs. Use `store_fact` ONLY for appliance-wide properties (os, hostname, kernel); use `link_entities` for anything with structure or relationships.",
			Parameters: map[string]ToolParam{
				"subject":       {Type: "string", Description: "The subject entity's name, e.g. 'nginx', 'app', 'postgres'."},
				"subject_kind":  {Type: "string", Description: "Subject type: service, app, database, host, file, process, port, or thing. Defaults to thing."},
				"relation":      {Type: "string", Description: "The relationship verb, e.g. 'runs on', 'proxies to', 'connects to', 'depends on', 'listens on', 'reads config from'."},
				"object":        {Type: "string", Description: "The object entity's name, e.g. 'postgres', '/etc/nginx/nginx.conf', 'port 5432'."},
				"object_kind":   {Type: "string", Description: "Object type (see subject_kind). Defaults to thing."},
				"subject_attrs": {Type: "object", Description: "Optional non-relational facts about the subject as key/value strings, e.g. {\"version\": \"1.24\", \"port\": \"443\"}."},
				"note":          {Type: "string", Description: "Optional qualifier on the relationship, e.g. 'over unix socket'."},
				"replace":       {Type: "boolean", Description: "True if this CORRECTS a single-valued relation (removes the prior value for this subject+relation)."},
			},
			Required: []string{"subject", "relation", "object"},
		},
		Handler: func(args map[string]any) (string, error) {
			subject, _ := args["subject"].(string)
			relation, _ := args["relation"].(string)
			object, _ := args["object"].(string)
			if strings.TrimSpace(subject) == "" || strings.TrimSpace(relation) == "" || strings.TrimSpace(object) == "" {
				return "", fmt.Errorf("subject, relation, and object are required")
			}
			subjectKind, _ := args["subject_kind"].(string)
			objectKind, _ := args["object_kind"].(string)
			note, _ := args["note"].(string)
			replace, _ := args["replace"].(bool)
			var attrs map[string]string
			if raw, ok := args["subject_attrs"].(map[string]any); ok {
				attrs = make(map[string]string)
				for k, v := range raw {
					if s, ok := v.(string); ok {
						attrs[k] = s
					}
				}
			}
			if err := recordScopedLink(appliance, subjectKind, subject, attrs, relation, objectKind, object, note, replace); err != nil {
				return "", err
			}
			emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Linked: %s → %s → %s", subject, relation, object)})
			return "relationship recorded", nil
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
			if ownerUDB == nil {
				return "", fmt.Errorf("no database")
			}
			// Rules live on the owner's store so they're shared across everyone
			// using the appliance (see the rules read above).
			storeRule(ownerUDB, appliance.ID, rule)
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
			// Cutover: facts live in THIS appliance's scope (graph attrs). Cross-
			// appliance search is no longer supported — each appliance is its own
			// scope — so the optional "appliance" filter is ignored.
			attrs := scopedApplianceFacts(udb, appliance)
			keys := make([]string, 0, len(attrs))
			for k := range attrs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			q := strings.ToLower(query)
			var lines []string
			for _, k := range keys {
				if strings.Contains(strings.ToLower(k), q) || strings.Contains(strings.ToLower(attrs[k]), q) {
					lines = append(lines, "- "+k+": "+attrs[k])
				}
			}
			emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("search_facts %q: %d result(s)", query, len(lines))})
			if len(lines) == 0 {
				return "no facts found", nil
			}
			return strings.Join(lines, "\n"), nil
		},
		NeedsConfirm: false,
	}

	// search_knowledge searches the curated knowledge collections the owner LINKED
	// to this appliance (runbooks, vendor docs, guides) — authoritative reference
	// material to ground answers ALONGSIDE what the worker finds on the system
	// itself. Local-only: a vector search over the owner's collections (query
	// embedded via the local llama.cpp server); it never touches the live system
	// or any third party. Only attached to the worker when the appliance has
	// linked collections.
	search_knowledge_tool := AgentToolDef{
		Tool: Tool{
			Name:        "search_knowledge",
			Description: "Search the curated KNOWLEDGE linked to this appliance (runbooks, vendor docs, guides the owner attached) for material relevant to the task. Returns the top matching passages with their source. Use it to ground your answer in authoritative reference material — it does NOT touch the live system, so pair it with the system-probing tools rather than replacing them.",
			Parameters: map[string]ToolParam{
				"query": {Type: "string", Description: "What to look up, in natural language."},
				"k":     {Type: "number", Description: "Max passages to return (default 5, max 12)."},
			},
			Required: []string{"query"},
		},
		Handler: func(args map[string]any) (string, error) {
			query, _ := args["query"].(string)
			query = strings.TrimSpace(query)
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			k := 5
			if v, ok := args["k"].(float64); ok && int(v) > 0 {
				k = int(v)
				if k > 12 {
					k = 12
				}
			}
			hits := SearchCollections(ctx, CollectionsDB(), ownerUser, appliance.Collections, query, k)
			emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("search_knowledge %q: %d passage(s)", query, len(hits))})
			if len(hits) == 0 {
				return "No matching passages in the linked knowledge.", nil
			}
			var b strings.Builder
			for i, h := range hits {
				label := strings.TrimSpace(h.Title)
				if label == "" {
					label = h.Source
				}
				fmt.Fprintf(&b, "%d. [%s] %s\n\n", i+1, label, strings.TrimSpace(h.Text))
			}
			return strings.TrimSpace(b.String()), nil
		},
		NeedsConfirm: false,
	}

	// Build the worker base prompt and inject what the worker needs directly.
	workerPrompt := func() string {
		if appliance.Type == "command" {
			return buildCommandChatSystemPrompt(appliance, udb)
		}
		if appliance.Type == "repo" {
			// Single-worker update pass (existing profile) reuses the repo
			// investigator discipline; the probe-worker split uses its own
			// prompt at the dispatch site.
			return buildRepoInvestigatorPrompt(appliance)
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
		// Facts now come from the appliance's SCOPE (graph entity attrs), not
		// ssh_facts. Graph attrs carry no per-fact age, so the prompt leans on
		// the standing "always re-probe live state" rule rather than age cutoffs.
		if fb := scopedFactsBlock(udb, appliance); fb != "" {
			cachedFacts = fb
			workerPrompt += "\n\n## What We Already Know About This System\n\n"
			workerPrompt += fmt.Sprintf("Current time: %s\n\n", now.Format("2006-01-02 15:04 MST"))
			workerPrompt += "Verified facts recorded in prior sessions. For anything about LIVE state " +
				"(running processes, logged-in users, open ports, current disk/memory usage) always re-probe; " +
				"configuration, versions, hardware, and paths can be trusted.\n\n"
			workerPrompt += cachedFacts
		}
		if gb := scopedGraphBlock(appliance); gb != "" {
			workerPrompt += "\n\n## System Map (components and how they connect)\n\n" + gb + "\n"
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
		if rules := rulesForAppliance(ownerUDB, appliance.ID); len(rules) > 0 {
			// Rules are the owner's operator directives for THIS appliance — read
			// from the owner's store so a shared appliance applies the same
			// standing instructions for everyone, not just the owner.
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

	// list_guides / push_to_guide — the user asked to "add what I look up to a
	// guide". These write into the user's Guides via the generic core
	// DocumentTarget seam (guides registers itself; servitor never imports it),
	// same local-write posture as save_to_techwriter. Content lands as a new
	// section the user can polish in the Guides app; it's a revision like any edit.
	list_guides_tool := AgentToolDef{
		Tool: Tool{
			Name:        "list_guides",
			Description: "List the user's existing guides (living multi-section documents in the gohort Guides app), so you can pick the right one to push a finding into with push_to_guide. Local read — do NOT look for guides on the remote system. No arguments.",
		},
		Handler: func(args map[string]any) (string, error) {
			ds := ListDocuments(userID, "guide")
			if len(ds) == 0 {
				return "The user has no guides yet. push_to_guide with a new guide name will create one.", nil
			}
			var b strings.Builder
			b.WriteString("The user's guides (push into one by its name, or a new name to create one):\n")
			for _, d := range ds {
				fmt.Fprintf(&b, "- %s\n", d.Title)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
		NeedsConfirm: false,
	}

	push_to_guide_tool := AgentToolDef{
		Tool: Tool{
			Name:        "push_to_guide",
			Description: "Add a finding from this investigation to one of the user's GUIDES (living documents in the gohort Guides app) as a new section. Local save action — do NOT run anything on the appliance or look for Guides on the remote system. Use when the user asks to add/document something you looked up into a guide (\"add the cron jobs to my Ops guide\"). If a guide with the given name exists it's appended to; otherwise a new guide by that name is created. Call list_guides first if unsure of the exact name.",
			Parameters: map[string]ToolParam{
				"guide":         {Type: "string", Description: "The target guide's name (e.g. 'Ops', 'DB Runbook'). If none matches an existing guide, a new guide with this name is created."},
				"section_title": {Type: "string", Description: "Title for the new section (e.g. 'Cron jobs', 'Disk layout')."},
				"content":       {Type: "string", Description: "The section body in markdown — the finding, written up cleanly. No top-level heading; the title is separate."},
			},
			Required: []string{"guide", "section_title", "content"},
		},
		Handler: func(args map[string]any) (string, error) {
			guide, _ := args["guide"].(string)
			title, _ := args["section_title"].(string)
			content, _ := args["content"].(string)
			guide = strings.TrimSpace(guide)
			content = strings.TrimSpace(content)
			if content == "" {
				return "", fmt.Errorf("content is required")
			}
			// Resolve the guide by name; create a new one when nothing matches.
			docID, newTitle := "", ""
			if guide != "" {
				for _, d := range ListDocuments(userID, "guide") {
					if strings.EqualFold(strings.TrimSpace(d.Title), guide) {
						docID = d.ID
						break
					}
				}
				if docID == "" {
					newTitle = guide
				}
			}
			if _, err := AppendToDocument(ctx, userID, "guide", docID, newTitle, title, content); err != nil {
				return "", fmt.Errorf("push to guide failed: %w", err)
			}
			if newTitle != "" {
				return fmt.Sprintf("Created guide %q and added the %q section.", newTitle, strings.TrimSpace(title)), nil
			}
			return fmt.Sprintf("Added the %q section to the %q guide.", strings.TrimSpace(title), guide), nil
		},
		NeedsConfirm: false,
	}

	// workerTools holds a placeholder run_command entry. Every call site must use
	// withFreshRunTool(workerTools) so each invocation gets isolated counters.
	var workerTools []AgentToolDef
	if appliance.Type == "repo" {
		// Repo workers search/read the encrypted code store instead of
		// executing commands; the recording/plan/map tools are shared and
		// scope-based, so they carry over unchanged.
		workerTools = append(repoCodeTools(ownerUser, appliance.ID),
			note_lesson_tool, record_technique_tool, record_discovery_tool, store_fact_tool, link_entities_tool, store_rule_tool, search_facts_tool,
			save_to_codewriter_tool, save_to_techwriter_tool, push_to_guide_tool, list_guides_tool,
		)
	} else if appliance.Type == "command" {
		workerTools = []AgentToolDef{
			newRunTool(), read_log_tool, search_logs_tool,
			note_lesson_tool, record_technique_tool, record_discovery_tool, store_fact_tool, link_entities_tool, store_rule_tool, search_facts_tool,
			count_lines_tool, read_range_tool, save_to_codewriter_tool, save_to_techwriter_tool, push_to_guide_tool, list_guides_tool,
		}
	} else {
		workerTools = []AgentToolDef{
			newRunTool(), read_log_tool, search_logs_tool, newRunPtyTool(),
			note_lesson_tool, record_technique_tool, record_discovery_tool, store_fact_tool, link_entities_tool, store_rule_tool, search_facts_tool,
			count_lines_tool, read_range_tool,
			watch_condition_tool, list_watches_tool, save_to_codewriter_tool, save_to_techwriter_tool, push_to_guide_tool, list_guides_tool,
		}
	}
	// Curated linked knowledge (owner-attached collections) is searchable by the
	// worker via search_knowledge — added for every appliance type, but only when
	// the appliance actually has collections linked, so agents without any don't
	// see a dead tool.
	if len(appliance.Collections) > 0 {
		workerTools = append(workerTools, search_knowledge_tool)
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
			var snapshot string
			if appliance.Type == "repo" {
				emit(id, probeEvent{Kind: "status", Text: "Reading repository layout…"})
				snapshot = runRepoSnapshot(ownerUser, appliance.ID)
			} else {
				emit(id, probeEvent{Kind: "status", Text: "Taking system snapshot…"})
				snapshot = runQuickSnapshot(ctx, sshExec)
			}
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
					return fmt.Sprintf("Step %d marked done. Move to the next pending step, OR if all pending steps are done, revisit any blocked steps now (call mark_step_in_progress on them — what you learned from the other steps often unblocks them).", stepID), nil
				},
			}
			mark_step_blocked_tool := AgentToolDef{
				Tool: Tool{
					Name:        "mark_step_blocked",
					Description: "Mark a plan step as a GENUINE dead-end — the step cannot be completed no matter how many more rounds you have: no access, a required tool is missing, the target service is unreachable, or every reasonable angle has been exhausted. Do NOT use this to hide difficulty on the first attempt (try a couple of angles first), and NEVER use it because you are low on rounds or 'out of time' — that is not a blocker. Unfinished steps should be left pending/in-progress, not blocked; the investigation automatically receives more rounds to finish pending work, and a slow step is faster revisited after you've worked the rest of the plan. The reason appears in the final report's gap section, so it must describe a real obstacle, never a time/round limit.",
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
			// Re-mapping banner — when the appliance already had a
			// profile coming into this run, the prior facts /
			// discoveries / techniques below can mislead the
			// investigator into skipping the plan ("we already know
			// this system"). Spell out that this is a fresh re-derivation
			// before listing the prior context so the LLM doesn't read
			// the prior data as "work is done."
			if saveProfile && strings.TrimSpace(appliance.Profile) != "" {
				invMsg.WriteString("## RE-MAPPING (full re-derivation)\n\n")
				invMsg.WriteString("This system was mapped before — prior facts, discoveries, and techniques are listed below FOR YOUR REFERENCE ONLY. They are NOT a substitute for a fresh investigation. Re-verify what's still true, discover what's changed, and produce a complete new profile.\n\n")
				invMsg.WriteString("You MUST emit a fresh `set_plan` as your first tool call. The previous plan is gone; treat this run as a clean slate that benefits from prior context, not as a continuation.\n\n")
			}
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
				if gb := scopedGraphBlock(appliance); gb != "" {
					invMsg.WriteString("## System Map so far (extend it — don't re-map what's here)\n\n")
					invMsg.WriteString(gb)
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
			var invHistory []Message
			var invErr error
			investigatorTools := []AgentToolDef{
				set_plan_tool, mark_step_in_progress_tool, record_step_findings_tool, mark_step_blocked_tool,
				revise_plan_tool, report_gaps_tool,
				probe_tool, store_fact_tool, link_entities_tool, record_discovery_tool, record_technique_tool, note_lesson_tool,
			}
			assertOnlyAllowedTools("servitor.investigator", investigatorTools, servitorOrchestratorToolAllowList)
			// Per-step pacing reset — the soft-pacing windows
			// (midpoint nudge, wrap-up warning, failure streak)
			// rebase whenever the in_progress step ID changes. Stops
			// the "you're near the wrap-up cap" message from firing
			// at the wrong moment when the LLM is just starting a
			// fresh step. The closure tracks the last step we
			// announced a reset for; returns true once per real
			// transition.
			lastInProgressStep := 0
			stepResetCb := func() bool {
				cur := 0
				for _, s := range plan.Snapshot() {
					if s.Status == PlanStepInProgress {
						cur = s.ID
						break
					}
				}
				if cur == 0 || cur == lastInProgressStep {
					return false
				}
				lastInProgressStep = cur
				return true
			}
			// PendingWorkFn lets the agent-loop's wrap-up nudge know
			// when there are still authorized plan steps queued, so it
			// reframes "stop exploring" as "finish the current step
			// and continue down the list." Without this, the worker
			// reads the default wrap-up as license to skip remaining
			// steps and write a summary — observed dropping ~5 steps
			// from longer plans.
			pendingPlanWork := func() int {
				n := 0
				for _, s := range plan.Snapshot() {
					if s.Status == PlanStepPending || s.Status == PlanStepInProgress {
						n++
					}
				}
				return n
			}
			// Per-step stuck detector — when the investigator burns too
			// many rounds on a single step without advancing, inject a
			// nudge urging it to mark the step blocked and move on. The
			// 75-round budget is fleet-wide; without this guard the LLM
			// can spend 40+ rounds wrestling with one bad path while
			// every other plan step goes untouched, then hit the
			// wrap-up nudge with most of the plan still pending.
			//
			// Thresholds:
			//   - Soft nudge at 12 rounds on one step: "move to another step, leave this pending"
			//   - Firm nudge at 20 rounds: "switch steps NOW; don't block for pacing"
			// The nudges push DEFER-and-revisit, not mark_step_blocked —
			// blocking zeroes the pending count and defeats the continuation
			// that grants unfinished plans more rounds. A slow step stays
			// pending/in-progress and gets revisited with more budget.
			//
			// The nudges are one-shot per step transition — when the
			// LLM advances to a new step, the counter resets and the
			// flags clear so subsequent steps get the same grace period.
			stuckTrackedStep := 0
			stuckRoundCount := 0
			softNudgeFired := false
			firmNudgeFired := false
			stuckMsgFn := func() []Message {
				curStep := 0
				stepTitle := ""
				for _, s := range plan.Snapshot() {
					if s.Status == PlanStepInProgress {
						curStep = s.ID
						stepTitle = s.Title
						break
					}
				}
				if curStep == 0 {
					// No step in progress (pre-plan, between steps, or
					// final wrap-up). Don't count and don't nudge.
					return nil
				}
				if curStep != stuckTrackedStep {
					stuckTrackedStep = curStep
					stuckRoundCount = 0
					softNudgeFired = false
					firmNudgeFired = false
				}
				stuckRoundCount++
				if stuckRoundCount == 12 && !softNudgeFired {
					softNudgeFired = true
					return []Message{{Role: "user", Content: fmt.Sprintf(
						"Pacing check: you've spent 12 rounds on step %d (%q) without advancing. Move to another pending step now — call mark_step_in_progress on it and work it; leave this step unfinished (do NOT mark it blocked) and revisit it later with what you learn elsewhere. Coming back fresh is faster than grinding. Don't burn more than 8 more rounds here before switching.",
						curStep, stepTitle)}}
				}
				if stuckRoundCount == 20 && !firmNudgeFired {
					firmNudgeFired = true
					return []Message{{Role: "user", Content: fmt.Sprintf(
						"Hard pacing limit: you've spent 20 rounds on step %d (%q). Switch to another pending step NOW — call mark_step_in_progress on the next one and work it. Leave step %d unfinished and pending; do NOT mark it blocked just because it's slow (blocking it for pacing/time is invalid — you'll get more rounds to revisit it). Only block a step for a genuine dead-end (no access, missing tool, unreachable).",
						curStep, stepTitle, curStep)}}
				}
				return nil
			}
			// One investigator pass = one round budget. Extracted so the
			// continuation loop below can re-run it verbatim.
			const (
				investigatorRoundBudget = 75 // rounds per investigator pass
				maxInvestigatorPasses   = 2  // extra budgets granted while steps keep resolving
			)
			invCfg := AgentLoopConfig{
				SystemPrompt:    buildInvestigatorSystemPrompt(appliance),
				Tools:           investigatorTools,
				MaxRounds:       investigatorRoundBudget,
				RouteKey:        "app.servitor",
				MaskDebugOutput: true,
				SerialTools:     true,
				ChatOptions:     append([]ChatOption{WithTemperature(0.3), WithThink(true)}, orchestratorThinkOpts()...),
				OnRoundReset:    stepResetCb,
				OnRoundStart:    stuckMsgFn,
				PendingWorkFn:   pendingPlanWork,
			}
			withHeartbeat(ctx, id, "Investigator", func() {
				invResp, invHistory, invErr = a.RunAgentLoop(ctx,
					[]Message{{Role: "user", Content: invMsg.String()}}, invCfg)
			})
			// Productive continuation — a single round budget often isn't
			// enough to work a 10–15 step plan to completion, so the
			// investigator kept "running out of rounds" and synthesizing a
			// half-finished profile. When a pass exhausts its budget
			// (HitRoundCap) with steps still PENDING, grant another budget —
			// but only while it keeps resolving steps. A pass that clears
			// nothing means it's genuinely stuck (every remaining step
			// dead-ended), so stop and synthesize what we have rather than
			// grinding in circles. Total work is bounded at
			// (1 + maxInvestigatorPasses) budgets.
			prevPending := -1
			for pass := 0; pass < maxInvestigatorPasses && invErr == nil && ctx.Err() == nil; pass++ {
				if invResp == nil || !invResp.HitRoundCap {
					break // natural finish — not a cap hit
				}
				pending := pendingPlanWork()
				if pending == 0 {
					break // capped, but the plan is fully resolved — nothing left
				}
				if prevPending >= 0 && pending >= prevPending {
					emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf(
						"Investigator stalled with %d step(s) still pending — wrapping up with findings so far.", pending)})
					break
				}
				prevPending = pending
				emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf(
					"Investigator reached its round budget with %d step(s) pending — continuing the investigation…", pending)})
				// Fresh stuck-detector window for the new budget.
				stuckTrackedStep, stuckRoundCount, softNudgeFired, firmNudgeFired = 0, 0, false, false
				withHeartbeat(ctx, id, "Investigator (continued)", func() {
					invResp, invHistory, invErr = a.RunAgentLoop(ctx, invHistory, invCfg)
				})
			}
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
				// Repo docs go stale when the code is refreshed (re-cloned) after the
				// last Map run — the files update but the synthesized docs don't. Warn
				// so the investigator re-verifies against current files.
				staleNote := ""
				if appliance.Type == "repo" && repoOverviewStale(appliance) {
					staleNote = repoStaleDocBanner
				}
				if age != "" {
					return fmt.Sprintf("%s[Last updated: %s]\n\n%s", staleNote, age, content), nil
				}
				return staleNote + content, nil
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
		hasFreshImage := false
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				hasFreshImage = len(messages[i].Images) > 0
				break
			}
		}
		leadPrompt := buildLeadSystemPrompt(udb, appliance, docs, cachedFacts, cachedNotes, cachedTechniques, cachedRules, cachedDiscoveries, hasFreshImage)
		emit(id, probeEvent{Kind: "status", Text: "Investigator analyzing…"})

		// Resolve the per-session injection queue so the orchestrator picks
		// up mid-flight user notes between rounds. Workers don't get the hook
		// — they finish their current task before the orchestrator sees the note.
		injQ := LookupInjectionQueue(id)
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

		var err error
		docInvestigatorTools := []AgentToolDef{read_doc_tool, update_doc_tool, probe_tool}
		assertOnlyAllowedTools("servitor.doc_investigator", docInvestigatorTools, servitorOrchestratorToolAllowList)
		const maxDocInvestigatorPasses = 2 // extra budgets while probes keep yielding new data

		// Lead migration (slice 2b): the investigator runs through the orchestrate
		// SCOPED path, so its sessions and tool recordings land in the appliance
		// scope (app:servitor:<id>). It keeps its OWN complete prompt verbatim via
		// SystemPromptOverride (content parity) and its loop knobs via Loop; the
		// mid-flight injection drain (with the notes_consumed UI signal) rides
		// Loop.OnRoundStart. All per-appliance context is in the system prompt, so
		// the run message is just the conversation.
		orch := servitorOrch()
		if orch == nil {
			emit(id, probeEvent{Kind: "error", Text: "orchestrate runtime unavailable"})
			return
		}
		var leadImages [][]byte
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				leadImages = messages[i].Images
				break
			}
		}
		leadScope := orchestrate.AgentScope{
			AgentID:   servitorInvestigatorAgentID,
			ScopeUser: applianceMemScope(appliance.ID),
			SessionID: id,
		}
		leadLoop := &orchestrate.AgentLoopOverrides{
			MaxRounds:    75,
			SerialTools:  true,
			ChatOptions:  append([]ChatOption{WithTemperature(0.2), WithThink(true)}, orchestratorThinkOpts()...),
			OnRoundStart: drainInjections,
		}
		leadStatus := func(s string) { emit(id, probeEvent{Kind: "status", Text: s}) }
		var res orchestrate.AgentSyncResult
		withHeartbeat(ctx, id, "Investigator: working", func() {
			res, err = orch.RunScopedAgentRich(ctx, leadScope, orchestrate.AgentSyncRun{
				SubSessionID:         id,
				Message:              buildScopedLeadMessage(messages),
				Images:               leadImages,
				FreshSession:         true,
				SystemPromptOverride: leadPrompt,
				AppTools:             docInvestigatorTools,
				Loop:                 leadLoop,
				StatusCallback:       leadStatus,
			})
		})
		if res.Text != "" {
			reply = strings.TrimSpace(res.Text)
		}
		// Productive continuation — when the run caps (HitRoundCap) but probes are
		// still yielding NEW data, continue the SAME scoped session (FreshSession
		// defaults false) with another budget; stop once a pass gathers nothing new.
		for pass := 0; pass < maxDocInvestigatorPasses && err == nil && ctx.Err() == nil; pass++ {
			if !res.HitRoundCap {
				break // natural finish — not a cap hit
			}
			before := len(allProbeResults)
			emit(id, probeEvent{Kind: "status", Text: "Investigator reached its round budget — continuing the investigation…"})
			withHeartbeat(ctx, id, "Investigator: working (continued)", func() {
				res, err = orch.RunScopedAgentRich(ctx, leadScope, orchestrate.AgentSyncRun{
					SubSessionID:         id,
					Message:              "Continue the investigation from where you left off and finish answering the user's question.",
					SystemPromptOverride: leadPrompt,
					AppTools:             docInvestigatorTools,
					Loop:                 leadLoop,
					StatusCallback:       leadStatus,
				})
			})
			if res.Text != "" {
				reply = strings.TrimSpace(res.Text)
			}
			if len(allProbeResults) == before {
				break // no new probe data this pass — stop rather than grind
			}
		}
		if err != nil && ctx.Err() == nil {
			emit(id, probeEvent{Kind: "error", Text: err.Error()})
			return
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
					Tools:           []AgentToolDef{read_doc_tool, update_doc_tool, store_fact_tool, link_entities_tool, record_discovery_tool, record_technique_tool, note_lesson_tool},
					MaxRounds:       10,
					RouteKey:        "app.servitor",
					MaskDebugOutput: true,
					ChatOptions:     []ChatOption{WithThink(false)},
				})
				emit(id, probeEvent{Kind: "status", Text: "Background: knowledge consolidated."})
			}
		}

		// Verification pass.
		if reply != "" && len(allProbeResults) > 0 {
			emit(id, probeEvent{Kind: "status", Text: "Verifying names and identifiers…"})
			rawFindings := strings.Join(allProbeResults, "\n\n---\n\n")
			if len(rawFindings) > 24000 {
				rawFindings = rawFindings[:24000] + "\n... [truncated]"
			}
			verifyPrompt := "You are a fact-checker. Your only job is to verify that every specific identifier in a response — table names, service names, file paths, usernames, database names, column names, IP addresses, port numbers, and version strings — appears character-for-character in the raw worker findings below.\n\n" +
				"If all identifiers match exactly: respond with only the word OK.\n\n" +
				"If any identifier was altered (even one character — wrong underscore, wrong prefix, wrong suffix, wrong capitalization): respond with the corrected version of the full response, with all incorrect identifiers replaced by the exact strings from the findings. Do not change anything else.\n\n" +
				"## Raw Worker Findings\n\n" + rawFindings
			verifyResp, verifyErr := a.WorkerChat(ctx,
				[]Message{
					{Role: "user", Content: "## Response to verify\n\n" + reply},
				},
				WithSystemPrompt(verifyPrompt),
				WithTemperature(0.0),
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

	// Persist this turn to the chat session that backs the rail. Includes
	// map runs (saveProfile=true): handleMap pre-creates the session
	// record, so the reconnaissance prompt + final summary land in the rail
	// and stay reviewable, not just streamed live. The run id IS the
	// session id; appendTurn no-ops if no record exists (so a saveProfile
	// run without a pre-created session simply isn't persisted). listSessions
	// surfaces it on the 'done' refresh, exactly like orchestrate.
	if udb != nil {
		var lastUser string
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				lastUser = messages[i].Content
				break
			}
		}
		appendTurn(udb, appliance.ID, id, lastUser, reply)
	}

	if consolidateFn != nil {
		emit(id, probeEvent{Kind: "status", Text: "Background: consolidating knowledge..."})
		go consolidateFn()
	}

	if saveProfile && ownerUDB != nil {
		// The profile is shared appliance knowledge — persist to the OWNER's
		// store so a shared appliance has one profile regardless of who mapped it.
		var existing Appliance
		if ownerUDB.Get(applianceTable, appliance.ID, &existing) {
			// Don't let a Map run that hit issues clobber a good profile. Only
			// REPLACE the profile when this run actually completed and synthesized
			// a substantive mapping — a cancelled run, or a near-empty / stub
			// reply (a failed synthesis, an error note), or one that collapsed to
			// a fraction of the prior profile, keeps the previous one instead. A
			// stale-but-real profile beats an almost-empty one.
			newProfile := strings.TrimSpace(reply)
			prior := strings.TrimSpace(existing.Profile)
			tooThin := len(newProfile) < minMapProfileChars ||
				(prior != "" && len(newProfile) < len(prior)/3)
			if ctx.Err() != nil || tooThin {
				emit(id, probeEvent{Kind: "status", Text: "Map didn't produce a complete profile — keeping the previous one."})
				Log("[servitor.map] kept prior profile for %q (new=%d chars, prior=%d chars, cancelled=%v)",
					appliance.Name, len(newProfile), len(prior), ctx.Err() != nil)
			} else {
				existing.Profile = reply
				existing.Scanned = time.Now().Format(time.RFC3339)
				if fresh := extractLogMap(reply); len(fresh) > 0 {
					existing.LogMap = mergeLogMap(existing.LogMap, fresh)
				}
				ownerUDB.Set(applianceTable, appliance.ID, existing)
				extractDocsFromProfile(ownerUDB, appliance.ID, reply)
			}
		}
	}
}

// minMapProfileChars is the floor below which a Map run's synthesized profile is
// treated as "didn't complete" — a real reconnaissance profile is a structured,
// multi-section markdown doc well above this; a failed/partial run yields a short
// stub or error note. Below it (or a big collapse vs. the prior profile), the
// existing profile is preserved rather than overwritten.
const minMapProfileChars = 250

// handleRules handles GET (list) and POST (create) for appliance rules.
func (T *Servitor) handleRules(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
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
		// Rules are the owner's directives — read from the owner's store so a
		// non-owner viewing a shared appliance sees the same rules (read-only).
		src := udb
		if _, _, ownerUDB, found := T.resolveAppliance(userID, udb, applianceID); found {
			src = ownerUDB
		}
		rules := rulesForAppliance(src, applianceID)
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
		a, _, ownerUDB, found := T.resolveAppliance(userID, udb, req.ApplianceID)
		if !found {
			http.Error(w, "appliance not found", http.StatusNotFound)
			return
		}
		// Only the owner or an admin sets rules — a shared appliance's rules are
		// the owner's, applied to everyone; a non-owner can't change them.
		if !canManageAppliance(userID, a, servitorIsAdmin(r)) {
			http.Error(w, "only the owner or an admin can set rules on a shared appliance", http.StatusForbidden)
			return
		}
		id := storeRule(ownerUDB, req.ApplianceID, req.Rule)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRuleDelete handles DELETE /api/rules/<id>.
func (T *Servitor) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
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
	// The rule id is "<applianceID>:<uuid>", so resolve the owning appliance
	// from it — a shared appliance's rules live in the owner's store, and only
	// the owner or an admin may delete them.
	applianceID := id
	if i := strings.Index(id, ":"); i >= 0 {
		applianceID = id[:i]
	}
	a, _, ownerUDB, found := T.resolveAppliance(userID, udb, applianceID)
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !canManageAppliance(userID, a, servitorIsAdmin(r)) {
		http.Error(w, "only the owner or an admin can delete rules on a shared appliance", http.StatusForbidden)
		return
	}
	deleteRule(ownerUDB, id)
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
		"guide":      HasDocumentTarget("guide"),
	})
}

// handleSaveArticle saves the given assistant response to TechWriter as-is.
// Subject is derived from the first heading/line; body is the verbatim text.
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
	var req struct {
		Text    string `json:"text"`
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	subject := strings.TrimSpace(req.Subject)
	body := req.Text
	if subject == "" {
		lines := strings.SplitN(strings.TrimSpace(body), "\n", 2)
		subject = strings.TrimPrefix(strings.TrimSpace(lines[0]), "# ")
		subject = strings.TrimPrefix(subject, "## ")
		if subject == "" {
			subject = "Untitled"
		}
	}
	id, err := SaveArticleFunc(userID, subject, body)
	if err != nil {
		http.Error(w, "save error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id, "subject": subject})
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
	resp, err := T.WorkerChat(r.Context(),
		[]Message{{Role: "user", Content: userMsg}},
		WithSystemPrompt(sysPrompt),
		WithJSONMode(),
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
