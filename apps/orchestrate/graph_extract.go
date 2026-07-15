// Automatic entity extraction — off-hot-path population of the graph layer.
//
// The graph (link_entities) is the strongest memory layer architecturally but is
// dead weight while it depends on the model deciding to file relationships by
// hand. This runs a conservative worker-LLM extraction over a completed turn's
// user message and writes the subject-relation-object triples it finds straight
// into the graph via the same UpsertGraphEntity (alias-merge) + LinkGraphEdge
// path link_entities uses — so the graph populates itself.
//
// Two hard design constraints, learned the expensive way:
//   - OFF THE HOT PATH. It fires AFTER the turn in its own goroutine, single-
//     flight + cooldown per namespace, so it never blocks the response and
//     self-throttles on the shared GPU (a single card can't run an extraction
//     pass inline every turn).
//   - CONSERVATIVE, so the graph fills CLEAN not fragmented. A keyed entity fact
//     store was reverted once because the LLM picked inconsistent keys; the
//     guards here are (a) extract only EXPLICIT relationships between NAMED
//     entities, never inferred, (b) alias-merge on write (UpsertGraphEntity),
//     (c) never replace — corrections stay the user's explicit call. Extracted
//     edges are stamped Source=observed so a bad batch can be told apart from
//     hand-curated edges and pruned.

package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	. "github.com/cmcoffee/gohort/core"
)

// TunableGraphExtract gates automatic entity extraction. Off by default: it costs
// one worker round-trip per eligible turn (bounded by the cooldown).
const TunableGraphExtract = "tune_graph_extract"

func init() {
	RegisterTunable(TunableSpec{Key: TunableGraphExtract, Category: "Memory",
		Label: "Automatic entity extraction (0 = off)",
		Help:  "After a turn, run a worker-LLM pass over the user's message and auto-populate the graph memory with the entity relationships it states. Off the hot path (background, single-flight + cooldown). Conservative: explicit relationships between named entities only, alias-merged, never auto-replacing. Extracted edges are marked observed.",
		Kind:  KindBool, Default: 0, Min: 0, Max: 1})
}

func graphExtractEnabled() bool { return TuneBool(TunableGraphExtract) }

const (
	// graphExtractMinChars skips messages too short to plausibly state a
	// relationship, so trivial turns ("thanks", "ok") never spend a worker call.
	graphExtractMinChars = 40
	// graphExtractCooldown is the minimum gap between per-turn extractions per
	// namespace. Best-effort population — a turn skipped under the cooldown is not
	// lost: it gets extracted when it folds (extractGraphFromFold, no cooldown).
	graphExtractCooldown = 90 * time.Second
	// foldExtractMaxChars caps the extraction input built from a folded span, so a
	// very long batch doesn't blow the worker prompt.
	foldExtractMaxChars = 6000
)

var (
	graphExtractMu       sync.Mutex
	graphExtractInFlight = map[string]bool{}
	graphExtractLast     = map[string]time.Time{}
	// graphExtractSerial serializes the WRITE half per namespace across both
	// triggers. The per-turn path already single-flights via
	// graphExtractInFlight, but the fold path deliberately has no such drop
	// (a skipped fold's spans would be permanently missed), so rapid folds —
	// or a fold landing beside a per-turn pass — could run UpsertGraphEntity
	// merges concurrently and race aliases away. Serializing (rather than
	// dropping) keeps the fold's completeness guarantee.
	graphExtractSerialMu sync.Mutex
	graphExtractSerial   = map[string]*sync.Mutex{}
)

// graphExtractNSLock returns the per-namespace serialization mutex, minting it
// on first use.
func graphExtractNSLock(namespace string) *sync.Mutex {
	graphExtractSerialMu.Lock()
	defer graphExtractSerialMu.Unlock()
	mu, ok := graphExtractSerial[namespace]
	if !ok {
		mu = &sync.Mutex{}
		graphExtractSerial[namespace] = mu
	}
	return mu
}

// maybeExtractGraph fires a background entity-extraction pass over text, subject
// to the gate, a length floor, and single-flight + cooldown per namespace. It
// returns immediately; the work (a worker call + graph writes) runs in its own
// goroutine so the turn is never blocked. Mirrors maybeSweepFacts.
func maybeExtractGraph(db Database, namespace, text string, chat FactChatFunc) {
	if !graphExtractEnabled() || db == nil || chat == nil {
		return
	}
	text = strings.TrimSpace(text)
	namespace = strings.TrimSpace(namespace)
	if namespace == "" || len(text) < graphExtractMinChars {
		return
	}
	graphExtractMu.Lock()
	if graphExtractInFlight[namespace] || time.Since(graphExtractLast[namespace]) < graphExtractCooldown {
		graphExtractMu.Unlock()
		return
	}
	graphExtractInFlight[namespace] = true
	graphExtractLast[namespace] = time.Now() // measured from pass start
	graphExtractMu.Unlock()
	go func() {
		defer func() {
			graphExtractMu.Lock()
			delete(graphExtractInFlight, namespace)
			graphExtractMu.Unlock()
			if r := recover(); r != nil {
				Debug("[graph-extract] panic (ns=%s): %v", namespace, r)
			}
		}()
		extractGraphFromText(db, namespace, text, chat)
	}()
}

