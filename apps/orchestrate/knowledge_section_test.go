package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
