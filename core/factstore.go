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
}

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
// against existing notes. Returns the stored fact + a boolean
// indicating whether it was new (false = a duplicate was found and
// the existing fact is returned instead).
//
// Dedup runs in two tiers:
//   1. Normalized text match (lowercase + collapsed whitespace +
//      stripped edge punctuation). Cheap; catches rephrasing.
//   2. Semantic similarity via embeddings (cosine ≥ 0.90). Catches
//      different wordings of the same fact ("user's name is Craig"
//      vs "Craig is the user's name"). Skipped when embeddings are
//      disabled or the namespace is empty.
//
// First-write-wins on duplicate — the existing fact stays, the new
// content is dropped. Match phantom's behavior.
func StoreMemoryFact(db Database, namespace, note string) (MemoryFact, bool) {
	namespace = strings.TrimSpace(namespace)
	note = strings.TrimSpace(note)
	if db == nil || namespace == "" || note == "" {
		return MemoryFact{}, false
	}
	existing := ListMemoryFacts(db, namespace)
	// Tier 1: normalized text match.
	wantNorm := normalizeFactNote(note)
	for _, f := range existing {
		if normalizeFactNote(f.Note) == wantNorm {
			return f, false
		}
	}
	// Tier 2: semantic similarity, when embeddings are available
	// and there's something to compare against.
	if cfg := GetEmbeddingConfig(); cfg.Enabled && len(existing) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if newVec, err := Embed(ctx, note); err == nil && len(newVec) > 0 {
			for _, f := range existing {
				existVec, err := Embed(ctx, f.Note)
				if err != nil || len(existVec) != len(newVec) {
					continue
				}
				if Cosine(newVec, existVec) >= factDedupSimThreshold {
					return f, false
				}
			}
		}
	}
	f := MemoryFact{
		Namespace: namespace,
		ID:        UUIDv4(),
		Note:      note,
		Created:   time.Now(),
	}
	db.Set(MemoryFactsTable, factDBKey(namespace, f.ID), f)
	return f, true
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
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Created.Before(out[j].Created)
	})
	return out
}

// SearchMemoryFacts is a substring scan across the namespace —
// case-insensitive on the Note. Used by recall-style tools that
// take a free-form query. Empty query returns all (sorted by
// ListMemoryFacts's oldest-first order).
func SearchMemoryFacts(db Database, namespace, query string) []MemoryFact {
	all := ListMemoryFacts(db, namespace)
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return all
	}
	var out []MemoryFact
	for _, f := range all {
		if strings.Contains(strings.ToLower(f.Note), q) {
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
			_, isNew := StoreMemoryFact(db, ns, note)
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
