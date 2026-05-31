// Skills — conditional prompt injection with optional tool inclusion.
//
// A skill is a markdown body + frontmatter declaring when it should
// fire. At turn start, the classifier scans the user message against
// every skill in the user's pool. Matching skills get their body
// appended to the system prompt for THIS turn only, and their
// declared tools get unioned into the active catalog.
//
// Conceptually: dynamic, context-sensitive prompts. No new agent
// loop, no separate runtime — just "when X is in the message, the
// host LLM also reads these instructions."
//
// Why this is small: skills are markdown with a YAML header. The
// classifier is three layers (triggers / embedding / LLM tiebreaker)
// but each is a few dozen lines. The activation is one append in
// the prompt-assembly site.
//
// Compared to agents (which have their own dispatch loop, memory,
// facts, etc.), skills are the lightest possible capability bundle.

package core

import (
	"context"
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
	// Embed the description so the classifier's L2 fuzzy match can
	// fire without re-embedding every turn. Failures are non-fatal —
	// the skill still works via triggers (L1); only fuzzy match is
	// disabled until the next save when the embedder might be back.
	if strings.TrimSpace(s.Description) != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if vec, err := Embed(ctx, s.Description); err == nil {
			s.Embedding = vec
		}
	}
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

// ActiveSkills runs the classifier against the user message + any
// attachment filenames and returns the skills that should activate
// this turn. Tiered: explicit triggers first (cheap), embedding
// similarity second (for fuzzy matches). LLM tiebreaker layer is
// available via ClassifyWithLLM but is opt-in per call site.
//
// Caps the result at maxActiveSkills entries — beyond that, the
// system prompt gets bloated and skill discrimination collapses.
const maxActiveSkills = 3

// skillSimilarityThreshold is the L2 cosine cutoff for activating a
// skill from its description alone (no trigger hit). Higher because
// without a trigger signal the embedding match needs to carry the
// whole confidence load.
const skillSimilarityThreshold = 0.55

// skillGatekeeperThreshold is the cosine cutoff for CONFIRMING an
// L1 trigger match. Much more lenient than skillSimilarityThreshold
// because the trigger already gave a positive signal — we just want
// to reject obvious false positives (e.g. "kitchen recipe" trips a
// "kitchen" trigger on a pickleball skill; gatekeeper sees the
// cosine to "pickleball expert" description is ~0.05 and rejects).
//
// Genuinely-related messages clear 0.30 easily; only off-topic
// substring-bombs fall below.
const skillGatekeeperThreshold = 0.30

// SkillActivation reports why a skill activated this turn — what
// matched (L1 trigger / L2 embedding) and the confidence score.
// Used by the activity pane to show classifier reasoning to the
// user; downstream RAG callers only need the SkillRecord itself.
type SkillActivation struct {
	Skill   SkillRecord
	Reason  string  // "trigger" (L1) or "embedding" (L2)
	Trigger string  // matched substring/glob (L1 only; empty for L2)
	Score   float32 // gatekeeper cosine for L1 (or 0 when gatekeeper skipped), similarity cosine for L2
}

// ActiveSkills is the records-only convenience wrapper. New code
// wanting confidence visibility should call ActiveSkillsWithScores.
func ActiveSkills(db Database, username, userMsg string, attachmentNames []string) []SkillRecord {
	return ActiveSkillsContextual(db, username, userMsg, "", attachmentNames)
}

// ActiveSkillsContextual is the records-only convenience wrapper that
// also accepts prior conversational context (last assistant message)
// for follow-up turn classification.
func ActiveSkillsContextual(db Database, username, userMsg, priorContext string, attachmentNames []string) []SkillRecord {
	acts := ActiveSkillsWithScores(db, username, userMsg, priorContext, attachmentNames, nil)
	if len(acts) == 0 {
		return nil
	}
	out := make([]SkillRecord, len(acts))
	for i, a := range acts {
		out[i] = a.Skill
	}
	return out
}

