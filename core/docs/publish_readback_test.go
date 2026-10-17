package docs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// One page read twice in Confluence's storage format: an edit to one section
// is that section, an added and a removed section are each themselves, and
// numbering, spacing and the table of contents are not edits.
func TestChangedSectionsFindsOnlyRealEdits(t *testing.T) {
	before := SplitSections(`<h1>Dump Triage</h1><p>What a dump says.</p>` +
		`<h2>Contents</h2><ol><li>Timeouts</li><li>Disk full</li></ol>` +
		`<h2>1. Timeouts</h2><p>A timeout line means the license server is unreachable.</p>` +
		`<h2>2. Disk full</h2><p>When /var fills, dumps are empty.</p>` +
		`<h2>3. Old notes</h2><p>Retired.</p>`)
	after := SplitSections(`<h1>Dump Triage</h1><p>What a   dump says.</p>` +
		`<h2>Contents</h2><ol><li>Timeouts</li><li>Disk full</li><li>Reading a dump</li></ol>` +
		`<h2>Timeouts</h2><p>A timeout line means the license server is unreachable. Check the firewall first.</p>` +
		`<h2>2. Disk full</h2><p>When /var fills,</p><p>dumps are empty.</p>` +
		`<h2>4. Reading a dump</h2><ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[cat summary.txt]]></ac:plain-text-body></ac:structured-macro>`)
	got := map[string]SectionChange{}
	for _, c := range ChangedSections(before, after) {
		got[c.Kind+":"+c.Title] = c
	}
	if len(got) != 3 {
		t.Fatalf("want 3 changes (edited, added, removed), got %d: %+v", len(got), got)
	}
	ed, ok := got["changed:Timeouts"]
	if !ok || !strings.Contains(ed.After, "Check the firewall first") || strings.Contains(ed.Before, "firewall") {
		t.Errorf("the edited section is missing or wrong: %+v", got)
	}
	if add, ok := got["added:4. Reading a dump"]; !ok || !strings.Contains(add.After, "cat summary.txt") {
		t.Errorf("the added section is missing, or lost its code: %+v", got)
	}
	if _, ok := got["removed:3. Old notes"]; !ok {
		t.Errorf("the removed section is missing: %+v", got)
	}
}

// Markdown reads the same way: headings by # lines, but not inside a code
// fence, where a # is a comment.
func TestSplitSectionsMarkdown(t *testing.T) {
	secs := SplitSections("# Guide\n\nIntro.\n\n## Setup\n\n```sh\n# not a heading\nmake\n```\n\n### Detail\n\nStill setup.\n\n## Use\n\nRun it.")
	var titles []string
	for _, s := range secs {
		titles = append(titles, s.Title)
	}
	if strings.Join(titles, "|") != "Guide|Setup|Use" {
		t.Fatalf("sections = %q", titles)
	}
	if !strings.Contains(secs[1].Text, "# not a heading") || !strings.Contains(secs[1].Text, "Still setup") {
		t.Errorf("Setup lost its code or its subsection: %q", secs[1].Text)
	}
}

// A page read by a tool: the API's envelope comes off, the body is the
// longest string in the JSON, and a version number beside it is kept.
func TestBodyFromToolResult(t *testing.T) {
	out := "HTTP 200 OK\n" + `{"id":"42","title":"Guide","version":{"number":7},"body":{"storage":{"value":"<h2>Setup</h2><p>Run the installer and then reboot.</p>"}}}`
	cp := BodyFromToolResult(out)
	if !strings.HasPrefix(cp.Body, "<h2>Setup</h2>") || cp.Version != "7" {
		t.Fatalf("got %+v", cp)
	}
	if cp := BodyFromToolResult("## Setup\n\nPlain text."); cp.Body != "## Setup\n\nPlain text." {
		t.Errorf("plain text should pass through, got %+v", cp)
	}
}

// readingDest reads its copy before an update, as a destination should, and
// reports it after writing.
type readingDest struct {
	fakeDest
	page string
}

func (d *readingDest) Publish(ctx context.Context, user string, req PublishRequest) (PublishResult, error) {
	if req.ExternalID != "" {
		if err := CheckBeforeUpdate(ctx, PublishedCopy{Body: d.page}); err != nil {
			return PublishResult{}, err
		}
	}
	d.got = append(d.got, req)
	d.page = req.Doc.Markdown
	ReportReadBack(ctx, PublishedCopy{Body: d.page, Version: "2"})
	return PublishResult{ExternalID: "page-1"}, nil
}

type recordingWatcher struct {
	stop    error
	checked []string
	landed  []string
}

func (w *recordingWatcher) BeforeUpdate(ctx context.Context, user string, doc PublishDoc, kind string, cur PublishedCopy) error {
	w.checked = append(w.checked, cur.Body)
	return w.stop
}

func (w *recordingWatcher) Landed(user string, doc PublishDoc, kind string, cur PublishedCopy, read bool) {
	if read {
		w.landed = append(w.landed, cur.Body)
	}
}

// Every publish of a watched document goes through its watcher: an update is
// shown the copy first and can stop it before anything is written, a first
// publish is not checked, and what landed is reported either way.
func TestPublishDocumentGuardsUpdatesOfWatchedDocuments(t *testing.T) {
	reset()
	defer func() { watchersMu.Lock(); watchers = map[string]PublishWatcher{}; watchersMu.Unlock() }()
	d := &readingDest{fakeDest: fakeDest{kind: "wiki", available: true}, page: "edited on the wiki"}
	RegisterPublishDestination(d)
	w := &recordingWatcher{}
	RegisterPublishWatcher("guide", w)
	ctx := context.Background()

	if _, err := PublishDocument(ctx, "u", "wiki", PublishRequest{Doc: sampleDoc()}); err != nil {
		t.Fatal(err)
	}
	if len(w.checked) != 0 || len(w.landed) != 1 {
		t.Fatalf("a first publish: checked %d, landed %d", len(w.checked), len(w.landed))
	}

	d.page = "edited on the wiki"
	w.stop = errors.New("edited there")
	_, err := PublishDocument(ctx, "u", "wiki", PublishRequest{Doc: sampleDoc(), ExternalID: "page-1"})
	if err == nil || err.Error() != "edited there" {
		t.Fatalf("a stopped update should end with the watcher's reason, got %v", err)
	}
	if len(d.got) != 1 || w.checked[0] != "edited on the wiki" {
		t.Fatalf("the stopped update reached the page, or the watcher saw the wrong copy: got=%d checked=%q", len(d.got), w.checked)
	}

	w.stop = nil
	if _, err := PublishDocument(ctx, "u", "wiki", PublishRequest{Doc: sampleDoc(), ExternalID: "page-1"}); err != nil {
		t.Fatal(err)
	}
	if len(d.got) != 2 || len(w.landed) != 2 {
		t.Fatalf("a cleared update should write and land: got=%d landed=%d", len(d.got), len(w.landed))
	}

	// A document nobody watches publishes as before.
	other := sampleDoc()
	other.SourceKind = "note"
	if _, err := PublishDocument(ctx, "u", "wiki", PublishRequest{Doc: other, ExternalID: "page-1"}); err != nil {
		t.Fatal(err)
	}
	if len(w.checked) != 2 {
		t.Errorf("an unwatched document was checked")
	}
}
