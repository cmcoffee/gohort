// A record of every time a correction check or a guardrail fired, with what
// the judge said on each reading, so false positives can be reviewed and the
// checks tuned against evidence rather than against memory.
//
// The per-session ⚠ trail already says a correction happened, and the server
// log says what the judge thought. Neither can answer the question tuning asks:
// across the whole deployment, which checks fire, which pre-filter arm put the
// turn in front of the judge, how often the closer reading disagreed with the
// fast one, and what the retry produced. The trail is per conversation and has
// to be found first; the log is text, rotates, and carries no identity. This is
// one table, time-ordered, read by the admin.
//
// Recording is a side effect of a turn, never part of it. Every write goes
// through one background writer with a bounded queue: a full queue drops the
// record, a failing store is recovered, and nothing here ever waits on disk
// while a reply is waiting on it.
//
// What is kept from the conversation is deliberately small: the sentence the
// check quoted and about three hundred characters of the reply. From a private
// turn (an agent forced private, Private mode on, an incognito session) nothing
// textual is kept at all, only the shape of what happened: which check, which
// arm, which verdicts, counts.

package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/sections"
	"github.com/cmcoffee/gohort/core/ui"
)

const (
	// firingTable holds one record per firing, keyed by firingKey so the keys
	// sort in time order and the prune can stop at the first one it keeps.
	firingTable = "judge_records"
	// firingRetention is how long a record is kept. Long enough to see a
	// pattern across a few weeks of ordinary use, short enough that the
	// reply excerpts it holds do not become an archive nobody asked for.
	firingRetention = 30 * 24 * time.Hour
	// firingExcerptMax is the reply excerpt, in characters. Enough to read
	// the sentence in its context; not the reply.
	firingExcerptMax = 300
	// firingQueueMax bounds what may wait for the writer. A burst past it is
	// dropped rather than queued without limit, because the alternative is a
	// review aid holding memory the turns need.
	firingQueueMax = 512
	// firingPruneEvery is how often a write also prunes. Pruning on write
	// rather than in a maintenance pass keeps the table bounded without
	// anybody having to press a button, and the throttle keeps a busy hour
	// from listing the table on every conviction.
	firingPruneEvery = time.Hour
	// firingListMax caps the recent-firings table on the admin view.
	firingListMax = 200
)

// Sources of a firing.
const (
	firingSourceJudge     = "judge"     // the end-of-turn claim judge
	firingSourceCheck     = "check"     // a phrase-list or framework correction check
	firingSourceGuardrail = "guardrail" // a user guardrail, judged by the warden
)

// Outcomes of a firing.
const (
	firingActed      = "acted"      // a correction was spent or a block stood
	firingLetStand   = "let stand"  // flagged, and the reply went out anyway
	firingOverturned = "overturned" // a closer look cleared what the first flagged
	firingSteered    = "steered"    // a guardrail steered the turn rather than refusing it
)

// firingReading is one reading of one judge: the fast one, the confirming
// one, a warden's verdict, an appeal's re-check.
type firingReading struct {
	// Verdict is the reading's answer in the judge's own terms: KEPT, UNKEPT,
	// MACHINERY, VIOLATE, COMPLY, or NO OPINION when it could not answer.
	Verdict string `json:"verdict"`
	// Claim is the sentence this reading quoted. Empty on a private turn.
	Claim string `json:"claim,omitempty"`
	// Why is the reading's one-line reason. Empty on a private turn: it is
	// the judge describing the reply, and describing it is holding it.
	Why string `json:"why,omitempty"`
}

