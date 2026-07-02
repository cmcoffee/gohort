// Scoped flat memory primitive shared across apps that learn from
// conversation (orchestrate, servitor, future). Each MemoryFact is a
// freeform note under a namespace ("agent:<id>", "appliance:<id>", …);
// no key dimension, no tags. Dedup at the save site (text normalize +
// semantic similarity) keeps the store unique without requiring the
// LLM to invent stable keys — that was the source of the duplicate-
// facts problem the keyed model had.
//
// Two ways apps consume this:
//   1. LLM in-band: bind store_fact / forget_fact / list_facts tools
//      to the agent. The model records notes as it learns them.
//   2. Prompt injection: RenderMemoryFactsBlock returns a markdown
//      block apps paste into every system prompt — "always on" memory.
//
// Storage: kvlite table "core_facts" keyed by "<namespace>/<id>".
// Per-(user, app) isolation comes from the caller passing a user-
// scoped UserDB; the namespace further partitions by agent.

package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// MemoryFactsTable is the kvlite table name. One table shared across
// apps so a future cross-app "show me everything I remember" surface
// can enumerate without per-app glue. The namespace field on each
// fact keeps app data separated.
const MemoryFactsTable = "core_facts"

// MemoryFact is one note in a per-namespace memory store. No key —
// the LLM saves text directly; dedup at save time prevents
// duplicates that key-based stores accumulate when the LLM picks
// inconsistent keys for related content.
type MemoryFact struct {
	Namespace string    `json:"namespace"`
	ID        string    `json:"id"`
	Note      string    `json:"note"`
	Created   time.Time `json:"created"`
	// Updated tracks the last time this fact's meaning changed — set to Created
	// on save, and bumped when the fact is superseded (its status changes). It is
	// NOT bumped by the transparent vector backfill (a cache fill, not a content
	// change). Enables auditability and recency/staleness-aware pruning (a soft
	// fact cap can evict least-recently-updated first). Zero on facts saved before
	// this field existed.
	Updated time.Time `json:"updated,omitempty"`
	// Vector is the note's embedding, computed once at save time and reused for
	// dedup/supersession/search — so a namespace of N facts costs O(1) embed
	// calls per save/search instead of O(N) re-embedding every existing note.
	// Empty on facts saved before this field existed; factVector backfills
	// those lazily on first use. Mirrors EmbeddedChunk.Vector in vector_store.go.
	Vector []float32 `json:"vector,omitempty"`
	// SupersededAt is non-zero once a later fact replaced this one
	// (the user changed an attribute: moved cities, switched jobs).
	// Superseded facts stay in the table for history but are filtered
	// out of ListMemoryFacts, so they no longer inject or dedup.
	SupersededAt time.Time `json:"superseded_at,omitempty"`
	SupersededBy string    `json:"superseded_by,omitempty"` // ID of the replacing fact
}

// FactChatFunc runs one worker-tier LLM chat for supersession judging.
// Its signature matches AppCore.WorkerChat and Session.Chat, so callers
// pass either as a method value. Optional: without it, StoreMemoryFact
// behaves exactly as before (dedup only, no supersession).
type FactChatFunc func(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error)

// factDedupSimThreshold is the cosine cutoff above which two notes
// are considered semantic duplicates. Same value phantom uses for
// its memory layer — calibrated for "rephrasing the same fact"
// without flagging genuinely different facts.
const factDedupSimThreshold = 0.90

// factDBKey assembles the kvlite key. Slash-delimited so namespace
// listings can use prefix scans cleanly.
func factDBKey(namespace, id string) string {
	return namespace + "/" + id
}

