// Unified memory surface — the "one simple system" collapse of the eight
// sibling memory tools (store_fact / forget_fact / search_facts / memory /
// knowledge_search / fetch_knowledge_doc / recall_history / expand_history)
// into THREE verbs the model reasons about without picking a layer:
//
//	remember(content, pin?)  — save. pin=true  → always-in-prompt (Explicit)
//	                                  pin=false → recall-on-demand (Reference)
//	recall(query | id)       — read. spans Explicit + Reference + Knowledge +
//	                                  History, merged and provenance-tagged;
//	                                  id=<recall id> fetches the full item.
//	forget(id)               — drop one item by the id recall handed back.
//
// The four storage layers are UNCHANGED — this file is an interface, not a
// migration. Each verb routes to the same primitives the legacy tools call
// (storeFactNote / memorySave / searchAgentKnowledge / SearchRecall / …), so
// dedup, supersession, the relevance floor, and scope gating all still apply.
// The distinction the memory-vs-knowledge rule cares about survives as an
// ATTRIBUTE (pin, and the [tag] on every recall hit), not as a tool the model
// has to choose between.
//
// Gated behind the tune_unified_memory knob (0 = legacy 8-tool surface, the
// default; 1 = this 3-verb surface) so the two can be A/B'd without a code
// change. The graph tools (link_entities / recall_about / forget_graph) are
// NOT part of this collapse and stay wired in both modes.

package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// TunableUnifiedMemory toggles the collapsed remember/recall/forget surface.
const TunableUnifiedMemory = "tune_unified_memory"

func init() {
	RegisterTunable(TunableSpec{
		Key:      TunableUnifiedMemory,
		Category: "Memory",
		Label:    "Unified memory tools (remember/recall/forget)",
		Help:     "Off = legacy eight memory tools (store_fact, memory, knowledge_search, …). On = the collapsed three-verb surface. Restart not required; applies to new turns.",
		Kind:     KindBool,
		Default:  0,
		Min:      0,
		Max:      1,
	})
}

// unifiedMemoryEnabled reports whether the collapsed surface is in force.
func unifiedMemoryEnabled() bool { return TuneBool(TunableUnifiedMemory) }

// --- mode-aware tool-name phrases -----------------------------------------
//
// Prose in the seed prompts and a few shared handler messages names the memory
// tools literally ("call store_fact", "via knowledge_search"). Under the
// collapsed surface those names don't exist, so these helpers resolve to the
// phrase for the active mode. Seed prompts regenerate on every loadAgent and
// handler messages build per call, so both track the live tune_unified_memory
// flag with no restart. Backticks in the returned strings are fine — the
// no-backticks-in-raw-strings rule only applies to the raw-string prompts these
// splice INTO, not to these double-quoted returns.

func memPinPhrase() string { // save a short, always-in-prompt note
	if unifiedMemoryEnabled() {
		return "remember (pin=true)"
	}
	return "store_fact"
}

func memFindingSavePhrase() string { // save a pull-only finding
	if unifiedMemoryEnabled() {
		return "remember (pin=false)"
	}
	return "memory(save)"
}

func memRecallPhrase() string { // search your own saved findings
	if unifiedMemoryEnabled() {
		return "recall"
	}
	return "memory(search)"
}

func memKnowledgePhrase() string { // search the curated knowledge corpus
	if unifiedMemoryEnabled() {
		return "recall"
	}
	return "knowledge_search"
}

func memHistoryPhrase() string { // search folded-away conversation history
	if unifiedMemoryEnabled() {
		return "recall"
	}
	return "recall_history and expand_history"
}

func memForgetPhrase() string { // drop a stored always-in-prompt note by index
	if unifiedMemoryEnabled() {
		return "forget"
	}
	return "forget_fact"
}

func memLessonsLogPhrase() string { // the trio backing the per-user lessons log
	if unifiedMemoryEnabled() {
		return "remember (pin=true) / forget / recall"
	}
	return "store_fact / forget_fact / list_facts"
}

func memRefMemToolsClause() string { // how the Reference-Memory layer is reached
	if unifiedMemoryEnabled() {
		return "the `remember` and `recall` tools"
	}
	return "the `memory` tool: action=\"save\"|\"search\"|\"forget\""
}

