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
	RegisterTunable(TunableSpec{App: "/orchestrate", Key: TunableGraphExtract, Category: "Memory",
		Label:  "Automatic entity extraction (0 = off)",
		Help:   "After a turn, populate the graph memory with the relationships the message states.",
		Detail: "A worker-LLM pass does it off the hot path, in the background, single-flight and with a cooldown. It is conservative: explicit relationships between named entities only, alias-merged, never auto-replacing. Extracted edges are marked observed.",
		Kind:   KindBool, Default: 0, Min: 0, Max: 1})
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

// turnExtractText labels one live turn's two halves for the extractor, the same
// way foldExtractText labels a folded span — so the judge reads one shape
// whether a relationship is caught on the turn or later when it folds.
//
// A nil or empty reply degrades to the user's text alone, which is exactly what
// this path passed before.
func turnExtractText(userSaid string, resp *Response) string {
	userSaid = strings.TrimSpace(userSaid)
	reply := ""
	if resp != nil {
		reply = strings.TrimSpace(resp.Content)
	}
	if reply == "" {
		return userSaid
	}
	return labelledExtractInput(userSaid, reply)
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
	text := foldExtractText(folded)
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

// foldExtractText joins a folded batch into one extraction input: the USER
// messages first, then the ASSISTANT ones, each side labelled.
//
// Assistant text is included because reading only the user's half was reading
// the wrong half. In an assistant that investigates — a troubleshooter, a
// researcher — the user contributes a question and the ENTITIES AND
// RELATIONSHIPS ARE IN THE ANSWER: "node-7 runs the batch service", "that
// template wrote the broken path". The graph stayed empty while the
// conversation was full
// of exactly what it exists to hold, and the tunable read as not working when
// it was working on nothing.
//
// TOOL messages stay out. They are raw capture — log lines, JSON, file dumps —
// where a triple is as likely to come from example output as from a fact about
// this deployment, and they are the bulk of the volume. The assistant's reading
// of a tool result is what belongs here, and that arrives as assistant text.
//
// LABELLED, because who said it decides whether it is a fact. The judge is told
// to take the user's statements as given and to require the assistant to have
// ASSERTED something rather than wondered about it; without the labels it cannot
// tell a finding from a hypothesis, and a graph full of the assistant's guesses
// is worse than an empty one.
//
// The USER SIDE GOES FIRST and the assistant side takes what budget is left.
// One assistant turn can run longer than every user message in a fold combined,
// so appending in message order would let a single answer push the whole
// conversation out of the window. The half that gets truncated is the verbose,
// lower-signal one.
func foldExtractText(folded []Message) string {
	var user, asst strings.Builder
	for _, m := range folded {
		t := strings.TrimSpace(m.Content)
		if t == "" {
			continue
		}
		switch m.Role {
		case "user":
			user.WriteString(t)
			user.WriteByte('\n')
		case "assistant":
			asst.WriteString(t)
			asst.WriteByte('\n')
		}
	}
	u := strings.TrimSpace(user.String())
	a := strings.TrimSpace(asst.String())
	return labelledExtractInput(u, a)
}

// labelledExtractInput assembles the two halves under their labels, within one
// budget.
//
// The budget covers the ASSEMBLED string, labels included — it exists so the
// worker prompt cannot blow up, and a cap that measures only the content is a
// cap the labels walk straight past.
func labelledExtractInput(user, assistant string) string {
	const userLabel, asstLabel = "USER SAID:\n", "\nASSISTANT SAID:\n"
	var b strings.Builder
	if user != "" {
		b.WriteString(userLabel)
		b.WriteString(truncateRunes(user, foldExtractMaxChars-b.Len()))
		b.WriteByte('\n')
	}
	// Only worth a section if what survives could state a relationship at all;
	// graphExtractMinChars is the same floor the callers use to skip a pass.
	if room := foldExtractMaxChars - b.Len() - len(asstLabel); assistant != "" && room > graphExtractMinChars {
		b.WriteString(asstLabel)
		b.WriteString(truncateRunes(assistant, room))
	}
	return strings.TrimSpace(b.String())
}

// truncateRunes cuts s to at most n bytes on a rune boundary — a byte slice can
// split a UTF-8 sequence and hand the worker prompt an invalid trailing byte.
func truncateRunes(s string, n int) string {
	if len(s) <= n || n <= 0 {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
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
- Only NAMED entities: specific people, organizations, places, projects, or named things. Skip generic nouns ("a dog", "the meeting") unless they carry a proper name.
- relation is a short lowercase verb phrase ("works at", "owns", "lives in", "married to", "manages").
- subject_kind / object_kind is one of: person, org, project, place, thing.
- The text may be labelled "USER SAID" and "ASSISTANT SAID". Take what the USER states as given. From the ASSISTANT take only what it ASSERTS as established: skip anything hedged, proposed or asked about ("might be", "could indicate", "let me check whether", "if X then Y", "I suspect"). A hypothesis it was still testing is not a fact about the world.
- Skip anything the assistant is quoting as an EXAMPLE, or describing as what it would do rather than what is.
- If the text states no such relationship, reply with an empty array.

TEXT:
%s

Reply with ONLY a JSON array of objects, each {"subject","subject_kind","relation","object","object_kind"}. Reply [] if none.`, text)},
	}, WithSystemPrompt("You extract explicit relationships between named entities as subject-relation-object triples for a knowledge graph. Be conservative: never infer, named entities only, and never promote a speaker's speculation to a fact. Reply with ONLY a JSON array."),
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
