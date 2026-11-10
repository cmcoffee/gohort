package checktest

// The judge map: which checks the Judge model and System One have earned.
//
// A check goes to the Judge model, or is first asked of System One, only when
// its cases all came out right there (core.JudgeMapGate, set here): on the
// model that tier is now, for the check as it is now, in mapRuns runs in a
// row. Anything else stays on the worker. A switch could hand a check to a
// model that had never been asked its cases; the map cannot.
//
// What the mapping was earned on is kept with it, so it lapses by itself when
// that changes: the Judge model or System One's choice changes (their
// identity), or the check does (its fingerprint, the prompts it sends for its
// cases). Missing lists what has lapsed or was never run, for a run to map.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/decide"
)

// mapTable is where the map is kept, one Mapping per check key.
const mapTable = "judge_map"

// mapRuns is how many runs in a row a tier must get every case right in to
// be given a check: one clean run can be luck (the judge went 15, 14, 15 of
// the warden's 17 in three runs).
const mapRuns = 2

// TierWorker is the worker, the tier every check falls back to. Its results
// are kept beside the others for comparison; nothing is mapped to it.
const TierWorker = "worker"

// TierResult is how one tier did on one check, on one model.
type TierResult struct {
	// Model is the identity it was run on (TierModel).
	Model string `json:"model"`
	// Clean is how many runs in a row got every case right; 0 after a miss.
	Clean int `json:"clean"`
	// Cases is how many cases the last run asked; Missed names the ones it
	// got wrong (System One's unsure answers are not misses: live, the full
	// answer is asked for them).
	Cases  int      `json:"cases"`
	Missed []string `json:"missed,omitempty"`
	// MS is the median time a case took on the last run.
	MS int64     `json:"ms"`
	At time.Time `json:"at"`
}

// mapped reports whether the result gives the check to its tier on model.
func (r TierResult) mapped(model string) bool {
	return model != "" && r.Model == model && r.Clean >= mapRuns
}

// Mapping is one check's place on the map: the fingerprint it was tested at,
// and each tier's result.
type Mapping struct {
	Key         string                `json:"key"`
	Fingerprint string                `json:"fingerprint"`
	Tiers       map[string]TierResult `json:"tiers"`
}

var store struct {
	mu     sync.RWMutex
	loaded bool
	m      map[string]Mapping
}

func init() { JudgeMapGate = mappedNow }

// load reads the map once a database is there.
func load() {
	store.mu.RLock()
	done := store.loaded
	store.mu.RUnlock()
	if done || RootDB == nil {
		return
	}
	m := map[string]Mapping{}
	for _, k := range RootDB.Keys(mapTable) {
		var e Mapping
		if RootDB.Get(mapTable, k, &e) {
			m[k] = e
		}
	}
	store.mu.Lock()
	if !store.loaded {
		store.m, store.loaded = m, true
	}
	store.mu.Unlock()
}

// MappingFor is check key's place on the map, ok false when it has none.
func MappingFor(key string) (Mapping, bool) {
	load()
	store.mu.RLock()
	defer store.mu.RUnlock()
	e, ok := store.m[key]
	return e, ok
}

// saveMapping keeps e.
func saveMapping(e Mapping) {
	load()
	store.mu.Lock()
	if store.m == nil {
		store.m = map[string]Mapping{}
	}
	store.m[e.Key] = e
	store.mu.Unlock()
	if RootDB != nil {
		RootDB.Set(mapTable, e.Key, e)
	}
}

// TierModel is the model a tier is now, "" when it has none: the worker
// always; the Judge model as it is running; System One's choice, by what is
// behind it.
func TierModel(tier string) string {
	switch tier {
	case TierWorker:
		return TierWorker
	case MapJudge:
		if JudgeLLM() == nil {
			return ""
		}
		live, _, _ := LiveJudgeLLM()
		return "judge:" + live
	case MapSystemOne:
		if decide.Model() == nil {
			return ""
		}
		return decide.Identity(decide.Chosen())
	}
	return ""
}

// mappedNow is core.JudgeMapGate: check key is mapped to tier when its
// mapping was earned on the check as it is now and on the model the tier is
// now.
func mappedNow(key, tier string) bool { return mapGateFor(key, tier, TierModel(tier)) }

// mapGateFor is mappedNow with the tier's model given.
func mapGateFor(key, tier, model string) bool {
	e, ok := MappingFor(key)
	if !ok {
		return false
	}
	ch, ok := checkFor(key)
	// While its fingerprint is being taken, the check's own code is running
	// against a stand-in: it is not mapped to anything then.
	if !ok || !ch.Structured || e.Fingerprint == "" || beingTaken(key) || e.Fingerprint != Fingerprint(ch) {
		return false
	}
	return e.Tiers[tier].mapped(model)
}

