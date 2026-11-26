package mcpserver

// One app over plain HTTP, at its MCP address (/mcp/app/<slug>/), for a script
// or a server that would rather not speak MCP. It is the same door as the
// app's MCP server, opened by the same keys under the same grant: what a key
// reaches decides how it is opened, not which protocol it speaks
// (docs/design/external-access.md). The same tools, run the same way, as the
// same jobs (app_jobs.go), so a call started over one protocol is collected
// over either.
//
//	GET    tools              the app's tools, with their input schemas
//	POST   tools/<name>       call one: the body is its arguments, a JSON object.
//	                          Waits up to 20s (?wait=0..20) for the run:
//	                          200 its result, 202 the job still running
//	GET    jobs               this key's calls of the app from the last hour
//	GET    jobs/<id>          one of them (?wait=0..20 to wait for it to end)
//	DELETE jobs/<id>          stop it
//
// A tool that ran and failed answers 422, a stopped job 409, a call over the
// key's running cap 429, an action that asks to confirm first 409 (nobody is
// asked over plain HTTP). The key goes in the X-API-Key header; a sign-in
// cookie alone is refused, and nothing here answers another site's page.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/apps/customapps"
	. "github.com/cmcoffee/gohort/core"
)

// appHTTPMaxBody bounds a call's arguments.
const appHTTPMaxBody = 16 << 20

// appSubPath is what follows "/app/<slug>/" in path: "" for the app's address
// itself, "tools/<name>" or "jobs/<id>" for its HTTP API.
func appSubPath(path string) string {
	i := strings.Index(path, "/app/")
	if i < 0 {
		return ""
	}
	rest := path[i+len("/app/"):]
	j := strings.Index(rest, "/")
	if j < 0 {
		return ""
	}
	return strings.Trim(rest[j+1:], "/")
}

// refusalStatus is the HTTP status a key refusal (keyApp) stands for.
func refusalStatus(refusal string) int {
	switch {
	case strings.HasPrefix(refusal, "Unauthorized"):
		return http.StatusUnauthorized
	case strings.HasPrefix(refusal, "Not found"):
		return http.StatusNotFound
	}
	return http.StatusForbidden
}

// appHTTPCall is one request to an app's HTTP API.
type appHTTPCall struct {
	T    *MCPServer
	w    http.ResponseWriter
	r    *http.Request
	slug string
	sub  string
	tok  *AccountToken
	app  customapps.OfferingApp
}

// reply writes v as JSON with status.
func (c *appHTTPCall) reply(status int, v any) {
	c.w.Header().Set("Content-Type", "application/json")
	c.w.Header().Set("Cache-Control", "no-store")
	c.w.WriteHeader(status)
	json.NewEncoder(c.w).Encode(v)
	owner := ""
	if c.tok != nil {
		owner = c.tok.Owner
	}
	Log("[mcpserver] app %q: http %s %s -> %d (owner=%s)", c.slug, c.r.Method, c.sub, status, owner)
}

func (c *appHTTPCall) fail(status int, msg string, extra ...map[string]any) {
	out := map[string]any{"ok": false, "error": msg}
	for _, e := range extra {
		for k, v := range e {
			out[k] = v
		}
	}
	c.reply(status, out)
}

// handleAppHTTP serves the HTTP API under one app's address.
func (T *MCPServer) handleAppHTTP(w http.ResponseWriter, r *http.Request, slug, sub string) {
	c := &appHTTPCall{T: T, w: w, r: r, slug: slug, sub: sub}
	tok, app, refusal := T.keyApp(r, slug, "http "+r.Method+" "+sub)
	if refusal != "" {
		c.fail(refusalStatus(refusal), refusal)
		return
	}
	c.tok, c.app = tok, app
	head, rest, _ := strings.Cut(sub, "/")
	switch {
	case head == "tools" && rest == "":
		c.only(http.MethodGet, c.listTools)
	case head == "tools":
		c.only(http.MethodPost, func() { c.callTool(rest) })
	case head == "jobs" && rest == "":
		c.only(http.MethodGet, c.listJobs)
	case head == "jobs":
		switch r.Method {
		case http.MethodGet:
			c.getJob(rest)
		case http.MethodDelete:
			c.stopJob(rest)
		default:
			w.Header().Set("Allow", "GET, DELETE")
			c.fail(http.StatusMethodNotAllowed, "jobs/<id> takes GET (its state, ?wait=0..20) or DELETE (stop it)")
		}
	default:
		c.fail(http.StatusNotFound, fmt.Sprintf("no %q here: this app's HTTP API is tools, tools/<name>, jobs and jobs/<id>", sub))
	}
}

