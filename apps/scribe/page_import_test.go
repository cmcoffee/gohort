package scribe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/snugforge/kvlite"
)

// wikiDest holds one page that existed before Scribe, and records what a
// publish to it asked for.
type wikiDest struct{ published *docs.PublishRequest }

func (wikiDest) Kind() string                    { return "wikitest" }
func (wikiDest) Label() string                   { return "Team wiki" }
func (wikiDest) Available(string) (bool, string) { return true, "" }
func (wikiDest) Targets(context.Context, string) ([]docs.PublishTarget, error) {
	return []docs.PublishTarget{{ID: "space-1", Title: "Ops"}}, nil
}
func (d wikiDest) Publish(ctx context.Context, user string, req docs.PublishRequest) (docs.PublishResult, error) {
	*d.published = req
	return docs.PublishResult{ExternalID: req.ExternalID, URL: "https://wiki.example.com/pages/77", Updated: req.ExternalID != ""}, nil
}
func (wikiDest) LinkPage(ctx context.Context, user, target, link string) (docs.LinkedPage, error) {
	return docs.LinkedPage{
		Record:   docs.PublishRecord{Kind: "wikitest", Target: "space-1", ExternalID: "77", URL: link, Version: 4},
		Copy:     docs.PublishedCopy{Body: "<h1>Runbook</h1><h2>Restart</h2><p>Restart it.</p>", Version: "4"},
		Title:    "Runbook",
		Markdown: "# Runbook\n\n## Restart\n\nRestart it.\n\n## Logs\n\nTail them.",
	}, nil
}

// A page that is already published is taken in as a guide that keeps it: its
// sections, the page as its published copy with what was read as the baseline,
// and the next publish updates that page rather than making another.
func TestAPublishedPageIsTakenInAndKept(t *testing.T) {
	var got docs.PublishRequest
	docs.RegisterPublishDestination(wikiDest{published: &got})
	T := &Scribe{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	udb := UserDB(T.DB, "alice")

	w := httptest.NewRecorder()
	T.handleImportPage(w, httptest.NewRequest(http.MethodPost, "/scribe/import/page",
		strings.NewReader(`{"kind":"wikitest","link":"https://wiki.example.com/pages/77/Runbook"}`)), udb, "alice")
	if w.Code != http.StatusOK {
		t.Fatalf("import answered %d: %s", w.Code, w.Body.String())
	}
	job := strings.Trim(strings.TrimSpace(strings.SplitN(strings.SplitN(w.Body.String(), `"job":"`, 2)[1], `"`, 2)[0]), " ")
	deadline := time.Now().Add(5 * time.Second)
	var j publishJob
	for {
		publishJobsMu.Lock()
		j = *publishJobs[importJobPrefix+job]
		publishJobsMu.Unlock()
		if j.Done || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !j.Done || !j.OK || j.GuideID == "" {
		t.Fatalf("the import ended %+v", j)
	}
	g, ok := loadGuide(udb, j.GuideID)
	if !ok || g.Title != "Runbook" || len(g.Sections) != 2 || g.Sections[0].Title != "Restart" {
		t.Fatalf("the guide: %+v", g)
	}
	rec, ok := docs.FindPublishRecord(g.Published, "wikitest")
	if !ok || rec.ExternalID != "77" || rec.Target != "space-1" || rec.Version != 4 {
		t.Errorf("the page is not its published copy: %+v", rec)
	}
	if b, ok := loadBaseline(udb, g.ID, "wikitest"); !ok || b.Version != "4" || len(b.Sections) == 0 {
		t.Errorf("what was read is not the copy's baseline: %+v %v", b, ok)
	}

	// Publishing it again goes to that page.
	p := republishOf(g, udb, "alice", rec)
	pj, ctx := p.start(context.Background())
	if err := p.do(ctx, pj); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got.ExternalID != "77" || got.Target != "space-1" {
		t.Errorf("the publish did not go to the page it was taken in from: %+v", got)
	}
}