// Mapped reports whether check key is mapped to tier now.
func Mapped(key, tier string) bool { return mappedNow(key, tier) }

// checkFor is the registered check with key.
func checkFor(key string) (Check, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	for _, c := range registry.checks {
		if c.Key == key {
			return c, true
		}
	}
	return Check{}, false
}

var prints struct {
	mu sync.Mutex
	m  map[string]string
	// taking are the checks whose fingerprint is being taken. Taking one
	// runs the check's own code, which asks the map whether the check is
	// mapped, which needs its fingerprint: without this, that recursed until
	// the stack ran out and the process died on the first live scan.
	taking map[string]bool
}

// beingTaken reports whether check key's fingerprint is being taken now.
func beingTaken(key string) bool {
	prints.mu.Lock()
	defer prints.mu.Unlock()
	return prints.taking[key]
}

// Fingerprint is a check as it is now: its cases and every prompt it sends
// for them, in full and as a System One decision, recorded from a stand-in
// model that answers nothing. A changed case, rewording, or reply shape
// changes it, and the check's mapping lapses. Worked out once per process
// (the code does not change under it); "" for a check with no cases.
func Fingerprint(c Check) string {
	prints.mu.Lock()
	if fp, ok := prints.m[c.Key]; ok {
		prints.mu.Unlock()
		return fp
	}
	if prints.taking == nil {
		prints.taking = map[string]bool{}
	}
	prints.taking[c.Key] = true
	prints.mu.Unlock()
	fp := fingerprint(c)
	prints.mu.Lock()
	delete(prints.taking, c.Key)
	if prints.m == nil {
		prints.m = map[string]string{}
	}
	prints.m[c.Key] = fp
	prints.mu.Unlock()
	return fp
}

