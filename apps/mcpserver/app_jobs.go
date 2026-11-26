package mcpserver

// An app's tool called over MCP, run as a job its client checks back on.
//
// A call used to run inside its request and end with it. A client that gave
// up waiting (Claude Code waits 60 seconds for the first byte of an HTTP tool
// call unless its server entry says otherwise) or dropped its connection
// stopped the action it had asked for, and the app's page then recorded a
// script that printed no JSON. Now every call runs as a job on a context of
// its own, and the request waits a short while for it (appWait). A call that
// ends in time answers exactly as before. One that does not answers with its
// job, which the app's result tool (<slug>_result) collects, waiting a short
// while each time it is called, or stops.
//
// As ask_agent's jobs (ask_jobs.go): a job belongs to the key that started it
// and no other key reads it, it is kept in memory for jobKeep after it ends so
// its answer can be fetched again, and a restart drops it, as it stops the run.
//
// A key has at most appJobCap jobs running at once. A run that outlives its
// client would otherwise pile up behind a client that keeps retrying.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/apps/customapps"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/textutil"
)

// appWait is how long an app tool call waits for its run before answering
// with a job, well inside the 60 seconds a client waits for a first byte.
// appStopWait is how long a stop waits for the run to end. appJobCap is how
// many jobs one key may have running at once.
var (
	appWait     = 20 * time.Second
	appStopWait = 10 * time.Second
	appJobCap   = 8
)

// appJob is one app tool call made over MCP.
type appJob struct {
	id, owner, key, slug, tool string
	started                    time.Time
	done                       chan struct{}
	cancel                     context.CancelFunc

	mu      sync.Mutex
	latest  string // the latest line the run printed
	ended   time.Time
	stopped bool
	text    string
	err     error
}

var appJobs = struct {
	sync.Mutex
	m map[string]*appJob
}{m: map[string]*appJob{}}

// running reports whether the job has not ended.
func (j *appJob) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.ended.IsZero()
}

func (j *appJob) hear(line string) {
	if line = strings.TrimSpace(line); line == "" {
		return
	}
	j.mu.Lock()
	j.latest = line
	j.mu.Unlock()
}

// sweepAppJobs drops the jobs that ended more than jobKeep ago. Called with
// appJobs held.
func sweepAppJobs() {
	for id, j := range appJobs.m {
		j.mu.Lock()
		gone := !j.ended.IsZero() && time.Since(j.ended) > jobKeep
		j.mu.Unlock()
		if gone {
			delete(appJobs.m, id)
		}
	}
}

// startAppJob runs one app tool call as a job of tok's: run gets a context
// that the caller going away does not end, only a stop. Refused when tok
// already has appJobCap jobs running.
func startAppJob(ctx context.Context, tok *AccountToken, app customapps.OfferingApp, tool string, run func(context.Context) (string, error)) (*appJob, error) {
	if tok == nil {
		return nil, fmt.Errorf("no key")
	}
	key := keyID(tok)
	appJobs.Lock()
	sweepAppJobs()
	var going []string
	for _, j := range appJobs.m {
		if j.key == key && j.owner == tok.Owner && j.running() {
			going = append(going, fmt.Sprintf("%s (%s)", j.id, j.tool))
		}
	}
	if len(going) >= appJobCap {
		appJobs.Unlock()
		sort.Strings(going)
		return nil, fmt.Errorf("this key already has %d app calls running, the most it may: %s. Collect one with its app's result tool (%s for this app), or stop one by calling that tool with its job and stop: true, then call again", len(going), strings.Join(going, ", "), resultToolName(app))
	}
	jctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	j := &appJob{id: newJobID(), owner: tok.Owner, key: key, slug: app.Slug, tool: tool, started: time.Now(), done: make(chan struct{}), cancel: cancel}
	jctx = customapps.WithCallerKey(customapps.WithCallProgress(jctx, j.hear), key)
	appJobs.m[j.id] = j
	appJobs.Unlock()
	go func() {
		defer cancel()
		text, err := run(jctx)
		j.mu.Lock()
		j.text, j.err, j.ended = text, err, time.Now()
		stopped := j.stopped
		j.mu.Unlock()
		close(j.done)
		if took := time.Since(j.started); took > appWait {
			Log("[mcpserver] app %q: job %s (%s) ended after %s (owner=%s, stopped=%v, error=%v)", j.slug, j.id, j.tool, took.Round(time.Second), j.owner, stopped, err != nil)
		}
	}()
	return j, nil
}

