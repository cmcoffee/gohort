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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
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

// runPublishJob publishes req to kind in the background as g's publish job,
// reporting its steps, and files rec (filled in with where it landed) on the
// guide when it succeeds. It does not wait: the job is what the Publish
// dialog follows, and rejoins when it opens mid-run.
func runPublishJob(r *http.Request, g Guide, ownerUDB Database, user, kind, where string, req docs.PublishRequest, rec docs.PublishRecord) {
	job := &publishJob{Target: where, Started: time.Now()}
	publishJobsMu.Lock()
	publishJobs[g.ID] = job
	publishJobsMu.Unlock()
	// Not on the request's context: the publish outlives the request, and a
	// closed modal must not cancel a post halfway through.
	ctx := docs.WithPublishSteps(context.WithoutCancel(r.Context()), job.step)
	go func() {
		res, err := docs.PublishDocument(ctx, user, kind, req)
		publishJobsMu.Lock()
		defer publishJobsMu.Unlock()
		job.Done, job.Ended = true, time.Now()
		if err != nil {
			job.Message = err.Error()
			return
		}
		job.OK, job.URL = true, res.URL
		job.Message = chFirst(res.Label, "Published to "+where+".")
		cur, ok := loadGuide(ownerUDB, g.ID)
		if !ok {
			return
		}
		rec.ExternalID, rec.URL, rec.Version, rec.At = res.ExternalID, res.URL, res.Version, now()
		cur.Published = docs.UpsertPublishRecord(cur.Published, rec)
		saveGuideRev(ownerUDB, cur, "Published to "+where)
	}()
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
func (T *Scribe) handlePublishTo(w http.ResponseWriter, r *http.Request, udb Database, user string) {
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
		http.Error(w, "You need edit access to publish this guide.", http.StatusForbidden)
		return
	}
	var body struct {
		Kind    string            `json:"kind"`
		Target  string            `json:"target"`
		Title   string            `json:"title"`
		Answers map[string]string `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var spec docs.PublishTargetSpec
	for _, s := range docs.PublishTargetSpecs(r.Context(), user) {
		if s.Kind == body.Kind && s.Target.ID == body.Target {
			spec = s
		}
	}
	if spec.Kind == "" {
		http.Error(w, "That publishing target is not one of yours any more.", http.StatusNotFound)
		return
	}
	if missing := docs.MissingAnswers(spec.Fields, body.Answers); len(missing) > 0 {
		http.Error(w, "Still needed: "+strings.Join(missing, ", "), http.StatusBadRequest)
		return
	}
	if j, running := currentPublishJob(g.ID); running && !j.Done {
		http.Error(w, "This guide is already being published to "+j.Target+".", http.StatusConflict)
		return
	}
	title := chFirst(strings.TrimSpace(body.Title), g.Title)
	prev, _ := docs.FindPublishRecord(g.Published, spec.Kind)
	runPublishJob(r, g, ownerUDB, user, spec.Kind, spec.Target.Title,
		docs.PublishRequest{
			Target: spec.Target.ID, Title: title, Doc: publishDoc(g), Answers: body.Answers,
			ExternalID: prev.ExternalID, Version: prev.Version,
		},
		docs.PublishRecord{Kind: spec.Kind, Target: spec.Target.ID, TargetTitle: spec.Target.Title, Title: title, Answers: body.Answers})
	writeJSON(w, map[string]any{"started": true, "target": spec.Target.Title})
}

// handlePublishJob reports a guide's publish: running (with its elapsed
// time), or how it ended. GET ?id=<guide>.
func (T *Scribe) handlePublishJob(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if _, _, _, _, found := T.resolve(r, udb, user, id); !found {
		http.NotFound(w, r)
		return
	}
	j, ok := currentPublishJob(id)
	if !ok {
		writeJSON(w, map[string]any{"none": true})
		return
	}
	writeJSON(w, map[string]any{
		"target": j.Target, "done": j.Done, "ok": j.OK, "message": j.Message, "url": j.URL,
		"elapsed": int(time.Since(j.Started).Seconds()),
		"took":    fmt.Sprintf("%ds", int(j.Ended.Sub(j.Started).Seconds())),
		"steps":   j.Steps,
	})
}

func chFirst(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
