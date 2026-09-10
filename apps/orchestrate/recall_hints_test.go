package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// knowledgeHints/memoryHints are the pure converters behind the scored sources:
// threshold cut, label, source-appropriate pull pointer, and dedupe key.
func TestRecallSourceHints(t *testing.T) {
	kn := knowledgeHints([]SearchHit{
		{ReportID: "doc-q3", Title: "Q3 pricing policy", Score: 0.88},
		{Title: "Loose note", Score: 0.80},               // no ReportID → knowledge_search fallback
		{ReportID: "doc-x", Title: "Below", Score: 0.40}, // sub-threshold → stops scan
	}, 0.70, nil)
	if len(kn) != 2 {
		t.Fatalf("knowledgeHints: want 2, got %d", len(kn))
	}
	if kn[0].source != "knowledge" || !strings.Contains(kn[0].pull, `fetch_knowledge_doc(doc_id="doc-q3")`) {
		t.Fatalf("knowledge hit 0 wrong: %+v", kn[0])
	}
	if kn[1].pull != "knowledge_search" { // no doc_id → search fallback
		t.Fatalf("no-doc_id hit should fall back to knowledge_search: %+v", kn[1])
	}

	mem := memoryHints([]SearchHit{{ReportID: "m1", Title: "Acme API pagination", Score: 0.79}}, 0.70)
	if len(mem) != 1 || mem[0].source != "memory" || mem[0].pull != `memory(action="search")` {
		t.Fatalf("memory hint wrong: %+v", mem)
	}
}

// mergeScoredHints ranks across sources by score, dedupes by key, and caps.
func TestMergeScoredHints(t *testing.T) {
	in := []recallHint{
		{source: "knowledge", score: 0.72, key: "doc:a"},
		{source: "memory", score: 0.91, key: "mem:b"},    // highest → first
		{source: "knowledge", score: 0.72, key: "doc:a"}, // dup key → dropped
		{source: "memory", score: 0.80, key: "mem:c"},
	}
	got := mergeScoredHints(in, 4)
	if len(got) != 3 {
		t.Fatalf("want 3 after dedupe, got %d", len(got))
	}
	if got[0].key != "mem:b" || got[1].key != "mem:c" || got[2].key != "doc:a" {
		t.Fatalf("wrong score ordering: %v", []string{got[0].key, got[1].key, got[2].key})
	}
	if len(mergeScoredHints(in, 1)) != 1 {
		t.Fatal("cap not applied")
	}
}

// formatRecallHints renders source tags, scored vs structural (graph) lines,
// and the pull pointers — and stays empty when there's nothing.
func TestFormatRecallHints(t *testing.T) {
	scored := []recallHint{
		{source: "knowledge", label: `"Q3 pricing policy"`, score: 0.88, scored: true, pull: `fetch_knowledge_doc(doc_id="doc-q3")`},
		{source: "memory", label: `"Acme API pagination"`, score: 0.79, scored: true, pull: `memory(action="search")`},
	}
	graph := []recallHint{
		{source: "graph", label: `"Acme" links to "Project Zeus" (sponsors), 3 total`, pull: `recall_about("Acme")`},
	}
	block := formatRecallHints(nil, scored, graph)
	for _, want := range []string{
		"knowledge · \"Q3 pricing policy\" (0.88) → fetch_knowledge_doc(doc_id=\"doc-q3\")",
		"memory · \"Acme API pagination\" (0.79) → memory(action=\"search\")",
		`graph · "Acme" links to "Project Zeus" (sponsors), 3 total → recall_about("Acme")`,
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing line %q:\n%s", want, block)
		}
	}
	// The graph line is structural — no "(0.xx)" score attached to it.
	graphLine := block[strings.Index(block, "graph · "):]
	graphLine = graphLine[:strings.IndexByte(graphLine, '\n')]
	if strings.Contains(graphLine, ") (0.") {
		t.Fatalf("graph line should carry no score: %q", graphLine)
	}
	if formatRecallHints(nil, nil, nil) != "" {
		t.Fatal("empty in → empty out")
	}
}