// StoreMemoryFact saves a new note under the namespace with dedup
// against existing notes. Returns the stored fact, a boolean
// indicating whether it was new (false = a duplicate was found and
// the existing fact is returned instead), and the list of facts this
// note superseded (nil unless a chat func was supplied and a changed
// fact was detected) so callers can surface what was dropped.
//
// Dedup runs in two tiers:
//   1. Normalized text match (lowercase + collapsed whitespace +
//      stripped edge punctuation). Cheap; catches rephrasing.
//   2. Semantic similarity via embeddings (cosine ≥ 0.90). Catches
//      different wordings of the same fact ("user's name is Robin"
//      vs "Robin is the user's name"). Skipped when embeddings are
//      disabled or the namespace is empty.
//
// First-write-wins on duplicate — the existing fact stays, the new
// content is dropped. Match phantom's behavior.
//
// Supersession: when an optional chat func is supplied AND embeddings
// surface a related-but-not-duplicate fact, the new note is checked for
// whether it REPLACES an existing one (same attribute that can't both be
// current — "lives in X" → "lives in Y"). Replaced facts are marked
// superseded so they stop injecting, instead of either being dropped as a
// near-duplicate (stuck on the stale value) or coexisting as a
// contradiction. Keys are NOT used for this — the keyed model was reverted
// because the LLM picks inconsistent keys; supersession runs on meaning.
func StoreMemoryFact(db Database, namespace, note string, chat ...FactChatFunc) (MemoryFact, bool, []MemoryFact) {
	namespace = strings.TrimSpace(namespace)
	note = strings.TrimSpace(note)
	if db == nil || namespace == "" || note == "" {
		return MemoryFact{}, false, nil
	}
	existing := ListMemoryFacts(db, namespace)
	// Tier 1: normalized text match.
	wantNorm := normalizeFactNote(note)
	for _, f := range existing {
		if normalizeFactNote(f.Note) == wantNorm {
			return f, false, nil
		}
	}
	// Tier 2: semantic similarity, when embeddings are available
	// and there's something to compare against. The same pass collects
	// "supersession candidates" — facts in the related-but-not-duplicate
	// band [factSupersedeBandFloor, factDedupSimThreshold) — at no extra
	// embedding cost, for the contradiction check below.
	var supersedeCandidates []MemoryFact
	var newVec []float32
	if cfg := GetEmbeddingConfig(); cfg.Enabled {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if v, err := Embed(ctx, note); err == nil && len(v) > 0 {
			newVec = v // cached on the new fact below
			for _, f := range existing {
				existVec := factVector(ctx, db, f) // cached, backfilled if legacy
				if len(existVec) != len(newVec) {
					continue
				}
				sim := Cosine(newVec, existVec)
				if sim >= factDedupSimThreshold {
					return f, false, nil
				}
				if sim >= factSupersedeBandFloor {
					supersedeCandidates = append(supersedeCandidates, f)
				}
			}
		}
	}

	now := time.Now()
	f := MemoryFact{
		Namespace: namespace,
		ID:        UUIDv4(),
		Note:      note,
		Created:   now,
		Updated:   now,
		Vector:    newVec,
	}

	// Supersession: only fires when a chat func was passed AND the embedding
	// pass found related candidates — so the common case (no related facts)
	// pays nothing. The LLM decides which candidates the new note replaces.
	var superseded []MemoryFact
	if len(chat) > 0 && chat[0] != nil && len(supersedeCandidates) > 0 {
		for _, old := range judgeSupersedes(chat[0], note, supersedeCandidates) {
			old.SupersededAt = now
			old.SupersededBy = f.ID
			old.Updated = now // status change — bump Updated
			db.Set(MemoryFactsTable, factDBKey(old.Namespace, old.ID), old)
			Debug("[factstore] superseded %q -> %q (ns=%s)", old.Note, note, namespace)
			superseded = append(superseded, old)
		}
	}

	db.Set(MemoryFactsTable, factDBKey(namespace, f.ID), f)
	return f, true, superseded
}

// factVector returns a fact's cached embedding, computing and lazily persisting
// it when absent — the one-time backfill for facts saved before MemoryFact.Vector
// existed. After the first touch every fact carries its own vector, so
// dedup/search never re-embed the whole namespace. Returns nil if embedding fails
// (caller skips the fact rather than crashing). ctx carries the shared timeout.
func factVector(ctx context.Context, db Database, f MemoryFact) []float32 {
	if len(f.Vector) > 0 {
		return f.Vector
	}
	vec, err := Embed(ctx, f.Note)
	if err != nil || len(vec) == 0 {
		return nil
	}
	f.Vector = vec
	db.Set(MemoryFactsTable, factDBKey(f.Namespace, f.ID), f) // backfill so this cost is paid once
	return vec
}

