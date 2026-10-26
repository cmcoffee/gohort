// Quick updates of a guide already published to one of the person's own
// targets (apps/publish, targets.go), with the answers it was published with.
// A first publish goes through the Publisher chat instead, which settles the
// target's questions with the person.
//
// A target's publish is a model pass that can take a minute, so it runs as a
// background job rather than on the request: it survives the modal closing,
// the modal shows it alive (spinner, elapsed seconds) while it runs, and a
// modal opened mid-run rejoins it. The outcome is kept for a while so a modal
// opened after the end says how it went instead of nothing.
package scribe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// publishJob is one guide's publish in flight, or its outcome.
type publishJob struct {
	Target  string    `json:"target"`
	Started time.Time `json:"started"`
	Done    bool      `json:"done"`
	OK      bool      `json:"ok"`
	Message string    `json:"message,omitempty"`
	URL     string    `json:"url,omitempty"`
	Ended   time.Time `json:"ended,omitempty"`
	// Steps are what the publish did, as it did them (docs.PublishStep):
	// shown under the spinner while it runs and kept after, so a publish
	// that ends "done" without doing what was meant leaves something to read.
	Steps []publishJobStep `json:"steps,omitempty"`
	// Now is what it is doing at this moment ("round 2 · Thinking · 14s"),
	// beside the spinner. Cleared when it ends: a finished publish is doing
	// nothing.
	Now string `json:"now,omitempty"`
	// Stopped is a publish someone stopped with the dialog's Stop.
	Stopped bool `json:"stopped,omitempty"`
	// cancel stops it. Closing the dialog does not: the publish outlives
	// the page, and Stop is how a person ends it.
	cancel context.CancelFunc
	// For the Monitor's row (publish_live.go): whose guide, which, and
	// whether it started on its own (Keep published) or was asked for.
	owner, guideID, title string
	background            bool
	// importing is a page being taken in (page_import.go) rather than a
	// guide going out; GuideID is the guide it became, once it has.
	importing bool
	GuideID   string `json:"guide_id,omitempty"`
	// linking is the guide being pointed at a page already published
	// somewhere (page_import.go), run as the guide's own job.
	linking bool
	// distilling is a guide being distilled into another (distill.go);
	// Target is the receiving guide.
	distilling bool
}

// publishJobStep is one step and when it happened, in seconds from the start.
type publishJobStep struct {
	At   int    `json:"at"`
	Text string `json:"text"`
}

// publishJobMaxSteps bounds a job's steps: a run that loops is still shown,
// its start and its latest, without growing without end.
const publishJobMaxSteps = 200

// step records one step of j. Safe from the publish's goroutines.
func (j *publishJob) step(text string) {
	publishJobsMu.Lock()
	defer publishJobsMu.Unlock()
	if len(j.Steps) >= publishJobMaxSteps {
		j.Steps = append(j.Steps[:publishJobMaxSteps/2], j.Steps[len(j.Steps)-publishJobMaxSteps/2+1:]...)
	}
	j.Steps = append(j.Steps, publishJobStep{At: int(time.Since(j.Started).Seconds()), Text: text})
}

// activity records what j is doing now. Safe from the publish's goroutines.
func (j *publishJob) activity(now string) {
	publishJobsMu.Lock()
	defer publishJobsMu.Unlock()
	if !j.Done {
		j.Now = now
	}
}

// publishRun is one publish of a guide to one place: what to send, where,
// and the record to file on the guide when it lands.
type publishRun struct {
	guideID  string
	ownerUDB Database
	user     string
	kind     string
	where    string // the place's name, for the dialog and the record's note
	req      docs.PublishRequest
	rec      docs.PublishRecord
	// background is a publish nobody asked for just now: Keep published
	// following a change. The Monitor paints it as work that acted alone.
	background bool
}

// runPublishJob publishes in the background as the guide's publish job. It
// does not wait: the job is what the Publish dialog follows, and rejoins when
// it opens mid-run.
func runPublishJob(r *http.Request, g Guide, ownerUDB Database, user, kind, where string, req docs.PublishRequest, rec docs.PublishRecord) {
	p := publishRun{guideID: g.ID, ownerUDB: ownerUDB, user: user, kind: kind, where: where, req: req, rec: rec}
	// Not on the request's context: the publish outlives the request, and a
	// closed modal must not cancel a post halfway through.
	job, ctx := p.start(context.WithoutCancel(r.Context()))
	go p.do(ctx, job)
}

