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
