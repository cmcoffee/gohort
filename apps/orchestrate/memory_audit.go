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
	"slices"
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
	// ID names this finding for Remove and Ignore. Derived from what it is
	// about, including the text behind it, so an ignored finding comes back
	// when that text changes: ignoring "this note" is not ignoring whatever
	// the note says next.
	ID string `json:"id,omitempty"`
	// Remove is what removing it does, as the question to confirm. Empty when
	// there is nothing precise to remove.
	Remove string `json:"remove,omitempty"`
	// Ignored marks a finding the owner set aside; listed only when asked for.
	Ignored bool `json:"ignored,omitempty"`

	// Server-side only: what the finding is about, so Remove acts on the
	// entry the server found rather than on anything a page sends.
	name   string // the dead tool's name, for a dead_tool finding
	target findingTarget
}

// findingTarget is the one entry a finding's Remove deletes.
type findingTarget struct {
	factID     string   // a saved fact
	noteLine   string   // one line of the working notes
	clearNotes bool     // the working notes as a whole (stale)
	entityID   string   // a graph entity...
	dropEntity bool     // ...removed whole, when its own name is the problem
	attrKeys   []string // ...or just these attributes
	aliases    []string // ...and these aliases
	reportIDs  []string // saved findings in reference memory
	chunkIDs   []string // ...and loose chunks that belong to no report
}

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
	current, orphaned := T.knownToolNames(udb, user)
	for n := range orphaned {
		current[n] = true // uncarried is still a name that exists
	}
	retired := observeToolNames(udb, user, current)

	// The app-agent exemption is GONE, and the registry is why.
	//
	// The scan used to read every snake_case identifier as a tool name and
	// check it against this user's pool, which collapsed for an app agent
	// working a per-system scope: service names, config keys, package names and
	// unit files are all snake_case, so servitor's "run systemctl_status" and
	// "check max_connections" were reported as tools that no longer exist. The
	// fix at the time was to skip those agents entirely, which also skipped
	// every real finding in their memory.
	//
	// A registry cannot make that mistake — systemctl_status was never a tool,
	// so it is not in the set and is never looked at. The exemption was a
	// workaround for a problem that no longer exists, and keeping it would go
	// on costing the findings it was never meant to suppress.
	var out []MemoryFinding

	ns := factsNamespace(agentID)
	stored := LoadOperatingNotes(udb, ns)
	notes := ResolveOperatingNotes(udb, ns, agent.SeedNotes).Text
	if strings.TrimSpace(notes) != "" {
		if parkedCallRE.MatchString(notes) {
			line := firstLineWhere(notes, parkedCallRE.MatchString)
			out = append(out, MemoryFinding{
				Layer: "Working notes", Kind: "parked_call",
				Detail: "This note records work to do later rather than the current state. If it names a tool call, the agent cannot make it from a note, and when the tool's schema isn't loaded it will improvise a way to reach it instead of asking.",
				Quote:  firstMatchingLine(notes, parkedCallRE),
				Remove: "Remove this line from the working notes?",
				target: findingTarget{noteLine: line},
			})
		}
		for _, f := range deadToolFindings("Working notes", notes, orphaned, retired) {
			name := f.name
			f.target.noteLine = firstLineWhere(notes, func(l string) bool { _, ok := mentionsName(l, name); return ok })
			f.Remove = "Remove the line that names it from the working notes?"
			out = append(out, f)
		}
		// Only stored notes have an age; a seed has never been rewritten and
		// saying so would be a complaint about configuration, not memory.
		if !stored.UpdatedAt.IsZero() && time.Since(stored.UpdatedAt) > staleNotesAfter {
			out = append(out, MemoryFinding{
				Layer: "Working notes", Kind: "stale_notes",
				Detail: fmt.Sprintf("Last rewritten %d days ago. Working notes describe work in progress and are injected into every turn, so an old one is steering the agent with a description of something long finished.",
					int(time.Since(stored.UpdatedAt).Hours()/24)),
				Quote:  truncateQuote(notes),
				Remove: "Clear the working notes? The agent starts them over on its next turn.",
				target: findingTarget{clearNotes: true, noteLine: stored.UpdatedAt.UTC().Format(time.RFC3339)},
			})
		}
	}

	for _, fact := range ListMemoryFacts(udb, ns) {
		for _, f := range deadToolFindings("Saved facts", fact.Note, orphaned, retired) {
			f.Remove, f.target.factID = "Delete this saved fact?", fact.ID
			out = append(out, f)
		}
		if parkedCallRE.MatchString(fact.Note) {
			out = append(out, MemoryFinding{
				Layer: "Saved facts", Kind: "parked_call",
				Detail: "A saved fact is a durable rule, not a task list. Work to do belongs in the conversation or a scheduled run, not in something replayed into every future turn.",
				Quote:  truncateQuote(fact.Note),
				Remove: "Delete this saved fact?",
				target: findingTarget{factID: fact.ID},
			})
		}
	}

	// Run on every scope now. These were skipped for app agents because the
	// dead-tool scan could say nothing true there; with a registry it can, and
	// the Reference Memory sweep is cheap when the retired set is empty —
	// mentionsName is a substring walk per retired name, and a scope that has
	// retired nothing does no work at all.
	out = append(out, auditGraphMemory(udb, ns, orphaned, retired)...)
	out = append(out, auditReferenceMemory(user, agentID, orphaned, retired)...)

	for i := range out {
		out[i].ID = findingID(agentID, out[i])
	}
	// Orphan findings first — those name something known to be gone, where the
	// others are judgements about shape. Not capped here: the caller sets the
	// ignored ones aside first, so an ignored finding never costs a real one
	// its place under the cap.
	sort.SliceStable(out, func(i, j int) bool { return kindRank(out[i].Kind) < kindRank(out[j].Kind) })
	return out
}

