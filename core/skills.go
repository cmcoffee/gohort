// Skills — LLM-callable prompt injections with optional tool
// inclusion.
//
// A skill is a markdown body + frontmatter. The "Available skills"
// block in each agent's system prompt lists every skill the agent
// can reach (name + 1-line description). When the host LLM judges
// that one fits the current conversation, it calls
// activate_skill(name) — the skill's body returns as the tool
// result (carried in conversation history for subsequent rounds)
// and its declared tools join the catalog. Same shape as the
// "Available agents" block + agents(action="run") dispatch.
//
// Conceptually: a catalog of inert capability bundles that the LLM
// pulls in when relevant. No classifier, no triggers, no embedding
// gatekeeper — every activation is a tool call the LLM explicitly
// made, visible in the activity log alongside everything else.
//
// Compared to agents (which have their own dispatch loop, memory,
// facts, etc.), skills are the lightest possible capability bundle.

package core

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const skillsTable = "skills"

// skillStore returns the canonical DB for skill persistence: the
// process-level RootDB. Skills MUST live in one shared store so
// writes from Builder (via orchestrate's user-scoped sess.DB) and
// reads from the admin app (via AuthDB) land at the same key.
// Without this, each surface would write to a different sub-DB
// and the data would be invisible across them. Mirrors temp tools'
// tempToolStore pattern.
func skillStore(fallback Database) Database {
	if RootDB != nil {
		return RootDB
	}
	return fallback
}

