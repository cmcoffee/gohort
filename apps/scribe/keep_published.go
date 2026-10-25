// Keep published: a guide whose published copies follow it.
//
// A living document (a troubleshooting guide agents report into, see
// agent_source.go) changes when the Guide Curator places findings in it. With
// Keep published on, each Curator pass that changed the guide updates every
// place it is already published, through the same job as the dialog's Update:
// the same page, title and answers as last time, with its steps and outcome in
// the dialog for anyone who opens it.
//
// On a CHANGE, not a clock: an unchanged guide is never republished, and a
// changed one is never far behind. Edits made on the page itself are not
// overwritten: they come back as findings first (published_edits.go). Only places it was already published to by
// hand are updated, so where it lands stays the person's choice. A failure is
// a notification; a success is routine and says nothing.

package scribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// keepPublishedWait is how long a pass waits for a publish already running
// on the guide (somebody's Update) to finish before giving up on this round.
var keepPublishedWait = 10 * time.Minute

var (
	keepMu     sync.Mutex
	keepActive = map[string]bool{} // guide id: a pass is running
	keepAgain  = map[string]bool{} // guide id: it changed again during the pass
)

// keepPublishedAfter starts a pass for each guide run changed that has Keep
// published on. Called when a Curator run ends, failed runs included: the
// entries before a failure are real writes.
func (T *Scribe) keepPublishedAfter(user string, run CuratorRun) {
	udb := UserDB(T.DB, user)
	if udb == nil {
		return
	}
	seen := map[string]bool{}
	for _, e := range run.Entries {
		switch e.Kind {
		case OutcomePlaced, OutcomeSuperseded, OutcomeCreated:
		default:
			continue
		}
		if e.GuideID == "" || e.Undone || seen[e.GuideID] {
			continue
		}
		seen[e.GuideID] = true
		if g, ok := loadGuide(udb, e.GuideID); ok && g.KeepPublished && len(g.Published) > 0 {
			go T.keepPublished(AppContext(), user, udb, g.ID)
		}
	}
}

// keepPublished updates every published copy of one guide. A second call
// while a pass runs does not start another: it asks the running pass to go
// once more when it ends, so a burst of changes is one more update, not one
// each.
func (T *Scribe) keepPublished(ctx context.Context, user string, udb Database, guideID string) {
	keepMu.Lock()
	if keepActive[guideID] {
		keepAgain[guideID] = true
		keepMu.Unlock()
		return
	}
	keepActive[guideID] = true
	keepMu.Unlock()
	for {
		T.republishAll(ctx, user, udb, guideID)
		keepMu.Lock()
		if !keepAgain[guideID] || ctx.Err() != nil {
			delete(keepActive, guideID)
			delete(keepAgain, guideID)
			keepMu.Unlock()
			return
		}
		delete(keepAgain, guideID)
		keepMu.Unlock()
	}
}

// republishAll runs one update per published copy, in turn, each as the
// guide's publish job so an open dialog shows it. The guide is read again for
// each, so a copy is updated with the guide as it stands.
func (T *Scribe) republishAll(ctx context.Context, user string, udb Database, guideID string) {
	g, ok := loadGuide(udb, guideID)
	if !ok || !g.KeepPublished {
		return
	}
	var failed []string
	for _, prev := range g.Published {
		if ctx.Err() != nil {
			return
		}
		cur, ok := loadGuide(udb, guideID)
		if !ok || !cur.KeepPublished {
			return
		}
		p := republishOf(cur, udb, user, prev)
		p.background = true
		if !waitForPublishJob(ctx, guideID, keepPublishedWait) {
			failed = append(failed, p.where+": another publish of this guide was still running")
			continue
		}
		job, jctx := p.start(ctx)
		if err := p.do(jctx, job); err != nil {
			// Stopped from the dialog: the person ended this pass, so the
			// rest of it is not started and nothing is reported as failed.
			if errors.Is(err, errPublishStopped) {
				Log("[scribe.keep_published] user=%q guide=%q: stopped from the Publish dialog", user, firstNonEmpty(g.Title, g.ID))
				return
			}
			// Edits made on the page went to the Curator instead of being
			// overwritten; its next pass updates the page with them in.
			if errors.Is(err, errEditedThere) {
				Log("[scribe.keep_published] user=%q guide=%q: %v", user, firstNonEmpty(g.Title, g.ID), err)
				continue
			}
			failed = append(failed, p.where+": "+err.Error())
			continue
		}
		Log("[scribe.keep_published] user=%q guide=%q updated in %s", user, firstNonEmpty(g.Title, g.ID), p.where)
	}
	if len(failed) > 0 {
		Log("[scribe.keep_published] user=%q guide=%q: %s", user, firstNonEmpty(g.Title, g.ID), strings.Join(failed, "; "))
		NotifyUser(user, "Scribe", fmt.Sprintf("Keeping %q published: the update failed for %s. Open its Publish dialog to see what happened.",
			firstNonEmpty(g.Title, "Untitled guide"), strings.Join(failed, "; ")))
	}
}

// waitForPublishJob waits until no publish of the guide is running, up to
// max. False when one still is, or ctx ended.
func waitForPublishJob(ctx context.Context, guideID string, max time.Duration) bool {
	deadline := time.Now().Add(max)
	for {
		if j, ok := currentPublishJob(guideID); !ok || j.Done {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
}

// handleKeepPublished switches Keep published on a guide: POST ?id=<guide>
// with {on}. Edit rights, like publishing.
func (T *Scribe) handleKeepPublished(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	g, ownerUDB, _, canEdit, found := T.resolve(r, udb, user, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	if !canEdit {
		http.Error(w, "You need edit access to this guide to change how it is published.", http.StatusForbidden)
		return
	}
	var body struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	g.KeepPublished = body.On
	saveGuide(ownerUDB, g)
	jsonapi.Write(w, map[string]any{"keep_published": g.KeepPublished})
}
