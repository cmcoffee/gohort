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
	"sync"
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
	// MemoryProvenance carries origin (Source/Volatility/AsOf) and retirement
	// (Reason/RetiredAt/Successor). Its Reason field REPLACES the former
	// SupersededAt/SupersededBy pair: supersession is now Reason==RetireSuperseded
	// with Successor set, one tombstone mechanism shared with eviction and merge.
	// Retired facts stay in the table for history (surfaced by recall when a live
	// query misses) but are filtered out of ListMemoryFacts, so they no longer
	// inject, dedup, or index — until tombstone retention hard-deletes them.
	// Legacy superseded_* rows are converted in MigrateLegacyFactStore.
	MemoryProvenance
}

// FactChatFunc runs one worker-tier LLM chat for supersession judging.
// Its signature matches AppCore.WorkerChat and Session.Chat, so callers
// pass either as a method value. Optional: without it, StoreMemoryFact
// behaves exactly as before (dedup only, no supersession).
type FactChatFunc func(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error)

// Memory-hygiene tunables (admin-configurable via core/tunables.go). All numeric,
// since the tunable registry only carries numbers.
const (
	// TunableFactSweepThreshold: when a namespace's non-superseded fact count
	// reaches this, an async prune sweep runs. 0 disables the sweep.
	TunableFactSweepThreshold = "tune_fact_sweep_threshold"
	// TunableFactHardCap: after a sweep, evict least-recently-updated facts until
	// the namespace is at or below this count. 0 disables the cap.
	TunableFactHardCap = "tune_fact_hard_cap"
	// TunableFactGate: 1 = apply the write-time relevance gate in strict modes
	// (reject ephemeral notes before they are stored); 0 = accept all (dedup +
	// supersession only).
	TunableFactGate = "tune_fact_gate"
	// TunableFactTombstoneDays: how many days a retired fact (superseded, evicted,
	// or merged) stays queryable so recall can explain a hole before it is
	// permanently deleted. 0 = delete retired facts immediately (retirement
	// collapses back to a plain delete).
	TunableFactTombstoneDays = "tune_fact_tombstone_days"
)

func init() {
	RegisterTunable(TunableSpec{Key: TunableFactSweepThreshold, Category: "Limits",
		Label: "Memory sweep threshold (0 = off)",
		Help:  "When an agent's saved-fact count reaches this, an async worker-LLM prune removes junk and collapses redundant notes so the always-in-prompt block stays lean.",
		Kind:  KindInt, Default: 40, Min: 0, Max: 500})
	RegisterTunable(TunableSpec{Key: TunableFactHardCap, Category: "Limits",
		Label: "Memory hard cap (0 = off)",
		Help:  "After a sweep, evict least-recently-updated facts until the store is at or below this many. Backstop against unbounded prompt growth.",
		Kind:  KindInt, Default: 60, Min: 0, Max: 1000})
	RegisterTunable(TunableSpec{Key: TunableFactGate, Category: "Limits",
		Label: "Memory relevance gate (chatbot mode)",
		Help:  "1 = reject ephemeral, non-durable notes at write time in chatbot-mode agents (the personal-assistant/group-chat persona). 0 = store everything the model decides to save.",
		Kind:  KindBool, Default: 1, Min: 0, Max: 1})
	RegisterTunable(TunableSpec{Key: TunableFactTombstoneDays, Category: "Limits",
		Label: "Memory tombstone retention (days, 0 = keep none)",
		Help:  "How long a retired fact (superseded, evicted, or merged) stays queryable so recall can explain a hole (\"you had X; it was dropped on <date>\") before it is permanently deleted. 0 = delete retired facts immediately.",
		Kind:  KindInt, Default: 30, Min: 0, Max: 365})
	RegisterTunable(TunableSpec{Key: TunableStaleVolatileDays, Category: "Memory",
		Label: "Volatile fact half-life (days)",
		Help:  "A fact classified volatile (prices, live status, versions) is flagged aging on pull once older than this, and stale past 2x. 0 = never flag volatile facts. Does not affect the always-in-prompt block (it shows the fixed as-of date).",
		Kind:  KindInt, Default: 3, Min: 0, Max: 365})
	RegisterTunable(TunableSpec{Key: TunableStaleSlowDays, Category: "Memory",
		Label: "Slow-changing fact half-life (days)",
		Help:  "A fact classified slow-changing (employer, city, role) is flagged aging on pull once older than this, and stale past 2x. 0 = never flag slow facts.",
		Kind:  KindInt, Default: 90, Min: 0, Max: 3650})
}