// --- mode-aware prompt rewrite --------------------------------------------
//
// The mem*Phrase helpers above cover prompts that were built with a hole for the
// tool name. Older seed prompts and guidance blocks instead HARDCODE the legacy
// names in prose ("call knowledge_search first", "capture gotchas via
// store_fact") — often several times in one raw-string prompt, where inline
// splicing would shatter the string. rewriteMemoryToolNames is the catch-all: a
// single pass over an assembled system prompt that swaps every legacy memory
// tool token for its collapsed-surface equivalent, but ONLY when the unified
// surface is live (in legacy mode it's a no-op, so the prose ships byte-for-byte
// as tuned). Applied at the two prompt-assembly tails (prependAgentContext,
// appendAgentCapabilityBlocks) so it reaches every surface — web, dispatch,
// worker, synthesis — from one place.
//
// The token set is deliberately underscore-bearing and word-bounded, which makes
// the pass collision-free: `\bknowledge_search\b` cannot match inside
// `skill_knowledge_search` (the underscore is a word char, so there's no
// boundary before "knowledge"), so the distinct skill_* tools are never touched.
// The replacements contain no legacy token, so a second pass is a no-op — the
// two call sites can both run on the same string without double-rewriting.
var legacyMemToolRE = regexp.MustCompile(`\b(knowledge_search|fetch_knowledge_doc|search_facts|recall_history|expand_history|list_facts|memory_search|store_fact|memory_save|forget_fact|memory_forget)\b`)

// unifiedMemToolReplacements maps each legacy tool token to its collapsed-surface
// phrasing. store_fact keeps the pin=true qualifier because the Explicit layer is
// the pinned one — a bare "remember" defaults to the unpinned Reference layer, so
// dropping the qualifier would silently change where the note lands.
var unifiedMemToolReplacements = map[string]string{
	"knowledge_search":    "recall",
	"fetch_knowledge_doc": "recall",
	"search_facts":        "recall",
	"recall_history":      "recall",
	"expand_history":      "recall",
	"list_facts":          "recall",
	"memory_search":       "recall",
	"store_fact":          "remember (pin=true)",
	"memory_save":         "remember",
	"forget_fact":         "forget",
	"memory_forget":       "forget",
}

// rewriteMemoryToolNames swaps hardcoded legacy memory-tool names in an
// assembled prompt for their unified-surface equivalents when the collapsed
// surface is live. No-op under the legacy surface.
func rewriteMemoryToolNames(s string) string {
	if !unifiedMemoryEnabled() {
		return s
	}
	return legacyMemToolRE.ReplaceAllStringFunc(s, func(m string) string {
		if r, ok := unifiedMemToolReplacements[m]; ok {
			return r
		}
		return m
	})
}

// unifiedRecallPerLayer caps how many hits recall pulls from each of the four
// layers before merging. Small on purpose — recall fans out across all layers,
// so a generous per-layer k would blow the round's context budget. The caller
// can raise it via k= up to KnowledgeMaxK.
const unifiedRecallPerLayer = 4

// unifiedMemoryTools returns the three collapsed verbs, gated to the layers this
// agent actually has. When every layer is suppressed the trio is omitted (the
// caller checks the same condition), so the model never sees dead tools.
func (t *chatTurn) unifiedMemoryTools() []AgentToolDef {
	return []AgentToolDef{t.rememberToolDef(), t.recallToolDef(), t.forgetToolDef()}
}

// hasAnyMemoryLayer reports whether at least one memory layer is available this
// turn — the gate for surfacing the unified trio at all.
func (t *chatTurn) hasAnyMemoryLayer() bool {
	return t.agentHasRetrievableContent() || !t.inferredOff() || !t.explicitOff()
}

// --- remember -------------------------------------------------------------