// ActiveSkillsWithScores runs the L1+L2 classifier with optional
// prior context. priorContext (typically the last assistant message)
// gets folded into BOTH the trigger scan and the embedding so
// follow-up turns ("were you talking about X or Y?") classify
// against the topic the conversation just established — not just
// the bare current message, which on short pronoun-heavy follow-ups
// would otherwise miss every skill.
//
// Pass "" for priorContext on first turns or when context shouldn't
// influence (e.g. tool-result classification). Cap input length —
// long prior context dilutes the embedding signal.
// allowedSkills is the set of skill IDs the calling agent has
// explicitly allowlisted. Only skills whose ID is in this set are
// considered — no auto-classification across all skills anymore.
// The agent owner is always the curator of which skills can fire,
// removing the silent-bleed failure mode where an unrelated skill
// classified onto an agent on a loose cosine match. Empty/nil =
// no skills fire (the agent has no active skill list yet).
func ActiveSkillsWithScores(db Database, username, userMsg, priorContext string, attachmentNames []string, allowedSkills []string) []SkillActivation {
	if len(allowedSkills) == 0 {
		return nil
	}
	all := LoadSkills(db, username)
	if len(all) == 0 {
		return nil
	}
	// Build the allowlist set once for O(1) lookup, then drop every
	// skill not in it before either L1 (trigger) or L2 (embedding)
	// runs. Saves the cosine work on skills the caller never granted.
	allow := make(map[string]bool, len(allowedSkills))
	for _, id := range allowedSkills {
		allow[id] = true
	}
	filtered := all[:0]
	for _, s := range all {
		if !allow[s.ID] {
			continue
		}
		filtered = append(filtered, s)
	}
	all = filtered
	if len(all) == 0 {
		return nil
	}
	// Cap prior context — long history dilutes the embedding's
	// signal toward the current question. Last ~1000 chars of the
	// prior assistant turn is the sweet spot: enough to anchor
	// pronouns and follow-ups, not enough to drown the current msg.
	const maxPriorChars = 1000
	prior := strings.TrimSpace(priorContext)
	if len(prior) > maxPriorChars {
		prior = prior[len(prior)-maxPriorChars:]
	}
	// IMPORTANT: prior context feeds the L2 EMBEDDING pass only —
	// NOT the L1 trigger substring scan. Triggers represent explicit
	// USER intent ("I'm asking about X"); substring matches against
	// the LLM's prior incidental mentions (it said "kubernetes" in
	// passing while answering about something else) would false-fire
	// the skill on the next turn. Embeddings are more conservative —
	// a brief mention nudges the vector slightly without dominating.
	//
	// Net behavior: follow-ups like "were you talking about X or Y?"
	// still activate skills via embedding similarity (semantic
	// continuity), but unrelated next-turn questions don't get hit by
	// substring noise from the prior reply.
	msgLower := strings.ToLower(userMsg)
	embedInput := strings.TrimSpace(userMsg)
	if prior != "" {
		embedInput = prior + "\n\n" + embedInput
	}
	picked := make([]SkillActivation, 0, maxActiveSkills)
	pickedIDs := map[string]bool{}

	// Embed once up front so both the L1 gatekeeper (confirms trigger
	// hits are semantically on-topic) and the L2 activation pass
	// (fuzzy match against descriptions) share the same vector. Skip
	// when the embed input is too short to embed meaningfully —
	// in that case L1 fires without a gatekeeper.
	var msgVec []float32
	if len(embedInput) >= 12 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if vec, err := Embed(ctx, embedInput); err == nil && len(vec) > 0 {
			msgVec = vec
		}
		cancel()
	}

	// Layer 1: explicit triggers + gatekeeper. User message only —
	// see header comment for why prior context doesn't feed triggers.
	for _, s := range all {
		if s.Disabled || pickedIDs[s.ID] {
			continue
		}
		matched := matchedTrigger(s.Triggers, msgLower, attachmentNames)
		if matched == "" {
			continue
		}
		// Gatekeeper. Skipped when either vector is unavailable
		// (short message or skill never embedded) — in that case
		// L1's trigger hit is trusted as-is. Score == 0 in that case
		// signals "L1 trusted without gatekeeper" to the UI.
		var gateScore float32
		if len(msgVec) > 0 && len(s.Embedding) > 0 {
			gateScore = cosineSim(msgVec, s.Embedding)
			if gateScore < skillGatekeeperThreshold {
				continue
			}
		}
		picked = append(picked, SkillActivation{
			Skill:   s,
			Reason:  "trigger",
			Trigger: matched,
			Score:   gateScore,
		})
		pickedIDs[s.ID] = true
		if len(picked) >= maxActiveSkills {
			return picked
		}
	}

	// Layer 2: embedding similarity for skills without explicit
	// triggers OR to find additional fuzzy matches above threshold.
	if len(msgVec) == 0 {
		return picked
	}
	type scored struct {
		skill SkillRecord
		score float32
	}
	var candidates []scored
	for _, s := range all {
		if s.Disabled || pickedIDs[s.ID] {
			continue
		}
		if len(s.Embedding) == 0 {
			continue
		}
		sc := cosineSim(msgVec, s.Embedding)
		if sc >= skillSimilarityThreshold {
			candidates = append(candidates, scored{s, sc})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	for _, c := range candidates {
		if len(picked) >= maxActiveSkills {
			break
		}
		picked = append(picked, SkillActivation{
			Skill:  c.skill,
			Reason: "embedding",
			Score:  c.score,
		})
	}
	return picked
}

// matchedTrigger is the score-returning variant of skillTriggerMatches:
// returns the matched trigger phrase (or empty when none matched).
// Used by ActiveSkillsWithScores so the activity pane can show which
// trigger fired.
func matchedTrigger(triggers []string, msgLower string, attachmentNames []string) string {
	if len(triggers) == 0 {
		return ""
	}
	for _, t := range triggers {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		tLower := strings.ToLower(t)
		if strings.HasPrefix(t, "*.") {
			suffix := strings.ToLower(t[1:])
			for _, name := range attachmentNames {
				if strings.HasSuffix(strings.ToLower(name), suffix) {
					return t
				}
			}
			continue
		}
		if strings.Contains(msgLower, tLower) {
			return t
		}
	}
	return ""
}

// skillTriggerMatches is retained as a back-compat predicate (returns
// only true/false) for any caller that doesn't care which trigger
// matched. ActiveSkillsWithScores uses matchedTrigger above to also
// capture the matched substring.
func skillTriggerMatches(triggers []string, msgLower string, attachmentNames []string) bool {
	if len(triggers) == 0 {
		return false
	}
	for _, t := range triggers {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		tLower := strings.ToLower(t)
		if strings.HasPrefix(t, "*.") {
			suffix := strings.ToLower(t[1:])
			for _, name := range attachmentNames {
				if strings.HasSuffix(strings.ToLower(name), suffix) {
					return true
				}
			}
			continue
		}
		if strings.Contains(msgLower, tLower) {
			return true
		}
	}
	return false
}

// cosineSim is a tight inline cosine — avoids a cross-package dep on
// math/vector libraries. Float32 to match the embedder's native
// output. Assumes both inputs have non-zero magnitude (callers gate
// on len != 0 already).
func cosineSim(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
	}
	for i := range a {
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt32(na) * sqrt32(nb))
}

func sqrt32(x float32) float32 {
	// Tight inline newton-raphson; for length normalization a few
	// iterations is enough — we don't need stdlib's full precision.
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 4; i++ {
		z = (z + x/z) * 0.5
	}
	return z
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
