// Operator-set tunables — a registry of formerly-hardcoded knobs the admin UI
// exposes (Site Settings → generated category sections). Each knob registers a
// TunableSpec (usually from an init() co-located with the value it replaces)
// and is read through a typed accessor (TuneInt / TuneFloat / TuneDuration).
// Stored in the main DB's WebTable; the admin handlers write them and call
// InvalidateTunables.
//
// Registry-driven on purpose: adding a knob is one RegisterTunable call plus
// swapping the const for the accessor at its use site — the admin form,
// GET/UPDATE/reset, and validation all derive from the registry, so the admin
// surface never grows by hand.
//
// Deliberately curated: only operator-meaningful knobs register here. Internal
// quality thresholds (memory dedup similarity, supersession band, hybrid score
// weights, temporal-decay halflife, agent-loop state machine) stay code
// constants — an operator can't judge a good value and a bad one silently
// corrupts behavior.
//
// Values are cached after first read (the main DB can sit on NFS, so a Get per
// call would add latency); the cache is cleared on any admin write.

package core

import (
	"sort"
	"sync"
	"time"
)

// TunableKind sets a knob's type and, for durations, its unit (so an operator
// enters "30" days, not 2592000 seconds).
type TunableKind int

const (
	KindInt     TunableKind = iota // plain integer
	KindFloat                      // float (uses Decimals for display)
	KindSeconds                    // duration, entered in seconds
	KindMinutes                    // duration, entered in minutes
	KindHours                      // duration, entered in hours
	KindDays                       // duration, entered in days
	KindBool                       // on/off flag; stored as 0/1, rendered as a toggle
)

// TunableSpec describes one operator knob: its storage key, the admin category
// it groups under, display copy, type/unit, default, and validation bounds.
type TunableSpec struct {
	Key      string
	Category string // admin section this knob groups under ("Retrieval", "Timeouts", "Limits", "Cache")
	Label    string
	Help     string
	Kind     TunableKind
	Default  float64 // numeric default in the knob's natural unit
	Min      float64
	Max      float64
	Decimals int // float display precision (KindFloat only)
}

var (
	tunablesMu  sync.RWMutex
	tunablesDB  Database
	tunSpecs    = map[string]TunableSpec{}
	tunRegOrder []string // registration order, for stable display
	tunCache    = map[string]float64{}
)

// RegisterTunable adds a knob to the registry. Call once per knob, typically
// from an init() in the file that owns the value it replaces. A duplicate key
// overwrites (last registration wins) so a knob is defined exactly once.
func RegisterTunable(spec TunableSpec) {
	if spec.Key == "" {
		return
	}
	tunablesMu.Lock()
	if _, exists := tunSpecs[spec.Key]; !exists {
		tunRegOrder = append(tunRegOrder, spec.Key)
	}
	tunSpecs[spec.Key] = spec
	delete(tunCache, spec.Key)
	tunablesMu.Unlock()
}

// SetTunablesDB wires the DB that holds the tunables (WebTable). Call once at
// startup with the main DB. A nil DB leaves every getter on its spec default.
func SetTunablesDB(db Database) {
	tunablesMu.Lock()
	tunablesDB = db
	tunCache = map[string]float64{}
	tunablesMu.Unlock()
}

// InvalidateTunables clears the value cache so the next read reflects a
// just-written value. The admin update handler calls this after a change.
func InvalidateTunables() {
	tunablesMu.Lock()
	tunCache = map[string]float64{}
	tunablesMu.Unlock()
}

// tuneValue resolves a knob's effective numeric value (stored override or spec
// default), cached. Unknown keys return 0.
func tuneValue(key string) float64 {
	tunablesMu.RLock()
	if v, ok := tunCache[key]; ok {
		tunablesMu.RUnlock()
		return v
	}
	spec, known := tunSpecs[key]
	db := tunablesDB
	tunablesMu.RUnlock()
	val := spec.Default // 0 for an unknown key
	if known && db != nil {
		var v float64
		if db.Get(WebTable, key, &v) && (v != 0 || spec.Min <= 0) {
			// Accept a stored 0 only when 0 is a legal value for this knob
			// (Min <= 0, e.g. "off"); otherwise treat 0 as unset → default.
			val = v
		}
	}
	tunablesMu.Lock()
	tunCache[key] = val
	tunablesMu.Unlock()
	return val
}

// TuneInt returns an integer knob's effective value.
func TuneInt(key string) int { return int(tuneValue(key)) }

// TuneBool returns a KindBool knob's effective value (any nonzero stored value
// is true). A stored 0 persists as false because a bool's Min is 0, so
// tuneValue accepts the stored zero rather than falling back to the default.
func TuneBool(key string) bool { return tuneValue(key) != 0 }

// TuneFloat returns a float knob's effective value.
func TuneFloat(key string) float64 { return tuneValue(key) }

// TuneDuration returns a duration knob as a time.Duration, converting from the
// spec's unit (seconds / minutes / hours / days).
func TuneDuration(key string) time.Duration {
	tunablesMu.RLock()
	kind := tunSpecs[key].Kind
	tunablesMu.RUnlock()
	v := tuneValue(key)
	var unit time.Duration
	switch kind {
	case KindSeconds:
		unit = time.Second
	case KindMinutes:
		unit = time.Minute
	case KindHours:
		unit = time.Hour
	case KindDays:
		unit = 24 * time.Hour
	default:
		unit = time.Second
	}
	return time.Duration(v * float64(unit))
}

