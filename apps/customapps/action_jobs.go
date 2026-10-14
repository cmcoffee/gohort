// An app's action, run as a job that may outlast the request that started it.
//
// An action is a script behind a button: a download, an extraction, anything
// that takes as long as its input makes it take. It used to run inside the
// request under the short script limit, so a slow one was killed, and the page
// waited on a request that showed no sign of life. Now every action runs as a
// job, on a context of its own, under the background-run limits (stopped when
// it goes quiet or would fill the disk, not at a fixed time). The request waits
// a little; a quick action answers exactly as before, and a slow one answers
// {"running": true, "job": <id>} and carries on. The page then follows it on
// action-progress/<name>: its latest output line while it runs, and its
// outcome, records and result when it ends. action-cancel/<name> stops it.
//
// The ActionList an actions section renders reads that endpoint as its
// ProgressSource, so a button shows the spinner and the latest line and
// rejoins a run already going when the page is reopened. window.app.action
// follows it too, so a page's own call resolves when the job ends.
package customapps

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// actionInlineWait is how long a request waits for its action before answering
// that it is still running. Long enough that most actions answer in one go.
var actionInlineWait = 15 * time.Second

// actionOutcomeKept is how long a finished job's outcome stays readable, so a
// page reopened after the end says how it went rather than nothing.
const actionOutcomeKept = 30 * time.Minute

// actionJob is one run of one action for one person.
type actionJob struct {
	id      string
	key     string // app owner / slug / person / action: whose, and which button
	cancel  context.CancelFunc
	done    chan struct{}
	started time.Time

	mu       sync.Mutex
	progress string
	res      actionOutcome
	err      error
	ended    time.Time
}

func (j *actionJob) setProgress(line string) {
	j.mu.Lock()
	j.progress = line
	j.mu.Unlock()
}

// status is what action-progress answers for this job.
func (j *actionJob) status() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ended.IsZero() {
		line := strings.TrimSpace(j.progress)
		if line == "" {
			line = "working"
		}
		return map[string]any{"running": true, "job": j.id, "progress": line + " - " + sinceShort(j.started)}
	}
	if j.err != nil {
		return map[string]any{"done": true, "job": j.id, "error": j.err.Error(), "outcome": "Failed: " + j.err.Error()}
	}
	out := map[string]any{"done": true, "job": j.id, "outcome": j.res.Message,
		"message": j.res.Message, "saved": j.res.Saved, "records": j.res.Records}
	if j.res.Result != nil {
		out["result"] = j.res.Result
	}
	return out
}

func sinceShort(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return d.String()
	}
	return d.Truncate(time.Second).String()
}

// actionJobs holds the running and recently ended jobs, by id and by the
// button each was last started from.
var actionJobs = struct {
	sync.Mutex
	byID  map[string]*actionJob
	byKey map[string]*actionJob
}{byID: map[string]*actionJob{}, byKey: map[string]*actionJob{}}

func actionJobKey(owner, slug, uid, action string) string {
	return owner + "/" + slug + "/" + uid + "/" + action
}

// startActionJob runs work as a job and returns it. work receives the job's
// own context and a progress reporter.
func startActionJob(key string, work func(ctx context.Context, progress func(string)) (actionOutcome, error)) *actionJob {
	ctx, cancel := context.WithCancel(context.Background())
	j := &actionJob{id: UUIDv4(), key: key, cancel: cancel, done: make(chan struct{}), started: time.Now()}
	actionJobs.Lock()
	pruneActionJobs()
	actionJobs.byID[j.id] = j
	actionJobs.byKey[key] = j
	actionJobs.Unlock()
	go func() {
		defer close(j.done)
		defer cancel()
		res, err := work(ctx, j.setProgress)
		if err == nil && ctx.Err() != nil {
			err = Error("stopped")
		}
		j.mu.Lock()
		j.res, j.err, j.ended = res, err, time.Now()
		j.mu.Unlock()
	}()
	return j
}

// pruneActionJobs drops outcomes past their keeping time. Called with the lock
// held.
func pruneActionJobs() {
	for id, j := range actionJobs.byID {
		j.mu.Lock()
		old := !j.ended.IsZero() && time.Since(j.ended) > actionOutcomeKept
		j.mu.Unlock()
		if old {
			delete(actionJobs.byID, id)
			if actionJobs.byKey[j.key] == j {
				delete(actionJobs.byKey, j.key)
			}
		}
	}
}

// findActionJob is the job a progress or cancel request means: the one named
// by ?job=, else the latest started from that button. Only that person's.
func findActionJob(r *http.Request, key string) *actionJob {
	actionJobs.Lock()
	defer actionJobs.Unlock()
	if id := strings.TrimSpace(r.URL.Query().Get("job")); id != "" {
		if j, ok := actionJobs.byID[id]; ok && j.key == key {
			return j
		}
		return nil
	}
	return actionJobs.byKey[key]
}

// handleActionProgress answers action-progress/<name>: {running, progress}
// while the person's job runs, {done, outcome, message, saved, records,
// result} (or {done, error}) once it ends, {} when there is none.
func (T *CustomApps) handleActionProgress(w http.ResponseWriter, r *http.Request, owner, uid string, spec AppSpec, name string) {
	j := findActionJob(r, actionJobKey(owner, spec.Slug, uid, name))
	if j == nil {
		writeJSON(w, map[string]any{})
		return
	}
	writeJSON(w, j.status())
}

// handleActionCancel answers action-cancel/<name>: stops the person's job.
func (T *CustomApps) handleActionCancel(w http.ResponseWriter, r *http.Request, owner, uid string, spec AppSpec, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if j := findActionJob(r, actionJobKey(owner, spec.Slug, uid, name)); j != nil {
		j.cancel()
	}
	writeJSON(w, map[string]any{"ok": true})
}
