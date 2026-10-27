package appscript

// What an app's scripts did when they ran for real. A script's log() lines
// went to the server log and nowhere else, and a failed data source left only
// an empty panel, so the author (or Builder, asked why the app is broken) had
// nothing to read but the code. Each live run now leaves a short record on the
// app: when, which script, how it ended, what it logged.
//
// Bounded: the newest runs, plus a tail of older failures so a burst of good
// page loads does not push the one that broke out of view. Quiet successes of
// the same script fold into one entry with a count.
//
// Private: a run another user of a shared app started keeps its outcome and
// error, never its log lines, which carry what that person put in.

import (
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// AppRun is one live run of an app's script.
type AppRun struct {
	At      string   `json:"at"`
	Kind    string   `json:"kind"`    // data | action
	Name    string   `json:"name"`    // the script's name
	Trigger string   `json:"trigger"` // page (someone asked) | schedule
	Other   bool     `json:"other_user,omitempty"`
	OK      bool     `json:"ok"`
	Error   string   `json:"error,omitempty"`
	Log     []string `json:"log,omitempty"`
	Ms      int64    `json:"ms"`
	Times   int      `json:"times"` // runs folded into this entry
}

const (
	runsTable    = "app_runs"
	runsKeep     = 20   // newest runs kept, whatever their outcome
	runsFailKeep = 10   // older failures kept past that
	runLogLines  = 40   // log() lines kept per run
	runLogWidth  = 300  // characters kept per log line
	runErrorTail = 1500 // characters kept of an error, from its end
)

var runsMu sync.Mutex

// runsStore is where an owner's app runs live: the host's bucket, beside the
// app's records. Nil before the database is up.
func runsStore(owner string) Database {
	if RootDB == nil || owner == "" {
		return nil
	}
	return UserDB(RootDB.Bucket(StoreName), owner)
}

// RecentRuns returns an app's run records, newest first.
func RecentRuns(owner, slug string) []AppRun {
	db := runsStore(owner)
	if db == nil {
		return nil
	}
	var runs []AppRun
	db.Get(runsTable, slug, &runs)
	return runs
}

// A deleted app's runs go with it.
func init() { RegisterAppSpecDeletedHook(ForgetRuns) }

// ForgetRuns drops an app's run records.
func ForgetRuns(owner, slug string) {
	if db := runsStore(owner); db != nil {
		runsMu.Lock()
		db.Unset(runsTable, slug)
		runsMu.Unlock()
	}
}

// recordRun files one run on its app.
func recordRun(owner, slug string, r AppRun) {
	db := runsStore(owner)
	if db == nil || slug == "" {
		return
	}
	runsMu.Lock()
	defer runsMu.Unlock()
	var runs []AppRun
	db.Get(runsTable, slug, &runs)
	db.Set(runsTable, slug, addRun(runs, r))
}

// addRun puts r at the head of runs (newest first) and trims the list.
func addRun(runs []AppRun, r AppRun) []AppRun {
	if r.Times < 1 {
		r.Times = 1
	}
	if len(runs) > 0 && quietTwin(runs[0], r) {
		head := runs[0]
		head.At, head.Ms, head.Times = r.At, r.Ms, head.Times+r.Times
		return append([]AppRun{head}, runs[1:]...)
	}
	all := append([]AppRun{r}, runs...)
	out := make([]AppRun, 0, runsKeep+runsFailKeep)
	failures := 0
	for i, run := range all {
		switch {
		case i < runsKeep:
			out = append(out, run)
		case !run.OK && failures < runsFailKeep:
			failures++
			out = append(out, run)
		}
	}
	return out
}

// quietTwin is whether b repeats a: the same script, started the same way,
// both succeeding without a word logged. Those fold together.
func quietTwin(a, b AppRun) bool {
	return a.OK && b.OK && len(a.Log) == 0 && len(b.Log) == 0 &&
		a.Kind == b.Kind && a.Name == b.Name && a.Trigger == b.Trigger && a.Other == b.Other
}

// RunTrace follows one live run: Track hooks its log() lines, Done files it.
type RunTrace struct {
	owner, slug string
	start       time.Time
	mu          sync.Mutex
	run         AppRun
	dropped     int
}

// Track starts a trace of j and hooks j's log() lines into it, keeping any
// OnLog the caller set. A run another user started keeps no lines.
func Track(j *Job) *RunTrace {
	t := &RunTrace{owner: j.Owner, slug: j.Slug, start: time.Now(), run: AppRun{
		Kind:    j.Kind,
		Name:    j.Name,
		Trigger: "schedule",
		Other:   j.Caller != "" && j.Caller != j.Owner,
	}}
	if j.Interactive {
		t.run.Trigger = "page"
	}
	prev := j.OnLog
	j.OnLog = func(level, msg string) {
		if prev != nil {
			prev(level, msg)
		}
		t.add(level, msg)
	}
	return t
}

func (t *RunTrace) add(level, msg string) {
	if t.run.Other {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.run.Log) >= runLogLines {
		t.dropped++
		return
	}
	line := strings.TrimSpace(msg)
	if r := []rune(line); len(r) > runLogWidth {
		line = string(r[:runLogWidth]) + "…"
	}
	if level != "" && level != "info" {
		line = level + ": " + line
	}
	t.run.Log = append(t.run.Log, line)
}

// Done files the run with how it ended.
func (t *RunTrace) Done(err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	r := t.run
	r.Log = append([]string(nil), t.run.Log...)
	if t.dropped > 0 {
		r.Log = append(r.Log, "("+strconv.Itoa(t.dropped)+" more lines not kept)")
	}
	t.mu.Unlock()
	r.At = time.Now().UTC().Format(time.RFC3339)
	r.Ms = time.Since(t.start).Milliseconds()
	r.OK = err == nil
	if err != nil {
		msg := err.Error()
		if rs := []rune(msg); len(rs) > runErrorTail {
			msg = "…" + string(rs[len(rs)-runErrorTail:])
		}
		r.Error = msg
	}
	recordRun(t.owner, t.slug, r)
}