// --- admin-facing introspection (registry is the single source of truth) ---

// AllTunableSpecs returns every registered spec, ordered by category (first
// appearance) then registration order within the category.
func AllTunableSpecs() []TunableSpec {
	tunablesMu.RLock()
	defer tunablesMu.RUnlock()
	catOrder := map[string]int{}
	var cats []string
	out := make([]TunableSpec, 0, len(tunRegOrder))
	for _, k := range tunRegOrder {
		s := tunSpecs[k]
		if _, seen := catOrder[s.Category]; !seen {
			catOrder[s.Category] = len(cats)
			cats = append(cats, s.Category)
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return catOrder[out[i].Category] < catOrder[out[j].Category]
	})
	return out
}

// TunableEffectiveValue returns a knob's current effective numeric value (for
// the admin GET, which shows what's actually in force).
func TunableEffectiveValue(key string) float64 { return tuneValue(key) }

// LookupTunable returns a spec by key.
func LookupTunable(key string) (TunableSpec, bool) {
	tunablesMu.RLock()
	defer tunablesMu.RUnlock()
	s, ok := tunSpecs[key]
	return s, ok
}

// ResetTunables clears every registered knob's stored override so the getters
// fall back to their spec defaults, then invalidates the cache.
func ResetTunables(db Database) {
	tunablesMu.RLock()
	keys := make([]string, len(tunRegOrder))
	copy(keys, tunRegOrder)
	tunablesMu.RUnlock()
	if db != nil {
		for _, k := range keys {
			db.Unset(WebTable, k)
		}
	}
	InvalidateTunables()
}

// --- Retrieval knobs (the first batch; registered here, co-located with the
// accessor names the orchestrate recall code already calls). ---

const (
	TunableKnowledgeTopK  = "tune_knowledge_topk"
	TunableKnowledgeMaxK  = "tune_knowledge_max_k"
	TunableReferenceK     = "tune_reference_k"
	TunableRecallMinScore = "tune_recall_min"
	TunableChunkChars     = "tune_chunk_chars"
	TunableLLMMaxRetries  = "tune_llm_retries"
)

func init() {
	RegisterTunable(TunableSpec{Key: TunableKnowledgeTopK, Category: "Retrieval", Label: "Knowledge recall top-k (default)",
		Help: "Passages pulled per knowledge_search when the agent doesn't specify one.", Kind: KindInt, Default: 5, Min: 1, Max: 100})
	RegisterTunable(TunableSpec{Key: TunableKnowledgeMaxK, Category: "Retrieval", Label: "Knowledge recall max-k (ceiling)",
		Help: "Largest k an agent may request per search.", Kind: KindInt, Default: 20, Min: 1, Max: 200})
	RegisterTunable(TunableSpec{Key: TunableReferenceK, Category: "Retrieval", Label: "Reference-memory recall k",
		Help: "Default passages for memory_search (reference memory).", Kind: KindInt, Default: 5, Min: 1, Max: 100})
	RegisterTunable(TunableSpec{Key: TunableRecallMinScore, Category: "Retrieval", Label: "Recall min score (0 = off)",
		Help: "Cosine floor below which a recall hit is dropped. 0 keeps every top-k hit; raise to trade recall for precision.", Kind: KindFloat, Default: 0, Min: 0, Max: 1, Decimals: 2})
	RegisterTunable(TunableSpec{Key: TunableRecencyWeight, Category: "Retrieval", Label: "Recency weight (0 = off)",
		Help: "How much recall down-weights aged facts/findings vs their semantic score. 0 = pure semantic (age ignored); 1 = full bite. Never drops a hit — a stale one is scaled by at least (1-weight). Reuses the fact staleness half-lives; stable facts and curated knowledge never decay.", Kind: KindFloat, Default: 0.3, Min: 0, Max: 1, Decimals: 2})
	RegisterTunable(TunableSpec{Key: TunableChunkChars, Category: "Retrieval", Label: "Embedding chunk size (chars)",
		Help: "Max characters per embedded chunk. Applies to NEW ingestions only — existing documents keep their chunking until re-ingested.", Kind: KindInt, Default: 1000, Min: 200, Max: 8000})
	RegisterTunable(TunableSpec{Key: TunableLLMMaxRetries, Category: "Limits", Label: "LLM retry attempts",
		Help: "Retries for a failed LLM call before giving up (per-call override still applies).", Kind: KindInt, Default: 5, Min: 0, Max: 20})
}

// Retrieval accessors — keep the names the orchestrate recall code already
// calls; bodies now resolve through the registry.

func KnowledgeTopK() int      { return TuneInt(TunableKnowledgeTopK) }
func KnowledgeMaxK() int      { return TuneInt(TunableKnowledgeMaxK) }
func ReferenceRecallK() int   { return TuneInt(TunableReferenceK) }
func RecallMinScore() float64 { return TuneFloat(TunableRecallMinScore) }
func ChunkChars() int         { return TuneInt(TunableChunkChars) }
func LLMMaxRetries() int      { return TuneInt(TunableLLMMaxRetries) }
