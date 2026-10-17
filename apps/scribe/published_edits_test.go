package scribe

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

const wikiKind = "target:wiki"

func publishedGuideFixture(t *testing.T) (*Scribe, Database, Guide) {
	t.Helper()
	src, udb, g := guideSourceFixture(t)
	g.Published = []docs.PublishRecord{{Kind: wikiKind, TargetTitle: "Team wiki", ExternalID: "https://wiki.example/pages/1"}}
	g = saveGuide(udb, g)
	return src.app, udb, g
}

const wikiPage = `<h1>Dump Triage</h1><h2>1. License server timeouts</h2><p>A timeout means the license server is unreachable.</p>` +
	`<h2>2. Disk full</h2><p>When /var fills, the collector writes empty dumps.</p>`

// An edit made on the published page is not overwritten: the update stops,
// the edited section is filed for the Curator as a finding for this guide,
// and once filed the same page no longer stops an update.
func TestAnEditOnThePublishedPageGoesToTheCurator(t *testing.T) {
	app, udb, g := publishedGuideFixture(t)
	w := guideWatcher{app: app}
	doc := docs.PublishDoc{SourceKind: "guide", SourceID: g.ID}
	w.Landed("u", doc, wikiKind, docs.PublishedCopy{Body: wikiPage}, true)

	if err := w.BeforeUpdate(context.Background(), "u", doc, wikiKind, docs.PublishedCopy{Body: wikiPage}); err != nil {
		t.Fatalf("an unedited page stopped the update: %v", err)
	}
	if n := len(listPendingFindings(udb)); n != 0 {
		t.Fatalf("an unedited page filed %d findings", n)
	}

	edited := strings.Replace(wikiPage, "is unreachable.", "is unreachable. Check the firewall first.", 1)
	err := w.BeforeUpdate(context.Background(), "u", doc, wikiKind, docs.PublishedCopy{Body: edited})
	if !errors.Is(err, errEditedThere) {
		t.Fatalf("an edited page should stop the update, got %v", err)
	}
	if !strings.Contains(err.Error(), "Team wiki") {
		t.Errorf("the reason should say where: %v", err)
	}
	pending := listPendingFindings(udb)
	if len(pending) != 1 {
		t.Fatalf("want one finding for the one edited section, got %d", len(pending))
	}
	f := pending[0]
	if f.Document != g.ID || f.Origin.SourceKind != "published copy" || f.Origin.ItemLabel != "Team wiki" {
		t.Errorf("the finding should be for this guide, from its published copy: %+v", f)
	}
	if !strings.Contains(f.Content, "Check the firewall first") || !strings.Contains(f.Content, "As last published") {
		t.Errorf("the finding should carry the edit and what it replaced: %q", f.Content)
	}

	if err := w.BeforeUpdate(context.Background(), "u", doc, wikiKind, docs.PublishedCopy{Body: edited}); err != nil {
		t.Fatalf("an edit already filed stopped the next update: %v", err)
	}
	if n := len(listPendingFindings(udb)); n != 1 {
		t.Errorf("the same edit was filed again: %d findings", n)
	}
}

// With nothing to compare against (published before copies were read back,
// or a publish that could not read its copy), a look keeps what it sees and
// files nothing: it cannot tell an edit from the original.
func TestAFirstLookFilesNothing(t *testing.T) {
	app, udb, g := publishedGuideFixture(t)
	w := guideWatcher{app: app}
	doc := docs.PublishDoc{SourceKind: "guide", SourceID: g.ID}
	w.Landed("u", doc, wikiKind, docs.PublishedCopy{Body: wikiPage}, true)
	w.Landed("u", doc, wikiKind, docs.PublishedCopy{}, false)
	if _, ok := loadBaseline(udb, g.ID, wikiKind); ok {
		t.Fatal("a publish that could not read its copy kept the old baseline")
	}
	if err := w.BeforeUpdate(context.Background(), "u", doc, wikiKind, docs.PublishedCopy{Body: wikiPage + "<p>anything</p>"}); err != nil {
		t.Fatalf("a first look stopped the update: %v", err)
	}
	if _, ok := loadBaseline(udb, g.ID, wikiKind); !ok {
		t.Error("a first look should keep what it saw")
	}
	if n := len(listPendingFindings(udb)); n != 0 {
		t.Errorf("a first look filed %d findings", n)
	}
}

// A background read overtaken by a publish is older than the baseline that
// publish left, so it is dropped rather than read as edits undoing it.
func TestAReadOvertakenByAPublishIsDropped(t *testing.T) {
	app, udb, g := publishedGuideFixture(t)
	w := guideWatcher{app: app}
	doc := docs.PublishDoc{SourceKind: "guide", SourceID: g.ID}
	w.Landed("u", doc, wikiKind, docs.PublishedCopy{Body: wikiPage}, true)
	since := baselineMark(loadBaseline(udb, g.ID, wikiKind))

	newer := strings.Replace(wikiPage, "empty dumps.", "empty dumps. Clear /var/tmp.", 1)
	w.Landed("u", doc, wikiKind, docs.PublishedCopy{Body: newer}, true)
	if n := app.takeInEdits("u", udb, g, wikiKind, docs.PublishedCopy{Body: wikiPage}, since); n != 0 {
		t.Fatalf("a read from before the publish filed %d findings", n)
	}
	if n := len(listPendingFindings(udb)); n != 0 {
		t.Errorf("%d findings filed", n)
	}
}

// Deleting a guide deletes what its copies were last seen to say.
func TestDeletingAGuideDropsItsBaselines(t *testing.T) {
	app, udb, g := publishedGuideFixture(t)
	guideWatcher{app: app}.Landed("u", docs.PublishDoc{SourceKind: "guide", SourceID: g.ID}, wikiKind, docs.PublishedCopy{Body: wikiPage}, true)
	deleteBaselines(udb, g.ID)
	if _, ok := loadBaseline(udb, g.ID, wikiKind); ok {
		t.Error("the baseline outlived its guide")
	}
}