func fingerprint(c Check) string {
	if c.Cases == nil {
		return ""
	}
	cases := c.Cases()
	if len(cases) == 0 {
		return ""
	}
	rec := &recorder{}
	for _, tc := range cases {
		keys := make([]string, 0, len(tc.Input))
		for k := range tc.Input {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		rec.write("case", tc.ID, tc.Want)
		for _, k := range keys {
			rec.write("in", k, tc.Input[k])
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if c.Run != nil {
			rec.write("full")
			c.Run(ctx, rec, tc)
		}
		if c.Decide != nil {
			rec.write("decide")
			c.Decide(ctx, rec, tc)
		}
		cancel()
	}
	return rec.sum()
}

// recorder is the stand-in model a fingerprint is taken with: it writes down
// every call (the options' system prompt and prefix, each message) and
// answers with an empty object, which every check reads as no verdict.
type recorder struct {
	mu    sync.Mutex
	parts []string
}

func (r *recorder) write(parts ...string) {
	r.mu.Lock()
	r.parts = append(r.parts, strings.Join(parts, "\x1f"))
	r.mu.Unlock()
}

func (r *recorder) sum() string {
	h := sha256.New()
	for _, p := range r.parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (r *recorder) Chat(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
	var cfg ChatConfig
	for _, o := range opts {
		o(&cfg)
	}
	r.write("call", cfg.SystemPrompt, cfg.DecidePrefix)
	for _, m := range msgs {
		r.write("msg", m.Role, m.Content)
	}
	return &Response{Content: "{}"}, nil
}

func (r *recorder) ChatStream(ctx context.Context, msgs []Message, _ StreamHandler, opts ...ChatOption) (*Response, error) {
	return r.Chat(ctx, msgs, opts...)
}

// record keeps what a run found: for each check and model whose every case
// the run answered, the tier's result, against the check's fingerprint and
// the model's identity. A clean run adds to the tier's clean count, a miss
// sets it back to 0, and a new fingerprint or model starts the count again.
func record(rep RunReport, models []Model) {
	byCheck := map[string][]Row{}
	for _, r := range rep.Rows {
		byCheck[r.Check] = append(byCheck[r.Check], r)
	}
	for key, rows := range byCheck {
		ch, ok := checkFor(key)
		if !ok {
			continue
		}
		all := len(ch.Cases())
		// Only a structured check is ever mapped, so only its prompts are
		// fingerprinted; a reading's results are kept to show how each
		// model does.
		fp := ""
		if ch.Structured {
			fp = Fingerprint(ch)
		}
		e, _ := MappingFor(key)
		if e.Fingerprint != fp || e.Tiers == nil {
			e = Mapping{Key: key, Fingerprint: fp, Tiers: map[string]TierResult{}}
		}
		changed := false
		for _, m := range models {
			tier := m.tier()
			var took []int64
			var missed []string
			for _, r := range rows {
				a, ok := r.Answers[m.Name]
				if !ok {
					continue
				}
				took = append(took, a.TookMS)
				if !(a.Pass || (tier == MapSystemOne && a.Unsure && a.Err == "")) {
					missed = append(missed, r.Title)
				}
			}
			if len(took) == 0 || len(took) < all {
				continue // not asked, or the run stopped part way through it
			}
			id := m.identity()
			res := e.Tiers[tier]
			if res.Model != id {
				res = TierResult{Model: id}
			}
			if len(missed) == 0 {
				res.Clean++
			} else {
				res.Clean = 0
			}
			res.Cases, res.Missed, res.MS, res.At = len(took), missed, median(took), rep.At
			e.Tiers[tier] = res
			changed = true
			if tier != TierWorker {
				// Every mapping's step in the log, so where a check stands can
				// be read without the page.
				state := fmt.Sprintf("%d clean run(s) of %d", res.Clean, mapRuns)
				switch {
				case !ch.Structured:
					state = "a reading: stays on the worker"
				case res.mapped(id):
					state = "MAPPED"
				case len(missed) > 0:
					state = "missed " + strings.Join(missed, "; ")
				}
				Log("[checktest] map: %s on %s (%s): %s", ch.Label, tier, id, state)
			}
		}
		if changed {
			saveMapping(e)
		}
	}
}

// median of ms, 0 for none.
func median(ms []int64) int64 {
	if len(ms) == 0 {
		return 0
	}
	s := append([]int64(nil), ms...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// Missing are the checks a run should ask of models to bring the map up to
// date: a check with cases whose mapping for a tier among them is from
// another fingerprint or model, was never run, or has clean runs short of
// mapRuns. A tier that missed a case on the check as it is, on the model it
// is, is not missing: asking again would only cost the same miss, until the
// map is refreshed by hand.
func Missing(models []Model) []string {
	var out []string
	for _, ch := range Checks() {
		fp := Fingerprint(ch)
		if fp == "" || !ch.Structured {
			continue
		}
		e, _ := MappingFor(ch.Key)
		for _, m := range models {
			tier := m.tier()
			if tier == TierWorker || (tier == MapSystemOne && ch.Decide == nil) {
				continue
			}
			id := m.identity()
			if id == "" {
				continue
			}
			r := e.Tiers[tier]
			if e.Fingerprint != fp || r.Model != id || (r.Clean > 0 && r.Clean < mapRuns) {
				out = append(out, ch.Key)
				break
			}
		}
	}
	return out
}

// tier is the map tier a model is for: its Tier, or by its name.
func (m Model) tier() string {
	if m.Tier != "" {
		return m.Tier
	}
	if m.Decide {
		return MapSystemOne
	}
	return m.Name
}

// identity is the model's identity for the map: Identity when set (a test),
// else what its tier is now.
func (m Model) identity() string {
	if m.Identity != "" {
		return m.Identity
	}
	return TierModel(m.tier())
}

// Standing is how a check stands for a tier now, in words: mapped, how far
// toward it, what it missed, or why it was never asked.
func Standing(key, tier string) string {
	ch, ok := checkFor(key)
	if !ok || Fingerprint(ch) == "" {
		return "no test cases"
	}
	if !ch.Structured {
		return "a reading, not a structured question: kept on the worker"
	}
	if tier == MapSystemOne && ch.Decide == nil {
		return "no System One form"
	}
	model := TierModel(tier)
	if model == "" {
		return "none set"
	}
	e, _ := MappingFor(key)
	r := e.Tiers[tier]
	switch {
	case e.Fingerprint != Fingerprint(ch):
		if r.Model != "" {
			return "the check changed: not tested as it is yet"
		}
		return "not tested yet"
	case r.Model != model:
		return "not tested on this model yet"
	case r.mapped(model):
		return fmt.Sprintf("all %d cases right, %d runs in a row", r.Cases, r.Clean)
	case r.Clean > 0:
		return fmt.Sprintf("all %d cases right once; one more run to map", r.Cases)
	}
	return fmt.Sprintf("missed %d of %d: %s", len(r.Missed), r.Cases, strings.Join(r.Missed, "; "))
}

// StableFingerprint reports whether c's fingerprint comes out the same taken
// twice, fresh: a prompt with the time or a random id in it would lapse its
// mapping on every look, and the check would never stay mapped.
func StableFingerprint(c Check) bool {
	a := fingerprint(c)
	return a != "" && a == fingerprint(c)
}

// ClearMap forgets every mapping: every check goes back to the worker until
// it is mapped again. It returns how many checks had one.
func ClearMap() int {
	load()
	store.mu.Lock()
	n := len(store.m)
	store.m = map[string]Mapping{}
	store.mu.Unlock()
	if RootDB != nil {
		for _, k := range RootDB.Keys(mapTable) {
			RootDB.Unset(mapTable, k)
		}
	}
	return n
}
