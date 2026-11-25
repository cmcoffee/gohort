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
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// actionInlineWait is how long a request waits for its action before answering
// that it is still running. Long enough that most actions answer in one go.
var actionInlineWait = 15 * time.Second

// actionOutcomeKept is how long a finished job's outcome stays readable, so a
// page reopened after the end says how it went rather than nothing.
const actionOutcomeKept = 30 * time.Minute

// appWork says whose a job is and what it is doing, for the live pill: the
// person it runs for, the app, and the work in words ("extract", "asking its
// agent").
type appWork struct {
	UID, Slug, AppName, What, Kind string
}

// actionJob is one run of one action (or one ask) for one person.
type actionJob struct {
	id      string
	key     string // app owner / slug / person / action: whose, and which button
	name    string // the action's name, or "ask"
	inputs  string // what the caller sent, so a repeat of the same call joins it
	work    appWork
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
func startActionJob(key string, info appWork, work func(ctx context.Context, progress func(string)) (actionOutcome, error)) *actionJob {
	j, _ := startOrJoinJob(key, "", "", info, work)
	return j
}

// startOrJoinJob starts work as a job, or, when the same call (key and inputs)
// is already running, hands back that job: a page that is reopened and calls
// again, or a button pressed twice, follows the run already going instead of
// starting a second. A call with other inputs starts its own, so a page that
// runs one action for each of several items still runs them all. joined says
// which.
func startOrJoinJob(key, name, inputs string, info appWork, work func(ctx context.Context, progress func(string)) (actionOutcome, error)) (j *actionJob, joined bool) {
	actionJobs.Lock()
	pruneActionJobs()
	for _, run := range actionJobs.byID {
		if run.key != key || run.inputs != inputs {
			continue
		}
		run.mu.Lock()
		going := run.ended.IsZero()
		run.mu.Unlock()
		if going {
			actionJobs.Unlock()
			return run, true
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	j = &actionJob{id: UUIDv4(), key: key, name: name, inputs: inputs, work: info, cancel: cancel, done: make(chan struct{}), started: time.Now()}
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
	return j, false
}

// trackWork records work that runs in the request rather than as a job of its
// own (a co-author proposal), so it shows in the live pill and Stop reaches it.
// cancel stops it; the returned finish records how it ended.
func trackWork(key string, info appWork, cancel context.CancelFunc) (finish func(error)) {
	j := &actionJob{id: UUIDv4(), key: key, work: info, cancel: cancel, done: make(chan struct{}), started: time.Now()}
	actionJobs.Lock()
	pruneActionJobs()
	actionJobs.byID[j.id] = j
	actionJobs.byKey[key] = j
	actionJobs.Unlock()
	return func(err error) {
		j.mu.Lock()
		j.err, j.ended = err, time.Now()
		j.mu.Unlock()
		close(j.done)
	}
}

// liveAppWork is every running app job as the live pill shows it: the app and
// the work, with its latest line, in the label (masked for anyone but the
// person it runs for, since a line a script printed can name what it is
// working on), the kind and elapsed time as the status, the app's page to go
// to, and Stop.
func (T *CustomApps) liveAppWork() []LiveEntry {
	actionJobs.Lock()
	jobs := make([]*actionJob, 0, len(actionJobs.byID))
	for _, j := range actionJobs.byID {
		jobs = append(jobs, j)
	}
	actionJobs.Unlock()
	var out []LiveEntry
	for _, j := range jobs {
		j.mu.Lock()
		ended, line := !j.ended.IsZero(), strings.TrimSpace(j.progress)
		j.mu.Unlock()
		if ended {
			continue
		}
		name := strings.TrimSpace(j.work.AppName)
		if name == "" {
			name = j.work.Slug
		}
		label := name + ": " + j.work.What
		if line != "" {
			label += " - " + line
		}
		out = append(out, LiveEntry{
			ID:        "app-" + j.id,
			Label:     label,
			App:       name,
			Status:    j.work.Kind + " - " + sinceShort(j.started),
			Owner:     j.work.UID,
			URL:       T.WebPath() + "/" + j.work.Slug + "/",
			CancelURL: T.WebPath() + "/_app/job-cancel?id=" + j.id,
		})
	}
	return out
}

// handleJobCancel is the live pill's Stop on an app job: POST
// _app/job-cancel?id=<job>. Only the person the job runs for may stop it.
func handleJobCancel(c *jsonapi.Call, _ struct{}) (any, error) {
	actionJobs.Lock()
	j := actionJobs.byID[strings.TrimSpace(c.R.URL.Query().Get("id"))]
	actionJobs.Unlock()
	if j == nil || j.work.UID != c.User {
		return nil, jsonapi.NotFound("404 page not found")
	}
	j.cancel()
	return map[string]any{"ok": true}, nil
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

// actionProgress answers action-progress/<name>: {running, progress} while
// the person's job runs, {done, outcome, message, saved, records, result} (or
// {done, error}) once it ends, {} when there is none.
func (T *CustomApps) actionProgress(a *appCall) (any, error) {
	j := findActionJob(a.R, actionJobKey(a.owner, a.spec.Slug, a.User, a.R.PathValue("name")))
	if j == nil {
		return map[string]any{}, nil
	}
	return j.status(), nil
}

// cancelAction answers action-cancel/<name>: stops the person's job.
func (T *CustomApps) cancelAction(a *appCall, _ struct{}) (any, error) {
	if j := findActionJob(a.R, actionJobKey(a.owner, a.spec.Slug, a.User, a.R.PathValue("name"))); j != nil {
		j.cancel()
	}
	return map[string]any{"ok": true}, nil
}

// appWorkPrefix is the start of the key of every job one person runs in one app.
func appWorkPrefix(owner, slug, uid string) string {
	return actionJobKey(owner, slug, uid, "")
}

// workRow is one job as window.app.work lists it: what it is and its status.
func (j *actionJob) workRow() map[string]any {
	row := j.status()
	row["kind"], row["name"], row["started"] = j.work.Kind, j.name, j.started
	return row
}

// appWorkList answers work: the person's jobs in this app, running or ended
// within actionOutcomeKept, newest first, so a page reopened after leaving
// finds what it started and how it went.
func (T *CustomApps) appWorkList(a *appCall) (any, error) {
	prefix := appWorkPrefix(a.owner, a.spec.Slug, a.User)
	actionJobs.Lock()
	pruneActionJobs()
	var jobs []*actionJob
	for _, j := range actionJobs.byID {
		if strings.HasPrefix(j.key, prefix) {
			jobs = append(jobs, j)
		}
	}
	actionJobs.Unlock()
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].started.After(jobs[k].started) })
	rows := []map[string]any{}
	for _, j := range jobs {
		rows = append(rows, j.workRow())
	}
	return map[string]any{"items": rows}, nil
}

// appWorkStatus answers work/<job>: that job's status, the person's own in
// this app only; {} when there is none.
func (T *CustomApps) appWorkStatus(a *appCall) (any, error) {
	actionJobs.Lock()
	j := actionJobs.byID[a.R.PathValue("job")]
	actionJobs.Unlock()
	if j == nil || !strings.HasPrefix(j.key, appWorkPrefix(a.owner, a.spec.Slug, a.User)) {
		return map[string]any{}, nil
	}
	return j.workRow(), nil
}