// auditGraphMemory scans entity names, aliases and attribute VALUES. An
// attribute is where a tool name realistically lands here ("uses_tool:
// get_top_stories"); the attribute KEY is skipped, because key names are
// snake_case by convention and auditing them would flag every entity in the
// graph.
func auditGraphMemory(udb Database, ns string, orphaned, retired map[string]bool) []MemoryFinding {
	var out []MemoryFinding
	for _, e := range ListGraphEntities(udb, ns) {
		parts := make([]string, 0, len(e.Attrs)+1+len(e.Aliases))
		parts = append(parts, e.Name)
		parts = append(parts, e.Aliases...)
		for _, v := range e.Attrs {
			parts = append(parts, v)
		}
		for _, f := range deadToolFindings("Graph Memory", strings.Join(parts, "\n"), orphaned, retired) {
			// Name the entity: "Graph Memory" alone doesn't tell you which of
			// thirty nodes to open.
			f.Detail = fmt.Sprintf("Entity %q: %s", e.Name, f.Detail)
			// Remove takes off only what names the tool, and the entity
			// itself only when its own name is the problem.
			f.target.entityID = e.ID
			if _, ok := mentionsName(e.Name, f.name); ok {
				f.target.dropEntity = true
				f.Remove = fmt.Sprintf("Delete the entity %q?", e.Name)
			} else {
				for k, v := range e.Attrs {
					if _, ok := mentionsName(v, f.name); ok {
						f.target.attrKeys = append(f.target.attrKeys, k)
					}
				}
				sort.Strings(f.target.attrKeys)
				for _, al := range e.Aliases {
					if _, ok := mentionsName(al, f.name); ok {
						f.target.aliases = append(f.target.aliases, al)
					}
				}
				f.Remove = fmt.Sprintf("Remove what names %q from the entity %q?", f.name, e.Name)
			}
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
func auditReferenceMemory(user, agentID string, orphaned, retired map[string]bool) []MemoryFinding {
	if VectorDB == nil {
		return nil
	}
	prefix := agentKnowledgePrefix(user, agentID)
	type hit struct {
		count   int
		example string
		detail  string
		name    string
		reports []string
		chunks  []string
	}
	byTool := map[string]*hit{}
	var order []string
	scanned := 0
	for _, c := range ChunksWhere(VectorDB, func(x EmbeddedChunk) bool {
		return sourceInScope(x.Source, prefix) && chunkProvenance(x.Source, x.ReportID) == "derived"
	}) {
		if scanned >= maxAuditChunkScan {
			break
		}
		scanned++
		for _, f := range deadToolFindings("Reference Memory", c.Text, orphaned, retired) {
			h, seen := byTool[f.Detail]
			if !seen {
				h = &hit{example: f.Quote, detail: f.Detail, name: f.name}
				byTool[f.Detail] = h
				order = append(order, f.Detail)
			}
			h.count++
			switch {
			case c.ReportID == "":
				h.chunks = append(h.chunks, c.ID)
			case !slices.Contains(h.reports, c.ReportID):
				h.reports = append(h.reports, c.ReportID)
			}
		}
	}
	out := make([]MemoryFinding, 0, len(order))
	for _, k := range order {
		h := byTool[k]
		detail := h.detail
		if h.count > 1 {
			detail = fmt.Sprintf("%s Referenced in %d saved entries.", detail, h.count)
		}
		remove := "Delete the saved finding that mentions it?"
		if n := len(h.reports) + len(h.chunks); n > 1 {
			remove = fmt.Sprintf("Delete the %d saved findings that mention it?", n)
		}
		out = append(out, MemoryFinding{
			Layer: "Reference Memory", Kind: "dead_tool",
			Detail: detail, Quote: h.example, Remove: remove,
			name: h.name, target: findingTarget{reportIDs: h.reports, chunkIDs: h.chunks},
		})
	}
	return out
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

// deadToolFindings reports names in text that WERE tools and no longer are.
//
// A lookup, not a judgement. The previous version inferred from the surrounding
// sentence whether a name was being used as a tool, and every false positive
// came from that step — a memory describing code says "the handler calls
// parse_config" in the same words a memory about a tool does. Nothing in the
// text distinguishes them, so nothing in the text is consulted: a name is
// reported when the registry says it was a tool, and otherwise never.
//
// That trades recall for precision deliberately. A tool retired before the
// registry saw it is invisible here. The pane's premise is precision over
// recall, and a list nobody trusts is one nobody reads.
func deadToolFindings(layer, text string, orphaned, retired map[string]bool) []MemoryFinding {
	var out []MemoryFinding
	for name := range orphaned {
		if at, ok := mentionsName(text, name); ok {
			out = append(out, MemoryFinding{
				Layer: layer, Kind: "dead_tool",
				Detail: fmt.Sprintf("References %q, which is in Orphaned Tools: its last carrying agent was deleted, so no agent can call it. Re-home the tool, or drop the reference.", name),
				Quote:  quoteAround(text, at),
				name:   name,
			})
		}
	}
	for name := range retired {
		if at, ok := mentionsName(text, name); ok {
			out = append(out, MemoryFinding{
				Layer: layer, Kind: "dead_tool",
				Detail: fmt.Sprintf("Names %q, which was a tool and is not any more: renamed or removed. Anything relying on it is describing a call that cannot be made.", name),
				Quote:  quoteAround(text, at),
				name:   name,
			})
		}
	}
	// Map iteration order is random and these land in a rendered list, so a
	// reader would see them reshuffle between opens of the same pane.
	sort.Slice(out, func(i, j int) bool { return out[i].Detail < out[j].Detail })
	return out
}

// looksLikeACall reports whether the identifier at [start,end) is being used as
// an invocation rather than mentioned in passing — "call foo_bar", "foo_bar(",
// "run foo_bar with". Without this every snake_case word in a sentence would
// be audited as a missing tool.
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

// firstMatchingLine returns the first line of text matching re, trimmed for a
// finding's Quote — so a finding shows the offending sentence rather than the
// whole note.
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
		Text: fmt.Sprintf("%q remembers using %q, but that tool's last agent was just deleted, so nothing can call it now. Approving re-homes the tool onto %s so its memory is true again. Ignoring this is fine: the tool's definition is kept in Orphaned Tools either way, and you can edit the memory instead from the agent's Memory pane.",
			rec.Name, tool, rec.Name),
	})
	Log("[orchestrate.memaudit] %s still references orphaned tool %q: suggested re-home", rec.Name, tool)
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
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
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
	a, ok := T.memoryAgent(r, udb, user, agentID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// POST acts: {undo} puts back a move the memory lifecycle made, and
	// {action, id} removes, ignores or restores one finding.
	if r.Method == http.MethodPost {
		var body struct {
			Undo   string `json:"undo"`
			Action string `json:"action"`
			ID     string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var err error
		switch {
		case strings.TrimSpace(body.Undo) != "":
			err = undoMemoryMove(udb, agentID, strings.TrimSpace(body.Undo))
		case strings.TrimSpace(body.Action) != "" && strings.TrimSpace(body.ID) != "":
			err = T.actOnFinding(udb, user, a, strings.TrimSpace(body.Action), strings.TrimSpace(body.ID))
		default:
			http.Error(w, "undo, or action and id, is required", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		return
	}
	findings, ignored := splitIgnoredFindings(udb, agentID, T.auditAgentMemory(udb, user, agentID, a))
	if ignored == nil {
		ignored = []MemoryFinding{}
	}
	moves := listMemoryMoves(udb, agentID)
	if moves == nil {
		moves = []memoryMove{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"findings": findings,
		"count":    len(findings),
		"ignored":  ignored,
		"moves":    moves,
	})
}