// extractGraphFromFold fires a background extraction over a batch of messages
// folding out of the live window. Unlike the per-turn trigger it has NO cooldown:
// a fold is the batch boundary, so extracting it is what GUARANTEES no turn's
// stated relationships are permanently missed — a turn the per-turn pass skipped
// under its cooldown gets caught here when it folds. Writes are idempotent
// (UpsertGraphEntity merges, LinkGraphEdge keys on the triple), so re-covering a
// turn the per-turn pass already handled costs nothing but a no-op write. Gated,
// best-effort, off the hot path (its own goroutine — the fold itself runs on the
// turn path).
func extractGraphFromFold(db Database, namespace string, folded []Message, chat FactChatFunc) {
	if !graphExtractEnabled() || db == nil || chat == nil {
		return
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return
	}
	text := foldUserText(folded)
	if len(text) < graphExtractMinChars {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				Debug("[graph-extract] fold panic (ns=%s): %v", namespace, r)
			}
		}()
		extractGraphFromText(db, namespace, text, chat)
	}()
}

// foldUserText joins the USER messages of a folded batch — the source of stated
// relationships — into one extraction input, capped at foldExtractMaxChars.
func foldUserText(folded []Message) string {
	var b strings.Builder
	for _, m := range folded {
		if m.Role != "user" {
			continue
		}
		if t := strings.TrimSpace(m.Content); t != "" {
			b.WriteString(t)
			b.WriteByte('\n')
		}
	}
	s := strings.TrimSpace(b.String())
	if len(s) > foldExtractMaxChars {
		// Cut on a rune boundary — a byte slice can split a UTF-8 sequence
		// and hand the worker prompt an invalid trailing byte.
		cut := foldExtractMaxChars
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return s
}

// graphTriple is one extracted subject-relation-object relationship.
type graphTriple struct {
	Subject     string `json:"subject"`
	SubjectKind string `json:"subject_kind"`
	Relation    string `json:"relation"`
	Object      string `json:"object"`
	ObjectKind  string `json:"object_kind"`
}

// extractGraphFromText runs the worker extraction and writes each triple into the
// graph, alias-merging entities and NEVER replacing (extraction is additive; a
// correction is the user's explicit link_entities(replace=true) call). Extracted
// edges are stamped observed. Returns the number of edges written (for tests /
// logging).
func extractGraphFromText(db Database, namespace, text string, chat FactChatFunc) int {
	// One extraction writes at a time per namespace (see graphExtractSerial).
	// Taken here, below the judge call sites' goroutines, so BOTH triggers
	// inherit it. The worker call runs inside the lock — serializing the LLM
	// round-trips is the point (concurrent extractions were the alias race).
	mu := graphExtractNSLock(namespace)
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	prov := MemoryProvenance{Source: MemSourceObserved, AsOf: now}
	written := 0
	for _, tr := range judgeGraphTriples(chat, text) {
		subject := strings.TrimSpace(tr.Subject)
		relation := strings.TrimSpace(tr.Relation)
		object := strings.TrimSpace(tr.Object)
		if subject == "" || relation == "" || object == "" || strings.EqualFold(subject, object) {
			continue
		}
		subj, _ := UpsertGraphEntity(db, namespace, tr.SubjectKind, subject, nil, nil)
		obj, _ := UpsertGraphEntity(db, namespace, tr.ObjectKind, object, nil, nil)
		if subj.ID == "" || obj.ID == "" {
			continue
		}
		LinkGraphEdgeP(db, namespace, subj.ID, relation, obj.ID, "", false, prov)
		written++
	}
	if written > 0 {
		Debug("[graph-extract] wrote %d edge(s) (ns=%s)", written, namespace)
	}
	return written
}

// judgeGraphTriples asks the worker to extract explicit named-entity
// relationships from text as subject-relation-object triples. Worker tier,
// no-think, JSON. Best-effort — nil chat / error / unparseable reply → no
// triples (nothing is written).
func judgeGraphTriples(chat FactChatFunc, text string) []graphTriple {
	if chat == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := chat(ctx, []Message{
		{Role: "user", Content: fmt.Sprintf(`Extract relationships between NAMED entities from the text below, as subject-relation-object triples for a knowledge graph.

Rules:
- Only EXPLICIT relationships the text actually states. Do NOT infer or guess.
- Only NAMED entities — specific people, organizations, places, projects, or named things. Skip generic nouns ("a dog", "the meeting") unless they carry a proper name.
- relation is a short lowercase verb phrase ("works at", "owns", "lives in", "married to", "manages").
- subject_kind / object_kind is one of: person, org, project, place, thing.
- If the text states no such relationship, reply with an empty array.

TEXT:
%s

Reply with ONLY a JSON array of objects, each {"subject","subject_kind","relation","object","object_kind"}. Reply [] if none.`, text)},
	}, WithSystemPrompt("You extract explicit relationships between named entities as subject-relation-object triples for a knowledge graph. Be conservative: never infer, named entities only. Reply with ONLY a JSON array."),
		WithThink(false),
		WithMaxTokens(512))
	if err != nil || resp == nil {
		return nil
	}
	var out []graphTriple
	if DecodeJSON(ResponseText(resp), &out) != nil {
		return nil
	}
	return out
}