func (t *chatTurn) rememberToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "remember",
			Description: "Save something worth keeping. ONE decision: `pin`.\n\n**pin=true** — a SHORT note that must shape EVERY future turn (a preference, an identity fact, a standing instruction). It's injected into your prompt automatically from now on, so keep it to a sentence and use it sparingly. Examples: \"User prefers metric units.\", \"Deploy header is X-Auth: <jwt>.\"\n\n**pin=false** (default) — a longer FINDING you might need to look up later (an API spec, a config recipe, a working approach, a document detail). It's stored for retrieval, not injected; you get it back later via `recall`. Paragraph-length, self-contained.\n\nRule of thumb: if you'd want it in front of you unprompted → pin=true; if you'd go looking for it when a relevant question comes up → pin=false. The framework dedupes either way. Don't re-save something `recall` just returned.\n\nRequired: `content`. Optional: `pin`, `topic` (snake_case bucket for findings), `subject` (short heading for a finding).",
			Parameters: map[string]ToolParam{
				"content": {Type: "string", Description: "What to remember, as a self-contained statement. For pin=true keep it to one sentence; for a finding, several sentences to a paragraph with enough context to make sense later out of context."},
				"pin":     {Type: "boolean", Description: "true → always-in-prompt note (durable preference/identity/instruction). false (default) → recall-on-demand finding (reference material)."},
				"topic":   {Type: "string", Description: "(findings only) snake_case bucket slug, e.g. `acme_api`. Reuse one from the \"Known topics\" block when it fits, or mint a new one. Omit for `general`."},
				"subject": {Type: "string", Description: "(findings only) short heading for THIS finding, e.g. \"Acme API rotates tokens every 24h\". Optional."},
			},
			Required: []string{"content"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			content := strings.TrimSpace(stringArg(args, "content"))
			if content == "" {
				return "", errors.New("content is required")
			}
			if boolArg(args, "pin") {
				if t.explicitOff() {
					return "", errors.New("always-in-prompt memory is disabled for this agent — call remember with pin=false (or omit pin) to save a recall-only finding instead")
				}
				return t.storeFactNote(content)
			}
			if t.inferredOff() {
				return "", errors.New("recall memory is disabled for this agent — call remember with pin=true to keep a short always-in-prompt note instead")
			}
			// memorySave reads content/topic/subject straight off args.
			return t.memorySave(args)
		},
	}
}

// --- recall ---------------------------------------------------------------

func (t *chatTurn) recallToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "recall",
			Description: "Look something up across ALL of your memory at once — no need to pick a source. Pass `query` to search; each hit is tagged with where it came from:\n\n  [pinned]    your always-in-prompt notes\n  [finding]   things you saved with remember (may have drifted — verify when it matters)\n  [knowledge] authoritative uploaded/shared docs (source of truth)\n  [history]   earlier in this conversation, aged out of view\n\nEvery hit carries an `id:`. To read the FULL item behind a hit (a whole document, the surrounding conversation), call recall again with that `id`. Pass the same id to `forget` to delete it (findings and pinned notes only).\n\nA 'no matches' result means your memory genuinely has nothing on this — do NOT speculate from it. Required: `query` OR `id`. Optional: `k`.",
			Parameters: map[string]ToolParam{
				"query": {Type: "string", Description: "What to look for, in natural language. Your current question, trimmed to the gist, usually works."},
				"id":    {Type: "string", Description: "An id from a prior recall hit (e.g. `doc:…`, `span:…`, `mem:…`, `fact:…`). Returns the full item behind that id instead of searching."},
				"k":     {Type: "number", Description: "Max hits per layer (default 4, cap follows the knowledge ceiling). Leave default unless you want a wider net."},
			},
			// Read-only content-wise, but the combined verb also fronts
			// forget-adjacent ids; CapRead keeps it in every read pool.
			Caps: []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			if id := strings.TrimSpace(stringArg(args, "id")); id != "" {
				return t.recallFetch(id)
			}
			query := strings.TrimSpace(stringArg(args, "query"))
			if query == "" {
				return "", errors.New("query or id is required")
			}
			return t.recallSearch(query, args)
		},
	}
}