// start makes this the guide's publish job, so the dialog follows it, and
// returns the context it runs on: parent's, ended early by the job's Stop.
func (p publishRun) start(parent context.Context) (*publishJob, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	job := &publishJob{Target: p.where, Started: time.Now(), cancel: cancel,
		owner: p.user, guideID: p.guideID, title: p.req.Title, background: p.background}
	publishJobsMu.Lock()
	publishJobs[p.guideID] = job
	publishJobsMu.Unlock()
	return job, ctx
}

// errPublishStopped is a publish ended by the dialog's Stop.
var errPublishStopped = errors.New("stopped")

// stopPublishJob stops the guide's running publish. False when none is
// running.
func stopPublishJob(guideID string) bool {
	publishJobsMu.Lock()
	defer publishJobsMu.Unlock()
	j, ok := publishJobs[guideID]
	if !ok || j.Done || j.cancel == nil {
		return false
	}
	j.Stopped = true
	j.Now = "stopping"
	j.cancel()
	return true
}

// do publishes and waits for the outcome, reporting its steps and activity on
// job, and files the record (filled in with where it landed) on the guide
// when it succeeds. Returns the publish's error.
func (p publishRun) do(ctx context.Context, job *publishJob) error {
	ctx = docs.WithPublishSteps(ctx, job.step)
	ctx = docs.WithPublishActivity(ctx, job.activity)
	res, err := docs.PublishDocument(ctx, p.user, p.kind, p.req)
	publishJobsMu.Lock()
	defer publishJobsMu.Unlock()
	job.Done, job.Ended, job.Now = true, time.Now(), ""
	if job.cancel != nil {
		job.cancel()
	}
	if job.Stopped {
		// Said as what it is: a stop can land after the write, so the page
		// may already carry the change.
		job.OK = false
		job.Message = "Stopped before it finished. Anything it had already changed in " + p.where + " stays there."
		return errPublishStopped
	}
	if err != nil {
		job.Message = err.Error()
		return err
	}
	job.OK, job.URL = true, res.URL
	job.Message = chFirst(res.Label, "Published to "+p.where+".")
	cur, ok := loadGuide(p.ownerUDB, p.guideID)
	if !ok {
		return nil
	}
	rec := p.rec
	rec.ExternalID, rec.URL, rec.Version, rec.At = res.ExternalID, res.URL, res.Version, now()
	// How the destination wrote the document's navigation, for the next
	// update; kept from the record when this publish named none.
	if res.NavLinks != nil || res.NavContents != nil {
		rec.NavLinks, rec.NavContents = res.NavLinks, res.NavContents
	} else if p.req.ExternalID != "" {
		rec.NavLinks, rec.NavContents = p.req.NavLinks, p.req.NavContents
	}
	// An update that came back without an address still went to the page
	// this guide already had: keep it, or the next publish makes a new one.
	if strings.TrimSpace(rec.ExternalID) == "" {
		rec.ExternalID = p.req.ExternalID
	}
	if strings.TrimSpace(rec.URL) == "" {
		rec.URL = p.rec.URL
	}
	cur.Published = docs.UpsertPublishRecord(cur.Published, rec)
	saveGuideRev(p.ownerUDB, cur, "Published to "+p.where)
	return nil
}

// republishOf is the publish that updates the copy prev records, with the
// guide as it stands now: the same page, title and answers as last time.
func republishOf(g Guide, ownerUDB Database, user string, prev docs.PublishRecord) publishRun {
	where := firstNonEmpty(prev.TargetTitle, prev.Kind)
	return publishRun{guideID: g.ID, ownerUDB: ownerUDB, user: user, kind: prev.Kind, where: where,
		req: docs.PublishRequest{
			Target:     prev.Target,
			Title:      prev.Title,
			Doc:        publishDoc(g, ownerUDB),
			ExternalID: prev.ExternalID,
			Version:    prev.Version,
			Answers:    prev.Answers,
			// How the destination wrote its navigation last time.
			NavLinks:    prev.NavLinks,
			NavContents: prev.NavContents,
		},
		rec: prev,
	}
}

// publishJobKeep is how long an outcome stays for a modal that opens late.
const publishJobKeep = 15 * time.Minute

