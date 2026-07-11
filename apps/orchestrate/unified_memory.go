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
	"sort"
	"strconv"
	"strings"
	"time"

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
	// A corpus-only agent (recallCorpusOnly) gets a tool that IS corpus search —
	// no multi-layer framing, no `layer` knob — so what the model is told matches
	// what recall can actually return. Every other agent gets the full four-layer
	// verb plus an optional `layer` filter to narrow to one source on demand.
	desc := "Look something up across ALL of your memory at once — no need to pick a source. Pass `query` to search; each hit is tagged with where it came from:\n\n  [pinned]    your always-in-prompt notes\n  [finding]   things you saved with remember (may have drifted — verify when it matters)\n  [knowledge] authoritative uploaded/shared docs (source of truth)\n  [history]   earlier in this conversation, aged out of view\n\nEvery hit carries an `id:`. To read the FULL item behind a hit (a whole document, the surrounding conversation), call recall again with that `id`. Pass the same id to `forget` to delete it (findings and pinned notes only).\n\nTo restrict the search to a SINGLE source, pass `layer` (e.g. `knowledge` to answer strictly from authoritative docs). Omit it to search everything.\n\nA 'no matches' result means your memory genuinely has nothing on this — do NOT speculate from it. Required: `query` OR `id`. Optional: `k`, `layer`."
	params := map[string]ToolParam{
		"query": {Type: "string", Description: "What to look for, in natural language. Your current question, trimmed to the gist, usually works."},
		"id":    {Type: "string", Description: "An id from a prior recall hit (e.g. `doc:…`, `span:…`, `mem:…`, `fact:…`). Returns the full item behind that id instead of searching."},
		"k":     {Type: "number", Description: "Max hits per layer (default 4, cap follows the knowledge ceiling). Leave default unless you want a wider net."},
		"layer": {Type: "string", Enum: []string{"knowledge", "finding", "pinned", "history"}, Description: "Optional. Restrict the search to ONE source, named by the tag you see on hits: `knowledge` (authoritative docs), `finding` (your saved findings), `pinned` (always-in-prompt notes), or `history` (earlier conversation). Omit to search all four. Use `knowledge` when the answer must come strictly from the corpus."},
	}
	if t.recallCorpusOnly() {
		desc = "Look something up in your knowledge corpus — the authoritative uploaded/shared documents this agent answers from. Pass `query` to search; each hit carries a `doc:` id, and calling recall again with that id returns the full document.\n\nA 'no matches' result means the corpus genuinely has nothing on this — do NOT speculate from it or fall back to general knowledge. Required: `query` OR `id`. Optional: `k`."
		params = map[string]ToolParam{
			"query": {Type: "string", Description: "What to look for, in natural language. Your current question, trimmed to the gist, usually works."},
			"id":    {Type: "string", Description: "A `doc:…` id from a prior recall hit. Returns the full document behind it instead of searching."},
			"k":     {Type: "number", Description: "Max hits (default 4, cap follows the knowledge ceiling). Leave default unless you want a wider net."},
		}
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        "recall",
			Description: desc,
			Parameters:  params,
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

// recallCorpusOnly reports whether this agent's recall must be capped to the
// authoritative Knowledge layer. True when the agent keeps NO memory of its own
// — both Explicit and Inferred are off — which is the "answer strictly from
// authoritative sources" configuration (KB readers, compliance bots). For such
// an agent, folding conversation [history] in beside the corpus dilutes the
// grounding, and the guarantee must not hinge on the model remembering to pass
// layer=knowledge, so it's enforced here rather than left to the prompt.
func (t *chatTurn) recallCorpusOnly() bool {
	return t.explicitOff() && t.inferredOff()
}

// recallLayerSet resolves which layers a recall call searches: the caller's
// optional `layer` arg (default = all four) capped by recallCorpusOnly. An
// unrecognized value falls back to all rather than silently narrowing to a
// wrong single layer. Keys match the [tag] prefixes recall stamps on hits.
func (t *chatTurn) recallLayerSet(args map[string]any) map[string]bool {
	if t.recallCorpusOnly() {
		return map[string]bool{"knowledge": true}
	}
	switch strings.ToLower(strings.TrimSpace(stringArg(args, "layer"))) {
	case "knowledge":
		return map[string]bool{"knowledge": true}
	case "finding", "findings":
		return map[string]bool{"finding": true}
	case "pinned":
		return map[string]bool{"pinned": true}
	case "history":
		return map[string]bool{"history": true}
	default: // "", "all", or unrecognized → search everything
		return map[string]bool{"pinned": true, "finding": true, "knowledge": true, "history": true}
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

	layers := t.recallLayerSet(args)
	now := time.Now()
	var sections []string

	// [pinned] — Explicit Memory. Tagged with fact:<id> so forget can target
	// the exact note regardless of its position in the prompt block. An aging or
	// stale note carries an as-of hint so the model weights it accordingly.
	if layers["pinned"] && !t.explicitOff() {
		facts := SearchMemoryFacts(t.udb, factsNamespace(t.agent.ID), query)
		if len(facts) > perLayer {
			facts = facts[:perLayer]
		}
		if len(facts) > 0 {
			var b strings.Builder
			for _, f := range facts {
				fmt.Fprintf(&b, "- [pinned] %s%s\n  id: fact:%s\n", strings.TrimSpace(f.Note), FactStalenessNote(f, now), f.ID)
			}
			sections = append(sections, strings.TrimRight(b.String(), "\n"))
		}
	}

	// [finding] + [knowledge] — one vector search over both, split by
	// provenance. Skip the query entirely when neither layer is wanted.
	// Reference is skipped when Inferred memory is off, but Knowledge (curated)
	// is always readable.
	if layers["finding"] || layers["knowledge"] {
		ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout())
		defer cancel()
		hits := searchAgentKnowledge(ctx, t.app.DB, t.user, t.ownerUser, t.agent.ID, generalTopic, query, perLayer*2, t.skillsActive, t.agent.AttachedCollections, ChunkScopeAll)
		var findings, knowledge []SearchHit
		for _, h := range hits {
			if h.Score < manualSearchMinScore {
				continue
			}
			if chunkProvenance(h.Source, h.ReportID) == "derived" {
				if !layers["finding"] || t.inferredOff() {
					continue // Reference layer not requested or suppressed this turn
				}
				findings = append(findings, h) // collect all; recency re-rank + cap below
			} else if layers["knowledge"] && len(knowledge) < perLayer {
				knowledge = append(knowledge, h)
			}
		}
		// Findings (self-saved reference material) get recency-reordered so a
		// fresher finding outranks an equally-relevant older one; curated
		// [knowledge] is authoritative source-of-truth and is left as ranked.
		findings = rerankFindingsByRecency(findings, now)
		if len(findings) > perLayer {
			findings = findings[:perLayer]
		}
		if s := renderRecallChunks("finding", "mem", findings); s != "" {
			sections = append(sections, s)
		}
		if s := renderRecallChunks("knowledge", "doc", knowledge); s != "" {
			sections = append(sections, s)
		}
	}

	// [history] — folded-away conversation spans. Best-effort: agents with no
	// archive simply contribute nothing. Capped out for corpus-only agents so
	// past conversation never poses as an authoritative source.
	if layers["history"] {
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
	}

	if len(sections) == 0 {
		return recallNoMatchMessage(layers), nil
	}
	return strings.Join(sections, "\n\n"), nil
}

// recallNoMatchMessage tailors the empty-result text to the layers that were
// actually searched, so a scoped recall doesn't claim it swept everything. The
// invariant across every variant: absence is not evidence — never infer an
// answer from a miss.
func recallNoMatchMessage(layers map[string]bool) string {
	if len(layers) == 1 && layers["knowledge"] {
		return "No matches in your knowledge corpus. Don't infer an answer from the absence or fall back to general knowledge — say the corpus had nothing on it."
	}
	if len(layers) == 1 {
		var only string
		for k := range layers {
			only = k
		}
		return fmt.Sprintf("No matches in your %s memory. Don't infer an answer from the absence — either widen the search (omit `layer`) or say you found nothing.", only)
	}
	return "No matches anywhere in your memory (pinned notes, findings, knowledge, or conversation history). Don't infer an answer from the absence — either rephrase, or proceed and say your memory had nothing on it."
}

// rerankFindingsByRecency re-orders finding hits by semantic score × recency, so
// a fresher finding outranks an equally-relevant older one. Findings are
// self-saved reference material, so they age off their ingestion Date at the
// "slow" half-life (VolSlow). No-op when recency weighting is off (strength 0)
// or there's nothing to reorder — so the default-safe case pays nothing.
func rerankFindingsByRecency(findings []SearchHit, now time.Time) []SearchHit {
	strength := RecencyWeight()
	if strength <= 0 || len(findings) < 2 {
		return findings
	}
	type scored struct {
		h SearchHit
		s float64
	}
	ranked := make([]scored, len(findings))
	for i, h := range findings {
		prov := MemoryProvenance{Volatility: VolSlow}
		if h.Date != "" {
			if ts, err := time.Parse(time.RFC3339, h.Date); err == nil {
				prov.AsOf = ts
			}
		}
		ranked[i] = scored{h, float64(h.Score) * prov.RecencyMultiplier(now, strength)}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].s > ranked[j].s })
	out := make([]SearchHit, len(ranked))
	for i, r := range ranked {
		out[i] = r.h
	}
	return out
}

// recallAgeNote renders a finding's saved date as a short freshness hint, or ""
// when there's no parseable date. Absolute date (not "N days ago") so the model
// judges it against today rather than a relative phrase that drifts.
func recallAgeNote(date string) string {
	if date == "" {
		return ""
	}
	ts, err := time.Parse(time.RFC3339, date)
	if err != nil {
		return ""
	}
	return "(saved " + ts.Format("2006-01-02") + ")"
}

// renderRecallChunks formats a bucket of vector hits under a [tag], stamping an
// id of the form <prefix>:<ReportID> so recall/forget can round-trip it. Uses
// an excerpt (not the full chunk) to keep recall from dumping whole documents.
// Findings carry a saved-date hint so the model can weight their freshness;
// curated knowledge cites via its own locator, so no date line there.
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
		if tag == "finding" {
			if age := recallAgeNote(h.Date); age != "" {
				fmt.Fprintf(&b, "  %s\n", age)
			}
		}
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