// FactWritePolicy carries the per-call context StoreMemoryFactP needs beyond the
// raw note. The zero value is the legacy permissive behavior (dedup only, no gate,
// no supersession), so StoreMemoryFact can wrap it without changing existing
// callers.
type FactWritePolicy struct {
	// Mode is the agent's MemoryMode ("agent" / "chatbot" / "shortcuts"). The
	// write-time relevance gate engages only for modes FactGateApplies reports
	// strict (currently "chatbot"), where broad personalization and conversational
	// chatter tend to accumulate as junk.
	Mode string
	// Chat is the worker-tier LLM used for the relevance gate, supersession
	// judging, and (once triggered) the prune sweep. Nil disables all three; dedup
	// still runs.
	Chat FactChatFunc
	// Source records HOW this note entered memory, stored on the fact's
	// provenance envelope. It drives the grounding split: user_stated and
	// retrieved facts are legitimate grounds (SourcedFactCorpus surfaces them to
	// the grounding gate), while observed/inferred/unknown are not. Zero
	// (MemSourceUnknown) leaves the origin unrecorded.
	Source MemSource
}

// FactWriteReason explains what StoreMemoryFactP did with a submitted note.
type FactWriteReason int

const (
	FactStored    FactWriteReason = iota // newly saved
	FactDuplicate                        // folded into an existing fact by dedup
	FactRejected                         // gate judged it non-durable, or input was empty
)

// FactWriteResult is the full outcome of a StoreMemoryFactP call. Fact holds the
// stored fact on FactStored or the existing duplicate on FactDuplicate, and is
// zero on FactRejected. Superseded lists facts the new note replaced.
type FactWriteResult struct {
	Fact       MemoryFact
	Reason     FactWriteReason
	Superseded []MemoryFact
}

// FactGateApplies reports whether the write-time relevance gate should run for an
// agent in the given memory mode. Scoped to "chatbot" mode (the broad,
// personal-assistant/group-chat persona where ephemeral junk collects) and
// controlled by the TunableFactGate on/off switch.
func FactGateApplies(mode string) bool {
	return mode == "chatbot" && TuneBool(TunableFactGate)
}

// volatileFactSignals mark a note whose value changes on a days-to-weeks scale
// (prices, live status, versions, standings). Category-based, NOT confidence-
// based: the model is most certain of exactly these stale priors (the "$1,600
// 5090" case), so the note's SUBJECT, not the model's sureness, sets volatility.
var volatileFactSignals = []string{
	"price", "cost", "costs ", "$", " usd", "dollar", "msrp", "how much",
	"in stock", "out of stock", "sold out", "availability", "available now",
	"currently", "current ", "latest", "right now", "as of ", "this week", "this month",
	"score", "standings", "ranked", "ranking", "leaderboard",
	"latest version", "version is", "current version",
	"stock price", "share price", "market cap",
}

// slowFactSignals mark a note whose value changes over months to years
// (employer, city, role, relationship, contact details).
var slowFactSignals = []string{
	"works at", "works for", "employed", "employer", "job at", "company is",
	"lives in", "based in", "located in", "moved to", "resides",
	"ceo", "president", "prime minister", "director of", "head of",
	"married", "dating", "spouse", "partner is", "girlfriend", "boyfriend",
	"phone number", "address is", "email is",
}

// classifyVolatility infers a note's Volatility from its subject. Conservative:
// defaults to VolStable (never flagged stale) and only escalates on a clear
// signal — a false "stable" is just the status quo, while a false "volatile"
// only adds a mild as-of marker. Volatile wins ties with slow.
func classifyVolatility(note string) Volatility {
	n := strings.ToLower(note)
	for _, s := range volatileFactSignals {
		if strings.Contains(n, s) {
			return VolVolatile
		}
	}
	for _, s := range slowFactSignals {
		if strings.Contains(n, s) {
			return VolSlow
		}
	}
	return VolStable
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
// against existing notes. Returns the stored fact, a boolean
// indicating whether it was new (false = a duplicate was found and
// the existing fact is returned instead), and the list of facts this
// note superseded (nil unless a chat func was supplied and a changed
// fact was detected) so callers can surface what was dropped.
//
// Dedup runs in two tiers:
//  1. Normalized text match (lowercase + collapsed whitespace +
//     stripped edge punctuation). Cheap; catches rephrasing.
//  2. Semantic similarity via embeddings (cosine ≥ 0.90). Catches
//     different wordings of the same fact ("user's name is Robin"
//     vs "Robin is the user's name"). Skipped when embeddings are
//     disabled or the namespace is empty.
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
	var c FactChatFunc
	if len(chat) > 0 {
		c = chat[0]
	}
	// Legacy wrapper: no Mode, so the relevance gate never engages (dedup +
	// supersession only), preserving the exact behavior every existing caller
	// depends on. A FactDuplicate maps to the old isNew=false.
	res := StoreMemoryFactP(db, namespace, note, FactWritePolicy{Chat: c})
	return res.Fact, res.Reason == FactStored, res.Superseded
}

