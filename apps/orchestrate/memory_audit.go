// Memory audit — the entries that reference something no longer there.
//
// Memory outlives the things it names. A tool gets orphaned when its last
// carrying agent is deleted; a note parks a call the model can no longer make.
// Nothing reconciled either against reality, so the agent went on believing a
// capability it had, and worked around its absence instead of reporting it —
// the "pending task: get_top_stories with category=all" chain.
//
// This is a READ. It never deletes: wrongly evicting someone's memory is worse
// than a stale entry, so findings point at the layer's existing editor and the
// owner decides. It also runs on pane load rather than behind a button —
// nobody clicks an audit button, and the note that caused all this survived
// months of not being looked for.
//
// Precision over recall, deliberately. A findings list that cries wolf is one
// people learn to scroll past, so each rule below either names something known
// to be gone (the orphan pool) or matches a shape that is wrong regardless of
// what exists (a parked invocation).

package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// staleNotesAfter is when working notes stop reading as "current state". Notes
// describe work in flight; two months untouched means the work moved on and
// nobody rewrote them.
const staleNotesAfter = 60 * 24 * time.Hour

const (
	// maxAuditFindings caps what the pane renders. Past a dozen the block
	// stops being a list of things to fix and becomes a wall to scroll past,
	// which is the failure mode this whole feature is trying to avoid.
	maxAuditFindings = 12
	// maxAuditChunkScan bounds the Reference Memory sweep. The derived corpus
	// is unbounded and this runs on every pane open, so it reads a slice
	// rather than the whole vector store.
	maxAuditChunkScan = 400
)

// MemoryFinding is one entry worth a second look.
type MemoryFinding struct {
	Layer  string `json:"layer"`  // "Working notes" | "Saved facts" — where to go fix it
	Kind   string `json:"kind"`   // parked_call | dead_tool | stale_notes
	Detail string `json:"detail"` // what is wrong, in a sentence
	Quote  string `json:"quote"`  // the offending text, trimmed
}

// toolIdent matches a snake_case identifier — the shape every tool name takes
// (verb_noun, never a bare word), which keeps ordinary prose out of the scan.
var toolIdent = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)

// parkedCallRE matches a note that records an invocation to make later. The
// shape is wrong on its own terms — a note cannot call a tool — so this fires
// whether or not the named tool still exists.
var parkedCallRE = regexp.MustCompile(`(?i)\b(pending|queued|todo|to-do|next)\b[^.\n]{0,20}\b(task|call|action|step|work)s?\b\s*[:\-]`)

// auditAgentMemory returns what looks wrong in an agent's memory, most
// actionable first. Nil when everything checks out.
func (T *OrchestrateApp) auditAgentMemory(udb Database, user, agentID string, agent AgentRecord) []MemoryFinding {
	if udb == nil {
		return nil
	}
	known, orphaned := T.knownToolNames(udb, user)
	// The dead-tool scan reads every snake_case identifier as a tool name and
	// checks it against THIS USER's pool. That premise holds for an agent's own
	// memory and collapses for an app agent working a per-system scope, where
	// the memory describes a machine: service names, config keys, package
	// names, unit files and paths are all snake_case, and servitor's notes are
	// full of "run systemctl_status" and "check max_connections". Every one of
	// those was reported as a tool that no longer exists — an audit confidently
	// flagging correctly-recorded system facts as broken references.
	//
	// The other findings still apply: a parked call is wrong on its own terms
	// and stale notes are stale whatever they describe.
	scanTools := !isAppAgent(agentID)
	var out []MemoryFinding

	ns := factsNamespace(agentID)
	stored := LoadOperatingNotes(udb, ns)
	notes := ResolveOperatingNotes(udb, ns, agent.SeedNotes).Text
	if strings.TrimSpace(notes) != "" {
		if parkedCallRE.MatchString(notes) {
			out = append(out, MemoryFinding{
				Layer: "Working notes", Kind: "parked_call",
				Detail: "This note records work to do later rather than the current state. If it names a tool call, the agent cannot make it from a note — and when the tool's schema isn't loaded it will improvise a way to reach it instead of asking.",
				Quote:  firstMatchingLine(notes, parkedCallRE),
			})
		}
		if scanTools {
			out = append(out, deadToolFindings("Working notes", notes, known, orphaned)...)
		}
		// Only stored notes have an age; a seed has never been rewritten and
		// saying so would be a complaint about configuration, not memory.
		if !stored.UpdatedAt.IsZero() && time.Since(stored.UpdatedAt) > staleNotesAfter {
			out = append(out, MemoryFinding{
				Layer: "Working notes", Kind: "stale_notes",
				Detail: fmt.Sprintf("Last rewritten %d days ago. Working notes describe work in progress and are injected into every turn, so an old one is steering the agent with a description of something long finished.",
					int(time.Since(stored.UpdatedAt).Hours()/24)),
				Quote: truncateQuote(notes),
			})
		}
	}

	for _, f := range ListMemoryFacts(udb, ns) {
		if scanTools {
			out = append(out, deadToolFindings("Saved facts", f.Note, known, orphaned)...)
		}
		if parkedCallRE.MatchString(f.Note) {
			out = append(out, MemoryFinding{
				Layer: "Saved facts", Kind: "parked_call",
				Detail: "A saved fact is a durable rule, not a task list. Work to do belongs in the conversation or a scheduled run, not in something replayed into every future turn.",
				Quote:  truncateQuote(f.Note),
			})
		}
	}

	// Both of these exist only to run the dead-tool scan, so on a per-system
	// scope they are skipped outright rather than called and filtered — which
	// also spares the Reference Memory sweep, the most expensive part of the
	// audit, on the scopes where it could never say anything true.
	if scanTools {
		out = append(out, auditGraphMemory(udb, ns, known, orphaned)...)
		out = append(out, auditReferenceMemory(user, agentID, known, orphaned)...)
	}

	// Orphan findings first — those name something known to be gone, where the
	// others are judgements about shape.
	sort.SliceStable(out, func(i, j int) bool { return kindRank(out[i].Kind) < kindRank(out[j].Kind) })
	if len(out) > maxAuditFindings {
		out = out[:maxAuditFindings]
	}
	return out
}