// only runs fn for method and refuses any other.
func (c *appHTTPCall) only(method string, fn func()) {
	if c.r.Method != method {
		c.w.Header().Set("Allow", method)
		c.fail(http.StatusMethodNotAllowed, fmt.Sprintf("%s takes %s", c.sub, method))
		return
	}
	fn()
}

// listTools is GET tools: the app's tools this key may call, as MCP lists
// them, without the result tool (jobs/<id> is its HTTP form).
func (c *appHTTPCall) listTools() {
	result := resultToolName(c.app)
	tools := []map[string]any{}
	for _, d := range allowedToolDefs(appToolDefs(c.tok, c.slug), c.tok) {
		if d["name"] == result {
			continue
		}
		tools = append(tools, map[string]any{"name": d["name"], "description": d["description"], "input_schema": d["inputSchema"]})
	}
	c.reply(http.StatusOK, map[string]any{
		"ok":    true,
		"app":   map[string]any{"slug": c.app.Slug, "name": c.app.Name, "description": c.app.Desc},
		"tools": tools,
	})
}

// waitParam is ?wait as a duration, def when absent, at most resultWait.
func (c *appHTTPCall) waitParam(def time.Duration) (time.Duration, bool) {
	v := strings.TrimSpace(c.r.URL.Query().Get("wait"))
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		c.fail(http.StatusBadRequest, "wait is a whole number of seconds, 0 to "+strconv.Itoa(int(resultWait/time.Second)))
		return 0, false
	}
	d := time.Duration(n) * time.Second
	if d > resultWait {
		d = resultWait
	}
	return d, true
}

// callTool is POST tools/<name>: the call, as a job, answered with its result
// when it ends within the wait, else with the job.
func (c *appHTTPCall) callTool(name string) {
	wait, ok := c.waitParam(appWait)
	if !ok {
		return
	}
	var def *AgentToolDef
	for i, d := range c.app.Tools {
		if d.Tool.Name == name {
			def = &c.app.Tools[i]
			break
		}
	}
	if def == nil {
		var have []string
		for _, d := range appToolDefs(c.tok, c.slug) {
			if n, _ := d["name"].(string); n != "" && n != resultToolName(c.app) {
				have = append(have, n)
			}
		}
		c.fail(http.StatusNotFound, fmt.Sprintf("%s offers no tool %q; it offers: %s", c.app.Name, name, strings.Join(have, ", ")))
		return
	}
	if !keyMayCall(c.tok, name) {
		c.fail(http.StatusForbidden, fmt.Sprintf("this key may not call %q: enable it on the key under Account > API keys > Access", name))
		return
	}
	if def.Confirmation != nil {
		c.fail(http.StatusConflict, fmt.Sprintf("%q asks to confirm before it runs (%q), and nobody is asked over plain HTTP: use it from the app's page, or over MCP from a client that can ask you", name, def.Confirmation.Prompt))
		return
	}
	args := map[string]any{}
	body := http.MaxBytesReader(c.w, c.r.Body, appHTTPMaxBody)
	dec := json.NewDecoder(body)
	dec.UseNumber()
	if err := dec.Decode(&args); err != nil && err.Error() != "EOF" {
		c.fail(http.StatusBadRequest, "the body is the tool's arguments, a JSON object: "+err.Error())
		return
	}
	if args == nil {
		args = map[string]any{}
	}
	// Numbers as the client wrote them: an app tool's inputs are strings.
	for k, v := range args {
		if n, ok := v.(json.Number); ok {
			args[k] = n.String()
		}
	}
	tool := *def
	j, err := startAppJob(c.r.Context(), c.tok, c.app, name, func(jctx context.Context) (string, error) { return tool.Handler(jctx, args) })
	if err != nil {
		c.fail(http.StatusTooManyRequests, err.Error())
		return
	}
	c.answerJob(j, wait, http.StatusAccepted)
}

