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
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// actionInlineWait is how long a request waits for its action before answering
// that it is still running. Long enough that most actions answer in one go.
var actionInlineWait = 15 * time.Second

// actionOutcomeKept is how long a finished job's outcome stays readable, so a
// page reopened after the end says how it went rather than nothing.
const actionOutcomeKept = 30 * time.Minute

// toldOutcomeKept is how long the outcome of work the person was told about
// stays readable: the notice links to it, and a notice is read hours later.
// In memory, so a restart still loses it.
const toldOutcomeKept = 24 * time.Hour

// appWork says whose a job is and what it is doing, for the live pill: the
// person it runs for, the app, and the work in words ("extract", "asking its
// agent").
type appWork struct {
	UID, Slug, AppName, What, Kind string
	// Owner is the app's owner, and Tell asks for a notice to UID when the
	// work ends with nobody watching it (tellIfUnwatched). Told is set when
	// the run sent one of its own, so a second is not sent.
	Owner string
	Tell  bool
	Told  *atomic.Bool
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
	watchers int       // requests waiting on it now
	seen     time.Time // when a page last asked how it was going
	told     bool      // a notice went out about it, linking to its outcome
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

// startActionJob runs work as a job of its own and returns it. work receives
// the job's own context and a progress reporter.
//
// It never joins a run already going, as a second press of a button does: a
// call from an agent or an MCP client is a request of its own, with its own
// inputs and its own caller waiting on its own answer. Joined by key alone,
// concurrent calls with other inputs were handed the first call's result and
// never ran (seen over MCP: 39 of 40 parallel calls answered with call 0's).
func startActionJob(key string, info appWork, work func(ctx context.Context, progress func(string)) (actionOutcome, error)) *actionJob {
	j, _ := startOrJoinJob(key, "", "own:"+UUIDv4(), info, work)
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
		// Only Stop cancels a job's context: the person ended it themselves.
		// Said as that, whatever the run says: a script killed mid-run
		// failed as one that printed no JSON, which read as a broken script
		// rather than one somebody stopped.
		stopped := ctx.Err() != nil
		if stopped {
			err = Error("stopped")
		}
		j.mu.Lock()
		j.res, j.err, j.ended = res, err, time.Now()
		unwatched := j.watchers == 0 && time.Since(j.seen) > unwatchedAfter
		j.mu.Unlock()
		if unwatched && !stopped {
			j.tellIfUnwatched()
		}
	}()
	return j, false
}

// unwatchedAfter is how long since a page last asked about a job before it
// counts as nobody watching: a following page asks every two seconds.
var unwatchedAfter = 10 * time.Second

// waitFor counts a request waiting on the job as watching it while fn runs.
func (j *actionJob) waitFor(fn func()) {
	j.mu.Lock()
	j.watchers++
	j.mu.Unlock()
	defer func() {
		j.mu.Lock()
		j.watchers--
		j.seen = time.Now()
		j.mu.Unlock()
	}()
	fn()
}

// markSeen records that a page asked how the job is going.
func (j *actionJob) markSeen() {
	j.mu.Lock()
	j.seen = time.Now()
	j.mu.Unlock()
}

// tellIfUnwatched tells the person their work has ended, when it was started
// from the app's page and ended with nobody watching it, as a native app's
// run does: they left, and should hear how it went without going back to
// look. Not when the run told them itself; a job they stopped is not asked.
func (j *actionJob) tellIfUnwatched() {
	w := j.work
	if !w.Tell || (w.Told != nil && w.Told.Load()) {
		return
	}
	j.mu.Lock()
	res, err := j.res, j.err
	j.mu.Unlock()
	app := strings.TrimSpace(w.AppName)
	if app == "" {
		app = w.Slug
	}
	var title, body string
	switch {
	case w.Kind == "ask" && err == nil:
		title, body = "Your answer from "+app+" is ready", "Open the app to read it."
	case w.Kind == "ask":
		title, body = app+" could not answer", err.Error()
	case err == nil:
		title, body = w.What+" finished", res.Message
	default:
		title, body = w.What+" failed", err.Error()
	}
	j.mu.Lock()
	j.told = true
	j.mu.Unlock()
	appscript.TellPerson(w.UID, w.Owner, w.Slug, title, body, "/apps/"+w.Slug+"/?work="+url.QueryEscape(j.id))
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
		keep := actionOutcomeKept
		if j.told {
			keep = toldOutcomeKept
		}
		old := !j.ended.IsZero() && time.Since(j.ended) > keep
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
	j.markSeen()
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
	row["kind"], row["name"], row["started"], row["what"] = j.work.Kind, j.name, j.started, j.work.What
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
	j.markSeen()
	return j.workRow(), nil
}

// workResultJS is served on every app page, around its frames. Opened from a
// notice (?work=<job>), it shows below the header how that work ended, drawn
// by gohort rather than the app, so it works for every app whatever its page
// remembers: an action's message, why it failed, or an ask's answer in full.
// Work still running is followed until it ends; an outcome no longer kept
// says so. Dismiss takes it away and the job off the address.
func workResultJS(base string) string {
	b, _ := json.Marshal(base)
	return `<script>(function(){var id=null;try{id=new URLSearchParams(location.search).get("work");}catch(e){}if(!id)return;var base=` + string(b) + `;` +
		`function place(bar){var tries=0;(function go(){var h=document.querySelector(".ui-page-header");if(h&&h.parentNode){h.parentNode.insertBefore(bar,h.nextSibling);return;}if(++tries<40){setTimeout(go,100);return;}var r=document.getElementById("ui-root")||document.body;r.insertBefore(bar,r.firstChild);})();}` +
		`var bar=document.createElement("div");bar.setAttribute("role","status");bar.setAttribute("data-work-result",id);` +
		`bar.style.cssText="display:flex;gap:12px;align-items:flex-start;margin:8px 0;padding:10px 12px;border:1px solid var(--border);border-radius:6px;background:var(--bg-1)";` +
		`var words=document.createElement("div");words.style.cssText="flex:1;min-width:0";var head=document.createElement("div");head.style.fontWeight="600";var body=document.createElement("div");body.style.cssText="white-space:pre-wrap;margin-top:4px;max-height:40vh;overflow:auto";` +
		`words.appendChild(head);words.appendChild(body);var close=document.createElement("button");close.type="button";close.className="ui-row-btn";close.textContent="Dismiss";` +
		`close.onclick=function(){bar.remove();try{var u=new URL(location.href);u.searchParams.delete("work");history.replaceState(history.state,"",u.pathname+u.search+u.hash);}catch(e){}};` +
		`bar.appendChild(words);bar.appendChild(close);head.textContent="Looking for how it went";place(bar);` +
		`function say(h,b){head.textContent=h;body.textContent=b||"";}` +
		`(function poll(){fetch(base+"work/"+encodeURIComponent(id),{credentials:"same-origin"}).then(function(r){return r.json();}).then(function(s){` +
		`var what=(s&&s.what)||"It";` +
		`if(!s||(!s.running&&!s.done)){say("That result is no longer kept","The outcome of work you were told about is kept for a day, and not across a restart of gohort.");return;}` +
		`if(s.running){say(what+" is still running",s.progress||"");setTimeout(poll,2000);return;}` +
		`if(s.error){say(s.kind==="ask"?"The app's agent could not answer":what+" failed",s.error);return;}` +
		`if(s.kind==="ask"){say("Your answer",(s.result&&s.result.text)||"");return;}` +
		`say(what+" finished",s.message||"");` +
		`}).catch(function(){say("Could not read how it went","");});})();})();</script>`
}