// auditGraphMemory scans entity names, aliases and attribute VALUES. An
// attribute is where a tool name realistically lands here ("uses_tool:
// get_top_stories"); the attribute KEY is skipped, because key names are
// snake_case by convention and auditing them would flag every entity in the
// graph.
func auditGraphMemory(udb Database, ns string, known, orphaned map[string]bool) []MemoryFinding {
	var out []MemoryFinding
	for _, e := range ListGraphEntities(udb, ns) {
		parts := make([]string, 0, len(e.Attrs)+1+len(e.Aliases))
		parts = append(parts, e.Name)
		parts = append(parts, e.Aliases...)
		for _, v := range e.Attrs {
			parts = append(parts, v)
		}
		for _, f := range deadToolFindings("Graph Memory", strings.Join(parts, "\n"), known, orphaned) {
			// Name the entity: "Graph Memory" alone doesn't tell you which of
			// thirty nodes to open.
			f.Detail = fmt.Sprintf("Entity %q — %s", e.Name, f.Detail)
			out = append(out, f)
		}
	}
	return out
}

// auditReferenceMemory scans the derived chunk corpus — memory_save findings
// and synthesis auto-ingest, which is exactly where "the working approach was
// tool X" gets recorded and then outlives X.
//
// Findings are aggregated PER TOOL rather than per chunk. One retired tool can
// appear in dozens of saved findings, and thirty rows saying the same thing is
// how a findings list stops being read; one row saying "referenced in 30
// entries" is the same information and remains actionable.
func auditReferenceMemory(user, agentID string, known, orphaned map[string]bool) []MemoryFinding {
	if VectorDB == nil {
		return nil
	}
	prefix := agentKnowledgePrefix(user, agentID)
	type hit struct {
		count   int
		example string
		detail  string
	}
	byTool := map[string]*hit{}
	var order []string
	scanned := 0
	for _, key := range VectorDB.Keys(EmbeddedChunks) {
		if scanned >= maxAuditChunkScan {
			break
		}
		var c EmbeddedChunk
		if !VectorDB.Get(EmbeddedChunks, key, &c) {
			continue
		}
		if !strings.HasPrefix(c.Source, prefix) && c.Source != prefix {
			continue
		}
		if chunkProvenance(c.Source, c.ReportID) != "derived" {
			continue
		}
		scanned++
		for _, f := range deadToolFindings("Reference Memory", c.Text, known, orphaned) {
			h, seen := byTool[f.Detail]
			if !seen {
				h = &hit{example: f.Quote, detail: f.Detail}
				byTool[f.Detail] = h
				order = append(order, f.Detail)
			}
			h.count++
		}
	}
	out := make([]MemoryFinding, 0, len(order))
	for _, k := range order {
		h := byTool[k]
		detail := h.detail
		if h.count > 1 {
			detail = fmt.Sprintf("%s Referenced in %d saved entries.", detail, h.count)
		}
		out = append(out, MemoryFinding{
			Layer: "Reference Memory", Kind: "dead_tool",
			Detail: detail, Quote: h.example,
		})
	}
	return out
}

