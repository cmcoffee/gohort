package scribe

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

// countingDest records each publish it is asked for and, when gate is set,
// holds each one until the test lets it go.
type countingDest struct {
	mu   sync.Mutex
	reqs []docs.PublishRequest
	gate chan struct{}
}

func (*countingDest) Kind() string                    { return "keeptest" }
func (*countingDest) Label() string                   { return "Keep test" }
func (*countingDest) Available(string) (bool, string) { return true, "" }
func (*countingDest) Targets(context.Context, string) ([]docs.PublishTarget, error) {
	return nil, nil
}
func (d *countingDest) Publish(ctx context.Context, user string, req docs.PublishRequest) (docs.PublishResult, error) {
	d.mu.Lock()
	d.reqs = append(d.reqs, req)
	gate := d.gate
	d.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return docs.PublishResult{URL: "https://wiki.example/p/" + req.ExternalID, ExternalID: req.ExternalID, Version: req.Version + 1, Updated: true}, nil
}
func (d *countingDest) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.reqs)
}

func keepFixture(t *testing.T, keep bool) (*Scribe, Database, *countingDest) {
	t.Helper()
	app := &Scribe{}
	app.DB = testStore(t)
	dest := &countingDest{}
	docs.RegisterPublishDestination(dest)
	udb := UserDB(app.DB, "u")
	saveGuide(udb, Guide{ID: "kg", Owner: "u", Title: "Dump Triage", KeepPublished: keep,
		Sections: []Section{{ID: "s", Title: "Disk full", Markdown: "Empty dumps.", Order: 1}},
		Published: []docs.PublishRecord{{Kind: "keeptest", Target: "SPACE", TargetTitle: "Team wiki", Title: "Dump Triage", ExternalID: "77", Version: 3}}})
	return app, udb, dest
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A Curator run that changed a guide with Keep published on updates the page
// it was published to: the same page and version, the guide as it now stands,
// and the record moves on. A run that changed nothing, or only held or
// discarded, publishes nothing.
func TestACuratorChangeUpdatesAKeptPublishedGuide(t *testing.T) {
	app, udb, dest := keepFixture(t, true)
	app.keepPublishedAfter("u", CuratorRun{Entries: []CuratorEntry{{Kind: OutcomeHeld, GuideID: "kg"}, {Kind: OutcomeDiscarded}}})
	time.Sleep(50 * time.Millisecond)
	if n := dest.count(); n != 0 {
		t.Fatalf("a run that changed nothing published %d time(s)", n)
	}

	g, _ := loadGuide(udb, "kg")
	g.Sections = append(g.Sections, Section{ID: "s2", Title: "License timeouts", Markdown: "Firewall first.", Order: 2})
	saveGuide(udb, g)
	app.keepPublishedAfter("u", CuratorRun{Entries: []CuratorEntry{{Kind: OutcomePlaced, GuideID: "kg", Section: "License timeouts"}}})
	waitFor(t, "the update", func() bool {
		j, ok := currentPublishJob("kg")
		return dest.count() == 1 && ok && j.Done
	})
	req := dest.reqs[0]
	if req.ExternalID != "77" || req.Version != 3 || req.Target != "SPACE" || !strings.Contains(req.Doc.Markdown, "Firewall first.") {
		t.Errorf("published %+v", req)
	}
	waitFor(t, "the record", func() bool {
		cur, _ := loadGuide(udb, "kg")
		rec, _ := docs.FindPublishRecord(cur.Published, "keeptest")
		return rec.Version == 4
	})
}

// Without Keep published, a Curator change leaves the published copy alone.
func TestAGuideWithoutKeepPublishedIsLeftAlone(t *testing.T) {
	app, _, dest := keepFixture(t, false)
	app.keepPublishedAfter("u", CuratorRun{Entries: []CuratorEntry{{Kind: OutcomePlaced, GuideID: "kg"}}})
	time.Sleep(50 * time.Millisecond)
	if n := dest.count(); n != 0 {
		t.Errorf("published %d time(s) with Keep published off", n)
	}
}

// Changes that arrive while an update is running are one more update when it
// ends, not one each.
func TestChangesDuringAnUpdateAreOneMoreUpdate(t *testing.T) {
	app, udb, dest := keepFixture(t, true)
	dest.gate = make(chan struct{})
	go app.keepPublished(context.Background(), "u", udb, "kg")
	waitFor(t, "the first update to start", func() bool { return dest.count() == 1 })
	for i := 0; i < 3; i++ {
		app.keepPublished(context.Background(), "u", udb, "kg")
	}
	close(dest.gate)
	waitFor(t, "the second update", func() bool { return dest.count() >= 2 })
	waitFor(t, "the pass to end", func() bool {
		keepMu.Lock()
		defer keepMu.Unlock()
		return !keepActive["kg"]
	})
	if n := dest.count(); n != 2 {
		t.Errorf("published %d times; want the first and one more", n)
	}
}

// The dialog's switch turns Keep published on and off, and the dialog's
// state reports it.
func TestKeepPublishedIsSwitchedFromTheDialog(t *testing.T) {
	app, udb, _ := keepFixture(t, false)
	w := httptest.NewRecorder()
	app.handleKeepPublished(w, httptest.NewRequest("POST", "/publish/keep?id=kg", strings.NewReader(`{"on":true}`)), udb, "u")
	if w.Code != 200 {
		t.Fatalf("switch on: %d %s", w.Code, w.Body.String())
	}
	if g, _ := loadGuide(udb, "kg"); !g.KeepPublished {
		t.Error("Keep published did not turn on")
	}
	w = httptest.NewRecorder()
	app.handlePublishState(w, httptest.NewRequest("GET", "/publish/state?id=kg", nil), udb, "u")
	if !strings.Contains(w.Body.String(), `"keep_published":true`) {
		t.Errorf("the dialog's state does not say so: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	app.handleKeepPublished(w, httptest.NewRequest("GET", "/publish/keep?id=kg", nil), udb, "u")
	if w.Code != 405 {
		t.Errorf("a GET changed it: %d", w.Code)
	}
}