// appJobFor is the job id of app's, when tok started it.
func appJobFor(tok *AccountToken, slug, id string) (*appJob, bool) {
	id = strings.TrimSpace(id)
	if tok == nil || id == "" {
		return nil, false
	}
	appJobs.Lock()
	defer appJobs.Unlock()
	sweepAppJobs()
	j, ok := appJobs.m[id]
	if !ok || j.owner != tok.Owner || j.key != keyID(tok) || !strings.EqualFold(j.slug, slug) {
		return nil, false
	}
	return j, true
}

// await is the call's answer when the run ends within wait, or a note that it
// is still working and how to check back. A client that goes away first gets
// its context's error; the run goes on.
func (j *appJob) await(ctx context.Context, wait time.Duration, result string) (string, error) {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-j.done:
		return j.outcome()
	case <-ctx.Done():
		return "", ctx.Err()
	case <-t.C:
		return j.stillWorking(result), nil
	}
}

// outcome is what an ended run answered.
func (j *appJob) outcome() (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.stopped {
		return "", fmt.Errorf("%s (job %s) was stopped", j.tool, j.id)
	}
	return j.text, j.err
}

// stillWorking is what a call says while its run goes on: the job, how long
// it has been going, the latest line it printed, and how to get the answer or
// stop it.
func (j *appJob) stillWorking(result string) string {
	j.mu.Lock()
	now := j.latest
	j.mu.Unlock()
	line := fmt.Sprintf("Still working on it (job %s, %s so far", j.id, time.Since(j.started).Round(time.Second))
	if now != "" {
		line += "; now: " + textutil.Clip(strings.Join(strings.Fields(now), " "), 200)
	}
	return line + fmt.Sprintf(`). Call %s with job "%s" to get the result; each call waits up to %s for it. To stop it, call %s with job "%s" and stop: true.`, result, j.id, resultWait, result, j.id)
}

// stop ends the run and waits a short while for it to finish ending.
func (j *appJob) stop() {
	j.mu.Lock()
	if j.ended.IsZero() {
		j.stopped = true
	}
	j.mu.Unlock()
	j.cancel()
	select {
	case <-j.done:
	case <-time.After(appStopWait):
	}
}

// resultToolName is app's result tool: <slug>_result, or <slug>_app_result
// when the app has a tool of its own by the first name.
func resultToolName(app customapps.OfferingApp) string {
	name := RefToolSlug(app.Slug + "_result")
	for _, d := range app.Tools {
		if d.Tool.Name == name {
			return RefToolSlug(app.Slug + "_app_result")
		}
	}
	return name
}

// resultToolDef is app's result tool as a tools/list entry.
func resultToolDef(app customapps.OfferingApp) map[string]any {
	name := firstNonEmptyText(app.Name, app.Slug)
	return map[string]any{
		"name": resultToolName(app),
		"description": fmt.Sprintf("App: %s. The result of one of this app's calls that was still running when it answered: it gave a job id. Waits up to %s each time it is called and says if the run is still going. With stop: true, stops the run instead. With no job, lists this key's calls of the app from the last hour, running or ended, for a client that lost the id.",
			name, resultWait),
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"job":  map[string]any{"type": "string", "description": "The job id the call answered with. Leave it out to list this key's jobs of the app."},
				"stop": map[string]any{"type": "boolean", "description": "true to stop the run instead of waiting for it."},
			},
		},
	}
}