// isRegisteredToolName reports whether a name is a built-in chat tool. Asked
// per candidate rather than by listing the catalog: the catalog builder needs
// live app state, and this only ever needs a membership test.
func isRegisteredToolName(name string) bool {
	_, ok := FindChatTool(name)
	return ok
}

func kindRank(kind string) int {
	switch kind {
	case "dead_tool":
		return 0
	case "parked_call":
		return 1
	default:
		return 2
	}
}

// deadToolFindings reports tool names in text that no longer resolve to
// anything callable. Two confidences, and nothing in between: a name in the
// ORPHAN pool is known to be gone, while a name matching no tool anywhere is
// only worth mentioning when the text is plainly talking about calling it —
// otherwise every snake_case phrase in ordinary prose becomes a finding.
func deadToolFindings(layer, text string, known, orphaned map[string]bool) []MemoryFinding {
	var out []MemoryFinding
	seen := map[string]bool{}
	for _, loc := range toolIdent.FindAllStringIndex(text, -1) {
		name := text[loc[0]:loc[1]]
		if seen[name] {
			continue
		}
		seen[name] = true
		switch {
		case orphaned[name]:
			out = append(out, MemoryFinding{
				Layer: layer, Kind: "dead_tool",
				Detail: fmt.Sprintf("References %q, which is in Orphaned Tools — its last carrying agent was deleted, so no agent can call it. Re-home the tool, or drop the reference.", name),
				Quote:  quoteAround(text, loc[0]),
			})
		case known[name] || isRegisteredToolName(name):
			// Resolves to something callable — nothing to say.
		case looksLikeACall(text, loc[0], loc[1]):
			out = append(out, MemoryFinding{
				Layer: layer, Kind: "dead_tool",
				Detail: fmt.Sprintf("Talks about calling %q, but no tool of that name exists — not in your pool, not shared, not orphaned. It was probably renamed or deleted.", name),
				Quote:  quoteAround(text, loc[0]),
			})
		}
	}
	return out
}

// looksLikeACall reports whether the identifier at [start,end) is being used as
// an invocation rather than mentioned in passing — "call foo_bar", "foo_bar(",
// "run foo_bar with". Without this every snake_case word in a sentence would
// be audited as a missing tool.
func looksLikeACall(text string, start, end int) bool {
	if end < len(text) && text[end] == '(' {
		return true
	}
	from := start - 24
	if from < 0 {
		from = 0
	}
	before := strings.ToLower(strings.TrimSpace(text[from:start]))
	for _, verb := range []string{"call", "calling", "run", "running", "use", "using", "invoke", "via", "task:", "tool"} {
		if strings.HasSuffix(before, verb) || strings.HasSuffix(before, verb+" the") {
			return true
		}
	}
	return false
}

// knownToolNames returns every name that resolves to a real tool for this user,
// and separately the orphan pool — a name in the second set is known dead
// rather than merely unrecognized.
func (T *OrchestrateApp) knownToolNames(udb Database, user string) (known, orphaned map[string]bool) {
	known, orphaned = map[string]bool{}, map[string]bool{}
	for _, p := range LoadPersistentTempTools(udb, user) {
		known[p.Tool.Name] = true
	}
	for _, p := range LoadSharedPersistentTempTools(udb) {
		known[p.Tool.Name] = true
	}
	for _, o := range LoadOrphanedTempTools(udb, user) {
		orphaned[o.Tool.Name] = true
		delete(known, o.Tool.Name) // orphaned wins: it is not callable
	}
	return known, orphaned
}

func firstMatchingLine(text string, re *regexp.Regexp) string {
	for _, line := range strings.Split(text, "\n") {
		if re.MatchString(line) {
			return truncateQuote(line)
		}
	}
	return truncateQuote(text)
}

// quoteAround returns the line containing an offset, so a finding shows the
// sentence the name appears in rather than a bare token.
func quoteAround(text string, at int) string {
	start := strings.LastIndexByte(text[:at], '\n') + 1
	end := strings.IndexByte(text[at:], '\n')
	if end < 0 {
		end = len(text)
	} else {
		end += at
	}
	return truncateQuote(text[start:end])
}