// recallSearch fans out across every available layer, applies the shared
// relevance floor, and renders one merged, provenance-tagged block. One embed
// covers Reference + Knowledge (ChunkScopeAll, split by provenance); facts and
// history run their own indexes.
func (t *chatTurn) recallSearch(query string, args map[string]any) (string, error) {
	perLayer := unifiedRecallPerLayer
	if v, ok := args["k"].(float64); ok && v > 0 {
		perLayer = int(v)
		if maxK := KnowledgeMaxK(); perLayer > maxK {
			perLayer = maxK
		}
	}

	var sections []string

	// [pinned] — Explicit Memory. Tagged with fact:<id> so forget can target
	// the exact note regardless of its position in the prompt block.
	if !t.explicitOff() {
		facts := SearchMemoryFacts(t.udb, factsNamespace(t.agent.ID), query)
		if len(facts) > perLayer {
			facts = facts[:perLayer]
		}
		if len(facts) > 0 {
			var b strings.Builder
			for _, f := range facts {
				fmt.Fprintf(&b, "- [pinned] %s\n  id: fact:%s\n", strings.TrimSpace(f.Note), f.ID)
			}
			sections = append(sections, strings.TrimRight(b.String(), "\n"))
		}
	}

	// [finding] + [knowledge] — one vector search over both, split by
	// provenance. Reference is skipped when Inferred memory is off, but
	// Knowledge (curated) is always readable.
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout())
	defer cancel()
	hits := searchAgentKnowledge(ctx, t.app.DB, t.user, t.ownerUser, t.agent.ID, generalTopic, query, perLayer*2, t.skillsActive, t.agent.AttachedCollections, ChunkScopeAll)
	var findings, knowledge []SearchHit
	for _, h := range hits {
		if h.Score < manualSearchMinScore {
			continue
		}
		if chunkProvenance(h.Source, h.ReportID) == "derived" {
			if t.inferredOff() {
				continue // Reference layer suppressed this turn
			}
			if len(findings) < perLayer {
				findings = append(findings, h)
			}
		} else if len(knowledge) < perLayer {
			knowledge = append(knowledge, h)
		}
	}
	if s := renderRecallChunks("finding", "mem", findings); s != "" {
		sections = append(sections, s)
	}
	if s := renderRecallChunks("knowledge", "doc", knowledge); s != "" {
		sections = append(sections, s)
	}

	// [history] — folded-away conversation spans. Best-effort: agents with no
	// archive simply contribute nothing.
	source := operatorLCMSource(t.agent.ID, cortexSessionID(t.agent.ID))
	if hh := SearchRecall(t.udb, source, query, perLayer); len(hh) > 0 {
		var b strings.Builder
		for _, h := range hh {
			label := h.Title
			if label == "" {
				label = h.Section
			}
			fmt.Fprintf(&b, "- [history] %s\n  id: span:%s\n  %s\n", label, h.ReportID, recallSnippet(h.Text))
		}
		sections = append(sections, strings.TrimRight(b.String(), "\n"))
	}

	if len(sections) == 0 {
		return "No matches anywhere in your memory (pinned notes, findings, knowledge, or conversation history). Don't infer an answer from the absence — either rephrase, or proceed and say your memory had nothing on it.", nil
	}
	return strings.Join(sections, "\n\n"), nil
}