// factSupersedeBandFloor is the cosine floor for a fact to count as a
// supersession candidate: related enough that a CHANGE is plausible, but
// below the dedup threshold (0.90, "same fact"). The band is only a cheap
// pre-filter — the LLM judge makes the actual replace/coexist call.
const factSupersedeBandFloor = 0.60

// judgeSupersedes asks the worker which candidate facts the new note
// replaces (same attribute, cannot both be current). Returns the subset to
// supersede. Best-effort: any error, nil chat, or unparseable reply yields
// no supersession (the new fact is simply added alongside).
func judgeSupersedes(chat FactChatFunc, newNote string, candidates []MemoryFact) []MemoryFact {
	if chat == nil || len(candidates) == 0 {
		return nil
	}
	var list strings.Builder
	for i, f := range candidates {
		fmt.Fprintf(&list, "%d. %s\n", i+1, f.Note)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := chat(ctx, []Message{
		{Role: "user", Content: fmt.Sprintf(`A memory store holds short facts about a user. A NEW fact is being saved. For each EXISTING fact listed, decide whether the new fact UPDATES or REPLACES it: they describe the SAME attribute or relationship and cannot both be currently true. Examples of replacement: "lives in Denver" replaced by "lives in Austin"; "works at X" replaced by "works at Y"; "phone is A" replaced by "phone is B".

Do NOT flag facts that can independently both be true: "likes coffee" and "likes tea" are different preferences; "has a dog" and "has a cat" coexist. When unsure, do NOT flag — only flag a clear replacement of the same attribute.

NEW fact: %q

EXISTING facts:
%s
Reply with ONLY a JSON array of the numbers of existing facts the new fact replaces. Reply [] if none.`, newNote, list.String())},
	}, WithSystemPrompt("You detect when a new memory fact supersedes existing ones (same attribute, cannot both be current). Reply with ONLY a JSON array of indices."),
		WithThink(false),
		WithMaxTokens(128))
	if err != nil || resp == nil {
		return nil
	}
	var idx []int
	if DecodeJSON(ResponseText(resp), &idx) != nil {
		return nil
	}
	var out []MemoryFact
	for _, n := range idx {
		if n >= 1 && n <= len(candidates) {
			out = append(out, candidates[n-1])
		}
	}
	return out
}

// ForgetMemoryFactByIndex removes the fact at the given 1-based
// index in the namespace's oldest-first list. Returns the removed
// fact + a boolean for success. Used by LLM-facing forget tools
// where the model picks a row from the listed view.
func ForgetMemoryFactByIndex(db Database, namespace string, index int) (MemoryFact, bool) {
	namespace = strings.TrimSpace(namespace)
	if db == nil || namespace == "" || index < 1 {
		return MemoryFact{}, false
	}
	facts := ListMemoryFacts(db, namespace)
	if index > len(facts) {
		return MemoryFact{}, false
	}
	target := facts[index-1]
	db.Unset(MemoryFactsTable, factDBKey(namespace, target.ID))
	return target, true
}

// ForgetMemoryFactByID removes one fact by its ID. Used by admin /
// programmatic paths where the ID is known. No-op when missing.
func ForgetMemoryFactByID(db Database, namespace, id string) bool {
	namespace = strings.TrimSpace(namespace)
	id = strings.TrimSpace(id)
	if db == nil || namespace == "" || id == "" {
		return false
	}
	if !db.Get(MemoryFactsTable, factDBKey(namespace, id), &MemoryFact{}) {
		return false
	}
	db.Unset(MemoryFactsTable, factDBKey(namespace, id))
	return true
}

// ListMemoryFacts returns every fact under the namespace, sorted
// oldest first so the index returned to the LLM matches the order
// it sees in the rendered block. Empty when nothing's stored or
// the db handle is nil.
func ListMemoryFacts(db Database, namespace string) []MemoryFact {
	namespace = strings.TrimSpace(namespace)
	if db == nil || namespace == "" {
		return nil
	}
	prefix := namespace + "/"
	var out []MemoryFact
	for _, k := range db.Keys(MemoryFactsTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var f MemoryFact
		if db.Get(MemoryFactsTable, k, &f) {
			// Superseded facts stay in the table for history but never
			// inject, dedup, or get indexed for forget — they are stale by
			// definition.
			if !f.SupersededAt.IsZero() {
				continue
			}
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Created.Before(out[j].Created)
	})
	return out
}

// factSearchMinScore is the cosine floor for a fact to count as a semantic
// match in SearchMemoryFacts. Well below the dedup threshold (0.90, "same
// fact") — recall wants "related enough to be worth showing," not "identical."
const factSearchMinScore = 0.35

// factSearchTopK caps how many semantic matches SearchMemoryFacts returns —
// enough for the LLM to find the relevant one without dumping the namespace.
const factSearchTopK = 8

// SearchMemoryFacts recalls facts for a free-form query. Semantic-first when
// embeddings are enabled: it ranks facts by cosine to the query and returns the
// most relevant — so "what's Emily's phone" surfaces a fact stored as "wife's
// number is +1415…" that a substring scan would miss. Falls back to a
// case-insensitive substring scan when embeddings are off, or when semantic
// finds nothing above the floor (which still catches exact-token matches a low
// cosine can miss). Empty query returns all (oldest-first).
//
// Facts carry their embedding (MemoryFact.Vector), computed once at save time,
// so a search embeds only the QUERY and scores against the cached fact vectors —
// O(1) embed calls, not one per fact. Legacy facts without a vector are
// backfilled on first touch by factVector.
func SearchMemoryFacts(db Database, namespace, query string) []MemoryFact {
	all := ListMemoryFacts(db, namespace)
	q := strings.TrimSpace(query)
	if q == "" {
		return all
	}
	if cfg := GetEmbeddingConfig(); cfg.Enabled && len(all) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if qVec, err := Embed(ctx, q); err == nil && len(qVec) > 0 {
			type scored struct {
				fact  MemoryFact
				score float32
			}
			var ranked []scored
			for _, f := range all {
				fVec := factVector(ctx, db, f) // cached, backfilled if legacy
				if len(fVec) != len(qVec) {
					continue
				}
				if s := Cosine(qVec, fVec); s >= factSearchMinScore {
					ranked = append(ranked, scored{f, s})
				}
			}
			if len(ranked) > 0 {
				sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
				if len(ranked) > factSearchTopK {
					ranked = ranked[:factSearchTopK]
				}
				out := make([]MemoryFact, len(ranked))
				for i, r := range ranked {
					out[i] = r.fact
				}
				return out
			}
			// Nothing above the floor — fall through to substring.
		}
	}
	ql := strings.ToLower(q)
	var out []MemoryFact
	for _, f := range all {
		if strings.Contains(strings.ToLower(f.Note), ql) {
			out = append(out, f)
		}
	}
	return out
}