func truncateQuote(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 180 {
		return strings.TrimSpace(string(r[:180])) + "…"
	}
	return s
}

// --- event-driven flagging ---------------------------------------------------

// noteOrphanedToolMemory runs at the moment a tool stops being callable —
// an agent delete took its last carrier — and queues a suggestion for every
// OTHER agent whose memory still names it.
//
// The Memory pane's audit only runs when someone opens the Memory pane, which
// is the same hole the original failure fell through: the note survived
// because nobody went looking. This fires on the event instead. Deleting an
// agent is exactly when the framework knows a capability just disappeared, and
// exactly when it can say who was still counting on it.
//
// Approving the suggestion RE-HOMES the tool onto the remembering agent, which
// makes the memory true again. That is the right repair and the reason this is
// a suggestion rather than a warning: the alternative fix — editing the memory
// — stays a manual call in the Memory pane, because this never deletes.
//
// Best-effort throughout: a delete must not fail because a suggestion could
// not be queued.
func noteOrphanedToolMemory(udb Database, owner string, toolNames []string) {
	if udb == nil || owner == "" || len(toolNames) == 0 || RootDB == nil {
		return
	}
	for _, rec := range listAgents(udb, owner) {
		ns := factsNamespace(rec.ID)
		for _, tool := range toolNames {
			if !memoryMentionsTool(udb, ns, tool) {
				continue
			}
			suggestOrphanedToolRehome(owner, rec, tool)
		}
	}
}

// memoryMentionsTool reports whether an agent's always-in-prompt memory names
// a tool. Notes, facts and graph attributes only — the derived corpus is a
// vector scan, too heavy to run per agent inside a delete, and the Memory
// pane's audit covers it when someone looks.
func memoryMentionsTool(udb Database, ns, tool string) bool {
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(tool) + `\b`)
	if word.MatchString(LoadOperatingNotes(udb, ns).Text) {
		return true
	}
	for _, f := range ListMemoryFacts(udb, ns) {
		if word.MatchString(f.Note) {
			return true
		}
	}
	for _, e := range ListGraphEntities(udb, ns) {
		for _, v := range e.Attrs {
			if word.MatchString(v) {
				return true
			}
		}
	}
	return false
}

// suggestOrphanedToolRehome queues the offer, once per (agent, tool).
func suggestOrphanedToolRehome(owner string, rec AgentRecord, tool string) {
	for _, ex := range ListAuthorizations(RootDB, owner) {
		if ex.Action == orphanMemoryRefAction && ex.Agent == rec.ID && ex.Brief == tool {
			return // already offered and still unanswered
		}
	}
	SaveAuthorization(RootDB, Authorization{
		Owner:  owner,
		Action: orphanMemoryRefAction,
		Agent:  rec.ID,
		Brief:  tool,
		Text: fmt.Sprintf("%q remembers using %q, but that tool's last agent was just deleted, so nothing can call it now. Approving re-homes the tool onto %s so its memory is true again. Ignoring this is fine — the tool's definition is kept in Orphaned Tools either way, and you can edit the memory instead from the agent's Memory pane.",
			rec.Name, tool, rec.Name),
	})
	Log("[orchestrate.memaudit] %s still references orphaned tool %q — suggested re-home", rec.Name, tool)
}

// orphanMemoryRefAction is the Authorizations action for the offer above.
// Classified as a SUGGESTION (see approvalIsSuggestion): nothing is blocked on
// it — the agent runs fine, it just runs with a belief that is no longer true.
const orphanMemoryRefAction = "orphan_memory_ref"

// --- HTTP handler ------------------------------------------------------------

// handleAgentMemoryAudit serves the findings the Memory pane shows as a
// "Needs attention" block. GET only — this never mutates; every fix routes
// through the layer's own editor, which is where the owner can see what they
// are removing.
func (T *OrchestrateApp) handleAgentMemoryAudit(w http.ResponseWriter, r *http.Request, user, agentID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	udb := UserDB(T.DB, user)
	if udb == nil {
		http.Error(w, "no store for user", http.StatusInternalServerError)
		return
	}
	if agentID == "" || strings.Contains(agentID, "/") {
		http.NotFound(w, r)
		return
	}
	a, ok := loadAgent(udb, agentID)
	if !ok || (a.Owner != user && a.Owner != seedOwner) {
		http.NotFound(w, r)
		return
	}
	findings := T.auditAgentMemory(udb, user, agentID, a)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"findings": findings,
		"count":    len(findings),
	})
}
