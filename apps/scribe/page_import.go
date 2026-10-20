package scribe

// Starting a guide from a page that is already published (docs.LinkPage).
//
// A page that existed before Scribe had no way in: importing it as HTML made
// a guide with no idea where it came from, so its first publish made a second
// page and the first drifted on its own. Taken in from its link, the guide
// records the page as its published copy, with what was read as the copy's
// baseline: publishing updates that page, and edits made there later come
// back through the Curator like any other (published_edits.go).
//
// It runs as a job, shown in the dialog and on the Monitor: a page read
// through an integration is a model run, tens of seconds.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

// importJobPrefix keys an import in the publish job table, apart from the
// guides' own publishes.
const importJobPrefix = "import:"

// importPlace is somewhere a page can be taken in from.
type importPlace struct {
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
	Title  string `json:"title"`
	Group  string `json:"group,omitempty"`
}

// importPlaces is the destinations this user can take a page in from: the
// built-in ones that are set up, and their own publishing targets.
func importPlaces(ctx context.Context, user string) []importPlace {
	var out []importPlace
	for _, d := range docs.PublishDestinations(user) {
		if d.Available && !strings.HasSuffix(d.Kind, ":") && docs.CanLinkPages(d.Kind) {
			out = append(out, importPlace{Kind: d.Kind, Title: d.Label})
		}
	}
	for _, s := range docs.PublishTargetSpecs(ctx, user) {
		if strings.HasPrefix(s.Kind, "target:") {
			out = append(out, importPlace{Kind: s.Kind, Target: s.Target.ID, Title: s.Target.Title, Group: s.Target.Group})
		}
	}
	return out
}