// RenderMemoryFactsBlock returns the markdown section apps paste
// into every system prompt — header + one row per fact. Empty
// string when nothing's stored so callers can append unconditionally.
// One numbered note per line — the index matches what
// ForgetMemoryFactByIndex accepts, so the LLM can reference it
// directly when calling forget.
//
// Default user-facing copy. Callers that want to retarget the block
// at a different memory shape (e.g. Builder's lessons-learned)
// should call RenderMemoryFactsBlockWith with override strings.
func RenderMemoryFactsBlock(facts []MemoryFact) string {
	return RenderMemoryFactsBlockWith(facts, "", "")
}

// RenderMemoryFactsBlockWith is the override variant. Empty header /
// intro fall back to the default user-facing copy, so partial
// overrides (just the intro, or just the header) work.
func RenderMemoryFactsBlockWith(facts []MemoryFact, header, intro string) string {
	if len(facts) == 0 {
		return ""
	}
	if header == "" {
		header = "## Saved facts"
	}
	if intro == "" {
		intro = "Structured facts you've stored from prior conversations with this user (distinct from the Memory section's longer notes — these are short, durable specifics like names, preferences, and dates). Apply when relevant; ignore otherwise. Each fact is numbered so you can reference an index when forgetting."
	}
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n\n")
	b.WriteString(intro)
	b.WriteString("\n\n")
	for i, f := range facts {
		b.WriteString(intToString(i + 1))
		b.WriteString(". ")
		b.WriteString(f.Note)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

func intToString(n int) string {
	// Avoids strconv dependency; tiny integers only.
	if n < 10 {
		return string(rune('0' + n))
	}
	return intToString(n/10) + string(rune('0'+n%10))
}

// normalizeFactNote lowercases + collapses whitespace + strips edge
// punctuation so cosmetic differences don't slip past the cheap
// tier-1 dedup pass.
func normalizeFactNote(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimFunc(s, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	})
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// MigrateLegacyFactStore converts old keyed-shape fact rows
// ({Namespace, Key, Value, Tags, TTL, Updated}) to the new flat
// {Namespace, ID, Note, Created} shape. Runs once at startup;
// idempotent — flat-shape rows on re-read are left alone.
//
// Conversion rule: Note becomes "<Key>: <Value>" so context the
// key provided isn't lost. Dedup runs over the migrated batch via
// StoreMemoryFact's normal path, so two old rows with similar
// values converge to one new row.
func MigrateLegacyFactStore(db Database) {
	if db == nil {
		return
	}
	// Probe for legacy entries: old shape had a "key" field; new
	// shape has "id" instead. Decode into a permissive map first.
	type legacy struct {
		Namespace string    `json:"namespace"`
		Key       string    `json:"key"`
		Value     string    `json:"value"`
		Tags      []string  `json:"tags,omitempty"`
		Updated   time.Time `json:"updated"`
		// Note + ID present indicates already-migrated row.
		ID   string `json:"id,omitempty"`
		Note string `json:"note,omitempty"`
	}
	type byNamespace struct {
		notes   []string
		oldKeys []string // DB keys to unset after migration
	}
	groups := map[string]*byNamespace{}
	allKeys := db.Keys(MemoryFactsTable)
	for _, k := range allKeys {
		var row legacy
		if !db.Get(MemoryFactsTable, k, &row) {
			continue
		}
		// Already-migrated row has ID + Note populated. Skip.
		if row.ID != "" && row.Note != "" {
			continue
		}
		// Legacy row: has Key or Value populated.
		if row.Key == "" && row.Value == "" {
			continue
		}
		g, ok := groups[row.Namespace]
		if !ok {
			g = &byNamespace{}
			groups[row.Namespace] = g
		}
		// Combine "<Key>: <Value>" so the key's contextual hint
		// survives the migration. Trim "<Key>: " when Key is empty.
		note := strings.TrimSpace(row.Value)
		if row.Key != "" {
			note = strings.TrimSpace(row.Key) + ": " + note
		}
		g.notes = append(g.notes, note)
		g.oldKeys = append(g.oldKeys, k)
	}
	if len(groups) == 0 {
		return
	}
	migrated, dedup := 0, 0
	for ns, g := range groups {
		// Delete old rows BEFORE re-storing so the new StoreMemoryFact
		// dedup pass sees a clean slate per namespace.
		for _, k := range g.oldKeys {
			db.Unset(MemoryFactsTable, k)
		}
		for _, note := range g.notes {
			_, isNew, _ := StoreMemoryFact(db, ns, note)
			if isNew {
				migrated++
			} else {
				dedup++
			}
		}
	}
	if migrated+dedup > 0 {
		Log("[factstore] migrated %d legacy fact(s) to flat shape (deduped %d on the way through)", migrated, dedup)
	}
}