// Auto-promote injects the BODY of very-high-confidence curated hits and excludes
// them from the pointer list; below-promote hits stay pointers.
func TestAutoPromote(t *testing.T) {
	// Score-descending, as the vector search returns them.
	hits := []SearchHit{
		{ReportID: "doc-hi", Title: "Exact match", Score: 0.95, Text: "the full body text to inject"},
		{ReportID: "doc-mid", Title: "Good match", Score: 0.78, Text: "not promoted"},
	}
	excl := map[string]bool{}
	promoted := topPromotable(hits, 0.92, 2, excl)
	if len(promoted) != 1 || promoted[0].ReportID != "doc-hi" {
		t.Fatalf("promote: want [doc-hi], got %+v", promoted)
	}
	if !excl["doc:doc-hi"] {
		t.Fatalf("promoted doc not recorded in exclude set: %v", excl)
	}

	// A high-scoring hit with no body is NOT promotable (nothing to inject).
	if got := topPromotable([]SearchHit{{ReportID: "x", Score: 0.99, Text: ""}}, 0.92, 2, map[string]bool{}); len(got) != 0 {
		t.Fatalf("empty-body hit should not promote: %+v", got)
	}

	// The promoted doc is excluded from pointers; the mid hit remains a pointer.
	ptrs := knowledgeHints(hits, 0.70, excl)
	if len(ptrs) != 1 || ptrs[0].key != "doc:doc-mid" {
		t.Fatalf("want only doc-mid as pointer, got %+v", ptrs)
	}

	// The rendered block carries the body under the promoted header, and the mid
	// hit as a pointer.
	block := formatRecallHints(promoted, ptrs, nil)
	if !strings.Contains(block, "the full body text to inject") {
		t.Fatalf("promoted body missing:\n%s", block)
	}
	if !strings.Contains(block, "Exact match") || !strings.Contains(block, "(0.95)") {
		t.Fatalf("promoted header missing:\n%s", block)
	}
	if !strings.Contains(block, "fetch_knowledge_doc(doc_id=\"doc-mid\")") {
		t.Fatalf("mid pointer missing:\n%s", block)
	}
}

// corpusNames is the knowledge pair a turn would be given, by name.
func corpusNames(turn *chatTurn) string {
	var names []string
	for _, td := range turn.corpusToolDefs() {
		names = append(names, td.Tool.Name)
	}
	return strings.Join(names, " ")
}

// The live failure this signal exists for. agentHasRetrievableContent INFERS a
// corpus from collection attachments; renderRecallHints SEARCHES one. They
// disagreed, and the losing reading was the damaging one: the hint block
// printed document titles and named fetch_knowledge_doc(doc_id=…) as the way to
// read them, while the catalog carried neither tool. The agent reported,
// accurately, that the tool was not in its tool set, and kept reporting it.
//
// A search that came back with documents is proof. It outranks the inference.
func TestASearchThatFoundDocumentsMintsTheKnowledgeTools(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.agent.AttachedCollections = nil
	turn.agent.IngestAttachments = false
	if got := corpusNames(turn); got != "" {
		t.Fatalf("fixture should start with nothing retrievable, got %q", got)
	}

	turn.hintedKnowledge = 3 // the recall search found three curated documents
	got := corpusNames(turn)
	for _, want := range []string{"knowledge_search", "fetch_knowledge_doc"} {
		if !strings.Contains(got, want) {
			t.Errorf("a turn whose search found documents must carry %q; got %q", want, got)
		}
	}
}

// One-way, and it has to be: recall hints are per-agent opt-in and skip short
// queries, so zero hits means "found nothing OR never looked". It can prove a
// corpus is reachable and must never be read as proof that one is not.
func TestTheHintSignalNeverWithholdsTheKnowledgeTools(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.agent.AttachedCollections = []string{"c-kiteworks"}
	turn.hintedKnowledge = 0 // hints off, or a query too short to run one

	if got := corpusNames(turn); !strings.Contains(got, "knowledge_search") {
		t.Errorf("a configured corpus stands on its own without a hint; got %q", got)
	}
}

// The gate the conversational catalog has always applied, unchanged: no corpus
// by either route, no tool. A knowledge tool over an empty corpus invites
// doc_ids the handler must then refuse.
func TestNoCorpusByEitherRouteStillWithholdsTheKnowledgeTools(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.agent.AttachedCollections = nil
	turn.agent.IngestAttachments = false
	turn.hintedKnowledge = 0

	if got := corpusNames(turn); got != "" {
		t.Errorf("nothing retrievable must stay tool-less; got %q", got)
	}
}