// handleImportPage is GET the places, POST {kind, target, link,
// keep_published} to start an import.
func (T *Scribe) handleImportPage(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"places": importPlaces(r.Context(), user)})
	case http.MethodPost:
		var body struct {
			Kind          string `json:"kind"`
			Target        string `json:"target"`
			Link          string `json:"link"`
			KeepPublished bool   `json:"keep_published"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var where string
		for _, p := range importPlaces(r.Context(), user) {
			if p.Kind == body.Kind && p.Target == body.Target {
				where = p.Title
			}
		}
		if where == "" {
			http.Error(w, "That is not one of the places you can take a page in from.", http.StatusNotFound)
			return
		}
		if strings.TrimSpace(body.Link) == "" {
			http.Error(w, "Paste the page's address.", http.StatusBadRequest)
			return
		}
		id := newID()
		// Not on the request's context: the read outlives the request.
		ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
		job := &publishJob{Target: where, Started: time.Now(), cancel: cancel,
			owner: user, guideID: importJobPrefix + id, importing: true}
		publishJobsMu.Lock()
		publishJobs[job.guideID] = job
		publishJobsMu.Unlock()
		go T.importPage(ctx, udb, user, where, body.Kind, body.Target, body.Link, body.KeepPublished, job)
		writeJSON(w, map[string]any{"job": id})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// importPage reads the page and makes the guide, reporting on job.
func (T *Scribe) importPage(ctx context.Context, udb Database, user, where, kind, target, link string, keep bool, job *publishJob) {
	ctx = docs.WithPublishSteps(ctx, job.step)
	ctx = docs.WithPublishActivity(ctx, job.activity)
	g, err := T.guideFromPage(ctx, udb, user, where, kind, target, link, keep)
	publishJobsMu.Lock()
	defer publishJobsMu.Unlock()
	job.Done, job.Ended, job.Now = true, time.Now(), ""
	if job.cancel != nil {
		job.cancel()
	}
	switch {
	case job.Stopped:
		job.Message = "Stopped before the page was taken in."
	case err != nil:
		job.Message = err.Error()
	default:
		job.OK, job.GuideID, job.URL = true, g.ID, firstNonEmpty(g.Published[0].URL)
		job.Message = "Took in " + g.Title + " from " + where + ", " + countOf(len(g.Sections), "section") + ". Publishing it there updates that page."
		Log("[scribe.import] user=%q took in %q from %s as guide %s", user, g.Title, where, g.ID)
	}
}

// guideFromPage takes in the page at link and saves the guide made from it,
// with the page as its published copy and what was read as that copy's
// baseline.
func (T *Scribe) guideFromPage(ctx context.Context, udb Database, user, where, kind, target, link string, keep bool) (Guide, error) {
	lp, err := docs.LinkPage(ctx, user, kind, target, link)
	if err != nil {
		return Guide{}, err
	}
	if ctx.Err() != nil {
		return Guide{}, errors.New("stopped")
	}
	md := strings.TrimSpace(lp.Markdown)
	if md == "" {
		return Guide{}, errors.New("the page was read, but it came back empty, so there is nothing to start a guide from")
	}
	title := firstNonEmpty(lp.Title, lp.Record.Title)
	// A page's own top heading is its title, not a section.
	if lines := strings.SplitN(md, "\n", 2); strings.HasPrefix(lines[0], "# ") {
		title = firstNonEmpty(title, strings.TrimSpace(strings.TrimPrefix(lines[0], "# ")))
		if len(lines) > 1 {
			md = strings.TrimSpace(lines[1])
		} else {
			md = ""
		}
	}
	g := newDocument(user, firstNonEmpty(title, "Imported page"), md, false)
	rec := lp.Record
	rec.Kind = kind
	rec.At = now()
	rec.Title = firstNonEmpty(rec.Title, g.Title)
	rec.TargetTitle = firstNonEmpty(rec.TargetTitle, where)
	g.Published = []docs.PublishRecord{rec}
	g.KeepPublished = keep
	g = saveGuideRev(udb, g, "Taken in from "+where)
	saveBaseline(udb, g.ID, kind, lp.Copy)
	docs.PublishStep(ctx, "Made the guide %q, %s", g.Title, countOf(len(g.Sections), "section"))
	return g, nil
}

// handleImportPageJob reports an import: GET ?job=<id>.
func (T *Scribe) handleImportPageJob(w http.ResponseWriter, r *http.Request, user string) {
	publishJobsMu.Lock()
	j, ok := publishJobs[importJobPrefix+strings.TrimSpace(r.URL.Query().Get("job"))]
	var snap publishJob
	if ok {
		snap = *j
	}
	publishJobsMu.Unlock()
	if !ok || snap.owner != user {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{
		"target": snap.Target, "done": snap.Done, "ok": snap.OK, "message": snap.Message,
		"guide_id": snap.GuideID, "steps": snap.Steps, "now": snap.Now, "stopped": snap.Stopped,
		"elapsed": int(time.Since(snap.Started).Seconds()),
	})
}

// handleImportPageStop stops an import: POST ?job=<id>.
func (T *Scribe) handleImportPageStop(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	publishJobsMu.Lock()
	defer publishJobsMu.Unlock()
	j, ok := publishJobs[importJobPrefix+strings.TrimSpace(r.URL.Query().Get("job"))]
	if !ok || j.owner != user {
		http.NotFound(w, r)
		return
	}
	stopping := !j.Done && j.cancel != nil
	if stopping {
		j.Stopped, j.Now = true, "stopping"
		j.cancel()
	}
	writeJSON(w, map[string]any{"stopping": stopping})
}

// countOf is "1 section" or "3 sections".
func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return itoa(n) + " " + noun + "s"
}

// handlePublishLink points a guide at a page already published somewhere:
// POST ?id=<guide> {kind, target, link}. The page becomes the guide's copy at
// that destination (in place of any it had there), with what was read as its
// baseline, so the next update puts the guide on that page. Edit rights, like
// publishing. Runs as the guide's publish job, which the Publish dialog
// follows.
func (T *Scribe) handlePublishLink(w http.ResponseWriter, r *http.Request, udb Database, user string) {
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
		http.Error(w, "You need edit access to change where this guide is published.", http.StatusForbidden)
		return
	}
	var body struct {
		Kind   string `json:"kind"`
		Target string `json:"target"`
		Link   string `json:"link"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var where string
	for _, p := range importPlaces(r.Context(), user) {
		if p.Kind == body.Kind && p.Target == body.Target {
			where = p.Title
		}
	}
	if where == "" {
		http.Error(w, "That is not one of the places you can link a page from.", http.StatusNotFound)
		return
	}
	if strings.TrimSpace(body.Link) == "" {
		http.Error(w, "Paste the page's address.", http.StatusBadRequest)
		return
	}
	if j, running := currentPublishJob(g.ID); running && !j.Done {
		http.Error(w, "This guide is already being published to "+j.Target+".", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	job := &publishJob{Target: where, Started: time.Now(), cancel: cancel,
		owner: user, guideID: g.ID, title: g.Title, linking: true}
	publishJobsMu.Lock()
	publishJobs[g.ID] = job
	publishJobsMu.Unlock()
	go T.linkGuide(ctx, ownerUDB, user, g.ID, where, body.Kind, body.Target, body.Link, job)
	writeJSON(w, map[string]any{"started": true, "target": where})
}

// linkGuide reads the page and records it as the guide's copy there.
func (T *Scribe) linkGuide(ctx context.Context, udb Database, user, guideID, where, kind, target, link string, job *publishJob) {
	ctx = docs.WithPublishSteps(ctx, job.step)
	ctx = docs.WithPublishActivity(ctx, job.activity)
	rec, err := func() (docs.PublishRecord, error) {
		lp, err := docs.LinkPage(ctx, user, kind, target, link)
		if err != nil {
			return docs.PublishRecord{}, err
		}
		if ctx.Err() != nil {
			return docs.PublishRecord{}, errors.New("stopped")
		}
		g, ok := loadGuide(udb, guideID)
		if !ok {
			return docs.PublishRecord{}, errors.New("the guide is gone")
		}
		rec := lp.Record
		rec.Kind = kind
		rec.At = now()
		rec.Title = firstNonEmpty(rec.Title, lp.Title, g.Title)
		rec.TargetTitle = firstNonEmpty(rec.TargetTitle, where)
		g.Published = docs.UpsertPublishRecord(g.Published, rec)
		saveGuideRev(udb, g, "Linked to a page in "+where)
		saveBaseline(udb, guideID, kind, lp.Copy)
		return rec, nil
	}()
	publishJobsMu.Lock()
	defer publishJobsMu.Unlock()
	job.Done, job.Ended, job.Now = true, time.Now(), ""
	if job.cancel != nil {
		job.cancel()
	}
	switch {
	case job.Stopped:
		job.Message = "Stopped before the guide was linked."
	case err != nil:
		job.Message = err.Error()
	default:
		job.OK, job.URL = true, rec.URL
		job.Message = "Linked to " + firstNonEmpty(rec.Title, "the page") + " in " + where + ". The next update puts this guide on that page, in place of what it says now."
		Log("[scribe.link] user=%q linked guide %s to %s %s", user, guideID, kind, rec.ExternalID)
	}
}