// StoreMemoryFactP is the policy-aware save path. It runs the two dedup tiers,
// then — when the policy supplies a worker chat — a relevance gate (strict modes
// only) and supersession judging, and finally kicks off a lazy prune sweep once
// the store crosses the sweep threshold. Returns a FactWriteResult so callers can
// distinguish stored / deduped / gate-rejected and surface the right message.
func StoreMemoryFactP(db Database, namespace, note string, p FactWritePolicy) FactWriteResult {
	namespace = strings.TrimSpace(namespace)
	note = strings.TrimSpace(note)
	if db == nil || namespace == "" || note == "" {
		return FactWriteResult{Reason: FactRejected}
	}
	existing := ListMemoryFacts(db, namespace)
	// Tier 1: normalized text match.
	wantNorm := normalizeFactNote(note)
	for _, f := range existing {
		if normalizeFactNote(f.Note) == wantNorm {
			return FactWriteResult{Fact: f, Reason: FactDuplicate}
		}
	}
	// Tier 2: semantic similarity, when embeddings are available and there's
	// something to compare against. The same pass collects "supersession
	// candidates" — facts in the related-but-not-duplicate band
	// [factSupersedeBandFloor, factDedupSimThreshold) — at no extra embedding cost.
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
					return FactWriteResult{Fact: f, Reason: FactDuplicate}
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
		// A fresh fact is confirmed-true as of now, its volatility is inferred from
		// its subject (category-based, deterministic), and its Source comes from the
		// write policy (MemSourceUnknown when the caller doesn't set one).
		MemoryProvenance: MemoryProvenance{AsOf: now, Volatility: classifyVolatility(note), Source: p.Source},
		Vector:           newVec,
	}

	// Relevance gate + supersession. When the gate applies (strict mode + worker
	// available), a single worker call answers both "does this belong?" and "what
	// does it replace?" — so a gated save costs at most one extra LLM round-trip.
	// Otherwise fall back to the supersession-only judge, which runs only when the
	// embedding pass surfaced related candidates (the common case pays nothing).
	var superseded []MemoryFact
	if p.Chat != nil && FactGateApplies(p.Mode) {
		relevant, toSupersede := judgeFactWrite(p.Chat, note, supersedeCandidates)
		if !relevant {
			Debug("[factstore] gate rejected non-durable note %q (ns=%s)", note, namespace)
			return FactWriteResult{Reason: FactRejected}
		}
		superseded = applySupersede(db, f.ID, now, toSupersede)
	} else if p.Chat != nil && len(supersedeCandidates) > 0 {
		superseded = applySupersede(db, f.ID, now, judgeSupersedes(p.Chat, note, supersedeCandidates))
	}

	db.Set(MemoryFactsTable, factDBKey(namespace, f.ID), f)

	// Live (non-superseded) count as the store now stands: existing minus what this
	// call just superseded, plus the one we added.
	live := len(existing) - len(superseded) + 1
	// Cheap synchronous backstop: keep the store at or under the hard cap on every
	// write, independent of the rate-limited async sweep. Just-added fact is the
	// most-recently-updated, so LRU never evicts it. Runs only when over the cap.
	if limit := TuneInt(TunableFactHardCap); limit > 0 && live > limit {
		enforceFactHardCap(db, namespace)
	}
	// Expensive periodic cleanup: once the store crosses the sweep threshold, an
	// async, single-flight, rate-limited sweep prunes junk and collapses redundancy.
	if th := TuneInt(TunableFactSweepThreshold); th > 0 && live >= th {
		maybeSweepFacts(db, namespace, p.Chat)
	}

	return FactWriteResult{Fact: f, Reason: FactStored, Superseded: superseded}
}

// retireFact moves one live fact into the history set with the given reason and
// (for supersede/merge) a successor pointer to the row that replaced or absorbed
// it. Retired facts stay in the table — filtered out of the live ListMemoryFacts
// view so they stop injecting, deduping, and indexing, but queryable by recall so
// a hole has a record — until tombstone retention hard-deletes them. Updated is
// bumped because retirement is a status change; the hard-cap LRU and retention
// sort both read it.
func retireFact(db Database, f MemoryFact, reason RetireReason, successor string, now time.Time) {
	f.Reason = reason
	f.RetiredAt = now
	f.Successor = successor
	f.Updated = now
	db.Set(MemoryFactsTable, factDBKey(f.Namespace, f.ID), f)
}