// SkillRecord is one authored skill. Stored per-user; activated by
// the classifier when its triggers/description match the current
// turn's user message.
type SkillRecord struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Triggers are substring patterns matched against the user
	// message (and attachment filenames). Glob-style `*.pdf` matches
	// any attachment ending in .pdf; everything else is a plain
	// case-insensitive substring. Empty triggers = embedding-only
	// activation (slower but useful when triggers are hard to
	// enumerate).
	Triggers []string `json:"triggers,omitempty"`
	// AllowedTools is the union of tool names this skill brings to
	// the catalog while active. Same shape as AgentRecord's
	// AllowedTools — resolved against the registered ChatTools pool.
	// Tools missing from the registry are silently skipped (e.g. a
	// skill that names create_agent on a non-Builder agent just
	// doesn't surface it; the Builder-exclusivity invariant holds).
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// Instructions is the markdown body appended to the host LLM's
	// system prompt when the skill is active. Plain markdown; no
	// templating. The skill's name is automatically added as an H2
	// header above the instructions on injection.
	Instructions string `json:"instructions"`
	// Owner is the user who authored the skill (or the seed marker
	// for framework-provided skills, when we add those).
	Owner string `json:"owner,omitempty"`
	// Disabled mutes the skill — classifier skips it entirely as if
	// it didn't exist. Use to pause a skill without losing its
	// definition (admin can re-enable later instead of re-authoring).
	Disabled bool `json:"disabled,omitempty"`

	// AttachedCollections lists collection IDs whose corpus becomes
	// searchable when this skill is active. Admin-curated only — the
	// derived "SelfTraining" path (paraphrased self-corpus from the
	// skill's own work) stays removed because it compounded drift,
	// but admin-attached collections are stable reference material
	// that pairs naturally with a skill's Instructions. Active path
	// only: when the classifier doesn't pick this skill, its
	// collections stay out of scope, so a heavy reference corpus
	// doesn't leak into unrelated turns. Empty by default — most
	// skills are pure behavior packets and don't carry docs.
	AttachedCollections []string `json:"attached_collections,omitempty"`
	// Embedding is cached at save time so the classifier doesn't
	// have to re-embed on every turn. Re-computed in SaveSkill from
	// the current Description. Persisted with the record so reloads
	// don't lose the cache.
	Embedding []float32 `json:"embedding,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

// LoadSkills returns every skill in the user's pool, ordered by
// most-recently-updated first (a stable, human-meaningful order for
// admin views). Empty username returns nil.
//
// Storage shape: one row per user keyed by username, value is a
// []SkillRecord. Same pattern as PersistentTempTools — fewer DB
// keys, atomic per-user updates, and the row count stays small.
func LoadSkills(db Database, username string) []SkillRecord {
	store := skillStore(db)
	if store == nil || username == "" {
		return nil
	}
	var out []SkillRecord
	if !store.Get(skillsTable, username, &out) {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Updated.After(out[j].Updated)
	})
	return out
}

// FindSkillByName looks up a skill by case-insensitive name match.
// Used by Builder's update / delete paths and by the classifier when
// trigger matches reference a skill by its declared name.
func FindSkillByName(db Database, username, name string) (SkillRecord, bool) {
	if name == "" {
		return SkillRecord{}, false
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, s := range LoadSkills(db, username) {
		if strings.ToLower(s.Name) == lower {
			return s, true
		}
	}
	return SkillRecord{}, false
}

// SaveSkill upserts a skill record in the user's pool. Assigns ID
// on create. Returns the saved record with ID + timestamps filled.
// Same-ID skills replace; matched-by-ID upserts atomically rewrite
// the per-user slice.
func SaveSkill(db Database, username string, s SkillRecord) (SkillRecord, error) {
	store := skillStore(db)
	if store == nil || username == "" {
		return SkillRecord{}, errString("save skill requires user")
	}
	if s.ID == "" {
		s.ID = "skill-" + UUIDv4()
		s.Created = time.Now()
	}
	s.Owner = username
	s.Updated = time.Now()
	// Description-embedding removed. Was used by the cosine
	// gatekeeper / fuzzy classifier that auto-fired skills; with
	// activation now exclusively LLM-driven via activate_skill, the
	// vector serves no purpose. Existing records' Embedding field
	// stays populated until they're saved again; the field is just
	// dead weight on disk.
	existing := LoadSkills(db, username)
	rest := existing[:0]
	for i := range existing {
		if existing[i].ID == s.ID {
			continue
		}
		rest = append(rest, existing[i])
	}
	rest = append(rest, s)
	store.Set(skillsTable, username, rest)
	return s, nil
}

// DeleteSkill removes a skill by ID. Returns true when an entry was
// removed. Also drops any corpus chunks stored under the skill's
// source prefix — orphan chunks would otherwise survive the skill's
// deletion and silently bloat the vector store.
func DeleteSkill(db Database, username, id string) bool {
	store := skillStore(db)
	if store == nil || username == "" || id == "" {
		return false
	}
	existing := LoadSkills(db, username)
	rest := existing[:0]
	removed := false
	for i := range existing {
		if existing[i].ID == id {
			removed = true
			continue
		}
		rest = append(rest, existing[i])
	}
	if !removed {
		return false
	}
	if len(rest) == 0 {
		store.Unset(skillsTable, username)
	} else {
		store.Set(skillsTable, username, rest)
	}
	// Drop the skill's corpus chunks from its dedicated store.
	if chunksDB := SkillChunksDB(username); chunksDB != nil {
		if n := WipeChunksBySourcePrefix(chunksDB, SkillSource(id)); n > 0 {
			Log("[skills] dropped %d chunk(s) for deleted skill %s", n, id)
		}
	}
	return true
}

// SkillSource returns the source-prefix used by the vector store
// for this skill's corpus. Centralized so the ingest path, the
// activation search, and the delete-cleanup all derive the same
// namespace from the skill ID.
func SkillSource(skillID string) string {
	return "skill:" + skillID
}

// SkillChunksDB returns the database the skill's knowledge chunks
// live in: a dedicated per-user sub-store of RootDB. Separate from
// any app's own per-(user, agent) knowledge store — skills are
// user-scoped, not agent-scoped, so they get their own home.
// Returns nil when RootDB isn't initialized; callers should treat
// that as "no corpus available" and skip both ingest and search.
func SkillChunksDB(username string) Database {
	if username == "" {
		return nil
	}
	// Skill corpus now lives in the shared, dedicated vector store
	// alongside all other knowledge, scoped logically by the
	// SkillSource("skill:<id>") tag rather than by a per-user sub-store.
	// Skill IDs are globally unique, so any future skill search MUST
	// scope to the requesting user's own skill IDs (the source tag does
	// not by itself partition by user). No skill chunks are ingested or
	// searched today; the only consumer is delete-cleanup.
	return VectorDB
}

// --- shared skill runtime (orchestrate + phantom) ---
//
// A skill is a domain pack: instructions + (optionally) searchable
// knowledge sources (attached collections and/or source-hooks). The LLM
// reaches a skill three ways, none of which tracks state across turns:
//   - read_skill(skill) — pull the skill's instructions to apply now;
//   - skill_knowledge_search(skill, query) — search the skill's sources
//     (collections + source-hooks, merged), with the instructions
//     attached the first time the skill is touched this turn;
//   - skill_knowledge_fetch_doc(skill, doc_id) — a full doc.
// Plus, when a skill's Triggers match the turn, the framework surfaces a
// soft HINT nudging the LLM to consult it (RenderSkillTriggerHints) — a
// signal, not a forced injection; consulting via the tools is what loads
// the instructions. A per-turn `delivered` set dedupes them so the LLM
// sees a consulted skill's instructions once.

// RenderAvailableSkills builds the "## Available skills" prompt block from
// the skills an agent/conversation can reach, so the LLM knows what
// read_skill / skill_knowledge_search can draw on. Empty when none.
func RenderAvailableSkills(skills []SkillRecord) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Available skills\n\n")
	b.WriteString("Domain packs you can draw on in your own context: read_skill(skill) returns its approach/instructions; skill_knowledge_search(skill, query) searches its sources (and attaches its approach the first time); skill_knowledge_fetch_doc(skill, doc_id) pulls a full document.\n\nRULE: when a listed skill covers the subject in front of you, consult it FIRST — call skill_knowledge_search (or read_skill) before web_search and before answering from memory. Its sources are authoritative for its domain and override your priors, so answering a covered question without it is a mistake even when you're confident. This fires on what you DISCOVER mid-task, not just the opening request: a repo that turns out to be Go → the Go skill, a tax-law doc → the tax skill, a PDF → the PDF skill, even if the user never named the domain. On FOLLOW-UPS the skill's instructions and what you already retrieved stay in your context — answer from that skill content, not your priors, and search the skill again only if the follow-up needs material you didn't pull. Skip a skill only for what it plainly doesn't cover or fast-changing facts (current events, latest figures). When a skill's trigger matches the turn you'll see a \"Likely relevant\" hint — treat it as a strong nudge to consult that skill, not a guarantee. Format: **name** — purpose.\n\n")
	for _, s := range skills {
		// Full description — descriptions are model-facing; show it
		// un-truncated so the whole activation cue is visible.
		desc := strings.TrimSpace(s.Description)
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString("- **")
		b.WriteString(s.Name)
		b.WriteString("** — ")
		b.WriteString(desc)
		if trig := strings.TrimSpace(strings.Join(s.Triggers, ", ")); trig != "" {
			b.WriteString(" (triggers: ")
			b.WriteString(trig)
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// SkillTriggersMatch reports whether a skill's Triggers match this turn: a
// glob trigger (contains '*' or '?') matches against the attachment
// filenames; any other trigger is a case-insensitive substring test against
// the message text. A skill with NO triggers never matches. A match is a
// relevance SIGNAL, not a command — it surfaces a HINT nudging the LLM to
// consult the skill (see RenderSkillTriggerHints), it does NOT force-inject
// the skill's instructions. Deterministic and framework-owned.
func SkillTriggersMatch(s SkillRecord, message string, attachmentNames []string) bool {
	return TriggersMatch(s.Triggers, message, attachmentNames)
}

// TriggersMatch reports whether any of the given triggers match this turn:
// a glob trigger (contains '*' or '?') matches against the attachment
// filenames; any other trigger is a case-insensitive substring test
// against the message. Empty triggers never match. Generic + framework-
// owned — shared by skills (SkillTriggersMatch) and agents (the per-turn
// dispatch hint) so trigger semantics read identically across both.
func TriggersMatch(triggers []string, message string, attachmentNames []string) bool {
	if len(triggers) == 0 {
		return false
	}
	lowerMsg := strings.ToLower(message)
	for _, t := range triggers {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if strings.ContainsAny(t, "*?") {
			pat := strings.ToLower(t)
			for _, name := range attachmentNames {
				if ok, _ := filepath.Match(pat, strings.ToLower(filepath.Base(name))); ok {
					return true
				}
			}
			continue
		}
		if strings.Contains(lowerMsg, strings.ToLower(t)) {
			return true
		}
	}
	return false
}

// RenderSkillTriggerHints returns a soft HINT block naming the allowed,
// enabled skills whose Triggers match this turn — a per-turn nudge to
// consult them, NOT the full instruction injection. A matched trigger is a
// relevance signal; the LLM still decides to consult (read_skill /
// skill_knowledge_search), and consulting is what actually loads the
// skill's approach. We hint instead of force-inject because a wrong hint is
// cheap (one line the LLM ignores) while a wrong injection is expensive (a
// wall of off-topic instructions steering the whole reply). Returns "" when
// nothing matches.
func RenderSkillTriggerHints(db Database, owner string, allowed []string, message string, attachmentNames []string) string {
	if owner == "" || len(allowed) == 0 {
		return ""
	}
	allowSet := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		allowSet[id] = true
	}
	var names []string
	for _, s := range LoadSkills(db, owner) {
		if s.Disabled || !allowSet[s.ID] {
			continue
		}
		if SkillTriggersMatch(s, message, attachmentNames) {
			names = append(names, s.Name)
		}
	}
	return SkillTriggerHintBlock(names)
}

// SkillTriggerHintBlock formats the trigger-match hint line for the given
// skill names. Empty names → "". Shared by orchestrate + phantom so the
// nudge reads identically on both surfaces.
func SkillTriggerHintBlock(names []string) string {
	if len(names) == 0 {
		return ""
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "**" + n + "**"
	}
	return "\n\n[Likely relevant this turn (triggers matched): " + strings.Join(quoted, ", ") + " — consult the fitting one FIRST via skill_knowledge_search / read_skill before answering. A trigger match is a hint, not a guarantee: skip a skill that doesn't actually fit.]\n\n"
}

// resolveAllowedSkill looks up a skill by name (case-insensitive),
// gated on the allowed-ID set. Shared by the three skill tools.
func resolveAllowedSkill(db Database, owner string, allowed []string, name string) (*SkillRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("skill name is required")
	}
	allowSet := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		allowSet[id] = true
	}
	for _, s := range LoadSkills(db, owner) {
		if s.Disabled || !strings.EqualFold(s.Name, name) {
			continue
		}
		if !allowSet[s.ID] {
			return nil, fmt.Errorf("skill %q is not enabled here", s.Name)
		}
		sc := s
		return &sc, nil
	}
	return nil, fmt.Errorf("no skill named %q (check the 'Available skills' block)", name)
}

// querySkillSourceHooks queries each source-hook the skill names in its
// AllowedTools (e.g. "courtlistener_search") for the query and returns
// their combined text, or "" when the skill has none / they're empty.
// Reuses the registered source-hook tool's own handler so the adapter
// logic lives in one place.
func querySkillSourceHooks(skill SkillRecord, query string) string {
	var b strings.Builder
	for _, name := range skill.AllowedTools {
		def, ok := SourceHookToolDefByName(name)
		if !ok || def.Handler == nil {
			continue
		}
		res, err := def.Handler(map[string]any{"query": query})
		if err != nil || strings.TrimSpace(res) == "" {
			continue
		}
		b.WriteString("\n\n— from ")
		b.WriteString(name)
		b.WriteString(" —\n")
		b.WriteString(strings.TrimSpace(res))
	}
	return b.String()
}

// skillInstructionsBlock formats a skill's instructions as the lens to
// apply to its knowledge, marking it delivered. Returns "" when the
// instructions were already delivered this turn (via read_skill, a prior
// search, or trigger-injection) or the skill has no body.
func skillInstructionsBlock(skill SkillRecord, delivered map[string]bool) string {
	if delivered != nil && delivered[skill.ID] {
		return ""
	}
	body := strings.TrimSpace(skill.Instructions)
	if delivered != nil {
		delivered[skill.ID] = true
	}
	if body == "" {
		return ""
	}
	return "Apply the \"" + skill.Name + "\" approach for the REST of this turn — it governs how you read these results AND how you reply, not just this one result:\n\n" + body + "\n\n---\n"
}

// BuildReadSkillTool builds read_skill(skill): returns the named skill's
// instructions to apply this turn. One-shot, no state — for when the LLM
// just wants the skill's approach (a PDF-handling method, an output
// format). Marks the skill delivered so the search tool won't repeat the
// instructions.
func BuildReadSkillTool(db Database, owner string, allowed []string, delivered map[string]bool) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "read_skill",
			Description: "Pull a named skill's instructions/approach into this turn and apply them now. Use when you want the skill's METHOD itself (how to handle a PDF, an output format, a voice) — not to search its knowledge (that's skill_knowledge_search). One-shot: it returns the instructions; there's nothing to activate or turn off.",
			Parameters: map[string]ToolParam{
				"skill": {Type: "string", Description: "Exact skill name from the 'Available skills' block (case-insensitive)."},
			},
			Required: []string{"skill"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			found, err := resolveAllowedSkill(db, owner, allowed, stringArgSkill(args, "skill"))
			if err != nil {
				return "", err
			}
			body := strings.TrimSpace(found.Instructions)
			if delivered != nil {
				delivered[found.ID] = true
			}
			if body == "" {
				return fmt.Sprintf("Skill %q has no instructions body — its value is its knowledge sources (use skill_knowledge_search).", found.Name), nil
			}
			return fmt.Sprintf("Skill %q — apply this approach for the REST of this turn, including your reply:\n\n%s", found.Name, body), nil
		},
	}
}

// BuildSkillKnowledgeSearchTool builds skill_knowledge_search(skill, query):
// searches the named skill's sources — its attached collections (via the
// app-provided searchCollections, which may be nil) AND its source-hooks,
// merged. The skill's instructions are attached the FIRST time the skill is
// touched this turn (deduped via delivered) so the LLM gets the lens with
// the evidence even if it skipped read_skill.
func BuildSkillKnowledgeSearchTool(db Database, owner string, allowed []string, delivered map[string]bool, searchCollections func(SkillRecord, string) string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "skill_knowledge_search",
			Description: "Search a named skill's reference sources (its document collections and live source APIs, merged and ranked) for material relevant to your query. Returns excerpts plus — the first time you touch the skill this turn — the skill's approach for interpreting them. Use when the request is in the skill's domain and you need grounded evidence. Pass a doc_id from the results to skill_knowledge_fetch_doc for the full document.",
			Parameters: map[string]ToolParam{
				"skill": {Type: "string", Description: "Exact skill name from the 'Available skills' block (case-insensitive)."},
				"query": {Type: "string", Description: "Natural-language search query — phrase like a web search."},
			},
			Required: []string{"skill", "query"},
			Caps:     []Capability{CapRead, CapNetwork},
		},
		Handler: func(args map[string]any) (string, error) {
			found, err := resolveAllowedSkill(db, owner, allowed, stringArgSkill(args, "skill"))
			if err != nil {
				return "", err
			}
			query := strings.TrimSpace(stringArgSkill(args, "query"))
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			var out strings.Builder
			out.WriteString(skillInstructionsBlock(*found, delivered))
			var collHits string
			if searchCollections != nil {
				collHits = strings.TrimSpace(searchCollections(*found, query))
			}
			hookHits := querySkillSourceHooks(*found, query)
			if collHits == "" && hookHits == "" {
				out.WriteString(fmt.Sprintf("No matches in the %q skill's sources for that query.", found.Name))
				return out.String(), nil
			}
			if collHits != "" {
				out.WriteString(collHits)
			}
			if hookHits != "" {
				out.WriteString(hookHits)
			}
			// Grounding reminder at the point of results (the global
			// grounding rule covers the same ground from the system prompt;
			// this keeps it salient right where the citations are).
			out.WriteString("\n\n[Grounding: cite only what appears in the results above — if a specific citation, number, name, or quote isn't here, say the sources don't specify it rather than supplying one from memory.]")
			return out.String(), nil
		},
	}
}

// BuildSkillKnowledgeFetchDocTool builds skill_knowledge_fetch_doc(skill,
// doc_id): pulls a full document from the skill's corpus via the app-
// provided fetchDoc (nil → unsupported). Mirrors fetch_knowledge_doc.
// Like the other skill tools, it attaches the skill's instructions on the
// first touch this turn (deduped via delivered) — so even if the LLM jumps
// straight to a fetch without read_skill / skill_knowledge_search first,
// it still gets the skill's lens.
func BuildSkillKnowledgeFetchDocTool(db Database, owner string, allowed []string, delivered map[string]bool, fetchDoc func(SkillRecord, string) (string, error)) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "skill_knowledge_fetch_doc",
			Description: "Fetch the full text of a document from a skill's corpus, by the doc_id returned in a skill_knowledge_search result. Use when an excerpt isn't enough.",
			Parameters: map[string]ToolParam{
				"skill":  {Type: "string", Description: "Exact skill name (case-insensitive)."},
				"doc_id": {Type: "string", Description: "The doc_id from a skill_knowledge_search hit."},
			},
			Required: []string{"skill", "doc_id"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			found, err := resolveAllowedSkill(db, owner, allowed, stringArgSkill(args, "skill"))
			if err != nil {
				return "", err
			}
			docID := strings.TrimSpace(stringArgSkill(args, "doc_id"))
			if docID == "" {
				return "", fmt.Errorf("doc_id is required")
			}
			if fetchDoc == nil {
				return "", fmt.Errorf("document fetch isn't available for skill %q here", found.Name)
			}
			doc, err := fetchDoc(*found, docID)
			if err != nil {
				return "", err
			}
			return skillInstructionsBlock(*found, delivered) + doc, nil
		},
	}
}

// stringArgSkill extracts a string arg with a case-insensitive fallback.
func stringArgSkill(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	lower := strings.ToLower(key)
	for k, v := range args {
		if strings.ToLower(k) == lower {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// SkillPromptSection returns the skill's instructions formatted for
// injection into a system prompt — an H2 header with the skill name
// followed by the body, separated by blank lines.
func SkillPromptSection(s SkillRecord) string {
	body := strings.TrimSpace(s.Instructions)
	if body == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Skill: ")
	b.WriteString(s.Name)
	b.WriteString("\n\n")
	b.WriteString(body)
	return b.String()
}
