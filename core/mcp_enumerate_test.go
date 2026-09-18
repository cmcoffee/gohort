package core

// Reading a listing tool's reply. Every server shapes this differently and the
// shape is declared nowhere a client can read, so the parser accepts what
// occurs and refuses what it cannot vouch for.
//
// Refusing matters more here than anywhere else: Documents is authoritative by
// contract, so a listing that came back half-complete is read by a caller as a
// deletion of the other half.

import (
	"strings"
	"testing"
	"time"
)

func TestMCPParsesABareArray(t *testing.T) {
	docs, err := mcpParseDocList(`[{"id":"p1","title":"Restarting","version":3},
	                               {"id":"p2","title":"Rotating"}]`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("docs = %+v", docs)
	}
	if docs[0].ID != "p1" || docs[0].Title != "Restarting" || docs[0].Version != "3" {
		t.Errorf("first = %+v", docs[0])
	}
}

// Servers wrap their results under whatever noun they favour, and insisting on
// one spelling would fail on the next server for no reason.
func TestMCPFindsTheArrayWhateverItIsCalled(t *testing.T) {
	for _, body := range []string{
		`{"results":[{"id":"a","title":"A"}]}`,
		`{"pages":[{"id":"a","title":"A"}]}`,
		`{"items":[{"id":"a","title":"A"}],"facets":["x","y"]}`,
	} {
		docs, err := mcpParseDocList(body)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if len(docs) != 1 || docs[0].ID != "a" {
			t.Errorf("%s -> %+v", body, docs)
		}
	}
}

// The one failure that must never pass as success.
func TestMCPRefusesAListingThatSaysThereIsMore(t *testing.T) {
	for _, body := range []string{
		`{"results":[{"id":"a"}],"next_cursor":"abc"}`,
		`{"results":[{"id":"a"}],"has_more":true}`,
		`{"results":[{"id":"a"}],"next":{"href":"/page/2"}}`,
		`{"results":[{"id":"a"}],"is_last_page":false}`,
	} {
		if _, err := mcpParseDocList(body); err == nil {
			t.Errorf("%s was accepted as a complete listing", body)
		} else if !strings.Contains(err.Error(), "more to come") {
			t.Errorf("%s -> %v", body, err)
		}
	}
	// And a listing that says it is COMPLETE is not refused for saying so.
	if _, err := mcpParseDocList(`{"results":[{"id":"a"}],"is_last_page":true,"has_more":false}`); err != nil {
		t.Errorf("a complete listing was refused: %v", err)
	}
}

// A row with no id could never be matched on the next sync, so it would be
// added and retired forever. The whole listing is refused rather than dropping
// rows out of a set the caller will treat as authoritative.
func TestMCPRefusesARowWithNoID(t *testing.T) {
	if _, err := mcpParseDocList(`{"results":[{"id":"a"},{"title":"no id"}]}`); err == nil {
		t.Fatal("a listing with an unidentifiable document was accepted")
	}
}

func TestMCPReadsTheIDAndVersionSpellingsThatOccur(t *testing.T) {
	docs, err := mcpParseDocList(`{"results":[
		{"key":"ENG-1","name":"By key","version":{"number":7}},
		{"page_id":"99","title":"By page_id","etag":"W/\"abc\""},
		{"uuid":"u-1","subject":"By uuid","versionNumber":2}
	]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("docs = %+v", docs)
	}
	if docs[0].ID != "ENG-1" || docs[0].Title != "By key" || docs[0].Version != "7" {
		t.Errorf("by key = %+v", docs[0])
	}
	if docs[1].ID != "99" || docs[1].Version != `W/"abc"` {
		t.Errorf("by page_id = %+v", docs[1])
	}
	if docs[2].ID != "u-1" || docs[2].Version != "2" {
		t.Errorf("by uuid = %+v", docs[2])
	}
}

func TestMCPReadsATimestampWhenThereIsNoVersion(t *testing.T) {
	docs, err := mcpParseDocList(`[{"id":"a","title":"A","lastModified":"2026-09-17T10:00:00Z"}]`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !docs[0].Updated.Equal(time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("updated = %v", docs[0].Updated)
	}
}

// A tool that answers in prose with the payload in a fence, or in prose around
// it, is a tool that still works.
func TestMCPFindsJSONInProse(t *testing.T) {
	for _, body := range []string{
		"Here are the pages:\n```json\n{\"results\":[{\"id\":\"a\"}]}\n```\nThat's all.",
		"Found 1 page: {\"results\":[{\"id\":\"a\"}]}",
	} {
		docs, err := mcpParseDocList(body)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if len(docs) != 1 || docs[0].ID != "a" {
			t.Errorf("%q -> %+v", body, docs)
		}
	}
}

func TestMCPRefusesWhatItCannotRead(t *testing.T) {
	for _, body := range []string{
		"",
		"no json here at all",
		`{"results":[]}`,
		`{"count":3}`,
	} {
		if _, err := mcpParseDocList(body); err == nil {
			t.Errorf("%q was accepted", body)
		}
	}
}

func TestMCPDocumentTextPrefersTheBody(t *testing.T) {
	// A named body field wins over a longer sibling, or a document with a
	// short body and a long changelog comes back as the changelog.
	got := mcpDocumentText(`{"body":"Short body.","changelog":"` + strings.Repeat("x", 200) + `"}`)
	if got != "Short body." {
		t.Errorf("got %q", got)
	}
	// No named field: the longest string is the document.
	got = mcpDocumentText(`{"zzz":"the actual long text of the page","a":"hi"}`)
	if got != "the actual long text of the page" {
		t.Errorf("got %q", got)
	}
	// Plain text passes through untouched.
	if got = mcpDocumentText("just the page, as text"); got != "just the page, as text" {
		t.Errorf("got %q", got)
	}
}