// firingRecord is one firing.
//
// Stage-5 tuning reads Arm, First, Confirm, Overturned and Retry together: an
// arm whose convictions are routinely overturned, or whose corrected retries
// come back clean only because the retry said less, is the arm to narrow.
type firingRecord struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
	// Kind is what happened, in the diag trail's own words
	// ("unkept-claim-corrected", "giveup-retried", "turn-judge-overturned"),
	// or "guardrail:<hook>" for a guardrail.
	Kind string `json:"kind"`
	// Finding is what was found, independent of what was done about it:
	// "unkept-claim" whether it was corrected, let stand or overturned. The
	// admin view counts and rates by it, because a rate over Kind would put
	// every overturn in a kind of its own and every other kind at zero.
	Finding string `json:"finding"`
	Source  string `json:"source"`
	Outcome string `json:"outcome"`
	// Arm is which pre-filter arm put the turn in front of the judge. Empty
	// for checks that run on every round and for guardrails.
	Arm        string         `json:"arm,omitempty"`
	First      firingReading  `json:"first"`
	Confirm    *firingReading `json:"confirm,omitempty"`
	Overturned bool           `json:"overturned"`
	// Claim is the quoted sentence the firing was about.
	Claim string `json:"claim,omitempty"`
	// Detail is the trail's own account, for checks that quote no sentence.
	Detail string `json:"detail,omitempty"`
	// Reply is an excerpt of the reply that was judged.
	Reply string `json:"reply,omitempty"`
	// Retry says what the correction produced: whether the next reading of
	// the retried reply passed, and an excerpt of that reply.
	Retry      string `json:"retry,omitempty"`
	RetryReply string `json:"retry_reply,omitempty"`
	// Hook, Rule and RuleID identify a guardrail firing. RuleID is a short
	// hash of the rule text, kept even when the text is not, so a private
	// agent's firings still group by rule.
	Hook   string `json:"hook,omitempty"`
	Rule   string `json:"rule,omitempty"`
	RuleID string `json:"rule_id,omitempty"`

	AgentID   string `json:"agent_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	User      string `json:"user,omitempty"`
	Owner     string `json:"owner,omitempty"`
	Session   string `json:"session,omitempty"`
	// Surface is where the turn ran: web, channel, scheduled, or the run
	// kind for a delegated one (dispatch, machine, pipeline, task).
	Surface string `json:"surface,omitempty"`
	// Private marks a record from which every piece of text was withheld.
	Private bool `json:"private,omitempty"`

	// The shape of the evidence the judge saw, for an export that can be
	// turned into a regression test. Labels only: a failed call keeps its
	// "[FAILED]" mark and loses the error text.
	ToolCalls     []string `json:"tool_calls,omitempty"`
	ToolCallCount int      `json:"tool_call_count,omitempty"`
	ToolErrors    int      `json:"tool_errors,omitempty"`
	Delivered     int      `json:"delivered,omitempty"`
	Backgrounded  bool     `json:"backgrounded,omitempty"`
	Unattended    bool     `json:"unattended,omitempty"`
	GivenEstimate string   `json:"given_estimate,omitempty"`
	PriorTurnWork []string `json:"prior_turn_work,omitempty"`
	PriorWork     int      `json:"prior_work,omitempty"`
	PriorReports  int      `json:"prior_reports,omitempty"`
	CatalogTools  int      `json:"catalog_tools,omitempty"`
}

// firingKey is a record's id: a fixed-width UTC stamp, so keys sort in time
// order, plus a short suffix so two firings in one nanosecond stay two.
func firingKey(at time.Time) string {
	return firingStamp(at) + "-" + strconv.FormatUint(uint64(firingSeq.next()), 36)
}

// firingStamp is the time half of a key, also what the prune compares with.
func firingStamp(at time.Time) string {
	return at.UTC().Format("20060102T150405.000000000")
}

var firingSeq seqCounter

type seqCounter struct {
	mu sync.Mutex
	n  uint32
}

func (c *seqCounter) next() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n
}

// firingExcerpt is the short excerpt a record keeps: markers stripped,
// whitespace folded, cut at firingExcerptMax.
func firingExcerpt(s string) string {
	s = strings.Join(strings.Fields(StripMetaTags(s)), " ")
	return truncateObs(s, firingExcerptMax)
}

// firingToolLabel keeps a call's label and its failure mark, and drops the
// error text: that is tool output, and it can carry anything.
func firingToolLabel(c string) string {
	if i := strings.Index(c, " [FAILED"); i >= 0 {
		return c[:i] + " [FAILED]"
	}
	return c
}

// ruleFingerprint groups a guardrail's firings without keeping its text.
func ruleFingerprint(rule string) string {
	if strings.TrimSpace(rule) == "" {
		return ""
	}
	h := fnv.New32a()
	h.Write([]byte(normalizeRuleText(rule)))
	return fmt.Sprintf("%08x", h.Sum32())
}

// --- the writer ------------------------------------------------------------

// firingWrite is one queued change to the table.
type firingWrite struct {
	db    Database
	apply func(Database)
	done  chan struct{} // a flush marker: closed when everything before it is written
}

// firingWriter serializes every write. One goroutine, first in first out, so
// a record rewritten as its outcome becomes known lands in the order the
// rewrites happened and the last one wins.
var firingWriter struct {
	once      sync.Once
	ch        chan firingWrite
	mu        sync.Mutex
	lastPrune time.Time
}

func startFiringWriter() {
	firingWriter.once.Do(func() {
		firingWriter.ch = make(chan firingWrite, firingQueueMax)
		go func() {
			for w := range firingWriter.ch {
				if w.done != nil {
					close(w.done)
					continue
				}
				runFiringWrite(w)
			}
		}()
	})
}

// runFiringWrite applies one write. A store that panics costs the record and
// nothing else.
func runFiringWrite(w firingWrite) {
	defer func() {
		if r := recover(); r != nil {
			Debug("[judge-records] write failed: %v", r)
		}
	}()
	w.apply(w.db)
	firingWriter.mu.Lock()
	due := time.Since(firingWriter.lastPrune) >= firingPruneEvery
	if due {
		firingWriter.lastPrune = time.Now()
	}
	firingWriter.mu.Unlock()
	if due {
		pruneFirings(w.db, time.Now())
	}
}

// enqueueFiring hands a write to the writer, or drops it when the queue is
// full. Never blocks: this is called from inside a turn.
func enqueueFiring(db Database, apply func(Database)) {
	if db == nil || apply == nil {
		return
	}
	startFiringWriter()
	select {
	case firingWriter.ch <- firingWrite{db: db, apply: apply}:
	default:
		Debug("[judge-records] queue full: a firing record was dropped")
	}
}

// saveFiring queues a snapshot of rec. A copy, so the caller may go on
// changing its own.
func saveFiring(db Database, rec firingRecord) {
	enqueueFiring(db, func(db Database) { db.Set(firingTable, rec.ID, rec) })
}

// flushFirings waits until everything queued before it has been written, or
// the timeout passes. For tests and for a reader that wants to see its own
// writes; nothing on a turn's path calls it.
func flushFirings(timeout time.Duration) bool {
	startFiringWriter()
	done := make(chan struct{})
	select {
	case firingWriter.ch <- firingWrite{done: done}:
	case <-time.After(timeout):
		return false
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// pruneFirings drops every record older than the retention window. Keys sort
// by time, so it stops at the first one it keeps.
func pruneFirings(db Database, now time.Time) int {
	if db == nil {
		return 0
	}
	keys := db.Keys(firingTable)
	sort.Strings(keys)
	cutoff := firingStamp(now.Add(-firingRetention))
	n := 0
	for _, k := range keys {
		if k >= cutoff {
			break
		}
		db.Unset(firingTable, k)
		n++
	}
	if n > 0 {
		Debug("[judge-records] pruned %d record(s) older than %d days", n, int(firingRetention/(24*time.Hour)))
	}
	return n
}

// loadFirings reads every record in the window, oldest first.
func loadFirings(db Database, now time.Time) []firingRecord {
	if db == nil {
		return nil
	}
	keys := db.Keys(firingTable)
	sort.Strings(keys)
	cutoff := firingStamp(now.Add(-firingRetention))
	out := make([]firingRecord, 0, len(keys))
	for _, k := range keys {
		if k < cutoff {
			continue // not pruned yet; outside the window all the same
		}
		var rec firingRecord
		if db.Get(firingTable, k, &rec) {
			out = append(out, rec)
		}
	}
	return out
}

// --- who, where, and whether any text may be kept --------------------------

// keepsNoText reports whether this turn is one nothing textual may be kept
// from. Every source of privacy a turn can have, ORed, because each one is a
// promise to somebody and a record that honours three of four breaks the
// fourth: the agent forced private, the Private toggle on this send, an
// incognito session, and a turn whose network connector was switched to
// private mid-flight (which is also how a delegated run inherits its caller's
// Private mode).
func (t *chatTurn) keepsNoText(ctx context.Context) bool {
	if t == nil {
		return true
	}
	if t.privateMode || agentForcesPrivate(t.agent) || t.incognitoSession() {
		return true
	}
	for _, c := range []context.Context{ctx, t.ctx} {
		if c == nil {
			continue
		}
		if conn := NetworkConnectorFromContext(c); conn != nil && !conn.Allowed() {
			return true
		}
	}
	return false
}

// firingSurface names where a run happened, from the run the context carries.
func firingSurface(kind string, live bool) string {
	switch kind {
	case "chat":
		return "web"
	case "channel":
		return "channel"
	case "scheduled", "standing":
		return "scheduled"
	case "":
		if live {
			return "web"
		}
		return "background"
	}
	return kind
}

// firingBase is a record with the turn's identity filled in and nothing else.
func (t *chatTurn) firingBase(ctx context.Context) firingRecord {
	at := time.Now()
	rec := firingRecord{ID: firingKey(at), At: at}
	if t == nil {
		rec.Private = true
		return rec
	}
	if ctx == nil {
		ctx = t.ctx
	}
	rec.AgentID, rec.AgentName = t.agent.ID, t.agent.Name
	rec.User, rec.Owner = t.user, t.ownerUser
	rec.Session = t.chatSessionID()
	if rec.Session == "" {
		rec.Session = t.diagSessionID
	}
	runKind := ""
	if t.app != nil {
		if id := parentRunFromCtx(ctx); id != "" {
			if r := t.app.runsRegistry().Get(id); r != nil {
				snap := r.Snapshot()
				runKind = snap.Kind
				if rec.Session == "" {
					rec.Session = snap.SessionID
				}
			}
		}
	}
	rec.Surface = firingSurface(runKind, t.sse != nil)
	rec.Private = t.keepsNoText(ctx)
	return rec
}

// firingDB is where this turn's firings are filed: the orchestrate app's own
// store, deployment-wide, because the reader is the admin tuning the checks
// rather than any one owner.
func (t *chatTurn) firingDB() Database {
	if t == nil || t.app == nil {
		return nil
	}
	return t.app.DB
}

// --- the claim judge ---------------------------------------------------------

// firingRecorder follows one loop's judge. One per loop because the retry a
// correction asks for is judged by the same loop, and pairing a conviction
// with what its retry produced is what makes the record worth reading.
type firingRecorder struct {
	mu   sync.Mutex
	db   Database
	base func() firingRecord
	// pending is the last conviction the loop corrected, waiting for the
	// reading of its retried reply.
	pending *firingRecord
}

// claimJudge is the turn's end-of-turn judge with its firings recorded.
//
// A wrapper rather than a change to the judge, so the judge stays a pure
// reading and the record stays a side effect: whatever the recorder does, the
// verdict it hands back is the one the judge reached.
func (t *chatTurn) claimJudge(ctx context.Context) TurnClaimJudge {
	if t == nil || t.app == nil {
		return nil
	}
	judge := t.app.turnClaimJudge(ctx)
	if judge == nil {
		return nil
	}
	db := t.firingDB()
	if db == nil {
		return judge
	}
	rec := &firingRecorder{db: db, base: func() firingRecord { return t.firingBase(ctx) }}
	return func(ev TurnClaimEvidence) (TurnClaimVerdict, bool) {
		v, ok := judge(ev)
		return rec.observe(ev, v, ok), ok
	}
}

// readingOf renders one judge reading for the record.
func readingOf(r TurnClaimVerdict, private bool) firingReading {
	out := firingReading{Verdict: "KEPT"}
	switch {
	case r.Unkept:
		out.Verdict, out.Claim, out.Why = "UNKEPT", r.Claim, r.Why
	case strings.TrimSpace(r.Machinery) != "":
		out.Verdict, out.Claim = "MACHINERY", r.Machinery
	}
	if private {
		out.Claim, out.Why = "", ""
	}
	return out
}

// flagged reports whether a reading found anything.
func flagged(r TurnClaimVerdict) bool {
	return r.Unkept || strings.TrimSpace(r.Machinery) != ""
}

// retryOutcome names what the reading of a retried reply said.
func retryOutcome(v TurnClaimVerdict, ok bool) string {
	switch {
	case !ok:
		return "the retry's reading reached no opinion"
	case strings.TrimSpace(v.Overturned) != "":
		return "the retry was flagged, then cleared by the confirming reading"
	case flagged(v):
		return "the retry was flagged again"
	}
	return "the retry passed"
}

// observe records what the judge did with one turn and hands the verdict back
// unchanged but for its Acted hook. Nothing it does can fail the turn.
func (r *firingRecorder) observe(ev TurnClaimEvidence, v TurnClaimVerdict, ok bool) (out TurnClaimVerdict) {
	out = v
	defer func() {
		if rec := recover(); rec != nil {
			Debug("[judge-records] recording failed: %v", rec)
			out = v
		}
	}()
	r.mu.Lock()
	defer r.mu.Unlock()
	// A reading after a correction is the reading of the retry.
	if p := r.pending; p != nil {
		r.pending = nil
		p.Retry = retryOutcome(v, ok)
		if !p.Private {
			p.RetryReply = firingExcerpt(ev.Reply)
		}
		saveFiring(r.db, *p)
	}
	if !ok || len(v.Readings) == 0 || !flagged(v.Readings[0]) {
		return out
	}
	rec := r.newRecord(ev, v)
	saveFiring(r.db, *rec)
	out.Acted = func(kind string) { r.acted(rec, kind) }
	return out
}

// newRecord builds the record for a verdict whose first reading convicted.
func (r *firingRecorder) newRecord(ev TurnClaimEvidence, v TurnClaimVerdict) *firingRecord {
	rec := r.base()
	priv := rec.Private
	first := v.Readings[0]
	rec.Source = firingSourceJudge
	rec.Arm = judgeTrigger(ev)
	rec.First = readingOf(first, priv)
	if len(v.Readings) > 1 {
		c := readingOf(v.Readings[1], priv)
		rec.Confirm = &c
	} else if strings.TrimSpace(v.Overturned) != "" {
		rec.Confirm = &firingReading{Verdict: "NO OPINION", Why: "the confirming reading could not be completed"}
	}
	rec.Finding = "machinery"
	rec.Claim = first.Machinery
	if first.Unkept {
		rec.Finding, rec.Claim = "unkept-claim", first.Claim
	}
	rec.Overturned = strings.TrimSpace(v.Overturned) != ""
	switch {
	case rec.Overturned:
		rec.Kind, rec.Outcome = "turn-judge-overturned", firingOverturned
	default:
		// Until the loop says otherwise: a conviction it took no action on,
		// which is what the round cap leaves.
		rec.Kind, rec.Outcome = rec.Finding+"-unacted", firingLetStand
	}
	rec.ToolCallCount = len(ev.ToolCalls)
	rec.ToolErrors, rec.Delivered = ev.ToolErrors, ev.Delivered
	rec.Backgrounded, rec.Unattended = ev.Backgrounded, ev.Unattended
	rec.PriorWork, rec.PriorReports, rec.CatalogTools = len(ev.PriorWork), len(ev.PriorReports), len(ev.CatalogTools)
	if priv {
		rec.Claim = ""
		return &rec
	}
	rec.Reply = firingExcerpt(ev.Reply)
	rec.GivenEstimate = strings.TrimSpace(ev.GivenEstimate)
	for _, c := range ev.ToolCalls {
		rec.ToolCalls = append(rec.ToolCalls, firingToolLabel(c))
	}
	rec.PriorTurnWork = append([]string(nil), ev.PriorTurnWork...)
	return &rec
}

// acted is the loop telling the record what it did with the verdict.
func (r *firingRecorder) acted(rec *firingRecord, kind string) {
	defer func() { _ = recover() }()
	r.mu.Lock()
	defer r.mu.Unlock()
	rec.Kind = kind
	switch {
	case kind == "turn-judge-overturned":
		rec.Outcome = firingOverturned
	case strings.HasSuffix(kind, "-uncorrected"):
		rec.Outcome = firingLetStand
	case strings.HasSuffix(kind, "-corrected"):
		rec.Outcome = firingActed
		// Said now and overwritten if the retry is read: a retry that ran its
		// tools cleanly is not selected by the pre-filter, and "never judged
		// again" is the true account of it.
		rec.Retry = "the retry was not put before the judge"
		r.pending = rec
	}
	saveFiring(r.db, *rec)
}

// --- phrase-list and framework checks ---------------------------------------

// isRecordedCheckKind picks the trail kinds that are a correction check
// firing. The claim judge's own kinds are left out, because the judge's
// recorder files those with both readings attached, and so are guardrail
// kinds, which are filed where the warden's verdict is in hand.
func isRecordedCheckKind(kind string) bool {
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "" || strings.HasPrefix(k, "guardrail") || k == "turn-judge-overturned" ||
		strings.HasPrefix(k, "unkept-claim-") || strings.HasPrefix(k, "machinery-") {
		return false
	}
	return strings.HasSuffix(k, "-corrected") || strings.HasSuffix(k, "-uncorrected") ||
		strings.HasSuffix(k, "-retried") || strings.HasPrefix(k, "ungrounded-claim-")
}

// checkFinding strips what was done from a check's kind, leaving what it found.
func checkFinding(kind string) string {
	for _, suffix := range []string{"-uncorrected", "-corrected", "-retried"} {
		if strings.HasSuffix(kind, suffix) {
			return strings.TrimSuffix(kind, suffix)
		}
	}
	return kind
}

// recordCheckFiring files a phrase-list or framework check from its trail
// entry. The detail IS the trigger context those checks have: the counts and
// the name or phrase that tripped them. Queued, never waited on.
func (t *chatTurn) recordCheckFiring(kind, detail string) {
	if t == nil || !isRecordedCheckKind(kind) {
		return
	}
	db := t.firingDB()
	if db == nil {
		return
	}
	defer func() { _ = recover() }()
	rec := t.firingBase(t.ctx)
	rec.Kind, rec.Finding, rec.Source = kind, checkFinding(kind), firingSourceCheck
	rec.First = firingReading{Verdict: "FLAGGED"}
	rec.Outcome = firingActed
	if strings.HasSuffix(kind, "-uncorrected") {
		rec.Outcome = firingLetStand
	}
	if !rec.Private {
		rec.Detail = firingExcerpt(detail)
	}
	saveFiring(db, rec)
}

// --- guardrails --------------------------------------------------------------

// recordGuardrailFiring files one warden verdict that stopped or steered a
// turn. candidate is what was judged; only a pre-output one is kept, as the
// reply excerpt, because the others are a tool call's arguments or the user's
// own request.
func (t *chatTurn) recordGuardrailFiring(rule, hook, reason, candidate, outcome string) {
	if t == nil {
		return
	}
	db := t.firingDB()
	if db == nil {
		return
	}
	defer func() { _ = recover() }()
	rec := t.firingBase(t.ctx)
	hook = strings.TrimSpace(hook)
	rec.Kind, rec.Finding, rec.Source = "guardrail:"+hook, "guardrail:"+hook, firingSourceGuardrail
	rec.Hook, rec.RuleID, rec.Outcome = hook, ruleFingerprint(rule), outcome
	rec.First = firingReading{Verdict: "VIOLATE"}
	if !rec.Private {
		rec.Rule = strings.TrimSpace(rule)
		rec.First.Why = strings.TrimSpace(reason)
		if hook == guardHookPreOutput {
			rec.Reply = firingExcerpt(candidate)
		}
	}
	saveFiring(db, rec)
}

// settleGuardrailFiring records an appeal's re-check against the block it
// disputed, as that block's confirming reading. An appeal the re-check upheld
// is the one place a guardrail is known to have been wrong, so it is marked
// overturned exactly as a judge conviction the closer reading cleared is.
//
// Found in the store rather than held on the turn: the most recent firing of
// the same rule on the same agent and session, inside the last hour, not
// already settled.
func (t *chatTurn) settleGuardrailFiring(rule, hook string, confirm firingReading, overturned bool, retry string) {
	if t == nil {
		return
	}
	db := t.firingDB()
	if db == nil {
		return
	}
	base := t.firingBase(t.ctx)
	if base.Private {
		confirm.Claim, confirm.Why = "", ""
	}
	id, agentID, session := ruleFingerprint(rule), t.agent.ID, base.Session
	since := firingStamp(time.Now().Add(-time.Hour))
	enqueueFiring(db, func(db Database) {
		keys := db.Keys(firingTable)
		sort.Strings(keys)
		for i := len(keys) - 1; i >= 0 && keys[i] >= since; i-- {
			var rec firingRecord
			if !db.Get(firingTable, keys[i], &rec) {
				continue
			}
			if rec.Source != firingSourceGuardrail || rec.RuleID != id || rec.AgentID != agentID ||
				rec.Session != session || rec.Confirm != nil || (hook != "" && rec.Hook != hook) {
				continue
			}
			c := confirm
			rec.Confirm, rec.Retry = &c, retry
			if overturned {
				rec.Overturned, rec.Outcome = true, firingOverturned
			}
			db.Set(firingTable, rec.ID, rec)
			return
		}
	})
}

// --- the admin view ----------------------------------------------------------

// firingCount is one row of a tally.
type firingCount struct {
	Key        string `json:"key"`
	Firings    int    `json:"firings"`
	Acted      int    `json:"acted"`
	LetStand   int    `json:"let_stand"`
	Overturned int    `json:"overturned"`
	// Rate is overturned over firings, as a percentage, for display.
	Rate string `json:"rate"`
}

// firingRow is a record plus the display fields the admin table reads.
type firingRow struct {
	firingRecord
	Agent          string `json:"agent"`
	FirstVerdict   string `json:"first_verdict"`
	FirstWhy       string `json:"first_why,omitempty"`
	ConfirmVerdict string `json:"confirm_verdict,omitempty"`
	ConfirmWhy     string `json:"confirm_why,omitempty"`
	Tools          string `json:"tools,omitempty"`
	// Group is finding and arm together, what overturned cases are banded by.
	Group string `json:"group"`
}

func rowOf(rec firingRecord) firingRow {
	row := firingRow{firingRecord: rec, Agent: rec.AgentName, FirstVerdict: rec.First.Verdict, FirstWhy: rec.First.Why}
	if row.Agent == "" {
		row.Agent = rec.AgentID
	}
	if rec.Confirm != nil {
		row.ConfirmVerdict, row.ConfirmWhy = rec.Confirm.Verdict, rec.Confirm.Why
	}
	row.Tools = strings.Join(rec.ToolCalls, ", ")
	arm := rec.Arm
	if arm == "" {
		arm = "no arm"
	}
	row.Group = rec.Finding + " - " + arm
	return row
}

// firingSummary is what the admin endpoint serves: tallies over the window,
// the overturned cases, and the recent firings.
type firingSummary struct {
	WindowDays int           `json:"window_days"`
	Total      int           `json:"total"`
	Overturned int           `json:"overturned_total"`
	ByKind     []firingCount `json:"by_kind"`
	ByArm      []firingCount `json:"by_arm"`
	Overturns  []firingRow   `json:"overturned"`
	Records    []firingRow   `json:"records"`
}

// firingFilter narrows the recent-firings list. Empty fields match anything.
type firingFilter struct {
	Kind       string // matches Kind or Finding
	Arm        string
	Overturned string // "1"/"true" or "0"/"false"
	Limit      int
}

func (f firingFilter) match(rec firingRecord) bool {
	if k := strings.TrimSpace(f.Kind); k != "" && !strings.EqualFold(k, rec.Kind) && !strings.EqualFold(k, rec.Finding) {
		return false
	}
	if a := strings.TrimSpace(f.Arm); a != "" && !strings.EqualFold(a, rec.Arm) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(f.Overturned)) {
	case "1", "true", "yes":
		return rec.Overturned
	case "0", "false", "no":
		return !rec.Overturned
	}
	return true
}

// summarizeFirings tallies a window of records, newest first in every list.
// The tallies cover the whole window whatever the filter; the filter narrows
// only the recent-firings list, so the rates never shift with what is being
// looked at.
func summarizeFirings(recs []firingRecord, f firingFilter) firingSummary {
	out := firingSummary{WindowDays: int(firingRetention / (24 * time.Hour)), Total: len(recs)}
	byKind, byArm := map[string]*firingCount{}, map[string]*firingCount{}
	tally := func(m map[string]*firingCount, key string, rec firingRecord) {
		c := m[key]
		if c == nil {
			c = &firingCount{Key: key}
			m[key] = c
		}
		c.Firings++
		switch rec.Outcome {
		case firingOverturned:
			c.Overturned++
		case firingLetStand:
			c.LetStand++
		default:
			c.Acted++
		}
	}
	limit := f.Limit
	if limit <= 0 || limit > firingListMax {
		limit = firingListMax
	}
	for i := len(recs) - 1; i >= 0; i-- {
		rec := recs[i]
		tally(byKind, rec.Finding, rec)
		if rec.Arm != "" {
			tally(byArm, rec.Arm, rec)
		}
		if rec.Overturned {
			out.Overturned++
			out.Overturns = append(out.Overturns, rowOf(rec))
		}
		if len(out.Records) < limit && f.match(rec) {
			out.Records = append(out.Records, rowOf(rec))
		}
	}
	// Overturned cases banded by finding and arm: the server orders the rows,
	// and the table draws a band per distinct Group in the order they arrive.
	sort.SliceStable(out.Overturns, func(i, j int) bool { return out.Overturns[i].Group < out.Overturns[j].Group })
	out.ByKind, out.ByArm = sortedCounts(byKind), sortedCounts(byArm)
	if out.Overturns == nil {
		out.Overturns = []firingRow{}
	}
	if out.Records == nil {
		out.Records = []firingRow{}
	}
	return out
}

// sortedCounts orders a tally busiest first and fills in its rates.
func sortedCounts(m map[string]*firingCount) []firingCount {
	out := make([]firingCount, 0, len(m))
	for _, c := range m {
		c.Rate = strconv.Itoa(c.Overturned*100/c.Firings) + "%"
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Firings != out[j].Firings {
			return out[i].Firings > out[j].Firings
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// handleConsoleFirings serves the admin view: GET
// /api/console/judge-records?kind=&arm=&overturned=&limit=. Admin only, because
// it spans every user's agents.
func (T *OrchestrateApp) handleConsoleFirings(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if !RequestIsAdmin(r) {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	f := firingFilter{Kind: q.Get("kind"), Arm: q.Get("arm"), Overturned: q.Get("overturned"), Limit: limit}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, summarizeFirings(loadFirings(T.DB, time.Now()), f))
}

// firingTestCase is a record exported as the start of a regression test: the
// evidence in the judge's own shape, and what each reading said about it.
type firingTestCase struct {
	// Evidence is TurnClaimEvidence as the judge would see it, filled from
	// what the record kept. Unmarshals straight into the core type.
	Evidence TurnClaimEvidence `json:"evidence"`
	// NotKept names the evidence fields the record does not hold, so the
	// test's author knows what to supply rather than mistaking an empty
	// field for an empty turn.
	NotKept    []string       `json:"not_kept"`
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Finding    string         `json:"finding"`
	Arm        string         `json:"arm,omitempty"`
	Claim      string         `json:"claim,omitempty"`
	First      firingReading  `json:"first"`
	Confirm    *firingReading `json:"confirm,omitempty"`
	Overturned bool           `json:"overturned"`
	Retry      string         `json:"retry,omitempty"`
}

func testCaseOf(rec firingRecord) firingTestCase {
	tc := firingTestCase{
		Evidence: TurnClaimEvidence{
			Reply:         rec.Reply,
			ToolCalls:     rec.ToolCalls,
			PriorTurnWork: rec.PriorTurnWork,
			ToolErrors:    rec.ToolErrors,
			Delivered:     rec.Delivered,
			Backgrounded:  rec.Backgrounded,
			GivenEstimate: rec.GivenEstimate,
			Unattended:    rec.Unattended,
		},
		NotKept: []string{"Request", "ToolOutputs", "PriorWork", "PriorReports", "CatalogTools", "LastToolError", "Now"},
		ID:      rec.ID, Kind: rec.Kind, Finding: rec.Finding, Arm: rec.Arm, Claim: rec.Claim,
		First: rec.First, Confirm: rec.Confirm, Overturned: rec.Overturned, Retry: rec.Retry,
	}
	if rec.Private {
		tc.NotKept = append(tc.NotKept, "Reply", "ToolCalls (a private turn keeps counts only)")
	} else if rec.Reply != "" {
		tc.NotKept = append(tc.NotKept, "Reply beyond its first "+strconv.Itoa(firingExcerptMax)+" characters")
	}
	return tc
}

// handleConsoleFiringExport downloads one record as a test case: GET
// /api/console/judge-records/export?id=. Admin only.
func (T *OrchestrateApp) handleConsoleFiringExport(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if !RequestIsAdmin(r) {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	var rec firingRecord
	if id == "" || T.DB == nil || !T.DB.Get(firingTable, id, &rec) {
		http.Error(w, "no such record", http.StatusNotFound)
		return
	}
	body, err := json.MarshalIndent(testCaseOf(rec), "", "  ")
	if err != nil {
		http.Error(w, "could not encode the record", http.StatusInternalServerError)
		return
	}
	name := "judge-case-" + strings.NewReplacer(".", "", ":", "").Replace(rec.ID) + ".json"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// The admin surface, contributed through the section registry like the
// deployment settings beside it.
//
// Three sections rather than one: the tallies, the overturned cases and the
// recent firings answer three different questions, and a table carries no
// heading of its own to say which one it is answering.
func init() {
	for _, s := range firingsSections() {
		sections.RegisterAdminSection(sections.AdminSectionEntry{App: "/orchestrate", Section: s})
	}
}

func firingsSections() []ui.Section {
	const api = "/orchestrate/api/console/judge-records"
	countCols := func(label string) []ui.Col {
		return []ui.Col{
			{Field: "key", Label: label, Flex: 3},
			{Field: "firings", Label: "Firings", Format: "thousands"},
			{Field: "acted", Label: "Acted", Format: "thousands"},
			{Field: "let_stand", Label: "Let stand", Format: "thousands"},
			{Field: "overturned", Label: "Overturned", Format: "thousands"},
			{Field: "rate", Label: "Overturn rate"},
		}
	}
	detail := ui.RecordView{Pairs: []ui.DisplayPair{
		{Label: "Kind", Field: "kind", Mono: true},
		{Label: "Finding", Field: "finding", Mono: true},
		{Label: "Pre-filter arm", Field: "arm"},
		{Label: "First reading", Field: "first_verdict"},
		{Label: "First reading's reason", Field: "first_why"},
		{Label: "Confirming reading", Field: "confirm_verdict"},
		{Label: "Confirming reading's reason", Field: "confirm_why"},
		{Label: "Quoted", Field: "claim", Block: true},
		{Label: "Reply", Field: "reply", Block: true},
		{Label: "Trail entry", Field: "detail", Block: true},
		{Label: "Retry", Field: "retry"},
		{Label: "Retried reply", Field: "retry_reply", Block: true},
		{Label: "Rule", Field: "rule"},
		{Label: "Hook", Field: "hook", Mono: true},
		{Label: "Tool actions", Field: "tools", Mono: true},
		{Label: "Tool errors", Field: "tool_errors"},
		{Label: "Files delivered", Field: "delivered"},
		{Label: "User", Field: "user", Mono: true},
		{Label: "Session", Field: "session", Mono: true},
		{Label: "Surface", Field: "surface"},
		{Label: "Nothing textual kept (private turn)", Field: "private"},
	}}
	rowActions := []ui.RowAction{
		ui.Expand("Detail", detail),
		{Type: "button", Label: "Export as test case", Method: "GET", Compact: true,
			PostTo: api + "/export?id={id}"},
	}
	recordCols := []ui.Col{
		{Field: "at", Label: "When", Format: "reltime", Mute: true},
		{Field: "kind", Label: "Kind", Flex: 2},
		{Field: "arm", Label: "Arm", Mute: true},
		{Field: "outcome", Label: "Outcome", Type: "badge", Badges: []ui.BadgeMapping{
			{Value: firingOverturned, Label: "overturned", Color: "warning"},
			{Value: firingActed, Label: "acted", Color: "mute"},
			{Value: firingLetStand, Label: "let stand", Color: "danger"},
			{Value: firingSteered, Label: "steered", Color: "mute"},
		}},
		{Field: "agent", Label: "Agent"},
		{Field: "surface", Label: "Surface", Mute: true},
		{Field: "claim", Label: "Quoted", Flex: 3, Line: 2},
	}
	return []ui.Section{
		{
			Group:    "Agents",
			Title:    "Correction checks",
			Subtitle: "Every correction check and guardrail that fired in the last 30 days, by finding and by pre-filter arm.",
			Detail: "A firing is a check that flagged a reply: the end-of-turn claim judge, a phrase-list correction, or a guardrail. " +
				"The judge reads a flagged reply twice, and a conviction the closer reading cleared is counted as overturned: the reply went out as written. " +
				"A guardrail block that an appeal later lifted is overturned too.\n\n" +
				"A high overturn rate on one finding or one pre-filter arm is the sign that check is too eager.\n\n" +
				"From a private turn (an agent forced private, Private mode, an incognito session) nothing textual is kept: only the check, the verdicts and the counts.",
			Wide: true,
			Body: ui.Stack{Children: []ui.Component{
				ui.Table{Source: api, RecordsField: "by_kind", RowKey: "key", Columns: countCols("Finding"),
					EmptyText: "Nothing has fired in the last 30 days."},
				ui.Table{Source: api, RecordsField: "by_arm", RowKey: "key", Columns: countCols("Pre-filter arm"),
					EmptyText: "The claim judge has convicted nothing in the last 30 days."},
			}},
		},
		{
			Group:    "Agents",
			Title:    "Overturned firings",
			Subtitle: "Convictions a closer reading or an appeal cleared, grouped by finding and arm.",
			Detail:   "Export as test case downloads a record in the judge's own evidence shape, to turn into a regression test. The export names the evidence fields the record does not keep.",
			Wide:     true,
			Body: ui.Table{Source: api, RecordsField: "overturned", RowKey: "id", GroupBy: "group",
				Columns: recordCols, RowActions: rowActions, EmptyText: "No overturned firings in the last 30 days."},
		},
		{
			Group:    "Agents",
			Title:    "Recent firings",
			Subtitle: "The latest firings, newest first. Expand one for both readings, the quoted sentence and what the retry produced.",
			Wide:     true,
			Body: ui.Table{Source: api, RecordsField: "records", RowKey: "id", Columns: recordCols, RowActions: rowActions,
				Search: true, SearchPlaceholder: "Filter by kind, arm, outcome or agent",
				EmptyText: "Nothing has fired in the last 30 days."},
		},
	}
}
