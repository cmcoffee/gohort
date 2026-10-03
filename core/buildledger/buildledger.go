// Package buildledger keeps the outcome of every build verification: each
// tool_def test, add_tool test_args run and app_def verify, with what failed
// and which model was serving when it did.
//
// Verification already had a record, and it was the wrong shape for this
// question. The session ledger in orchestrate holds each tool's CURRENT
// standing, replaced on every run, so "passed after six failed tests" and
// "passed first time" leave the same row behind. An app keeps only its last
// verdict. Neither can say which failures keep happening, or on which model,
// and that is what anything tuning the authoring prompts has to start from.
//
// So this ledger is append-only per target: one row per (kind, owner,
// target) holding that target's recent verifications in order. Attempts to
// green, first-try rate and the recurring failure classes are all read off
// those sequences; nothing is summarized at write time, so a better summary
// later still has the raw runs to work from.
//
// Detail and Classes are framework-authored text ONLY. A verification reaches
// real endpoints and runs real scripts, and its report carries their output;
// none of that is stored here. Whatever is later drafted from this ledger
// lands in system prompts that every user's agents read, so the ledger holds
// what the verifier concluded, never what the verified thing said.
//
// Which model and tier served the round is not known where the verdict is:
// the authoring tools see a ToolSession, not the loop. The round's step
// callback does know, and fires right after the round's tools return, so a
// verdict is recorded at once and stamped from that callback (Stamp). A path
// that never stamps leaves the model blank rather than guessing.
//
// A leaf package: it imports nothing of core. config.go wires the store; the
// authoring tools call Record; orchestrate calls Stamp and reads Report.
package buildledger

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Store is the slice of the database this package needs; core's Database
// satisfies it.
type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	Keys(table string) []string
}

// Kinds of build target.
const (
	KindTool = "tool"
	KindApp  = "app"
)

// Verdict is what one verification concluded.
type Verdict string

const (
	Pass Verdict = "pass"
	Fail Verdict = "fail"
	// Unproven is a run that found nothing wrong and could not prove the
	// thing works either: a write endpoint it would not fire, a shell tool
	// tested without cases, a read that came back empty. It is not a pass,
	// and counting it as a failure would blame the author for a check the
	// verifier declined to make.
	Unproven Verdict = "unproven"
)

// Outcome is one verification.
type Outcome struct {
	At      time.Time `json:"at"`
	Owner   string    `json:"owner"`
	Session string    `json:"session,omitempty"`
	Agent   string    `json:"agent,omitempty"`
	Kind    string    `json:"kind"`
	Target  string    `json:"target"`
	// Via names the action that verified: "tool_def test", "add_tool
	// test_args", "app_def verify".
	Via     string  `json:"via"`
	Verdict Verdict `json:"verdict"`
	// Classes are the failure kinds found, one per kind (a run with three
	// console errors has "console-error" once). Stable kebab-case tokens
	// chosen at the verifier, so two runs that failed the same way cluster.
	Classes []string `json:"classes,omitempty"`
	Detail  string   `json:"detail,omitempty"`
	// Model, Tier and Clauses describe the round that ran the verification,
	// filled by Stamp. Clauses are the framework prompt blocks live that
	// turn: what a lesson drafted from this failure would be an edit to.
	Model   string   `json:"model,omitempty"`
	Tier    string   `json:"tier,omitempty"`
	Clauses []string `json:"clauses,omitempty"`
}

// row is one target's history.
type row struct {
	Kind     string    `json:"kind"`
	Owner    string    `json:"owner"`
	Target   string    `json:"target"`
	Outcomes []Outcome `json:"outcomes"`
}

const table = "build_ledger"

// maxOutcomes bounds one target's history. A target that has been verified
// more than this many times keeps its newest runs; the episode in progress
// is what matters, and one that long is itself the finding.
const maxOutcomes = 40

// maxDetail bounds the stored summary line.
const maxDetail = 300