// applySupersede tombstones each old fact as superseded by newID and persists it.
// Returns the (pre-retirement) facts it touched so the caller can report what went
// stale by note.
func applySupersede(db Database, newID string, now time.Time, olds []MemoryFact) []MemoryFact {
	var out []MemoryFact
	for _, old := range olds {
		retireFact(db, old, RetireSuperseded, newID, now)
		Debug("[factstore] superseded %q (ns=%s)", old.Note, old.Namespace)
		out = append(out, old)
	}
	return out
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

// judgeFactWrite is the combined gate + supersession call used on the strict-mode
// write path. One worker turn returns whether the new note is durable enough to
// keep (relevant) and which existing candidates it replaces (supersedes). Fails
// OPEN on any error, nil chat, or unparseable reply: relevant=true, no
// supersession — a save is never silently dropped because the judge was down.
func judgeFactWrite(chat FactChatFunc, newNote string, candidates []MemoryFact) (bool, []MemoryFact) {
	if chat == nil {
		return true, nil
	}
	var list strings.Builder
	for i, f := range candidates {
		fmt.Fprintf(&list, "%d. %s\n", i+1, f.Note)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := chat(ctx, []Message{
		{Role: "user", Content: fmt.Sprintf(`A personal chatbot keeps a SMALL set of short, durable facts about a user. Every saved fact is injected into every future prompt forever, so junk is expensive. A NEW note is about to be saved. Make two judgments.

1. relevant: Is this a DURABLE fact worth recalling across sessions - a name, a stable preference, an identity, role, or relationship, ongoing project context, or a standing instruction? Reject it (relevant = false) if it is EPHEMERAL: small talk, a one-off remark, transient state ("user said hello", "user is typing", "it is raining right now"), or anything that will not matter next session. When you are genuinely unsure, keep it (relevant = true).

2. supersedes: For each EXISTING fact listed, does the new note REPLACE it - same attribute or relationship, and they cannot both be currently true ("lives in Denver" becomes "lives in Austin")? Do NOT flag facts that can independently coexist ("likes coffee" vs "likes tea"). List the numbers of the facts the new note replaces.

NEW note: %q

EXISTING facts:
%s
Reply with ONLY JSON: {"relevant": true or false, "supersedes": [numbers]}. Use [] when nothing is superseded.`, newNote, list.String())},
	}, WithSystemPrompt("You gate a personal chatbot's long-term memory: decide whether a new note is durable enough to keep, and which existing notes it replaces. Reply with ONLY the requested JSON object."),
		WithThink(false),
		WithMaxTokens(192))
	if err != nil || resp == nil {
		return true, nil
	}
	var parsed struct {
		Relevant   *bool `json:"relevant"`
		Supersedes []int `json:"supersedes"`
	}
	if DecodeJSON(ResponseText(resp), &parsed) != nil {
		return true, nil
	}
	relevant := parsed.Relevant == nil || *parsed.Relevant // missing field => keep
	var out []MemoryFact
	for _, n := range parsed.Supersedes {
		if n >= 1 && n <= len(candidates) {
			out = append(out, candidates[n-1])
		}
	}
	return relevant, out
}

// factSweepCooldown rate-limits how often one namespace is swept. Without it a
// store that legitimately sits above the threshold (nothing to prune) would fire a
// worker call on every subsequent write. The cheap hard-cap backstop runs on the
// write path regardless, so bounded growth doesn't depend on this cadence.
const factSweepCooldown = 10 * time.Minute

// factSweepInFlight + factSweepLast guard against stacking and over-frequent
// sweeps on the same namespace: a busy store fires many writes, and each would
// otherwise launch its own sweep.
var (
	factSweepMu       sync.Mutex
	factSweepInFlight = map[string]bool{}
	factSweepLast     = map[string]time.Time{}
)

// maybeSweepFacts launches an async, single-flight, cooldown-limited prune of a
// namespace that has grown past the sweep threshold. Best-effort and off the write
// path: a sweep never blocks or fails the save that triggered it. Requires a
// worker chat to judge what to prune.
func maybeSweepFacts(db Database, namespace string, chat FactChatFunc) {
	if db == nil || chat == nil {
		return
	}
	factSweepMu.Lock()
	if factSweepInFlight[namespace] || time.Since(factSweepLast[namespace]) < factSweepCooldown {
		factSweepMu.Unlock()
		return
	}
	factSweepInFlight[namespace] = true
	factSweepLast[namespace] = time.Now() // measured from sweep start
	factSweepMu.Unlock()
	go func() {
		defer func() {
			factSweepMu.Lock()
			delete(factSweepInFlight, namespace)
			factSweepMu.Unlock()
			if r := recover(); r != nil {
				Debug("[factstore] sweep panic (ns=%s): %v", namespace, r)
			}
		}()
		sweepFacts(db, namespace, chat)
	}()
}

// sweepFacts asks the worker which facts are junk and which cluster into one,
// applies both, then enforces the hard cap. All index-to-ID resolution happens up
// front so mutations don't shift the indices the judge referenced.
func sweepFacts(db Database, namespace string, chat FactChatFunc) {
	facts := ListMemoryFacts(db, namespace)
	if len(facts) == 0 {
		return
	}
	drop, merges := judgeFactSweep(chat, facts)
	removed, mergedGroups := 0, 0
	// Merges: tombstone the sources as RetireMerged FIRST so they leave the live
	// set (which keeps the combined note from deduping against a source about to
	// be gone), then store the combined note (plain policy so it can't recurse
	// into another sweep) and point each source's Successor at it — so a later
	// recall for a merged source can follow the pointer to the surviving note.
	for _, m := range merges {
		text := strings.TrimSpace(m.Keep)
		if text == "" || len(m.Replace) < 2 {
			continue
		}
		var srcs []MemoryFact
		for _, n := range m.Replace {
			if n >= 1 && n <= len(facts) {
				srcs = append(srcs, facts[n-1])
			}
		}
		if len(srcs) < 2 {
			continue
		}
		now := time.Now()
		for _, s := range srcs {
			retireFact(db, s, RetireMerged, "", now) // successor backfilled once we have the combined ID
			removed++
		}
		res := StoreMemoryFactP(db, namespace, text, FactWritePolicy{})
		// FactDuplicate counts too: the combined text deduped against an
		// existing live fact, so THAT fact is the survivor the sources merged
		// into — leaving Successor empty would strand the tombstones with a
		// pointer recall can't follow.
		if res.Reason == FactStored || res.Reason == FactDuplicate {
			for _, s := range srcs {
				retireFact(db, s, RetireMerged, res.Fact.ID, now)
			}
		}
		mergedGroups++
	}
	// Drops are clear junk: hard-delete, no tombstone. A junk note has nothing a
	// future recall would want to explain, so it earns no history record.
	for _, n := range drop {
		if n >= 1 && n <= len(facts) {
			if ForgetMemoryFactByID(db, namespace, facts[n-1].ID) {
				removed++
			}
		}
	}
	if removed > 0 || mergedGroups > 0 {
		Log("[factstore] sweep on %s: removed %d fact(s), merged %d cluster(s)", namespace, removed, mergedGroups)
	}
	enforceFactHardCap(db, namespace)
	enforceTombstoneRetention(db, namespace)
}

// factMerge is one cluster the sweep collapses: Replace holds the 1-based indices
// to remove, Keep the combined note that stands in for them.
type factMerge struct {
	Keep    string `json:"keep"`
	Replace []int  `json:"replace"`
}

// judgeFactSweep asks the worker to prune a large fact list CONSERVATIVELY. Best-
// effort: any error or unparseable reply yields no changes, so a sweep can only
// ever remove what the model explicitly named.
func judgeFactSweep(chat FactChatFunc, facts []MemoryFact) (drop []int, merges []factMerge) {
	if chat == nil || len(facts) == 0 {
		return nil, nil
	}
	var list strings.Builder
	for i, f := range facts {
		fmt.Fprintf(&list, "%d. %s\n", i+1, f.Note)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := chat(ctx, []Message{
		{Role: "user", Content: fmt.Sprintf(`Below is a numbered list of short memory facts kept about a user and injected into every prompt. The list has grown large. Prune it CONSERVATIVELY.

- drop: numbers of facts that are clearly worthless to keep - empty, meaningless, or transient one-offs that will never matter again. Do NOT drop a fact just because it is specific or old; only clear junk.
- merge: groups of facts that state the SAME thing in different words, or that read naturally as a single fact. For each group give the combined replacement text and the two-or-more numbers it replaces.

Keep anything you are unsure about. Deleting a real fact is far worse than leaving a redundant one.

FACTS:
%s
Reply with ONLY JSON: {"drop": [numbers], "merge": [{"keep": "combined text", "replace": [numbers]}]}. Use empty arrays when there is nothing to do.`, list.String())},
	}, WithSystemPrompt("You conservatively prune a user's long-term memory: remove only clear junk, merge only clear restatements, keep everything else. Reply with ONLY the requested JSON object."),
		WithThink(false),
		WithMaxTokens(512))
	if err != nil || resp == nil {
		return nil, nil
	}
	var parsed struct {
		Drop  []int       `json:"drop"`
		Merge []factMerge `json:"merge"`
	}
	if DecodeJSON(ResponseText(resp), &parsed) != nil {
		return nil, nil
	}
	return parsed.Drop, parsed.Merge
}

// enforceFactHardCap evicts least-recently-updated facts until the namespace holds
// at most TunableFactHardCap of them. The backstop that guarantees bounded prompt
// growth even if the sweep judge is timid. No-op when the cap is 0 (disabled).
func enforceFactHardCap(db Database, namespace string) {
	limit := TuneInt(TunableFactHardCap)
	if limit <= 0 {
		return
	}
	facts := ListMemoryFacts(db, namespace)
	if len(facts) <= limit {
		return
	}
	// Updated falls back to Created for facts saved before Updated existed.
	stamp := func(f MemoryFact) time.Time {
		if !f.Updated.IsZero() {
			return f.Updated
		}
		return f.Created
	}
	sort.Slice(facts, func(i, j int) bool { return stamp(facts[i]).Before(stamp(facts[j])) })
	now := time.Now()
	evicted := 0
	for i := 0; i < len(facts)-limit; i++ {
		// Tombstone rather than hard-delete: an evicted fact was real, just crowded
		// out for space, so it carries the highest false-negative risk. Keeping a
		// record lets recall answer "you had X; it was dropped on <date>" instead of
		// the model silently answering from a stale prior. Retention prunes it later.
		retireFact(db, facts[i], RetireEvicted, "", now)
		evicted++
	}
	if evicted > 0 {
		Log("[factstore] hard cap evicted %d least-recently-updated fact(s) from %s (limit %d)", evicted, namespace, limit)
	}
}

// enforceTombstoneRetention permanently deletes retired facts whose RetiredAt is
// older than the retention window (TunableFactTombstoneDays), bounding the history
// set so tombstones can't defeat the live hard cap they came from. A window of 0
// deletes every retired fact, collapsing retirement back to a plain delete. Runs
// inside the async sweep, off the write path.
func enforceTombstoneRetention(db Database, namespace string) {
	namespace = strings.TrimSpace(namespace)
	if db == nil || namespace == "" {
		return
	}
	days := TuneInt(TunableFactTombstoneDays)
	cutoff := time.Now().AddDate(0, 0, -days)
	prefix := namespace + "/"
	pruned := 0
	for _, k := range db.Keys(MemoryFactsTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var f MemoryFact
		if !db.Get(MemoryFactsTable, k, &f) || !f.Retired() {
			continue
		}
		if days <= 0 || f.RetiredAt.Before(cutoff) {
			db.Unset(MemoryFactsTable, k)
			pruned++
		}
	}
	if pruned > 0 {
		Debug("[factstore] tombstone retention pruned %d retired fact(s) from %s (window %dd)", pruned, namespace, days)
	}
}

// ListRetiredFacts returns the namespace's retired (tombstoned) facts, most
// recently retired first. Excluded from the live ListMemoryFacts view but kept
// for the retention window so recall can explain a hole ("you had X; it was
// <reason> on <date>") when a live query finds nothing. Empty when none.
func ListRetiredFacts(db Database, namespace string) []MemoryFact {
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
		if db.Get(MemoryFactsTable, k, &f) && f.Retired() {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RetiredAt.After(out[j].RetiredAt) })
	return out
}

// SearchRetiredFacts returns up to k retired (tombstoned) facts relevant to a
// free-form query — the lookup behind "you previously stored X; it was dropped
// on <date>". Semantic-first, like SearchMemoryFacts: tombstones keep their
// save-time Vector, so a natural-language question ("where does the user
// work?") finds a retired "works at Acme" that no substring test would.
// Falls back to significant-term overlap when embeddings are off or a
// tombstone predates vectors — NOT full-query substring, which a multi-word
// question essentially never satisfies. Empty when nothing relevant.
func SearchRetiredFacts(db Database, namespace, query string, k int) []MemoryFact {
	q := strings.TrimSpace(query)
	if q == "" || k <= 0 {
		return nil
	}
	retired := ListRetiredFacts(db, namespace) // newest-retired first
	if len(retired) == 0 {
		return nil
	}
	var out []MemoryFact
	matched := map[string]bool{}
	if cfg := GetEmbeddingConfig(); cfg.Enabled {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if qVec, err := Embed(ctx, q); err == nil && len(qVec) > 0 {
			type scored struct {
				fact  MemoryFact
				score float32
			}
			var ranked []scored
			for _, f := range retired {
				// No lazy backfill here (factVector would resurrect embed cost on
				// dead rows) — vectorless legacy tombstones fall to the term tier.
				if len(f.Vector) != len(qVec) {
					continue
				}
				if s := Cosine(qVec, f.Vector); s >= factSearchMinScore {
					ranked = append(ranked, scored{f, s})
				}
			}
			sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
			for _, r := range ranked {
				if len(out) >= k {
					return out
				}
				out = append(out, r.fact)
				matched[r.fact.ID] = true
			}
		}
	}
	// Term-overlap tier: covers embeddings-off and vectorless tombstones. A
	// note qualifies when it contains at least half of the query's significant
	// terms (substring containment, so "work" matches "works").
	terms := significantQueryTerms(q)
	if len(terms) == 0 {
		return out
	}
	need := (len(terms) + 1) / 2
	for _, f := range retired {
		if len(out) >= k {
			break
		}
		if matched[f.ID] {
			continue
		}
		note := strings.ToLower(f.Note)
		hits := 0
		for _, t := range terms {
			if strings.Contains(note, t) {
				hits++
			}
		}
		if hits >= need {
			out = append(out, f)
		}
	}
	return out
}

// factQueryStopwords are filler words dropped from a recall query before term
// matching — question scaffolding that would otherwise count as "overlap"
// with nearly any note.
var factQueryStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true, "was": true,
	"were": true, "be": true, "do": true, "does": true, "did": true,
	"what": true, "whats": true, "where": true, "who": true, "whom": true,
	"when": true, "how": true, "why": true, "which": true, "of": true,
	"for": true, "to": true, "in": true, "on": true, "at": true, "my": true,
	"your": true, "their": true, "his": true, "her": true, "its": true,
	"it": true, "i": true, "you": true, "and": true, "or": true,
	"about": true, "with": true, "have": true, "has": true, "had": true,
}

// significantQueryTerms lowercases a query and returns its content-bearing
// terms: 3+ characters, stopwords dropped, deduped, order preserved.
func significantQueryTerms(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(t) < 3 || factQueryStopwords[t] || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// ForgetMemoryFactByIndex removes the fact at the given 1-based
// index in the namespace's oldest-first list. Returns the removed
// fact + a boolean for success. Used by LLM-facing forget tools
// where the model picks a row from the listed view.
func ForgetMemoryFactByIndex(db Database, namespace string, index int) (MemoryFact, bool) {
	f, _, ok := ForgetMemoryFactByIndexQuoted(db, namespace, index, "")
	return f, ok
}

// ForgetMemoryFactByIndexQuoted is the race-safe form of forget-by-index. The
// numbered list an LLM reads in its prompt can shift between render and the
// forget call — a save in the same turn can trigger supersession or a sweep,
// removing or merging rows — and a bare index then deletes the WRONG note,
// irrecoverably (forget is a hard delete). quote is a verbatim snippet of the
// note the caller intends to drop:
//   - index resolves and the note contains quote (case-insensitive) → delete.
//   - mismatch (or index out of range) but exactly ONE live note contains
//     quote → the list shifted; delete that note instead (self-heals).
//   - otherwise → (zero, reason, false): nothing deleted, reason says why.
//
// Empty quote skips verification (legacy behavior — the index alone is
// trusted, wrong-note risk and all).
func ForgetMemoryFactByIndexQuoted(db Database, namespace string, index int, quote string) (MemoryFact, string, bool) {
	namespace = strings.TrimSpace(namespace)
	if db == nil || namespace == "" || index < 1 {
		return MemoryFact{}, "invalid index", false
	}
	quote = strings.TrimSpace(quote)
	facts := ListMemoryFacts(db, namespace)
	containsQuote := func(f MemoryFact) bool {
		return strings.Contains(strings.ToLower(f.Note), strings.ToLower(quote))
	}
	if index <= len(facts) {
		target := facts[index-1]
		if quote == "" || containsQuote(target) {
			db.Unset(MemoryFactsTable, factDBKey(namespace, target.ID))
			return target, "", true
		}
	} else if quote == "" {
		return MemoryFact{}, "index out of range", false
	}
	// Index and quote disagree (or the index ran off the end): the list has
	// changed since the caller read it. A unique quote match still identifies
	// the intended note.
	var match *MemoryFact
	for i := range facts {
		if containsQuote(facts[i]) {
			if match != nil {
				return MemoryFact{}, "the note list has changed and the quote matches more than one note — search the notes and retry with a more specific quote", false
			}
			match = &facts[i]
		}
	}
	if match == nil {
		return MemoryFact{}, "the note list has changed and no note contains that quote — it may already be gone; search the notes to confirm", false
	}
	target := *match
	db.Unset(MemoryFactsTable, factDBKey(namespace, target.ID))
	return target, "", true
}

// GetMemoryFactByID returns one fact (live or retired) by ID and whether it was
// found. Used to resolve a tombstone's Successor pointer when recall explains a
// hole ("merged into: <current note>").
func GetMemoryFactByID(db Database, namespace, id string) (MemoryFact, bool) {
	namespace = strings.TrimSpace(namespace)
	id = strings.TrimSpace(id)
	if db == nil || namespace == "" || id == "" {
		return MemoryFact{}, false
	}
	var f MemoryFact
	if db.Get(MemoryFactsTable, factDBKey(namespace, id), &f) {
		return f, true
	}
	return MemoryFact{}, false
}

// WipeMemoryFactNamespace removes EVERY fact row under the namespace — live
// and tombstoned alike. The scope-teardown primitive (an agent is deleted, a
// seed's customized shadow is reverted): unlike the forget paths, history is
// deliberately not retained — the owning scope is gone, so tombstones would
// just be orphaned rows nothing can ever query. Returns rows removed.
func WipeMemoryFactNamespace(db Database, namespace string) int {
	namespace = strings.TrimSpace(namespace)
	if db == nil || namespace == "" {
		return 0
	}
	prefix := namespace + "/"
	removed := 0
	for _, k := range db.Keys(MemoryFactsTable) {
		if strings.HasPrefix(k, prefix) {
			db.Unset(MemoryFactsTable, k)
			removed++
		}
	}
	return removed
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
			// Retired facts (superseded, evicted, merged) stay in the table for
			// history but never inject, dedup, or get indexed for forget — they
			// have left the live set by definition.
			if f.Retired() {
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
			now := time.Now()
			strength := RecencyWeight()
			var ranked []scored
			for _, f := range all {
				fVec := factVector(ctx, db, f) // cached, backfilled if legacy
				if len(fVec) != len(qVec) {
					continue
				}
				// Floor on the RAW semantic score (recency must not drop a
				// relevant hit below the floor), but rank on the recency-adjusted
				// score so a fresher fact outranks an equally-relevant stale one.
				if s := Cosine(qVec, fVec); s >= factSearchMinScore {
					ranked = append(ranked, scored{f, s * float32(f.RecencyMultiplier(now, strength))})
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
		b.WriteString(factProvenanceMarker(f))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// factProvenanceMarker returns a STABLE provenance suffix for a non-stable fact:
// its volatility class plus the absolute AsOf date. Absolute (not "N days ago")
// so the always-in-prompt block doesn't churn the prompt cache as the fact ages;
// the model judges freshness against today's date (carried on the user turn).
// Empty for stable facts and facts without an AsOf, so the common case is clean.
func factProvenanceMarker(f MemoryFact) string {
	if f.AsOf.IsZero() {
		return ""
	}
	switch f.Volatility {
	case VolVolatile:
		return " (volatile, as of " + f.AsOf.Format("2006-01-02") + "; verify if relied on)"
	case VolSlow:
		return " (may change, as of " + f.AsOf.Format("2006-01-02") + ")"
	default:
		return ""
	}
}

// FactStalenessNote returns a relative-time staleness hint for PULL surfaces
// (recall / search), or "" when the fact is Fresh. Relative time is fine here
// because pull results are not part of the cached always-in-prompt block.
func FactStalenessNote(f MemoryFact, now time.Time) string {
	switch f.Staleness(now) {
	case Stale:
		return fmt.Sprintf(" (STALE: last confirmed ~%dd ago, re-verify before relying)", factAgeDays(f.AsOf, now))
	case Aging:
		return fmt.Sprintf(" (aging: last confirmed ~%dd ago)", factAgeDays(f.AsOf, now))
	default:
		return ""
	}
}

// SourcedFactCorpus returns the notes of facts whose Source makes them a
// legitimate grounding source — user_stated (a human entered it) or retrieved
// (pulled from a tool at save time) — joined by newlines, for feeding the
// grounding gate's "sourced" corpus. Observed and inferred facts are excluded:
// an LLM-recorded note is ambiguous (it may be a stale prior, like the "$1,600
// 5090"), so it must not license a specific the model couldn't otherwise source.
// Empty when no fact qualifies, so callers can append it unconditionally.
func SourcedFactCorpus(facts []MemoryFact) string {
	var b strings.Builder
	for _, f := range facts {
		if f.Source == MemSourceUserStated || f.Source == MemSourceRetrieved {
			b.WriteString(f.Note)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// factAgeDays is the whole-day age of a timestamp at now, floored at 0.
func factAgeDays(t, now time.Time) int {
	if t.IsZero() {
		return 0
	}
	if d := int(now.Sub(t).Hours()) / 24; d > 0 {
		return d
	}
	return 0
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
	migrateSupersededTombstones(db)
}

// migrateSupersededTombstones converts rows written under the old supersession
// shape ({superseded_at, superseded_by}) to the unified retirement envelope
// ({reason: RetireSuperseded, retired_at, successor}). Runs at startup before
// serving (called from MigrateLegacyFactStore). MUST run before any live read:
// the retired MemoryFact no longer carries a superseded_at field, so an
// unconverted old row would decode with Reason==RetireLive and resurrect into
// the live set. Idempotent — a converted row has no superseded_at on re-scan and
// is skipped.
func migrateSupersededTombstones(db Database) {
	if db == nil {
		return
	}
	// Permissive probe. kvlite serializes with gob, which matches struct fields by
	// NAME and errors ("no fields matched") if a decoder shares none with the
	// stored row — and gob nests the embedded MemoryProvenance, so a converted row
	// has no TOP-LEVEL retirement fields to read. Including Namespace + ID (present
	// on every row, old and new) guarantees a match so the decode never fatals; the
	// legacy-only SupersededAt then distinguishes an unconverted row (it is absent,
	// hence zero, on new-shape rows).
	type probe struct {
		Namespace    string
		ID           string
		SupersededAt time.Time
		SupersededBy string
	}
	converted := 0
	for _, k := range db.Keys(MemoryFactsTable) {
		var p probe
		if !db.Get(MemoryFactsTable, k, &p) {
			continue
		}
		// Legacy superseded row only: the old top-level stamp is set. New-shape rows
		// (retirement nested under MemoryProvenance) decode with a zero SupersededAt.
		if p.SupersededAt.IsZero() {
			continue
		}
		var f MemoryFact
		if !db.Get(MemoryFactsTable, k, &f) {
			continue
		}
		f.Reason = RetireSuperseded
		f.RetiredAt = p.SupersededAt
		f.Successor = p.SupersededBy
		db.Set(MemoryFactsTable, factDBKey(f.Namespace, f.ID), f)
		converted++
	}
	if converted > 0 {
		Log("[factstore] converted %d legacy superseded fact(s) to retirement tombstones", converted)
	}
}