// answerJob answers with j's result when it ends within wait, else its state
// with running status: 202 for a call just made, 200 for a check on a job.
func (c *appHTTPCall) answerJob(j *appJob, wait time.Duration, running int) {
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-j.done:
		case <-t.C:
		case <-c.r.Context().Done():
			return // the client left; the job goes on
		}
	}
	select {
	case <-j.done:
	default:
		url := appServerPath(c.slug) + "jobs/" + j.id
		c.w.Header().Set("Location", url)
		out := j.view()
		out["ok"], out["job_url"] = true, url
		c.reply(running, out)
		return
	}
	out := j.view()
	text, err := j.outcome()
	switch {
	case j.wasStopped():
		out["ok"], out["error"] = false, err.Error()
		c.reply(http.StatusConflict, out)
	case err != nil:
		out["ok"], out["error"] = false, err.Error()
		c.reply(http.StatusUnprocessableEntity, out)
	default:
		out["ok"], out["result"] = true, resultValue(text)
		c.reply(http.StatusOK, out)
	}
}

// resultValue is a tool's answer as JSON: its own JSON when it printed some,
// else its text as a string.
func resultValue(text string) any {
	if t := strings.TrimSpace(text); t != "" && json.Valid([]byte(t)) {
		return json.RawMessage(t)
	}
	return text
}

// getJob is GET jobs/<id>.
func (c *appHTTPCall) getJob(id string) {
	wait, ok := c.waitParam(0)
	if !ok {
		return
	}
	j, found := appJobFor(c.tok, c.slug, id)
	if !found {
		c.fail(http.StatusNotFound, fmt.Sprintf("no job %q of %s for this key: it ended more than %s ago, gohort restarted since (which stops a running job), or another key started it", id, firstNonEmptyText(c.app.Name, c.app.Slug), jobKeep))
		return
	}
	c.answerJob(j, wait, http.StatusOK)
}

// stopJob is DELETE jobs/<id>.
func (c *appHTTPCall) stopJob(id string) {
	j, found := appJobFor(c.tok, c.slug, id)
	if !found {
		c.fail(http.StatusNotFound, fmt.Sprintf("no job %q of %s for this key", id, firstNonEmptyText(c.app.Name, c.app.Slug)))
		return
	}
	if !j.running() {
		out := j.view()
		out["ok"], out["already_ended"] = true, true
		c.reply(http.StatusOK, out)
		return
	}
	j.stop()
	Log("[mcpserver] app %q: job %s (%s) stopped over HTTP (owner=%s)", j.slug, j.id, j.tool, j.owner)
	out := j.view()
	out["ok"] = true
	c.reply(http.StatusOK, out)
}

// listJobs is GET jobs: this key's calls of the app still kept, newest first.
func (c *appHTTPCall) listJobs() {
	jobs := []map[string]any{}
	for _, j := range keyAppJobs(c.tok, c.slug) {
		jobs = append(jobs, j.view())
	}
	c.reply(http.StatusOK, map[string]any{"ok": true, "jobs": jobs, "kept_for_s": int(jobKeep / time.Second)})
}

// keyAppJobs are tok's jobs of slug still kept, newest first.
func keyAppJobs(tok *AccountToken, slug string) []*appJob {
	appJobs.Lock()
	sweepAppJobs()
	var mine []*appJob
	for _, j := range appJobs.m {
		if tok != nil && j.owner == tok.Owner && j.key == keyID(tok) && strings.EqualFold(j.slug, slug) {
			mine = append(mine, j)
		}
	}
	appJobs.Unlock()
	sort.Slice(mine, func(a, b int) bool { return mine[a].started.After(mine[b].started) })
	return mine
}

// view is the job's state as JSON fields: its id and tool, when it started,
// and either how long it has run and its latest line, or how and when it
// ended.
func (j *appJob) view() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := map[string]any{"job": j.id, "tool": j.tool, "started": j.started.UTC().Format(time.RFC3339)}
	if j.ended.IsZero() {
		out["running"], out["state"] = true, "running"
		out["elapsed_s"] = int(time.Since(j.started) / time.Second)
		if j.latest != "" {
			out["now"] = j.latest
		}
		return out
	}
	state := "ended"
	switch {
	case j.stopped:
		state = "stopped"
	case j.err != nil:
		state = "failed"
	}
	out["running"], out["state"] = false, state
	out["ended"] = j.ended.UTC().Format(time.RFC3339)
	out["elapsed_s"] = int(j.ended.Sub(j.started) / time.Second)
	return out
}

// wasStopped reports whether the job ended because it was stopped.
func (j *appJob) wasStopped() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stopped
}