// pendingTTL drops an unstamped verdict's stamp slot. A path whose turn never
// stamps would otherwise hold it until a later turn on the same session
// stamped it with that turn's model, which is the one wrong answer worse than
// a blank.
const pendingTTL = 30 * time.Minute

var (
	mu    sync.Mutex
	store Store
	// pending maps a chat session to the rows holding verdicts recorded on
	// it that are waiting for Stamp.
	pending = map[string]*pendingSet{}
	now     = time.Now
)

type pendingSet struct {
	keys map[string]bool
	at   time.Time
}

// SetStore wires the database. A nil store makes Record and Stamp no-ops.
func SetStore(s Store) {
	mu.Lock()
	store = s
	mu.Unlock()
}

func rowKey(kind, owner, target string) string {
	return kind + "|" + owner + "|" + target
}

// Record appends one verification. Owner, Kind and Target are required; an
// outcome missing any of them names nothing and is dropped. Recorded with a
// Session and no Model, it waits for that session's next Stamp.
func Record(o Outcome) {
	o.Owner = strings.TrimSpace(o.Owner)
	o.Kind = strings.TrimSpace(o.Kind)
	o.Target = strings.TrimSpace(o.Target)
	if o.Owner == "" || o.Kind == "" || o.Target == "" {
		return
	}
	if o.At.IsZero() {
		o.At = now()
	}
	o.Classes = normClasses(o.Classes)
	o.Detail = clip(strings.TrimSpace(o.Detail), maxDetail)

	mu.Lock()
	defer mu.Unlock()
	if store == nil {
		return
	}
	key := rowKey(o.Kind, o.Owner, o.Target)
	var r row
	if !store.Get(table, key, &r) {
		r = row{Kind: o.Kind, Owner: o.Owner, Target: o.Target}
	}
	r.Outcomes = append(r.Outcomes, o)
	if n := len(r.Outcomes); n > maxOutcomes {
		r.Outcomes = append([]Outcome(nil), r.Outcomes[n-maxOutcomes:]...)
	}
	store.Set(table, key, r)

	prunePending()
	if o.Session != "" && o.Model == "" && o.Tier == "" {
		p := pending[o.Session]
		if p == nil {
			p = &pendingSet{keys: map[string]bool{}}
			pending[o.Session] = p
		}
		p.keys[key] = true
		p.at = now()
	}
}

// Stamp fills in the serving model, tier and live prompt clauses on every
// verdict the session recorded since its last stamp. Called from a turn's
// step callback, which fires after the round's tools have run, so the
// verdicts it finds are that round's. Cheap when nothing is waiting.
func Stamp(session, model, tier string, clauses []string) {
	if session == "" || (model == "" && tier == "") {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	p := pending[session]
	if p == nil {
		return
	}
	delete(pending, session)
	if store == nil {
		return
	}
	for key := range p.keys {
		var r row
		if !store.Get(table, key, &r) {
			continue
		}
		changed := false
		for i := range r.Outcomes {
			o := &r.Outcomes[i]
			if o.Session != session || o.Model != "" || o.Tier != "" {
				continue
			}
			o.Model, o.Tier = model, tier
			o.Clauses = append([]string(nil), clauses...)
			changed = true
		}
		if changed {
			store.Set(table, key, r)
		}
	}
}

// prunePending drops stamp slots nobody claimed. Caller holds mu.
func prunePending() {
	cut := now().Add(-pendingTTL)
	for s, p := range pending {
		if p.at.Before(cut) {
			delete(pending, s)
		}
	}
}

// rows reads every target's history. Caller holds mu.
func rows() []row {
	if store == nil {
		return nil
	}
	var out []row
	for _, k := range store.Keys(table) {
		var r row
		if store.Get(table, k, &r) && len(r.Outcomes) > 0 {
			out = append(out, r)
		}
	}
	return out
}

func normClasses(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