var (
	publishJobsMu sync.Mutex
	publishJobs   = map[string]*publishJob{} // by guide id
)

func currentPublishJob(guideID string) (publishJob, bool) {
	publishJobsMu.Lock()
	defer publishJobsMu.Unlock()
	j, ok := publishJobs[guideID]
	if !ok {
		return publishJob{}, false
	}
	if j.Done && time.Since(j.Ended) > publishJobKeep {
		delete(publishJobs, guideID)
		return publishJob{}, false
	}
	out := *j
	out.Steps = append([]publishJobStep(nil), j.Steps...)
	return out, true
}

// handlePublishTo starts a publish to one target: POST ?id=<guide> with
// {kind, target, title, answers}.
func (T *Scribe) handlePublishTo(c *jsonapi.Call, body struct {
	Kind    string            `json:"kind"`
	Target  string            `json:"target"`
	Title   string            `json:"title"`
	Answers map[string]string `json:"answers"`
}) (any, error) {
	r, udb, user := c.R, c.UDB, c.User
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	g, ownerUDB, _, canEdit, found := T.resolve(r, udb, user, id)
	if !found {
		return nil, errNoGuide
	}
	if !canEdit {
		return nil, jsonapi.Forbidden("You need edit access to publish this guide.")
	}
	var spec docs.PublishTargetSpec
	for _, s := range docs.PublishTargetSpecs(r.Context(), user) {
		if s.Kind == body.Kind && s.Target.ID == body.Target {
			spec = s
		}
	}
	if spec.Kind == "" {
		return nil, jsonapi.NotFound("That publishing target is not one of yours any more.")
	}
	if missing := docs.MissingAnswers(spec.Fields, body.Answers); len(missing) > 0 {
		return nil, jsonapi.BadRequest("Still needed: " + strings.Join(missing, ", "))
	}
	if j, running := currentPublishJob(g.ID); running && !j.Done {
		return nil, jsonapi.Status(http.StatusConflict, "This guide is already being published to "+j.Target+".")
	}
	title := chFirst(strings.TrimSpace(body.Title), g.Title)
	prev, _ := docs.FindPublishRecord(g.Published, spec.Kind)
	runPublishJob(r, g, ownerUDB, user, spec.Kind, spec.Target.Title,
		docs.PublishRequest{
			Target: spec.Target.ID, Title: title, Doc: publishDoc(g, ownerUDB), Answers: body.Answers,
			ExternalID: prev.ExternalID, Version: prev.Version,
			NavLinks: prev.NavLinks, NavContents: prev.NavContents,
		},
		docs.PublishRecord{Kind: spec.Kind, Target: spec.Target.ID, TargetTitle: spec.Target.Title, Title: title, Answers: body.Answers})
	return map[string]any{"started": true, "target": spec.Target.Title}, nil
}

// handlePublishJob reports a guide's publish: running (with its elapsed
// time), or how it ended. GET ?id=<guide>.
func (T *Scribe) handlePublishJob(c *jsonapi.Call) (any, error) {
	r, udb, user := c.R, c.UDB, c.User
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if _, _, _, _, found := T.resolve(r, udb, user, id); !found {
		return nil, errNoGuide
	}
	j, ok := currentPublishJob(id)
	if !ok {
		return map[string]any{"none": true}, nil
	}
	return map[string]any{
		"target": j.Target, "done": j.Done, "ok": j.OK, "message": j.Message, "url": j.URL,
		"elapsed": int(time.Since(j.Started).Seconds()),
		"took":    fmt.Sprintf("%ds", int(j.Ended.Sub(j.Started).Seconds())),
		"steps":   j.Steps,
		"now":     j.Now,
		"stopped": j.Stopped,
		"linked":  j.linking,
	}, nil
}

// handlePublishStop stops the guide's running publish: POST ?id=<guide>.
// Edit rights, like starting one.
func (T *Scribe) handlePublishStop(c *jsonapi.Call, _ struct{}) (any, error) {
	r, udb, user := c.R, c.UDB, c.User
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	_, _, _, canEdit, found := T.resolve(r, udb, user, id)
	if !found {
		return nil, errNoGuide
	}
	if !canEdit {
		return nil, jsonapi.Forbidden("You need edit access to this guide to stop its publish.")
	}
	return map[string]any{"stopping": stopPublishJob(id)}, nil
}

func chFirst(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