// renderRecallChunks formats a bucket of vector hits under a [tag], stamping an
// id of the form <prefix>:<ReportID> so recall/forget can round-trip it. Uses
// an excerpt (not the full chunk) to keep recall from dumping whole documents.
func renderRecallChunks(tag, idPrefix string, hits []SearchHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	for _, h := range hits {
		name := strings.TrimSpace(h.Title)
		if name == "" {
			name = strings.TrimSpace(strings.TrimPrefix(h.Section, "## "))
		}
		if name == "" {
			name = "(untitled)"
		}
		fmt.Fprintf(&b, "- [%s] %s\n  id: %s:%s\n  %s\n", tag, name, idPrefix, h.ReportID,
			strings.ReplaceAll(knowledgeSearchExcerpt(h.Text), "\n", "\n  "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// recallSnippet trims a history passage for the search view.
func recallSnippet(text string) string {
	s := strings.TrimSpace(text)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return strings.ReplaceAll(s, "\n", "\n  ")
}

// recallFetch returns the FULL item behind a recall id — the drill-down that
// subsumes fetch_knowledge_doc (doc:/mem:) and expand_history (span:).
func (t *chatTurn) recallFetch(id string) (string, error) {
	kind, ref, ok := splitRecallID(id)
	if !ok {
		return "", fmt.Errorf("unrecognized id %q — pass an id exactly as recall returned it (doc:… / mem:… / span:… / fact:…)", id)
	}
	switch kind {
	case "doc", "mem":
		// Both curated docs and derived findings reconstruct through the
		// same doc-assembly handler — its allow predicate already spans the
		// agent's own derived corpus plus attached/curated collections.
		return t.fetchKnowledgeDocToolDef().Handler(map[string]any{"doc_id": ref})
	case "span":
		source := operatorLCMSource(t.agent.ID, cortexSessionID(t.agent.ID))
		chunks := FetchRecallSpanChunks(t.udb, source, ref)
		if len(chunks) == 0 {
			return "No such history span (it may have been cleared).", nil
		}
		doc := AssembleChunkDoc(chunks, 8000)
		if strings.TrimSpace(doc) == "" {
			return "That span is empty.", nil
		}
		return doc, nil
	case "fact":
		for _, f := range ListMemoryFacts(t.udb, factsNamespace(t.agent.ID)) {
			if f.ID == ref {
				return f.Note, nil
			}
		}
		return fmt.Sprintf("No pinned note with id fact:%s — it may have been forgotten.", ref), nil
	default:
		return "", fmt.Errorf("unrecognized id kind %q", kind)
	}
}

// --- forget ---------------------------------------------------------------

func (t *chatTurn) forgetToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "forget",
			Description: "Delete one thing from your memory by the id recall gave you.\n\n  fact:<id>  a pinned note (or pass a bare number matching the index in your \"Saved facts\" prompt block)\n  mem:<id>   a finding you saved with remember\n\n[knowledge] and [history] items are NOT deletable here — knowledge is admin-managed source-of-truth, and history is the immutable record of what was said. Required: `id`.",
			Parameters: map[string]ToolParam{
				"id": {Type: "string", Description: "The id from a recall hit (fact:… or mem:…), or a bare 1-based number to drop the matching pinned note in your \"Saved facts\" block."},
			},
			Required: []string{"id"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			id := strings.TrimSpace(stringArg(args, "id"))
			if id == "" {
				return "", errors.New("id is required")
			}
			// Bare integer → prompt-block index (the affordance the model has
			// for a pinned note it sees in its prompt but never recalled).
			if n, err := strconv.Atoi(id); err == nil {
				removed, ok := ForgetMemoryFactByIndex(t.udb, factsNamespace(t.agent.ID), n)
				if !ok {
					return "", fmt.Errorf("no pinned note at index %d", n)
				}
				return fmt.Sprintf("Forgot pinned note: %q.", removed.Note), nil
			}
			kind, ref, ok := splitRecallID(id)
			if !ok {
				return "", fmt.Errorf("unrecognized id %q — pass fact:… , mem:… , or a bare number", id)
			}
			switch kind {
			case "fact":
				if ForgetMemoryFactByID(t.udb, factsNamespace(t.agent.ID), ref) {
					return "Forgot pinned note.", nil
				}
				return fmt.Sprintf("No pinned note with id fact:%s — it may already be gone.", ref), nil
			case "mem":
				return t.forgetFindingByReportID(ref)
			case "doc":
				return "That's [knowledge] — admin-managed source-of-truth. It can't be deleted from here.", nil
			case "span":
				return "That's [history] — the immutable record of the conversation. It can't be deleted from here.", nil
			default:
				return "", fmt.Errorf("unrecognized id kind %q", kind)
			}
		},
	}
}

// forgetFindingByReportID deletes an entire saved finding (all chunks sharing
// the ReportID) from Reference Memory, scoped to the agent's own derived
// corpus so an id from another context can't reach across. Whole-finding
// granularity matches the "forget this one thing" mental model better than the
// legacy per-chunk delete.
func (t *chatTurn) forgetFindingByReportID(reportID string) (string, error) {
	if VectorDB == nil {
		return "", errors.New("vector store unavailable")
	}
	agentPrefix := knowledgeSource(t.user, t.agent.ID, "")
	var ids []string
	for _, key := range VectorDB.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !VectorDB.Get(EmbeddedChunks, key, &c) {
			continue
		}
		if c.ReportID != reportID {
			continue
		}
		inScope := c.Source == agentPrefix || strings.HasPrefix(c.Source, agentPrefix+":")
		if !inScope || chunkProvenance(c.Source, c.ReportID) != "derived" {
			return fmt.Sprintf("id mem:%s doesn't point at one of your own findings (it may be knowledge, already deleted, or from another context). Only findings you saved with remember can be forgotten.", reportID), nil
		}
		ids = append(ids, c.ID)
	}
	if len(ids) == 0 {
		return fmt.Sprintf("No finding with id mem:%s — it may already be gone.", reportID), nil
	}
	DeleteChunksByIDs(VectorDB, ids)
	Log("[orchestrate.unified_memory.forget] user=%q agent=%q dropped finding report_id=%s (%d chunks)",
		t.user, t.agent.ID, reportID, len(ids))
	return "Forgot that finding.", nil
}

// splitRecallID parses a "<kind>:<ref>" recall id. Returns ok=false when the
// string has no recognized prefix.
func splitRecallID(id string) (kind, ref string, ok bool) {
	i := strings.IndexByte(id, ':')
	if i <= 0 || i == len(id)-1 {
		return "", "", false
	}
	kind = id[:i]
	ref = id[i+1:]
	switch kind {
	case "fact", "mem", "doc", "span":
		return kind, ref, true
	}
	return "", "", false
}