// appResult is app's result tool: the answer of a job tok started, waiting a
// short while when it is still going, or, with stop, the job stopped.
func appResult(ctx context.Context, tok *AccountToken, app customapps.OfferingApp, args map[string]any) (string, error) {
	id, _ := args["job"].(string)
	if strings.TrimSpace(id) == "" {
		if truthy(args["stop"]) {
			return "", fmt.Errorf("stop needs the job to stop: call this with no arguments to list this key's jobs of the app")
		}
		return listAppJobs(tok, app), nil
	}
	j, ok := appJobFor(tok, app.Slug, id)
	if !ok {
		return "", fmt.Errorf("no job %q of %s for this key: it ended more than %s ago, gohort restarted since (which stops a running job), or another key started it", strings.TrimSpace(id), firstNonEmptyText(app.Name, app.Slug), jobKeep)
	}
	if truthy(args["stop"]) {
		if !j.running() {
			text, err := j.outcome()
			if err != nil {
				return "", fmt.Errorf("it had already ended: %w", err)
			}
			return "It had already ended. Its result:\n\n" + text, nil
		}
		j.stop()
		Log("[mcpserver] app %q: job %s (%s) stopped by its client (owner=%s)", j.slug, j.id, j.tool, j.owner)
		if j.running() {
			return fmt.Sprintf("Stopping %s (job %s): it has not finished ending yet.", j.tool, j.id), nil
		}
		return fmt.Sprintf("Stopped %s (job %s).", j.tool, j.id), nil
	}
	return j.await(ctx, resultWait, resultToolName(app))
}

// appJobListMax bounds a listing of a key's jobs.
const appJobListMax = 20

// listAppJobs is tok's jobs of app still kept (running, or ended within
// jobKeep), newest first: what a client that lost a job's id needs to find
// it again. A call whose client went away before it answered left a job the
// client was never told about.
func listAppJobs(tok *AccountToken, app customapps.OfferingApp) string {
	mine := keyAppJobs(tok, app.Slug)
	name := firstNonEmptyText(app.Name, app.Slug)
	if len(mine) == 0 {
		return fmt.Sprintf("No calls of %s from this key in the last %s.", name, jobKeep)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This key's calls of %s in the last %s, newest first:\n", name, jobKeep)
	for i, j := range mine {
		if i == appJobListMax {
			fmt.Fprintf(&b, "(and %d older)\n", len(mine)-appJobListMax)
			break
		}
		fmt.Fprintf(&b, "- job %s: %s, %s\n", j.id, j.tool, j.state())
	}
	fmt.Fprintf(&b, "Call %s with a job to get its result.", resultToolName(app))
	return b.String()
}

// state is how the job stands, in words: running and for how long with its
// latest line, or how it ended and when.
func (j *appJob) state() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ended.IsZero() {
		s := fmt.Sprintf("running for %s", time.Since(j.started).Round(time.Second))
		if j.latest != "" {
			s += " (now: " + textutil.Clip(strings.Join(strings.Fields(j.latest), " "), 120) + ")"
		}
		return s
	}
	how := "ended"
	switch {
	case j.stopped:
		how = "stopped"
	case j.err != nil:
		how = "failed"
	}
	return fmt.Sprintf("%s %s ago, after %s", how, time.Since(j.ended).Round(time.Second), j.ended.Sub(j.started).Round(time.Second))
}

// keyMayCall reports whether tok may call name: as its scope says, and an
// app's result tool whenever tok may call any of that app's tools. The result
// tool reads only tok's own jobs, of tools it was allowed to start, so it
// follows them as agent_result follows ask_agent (toolScope). A key narrowed
// to an app's tools names them one by one, never the result tool, so a long
// call's job could be started and not collected.
func keyMayCall(tok *AccountToken, name string) bool {
	if tok.AllowsTool(toolScope(name)) {
		return true
	}
	for _, a := range reachedApps(tok, "") {
		if name != resultToolName(a) {
			continue
		}
		for _, d := range a.Tools {
			if tok.AllowsTool(toolScope(d.Tool.Name)) {
				return true
			}
		}
	}
	return false
}

// runAppJob runs one of app's tools for tok as a job and answers as the call
// would, or with the job when it is still going after appWait.
func runAppJob(ctx context.Context, tok *AccountToken, app customapps.OfferingApp, d AgentToolDef, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	j, err := startAppJob(ctx, tok, app, d.Tool.Name, func(jctx context.Context) (string, error) { return d.Handler(jctx, args) })
	if err != nil {
		return "", err
	}
	return j.await(ctx, appWait, resultToolName(app))
}

// truthy reads a boolean argument as a client may send it.
func truthy(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "true", "yes", "1":
			return true
		}
	}
	return false
}

func firstNonEmptyText(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
