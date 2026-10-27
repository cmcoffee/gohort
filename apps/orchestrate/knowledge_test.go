package orchestrate

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// specChunks stands in for an ingested OpenAPI spec: one chunk per operation,
// sorted alphabetically the way fetch_knowledge_doc assembles them. DELETE
// sorts first, POST last — which is the whole shape of the bug.
func specChunks() []EmbeddedChunk {
	return []EmbeddedChunk{
		{Section: "## DELETE /rest/clients/{id}", Text: "Remove a client."},
		{Section: "## GET /rest/clients", Text: "List clients."},
		{Section: "## GET /rest/clients/{id}", Text: "Read one client."},
		{Section: "## POST /rest/clients — Create a client", Text: "Body: name, redirect_uri, scopes."},
		{Section: "## PUT /rest/clients/{id}", Text: "Replace a client."},
	}
}

// TestASectionDeepInTheDocumentIsReachable — the defect. A document larger than
// the fetch ceiling is truncated from the TOP, so every call returns the same
// opening slice and later sections are unreachable at any max_chars. Naming the
// section is the only parameter that can get past that.
func TestASectionDeepInTheDocumentIsReachable(t *testing.T) {
	got := chunksInSection(specChunks(), "POST /rest/clients")
	if len(got) != 1 {
		t.Fatalf("matched %d chunks, want the one POST operation: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Text, "redirect_uri") {
		t.Errorf("the matched chunk is not the POST body: %q", got[0].Text)
	}
}

// TestTheMatchIsASubstringOfTheHeading — a caller quotes a heading it read in a
// search hit, and those carry a summary after the path. Requiring the whole
// heading would fail on exactly what a reader is most likely to type.
func TestTheMatchIsASubstringOfTheHeading(t *testing.T) {
	for _, want := range []string{
		"POST /rest/clients",                   // path only, heading has a summary after it
		"post /rest/clients",                   // lower case
		"  POST /rest/clients  ",               // pasted with whitespace
		"POST /rest/clients — Create a client", // the full heading
		"Create a client",                      // the summary alone
	} {
		if n := len(chunksInSection(specChunks(), want)); n != 1 {
			t.Errorf("section %q matched %d chunks, want 1", want, n)
		}
	}
}

// TestAPrefixDoesNotSwallowSiblings — "GET /rest/clients" is a prefix of
// "GET /rest/clients/{id}", so a match on the collection endpoint returns both.
// That is the intended behavior for a substring match and worth pinning: a
// reader gets the neighbours rather than silently the wrong one.
func TestAPrefixDoesNotSwallowSiblings(t *testing.T) {
	got := chunksInSection(specChunks(), "GET /rest/clients")
	if len(got) != 2 {
		t.Fatalf("matched %d, want both the collection and the item endpoint", len(got))
	}
	// The narrower query still isolates one.
	if n := len(chunksInSection(specChunks(), "GET /rest/clients/{id}")); n != 1 {
		t.Errorf("the specific path matched %d chunks, want 1", n)
	}
}

// TestAnEmptySectionReturnsEverything — omitting the parameter must not filter
// the document down to nothing.
func TestAnEmptySectionReturnsEverything(t *testing.T) {
	all := specChunks()
	for _, empty := range []string{"", "   "} {
		if n := len(chunksInSection(all, empty)); n != len(all) {
			t.Errorf("section %q returned %d of %d chunks", empty, n, len(all))
		}
	}
}

// TestSectionHeadingsAreListedInOrderWithoutNoise — the list is what makes a
// truncated fetch actionable. Part suffixes and the document's own title are
// not sections a reader can ask for.
func TestSectionHeadingsAreListedInOrderWithoutNoise(t *testing.T) {
	chunks := []EmbeddedChunk{
		{Section: "## Acme API"}, // the doc title, repeated as a chunk section
		{Section: "## GET /rest/clients"},
		{Section: "## POST /rest/clients (part 1/2)"},
		{Section: "## POST /rest/clients (part 2/2)"},
		{Section: "## GET /rest/clients"}, // a duplicate
	}
	got := sectionHeadings(chunks, "Acme API")
	want := []string{"GET /rest/clients", "POST /rest/clients"}
	if len(got) != len(want) {
		t.Fatalf("headings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("heading %d = %q, want %q (order must follow assembly)", i, got[i], want[i])
		}
	}
}

// TestTheHeadingListIsBounded — a spec with hundreds of operations must not
// spend the whole reply listing them, which is the problem the list solves.
func TestTheHeadingListIsBounded(t *testing.T) {
	var many []string
	for i := 0; i < headingListMax+40; i++ {
		many = append(many, "GET /rest/thing/"+strings.Repeat("x", 3))
	}
	out := headingList(many)
	if !strings.Contains(out, "and 40 more") {
		t.Errorf("an over-long list did not say how many were dropped:\n%s", out[len(out)-120:])
	}
	if n := strings.Count(out, "\n") + 1; n > headingListMax+1 {
		t.Errorf("the list rendered %d lines, over the cap", n)
	}
	// A short list is rendered whole, with no dangling blank line.
	short := headingList([]string{"GET /a", "POST /b"})
	if short != "  GET /a\n  POST /b" {
		t.Errorf("short list rendered as %q", short)
	}
}

// TestRecordAgentTopicRecencyOrder: new slugs append; reusing an existing slug
// moves it to the end (most-recently-used), with no duplicates. The tail order
// is what the Known-topics cap keeps.
func TestRecordAgentTopicRecencyOrder(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	order := func() string { return strings.Join(listAgentTopics(db, "u", "a"), ",") }

	for _, top := range []string{"alpha", "beta", "gamma"} {
		recordAgentTopic(db, "u", "a", top)
	}
	if got := order(); got != "alpha,beta,gamma" {
		t.Fatalf("append order: got %q", got)
	}

	recordAgentTopic(db, "u", "a", "alpha") // reuse → moves to end
	if got := order(); got != "beta,gamma,alpha" {
		t.Fatalf("reuse should move to end: got %q", got)
	}

	recordAgentTopic(db, "u", "a", "beta") // reuse again, still no dupes
	if got := order(); got != "gamma,alpha,beta" {
		t.Fatalf("got %q", got)
	}
}

// Reference Memory chunks live in ONE global store, isolated by a string
// prefix rather than by a partition — so the comparison IS the boundary.

func TestSourceScopeMatchesTheCorpusAndItsTopics(t *testing.T) {
	p := agentKnowledgePrefix("alice", "agent-1")
	for _, in := range []string{p, p + ":runbooks", p + ":a:b"} {
		if !sourceInScope(in, p) {
			t.Errorf("%q belongs to this corpus", in)
		}
	}
}

// The reason this is a predicate and not a bare HasPrefix. Nothing leaks today
// — user agent ids are UUIDs and no two hand-written seed ids stand in this
// relation — so the boundary held by accident of naming, and adding one id
// beside an existing one would have opened it with nothing failing.
func TestANeighbouringCorpusIsNotSweptIn(t *testing.T) {
	p := agentKnowledgePrefix("alice", "app-guides")
	for _, other := range []string{
		agentKnowledgePrefix("alice", "app-guides-author"),
		agentKnowledgePrefix("alice", "app-guides-author") + ":runbooks",
		agentKnowledgePrefix("alice", "app-guides2"),
	} {
		if sourceInScope(other, p) {
			t.Errorf("%q is a DIFFERENT agent's corpus and must not match %q", other, p)
		}
	}
}

// The user half of the prefix has to hold the same way.
func TestAnotherUsersCorpusIsNotSweptIn(t *testing.T) {
	p := agentKnowledgePrefix("alice", "agent-1")
	for _, other := range []string{
		agentKnowledgePrefix("alice2", "agent-1"),
		agentKnowledgePrefix("alicebob", "agent-1"),
	} {
		if sourceInScope(other, p) {
			t.Errorf("%q belongs to another user and must not match %q", other, p)
		}
	}
}

// Scoped app-agent users carry colons of their own (app:servitor:<id>), which
// is the case most likely to be got wrong by hand.
func TestScopedAppAgentUsersStaySeparate(t *testing.T) {
	one := agentKnowledgePrefix("app:servitor:1", "app-servitor-investigator")
	ten := agentKnowledgePrefix("app:servitor:10", "app-servitor-investigator")
	if sourceInScope(ten, one) {
		t.Error("appliance 10's corpus must not fall inside appliance 1's")
	}
	if !sourceInScope(one+":facts", one) {
		t.Error("its own topics must still match")
	}
}

// The loose form cannot come back by hand. Ten sites checked this by
// open-coding strings.HasPrefix, and each was one edit away from being wrong;
// a predicate only helps while everyone uses it.
func TestNobodyOpenCodesTheScopeCheck(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		body, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "HasPrefix(c.Source, prefix)") {
			t.Errorf("%s compares a chunk Source by bare prefix — use sourceInScope, "+
				"or a neighbouring corpus whose name merely starts the same way gets swept in", n)
		}
	}
}
