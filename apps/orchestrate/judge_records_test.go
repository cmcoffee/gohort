package orchestrate

// The record of correction checks and guardrails that fired: what it keeps,
// what it withholds, what it forgets, and that none of it can cost a turn.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// firingApp is an app whose judge answers with the given readings in order,
// and whose store is a fresh in-memory one.
func firingApp(t *testing.T, readings ...string) (*OrchestrateApp, Database) {
	t.Helper()
	turns := make([]FakeTurn, 0, len(readings))
	for _, r := range readings {
		turns = append(turns, FakeTurn{Content: r})
	}
	db := &DBase{Store: kvlite.MemStore()}
	return &OrchestrateApp{AppCore: AppCore{LLM: &FakeLLM{Turns: turns}, DB: db}}, db
}

func firingTurn(app *OrchestrateApp, agent AgentRecord) *chatTurn {
	return &chatTurn{app: app, agent: agent, user: "alice", session: &ChatSession{ID: "s1"}}
}

// flushed reads the table once everything queued has been written.
func flushed(t *testing.T, db Database) []firingRecord {
	t.Helper()
	if !flushFirings(5 * time.Second) {
		t.Fatal("the writer never drained")
	}
	return loadFirings(db, time.Now())
}

const convicted = `{"verdict":"UNKEPT","claim":"Here's the picture you asked for.","why":"nothing was attached"}`
const kept = `{"verdict":"KEPT","claim":"","why":""}`

var pictureTurn = TurnClaimEvidence{
	Request: "draw me a lighthouse",
	Reply:   "Here's the picture you asked for. " + strings.Repeat("It came out well. ", 40),
	// A failed call: the record keeps the mark and not the error text.
	ToolCalls:  []string{"image/generate [FAILED: backend said the key is sk-not-a-real-key]"},
	ToolErrors: 1,
}

// A firing is filed with BOTH readings and the pre-filter arm that selected
// the turn, then updated with what the loop did and what the retry produced.
// Without the Readings the judge hands through, the record could only say
// what was acted on, never which reading made the mistake.
func TestAFiringIsRecordedWithBothReadingsAndTheArm(t *testing.T) {
	app, db := firingApp(t, convicted, convicted, kept)
	judge := firingTurn(app, AgentRecord{ID: "a1", Name: "Helper"}).claimJudge(context.Background())
	if judge == nil {
		t.Fatal("no judge")
	}
	v, ok := judge(pictureTurn)
	if !ok || !v.Unkept {
		t.Fatalf("precondition: both readings convict, got %+v ok=%v", v, ok)
	}
	if v.Acted == nil {
		t.Fatal("the verdict carries no way for the loop to say what it did with it")
	}
	v.Acted("unkept-claim-corrected")
	// The retry, read clean.
	retry := pictureTurn
	retry.Reply = "The image backend failed, so there is no picture yet."
	if _, ok := judge(retry); !ok {
		t.Fatal("the retry reading failed")
	}

	recs := flushed(t, db)
	if len(recs) != 1 {
		t.Fatalf("want one firing, got %d: %+v", len(recs), recs)
	}
	r := recs[0]
	if r.First.Verdict != "UNKEPT" || r.Confirm == nil || r.Confirm.Verdict != "UNKEPT" {
		t.Errorf("both readings must be kept: first=%+v confirm=%+v", r.First, r.Confirm)
	}
	if r.First.Why != "nothing was attached" {
		t.Errorf("the first reading's reason was lost: %+v", r.First)
	}
	if r.Arm != "no tools ran" && r.Arm != "tool errors" {
		t.Errorf("the arm was not recorded: %q", r.Arm)
	}
	if r.Arm != judgeTrigger(pictureTurn) {
		t.Errorf("arm %q is not the pre-filter's own label %q", r.Arm, judgeTrigger(pictureTurn))
	}
	if r.Kind != "unkept-claim-corrected" || r.Outcome != firingActed || r.Finding != "unkept-claim" {
		t.Errorf("what the loop did was not recorded: kind=%q outcome=%q finding=%q", r.Kind, r.Outcome, r.Finding)
	}
	if r.Overturned {
		t.Error("a conviction both readings made is not overturned")
	}
	if r.Claim != "Here's the picture you asked for." {
		t.Errorf("the quoted sentence was not kept: %q", r.Claim)
	}
	if n := len([]rune(r.Reply)); n == 0 || n > firingExcerptMax+1 {
		t.Errorf("the reply excerpt should be short and present, got %d chars", n)
	}
	if r.Retry != "the retry passed" || !strings.Contains(r.RetryReply, "backend failed") {
		t.Errorf("what the retry produced was not recorded: retry=%q reply=%q", r.Retry, r.RetryReply)
	}
	if len(r.ToolCalls) != 1 || r.ToolCalls[0] != "image/generate [FAILED]" {
		t.Errorf("tool labels should keep the failure mark and drop the error text: %v", r.ToolCalls)
	}
	if strings.Contains(r.Reply+strings.Join(r.ToolCalls, ""), "sk-not-a-real-key") {
		t.Error("tool error text leaked into the record")
	}
	if r.AgentID != "a1" || r.AgentName != "Helper" || r.User != "alice" || r.Session != "s1" || r.Surface == "" {
		t.Errorf("identity not recorded: %+v", r)
	}
}

// A conviction the confirming reading cleared is the case the record exists
// for, so it has to be filed as overturned with the reading that cleared it.
func TestAnOverturnedVerdictIsRecordedAsOverturned(t *testing.T) {
	app, db := firingApp(t, convicted, kept)
	judge := firingTurn(app, AgentRecord{ID: "a1"}).claimJudge(context.Background())
	v, ok := judge(pictureTurn)
	if !ok || v.Unkept || v.Overturned == "" {
		t.Fatalf("precondition: overturned, got %+v", v)
	}
	if len(v.Readings) != 2 {
		t.Fatalf("an overturned verdict must still carry both readings, got %d", len(v.Readings))
	}
	v.Acted("turn-judge-overturned")

	recs := flushed(t, db)
	if len(recs) != 1 {
		t.Fatalf("want one firing, got %d", len(recs))
	}
	r := recs[0]
	if !r.Overturned || r.Outcome != firingOverturned || r.Kind != "turn-judge-overturned" {
		t.Errorf("not recorded as overturned: %+v", r)
	}
	if r.First.Verdict != "UNKEPT" || r.Confirm == nil || r.Confirm.Verdict != "KEPT" {
		t.Errorf("the overturn should show which reading said what: first=%+v confirm=%+v", r.First, r.Confirm)
	}
	if r.Finding != "unkept-claim" {
		t.Errorf("the finding is what the first reading found, whatever became of it: %q", r.Finding)
	}
}

// A turn the first reading acquits is not a firing, and records nothing.
func TestAnAcquittalRecordsNothing(t *testing.T) {
	app, db := firingApp(t, kept)
	judge := firingTurn(app, AgentRecord{ID: "a1"}).claimJudge(context.Background())
	if _, ok := judge(pictureTurn); !ok {
		t.Fatal("reading failed")
	}
	if recs := flushed(t, db); len(recs) != 0 {
		t.Errorf("an acquittal was recorded: %+v", recs)
	}
}

// Nothing textual from a private turn: an agent forced private, Private mode
// on the send, an incognito session. The shape stays, so the firing still
// counts toward the rates; the words do not.
func TestAPrivateTurnsRecordKeepsNoText(t *testing.T) {
	for name, mk := range map[string]func(*OrchestrateApp) *chatTurn{
		"agent forced private": func(app *OrchestrateApp) *chatTurn {
			return firingTurn(app, AgentRecord{ID: "a1", ForcePrivate: true})
		},
		"private mode": func(app *OrchestrateApp) *chatTurn {
			turn := firingTurn(app, AgentRecord{ID: "a1"})
			turn.privateMode = true
			return turn
		},
		"incognito session": func(app *OrchestrateApp) *chatTurn {
			turn := firingTurn(app, AgentRecord{ID: "a1"})
			turn.session.Incognito = true
			return turn
		},
		"private switched on mid-turn": func(app *OrchestrateApp) *chatTurn {
			turn := firingTurn(app, AgentRecord{ID: "a1"})
			turn.ctx = WithNetworkConnector(context.Background(), NewNetworkConnector(true))
			return turn
		},
	} {
		t.Run(name, func(t *testing.T) {
			app, db := firingApp(t, convicted, convicted, kept)
			turn := mk(app)
			judge := turn.claimJudge(context.Background())
			v, _ := judge(pictureTurn)
			v.Acted("unkept-claim-corrected")
			judge(pictureTurn)
			turn.recordCheckFiring("giveup-retried", "The reply said \"let me grab those\" and called no tool.")
			turn.recordGuardrailFiring("never discuss pricing", guardHookPreOutput, "the draft named a price", "It costs 40 dollars.", firingActed)

			recs := flushed(t, db)
			if len(recs) != 3 {
				t.Fatalf("private turns still count: want 3 records, got %d", len(recs))
			}
			for _, r := range recs {
				if !r.Private {
					t.Errorf("%s: not marked private", r.Kind)
				}
				text := r.Claim + r.Reply + r.RetryReply + r.Detail + r.Rule + r.First.Claim + r.First.Why +
					r.GivenEstimate + strings.Join(r.ToolCalls, "") + strings.Join(r.PriorTurnWork, "")
				if r.Confirm != nil {
					text += r.Confirm.Claim + r.Confirm.Why
				}
				if text != "" {
					t.Errorf("%s: a private turn's record holds text: %q", r.Kind, text)
				}
				if r.Kind == "" || r.First.Verdict == "" {
					t.Errorf("the shape must survive: %+v", r)
				}
			}
		})
	}
}

// Thirty days, then gone. Pruned on write, so nothing has to remember to.
func TestRecordsOlderThanThirtyDaysArePruned(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	old := firingRecord{ID: firingKey(time.Now().Add(-31 * 24 * time.Hour)), Kind: "giveup-retried"}
	fresh := firingRecord{ID: firingKey(time.Now().Add(-29 * 24 * time.Hour)), Kind: "giveup-retried"}
	db.Set(firingTable, old.ID, old)
	db.Set(firingTable, fresh.ID, fresh)

	// Due for a prune, as the first write after startup is. Drained first, so
	// no write left over from another test takes the prune this one is owed.
	flushFirings(5 * time.Second)
	firingWriter.mu.Lock()
	firingWriter.lastPrune = time.Time{}
	firingWriter.mu.Unlock()
	saveFiring(db, firingRecord{ID: firingKey(time.Now()), Kind: "tool-mention-corrected"})
	flushed(t, db)

	var got firingRecord
	if db.Get(firingTable, old.ID, &got) {
		t.Error("a record older than 30 days survived a write")
	}
	if !db.Get(firingTable, fresh.ID, &got) {
		t.Error("a record inside the window was pruned")
	}
	if n := db.CountKeys(firingTable); n != 2 {
		t.Errorf("want 2 records left, got %d", n)
	}
}

// panickyDB fails every write the way a broken store does.
type panickyDB struct{ Database }

func (panickyDB) Set(table, key string, value interface{}) { panic("disk on fire") }

// Recording is a side effect. A store that fails, a recorder that panics, a
// queue that is full: the verdict the turn gets is the judge's, untouched.
func TestRecordingFailureDoesNotAffectTheTurn(t *testing.T) {
	app, _ := firingApp(t, convicted, convicted)
	app.DB = panickyDB{&DBase{Store: kvlite.MemStore()}}
	judge := firingTurn(app, AgentRecord{ID: "a1"}).claimJudge(context.Background())
	v, ok := judge(pictureTurn)
	if !ok || !v.Unkept || v.Claim != "Here's the picture you asked for." || v.Why != "nothing was attached" {
		t.Fatalf("a failing store changed the verdict: %+v ok=%v", v, ok)
	}
	v.Acted("unkept-claim-corrected") // must not panic
	if !flushFirings(5 * time.Second) {
		t.Fatal("a failed write stopped the writer")
	}

	// And the writer is still alive for the next, healthy store.
	db := &DBase{Store: kvlite.MemStore()}
	saveFiring(db, firingRecord{ID: firingKey(time.Now()), Kind: "giveup-retried"})
	if recs := flushed(t, db); len(recs) != 1 {
		t.Errorf("the writer did not survive a failing store: %d records", len(recs))
	}

	// A recorder that panics inside observe hands the verdict back unchanged.
	rec := &firingRecorder{db: db, base: func() firingRecord { panic("identity lookup failed") }}
	in := TurnClaimVerdict{Unkept: true, Claim: "x", Why: "y", Readings: []TurnClaimVerdict{{Unkept: true, Claim: "x"}}}
	out := rec.observe(pictureTurn, in, true)
	if !out.Unkept || out.Claim != "x" || out.Why != "y" {
		t.Errorf("a panicking recorder changed the verdict: %+v", out)
	}
}

// Enqueueing never waits, even with the queue full.
func TestAFullQueueDropsRatherThanBlocks(t *testing.T) {
	startFiringWriter()
	block := make(chan struct{})
	db := &DBase{Store: kvlite.MemStore()}
	// Hold the writer so the queue fills.
	enqueueFiring(db, func(Database) { <-block })
	done := make(chan struct{})
	go func() {
		for i := 0; i < firingQueueMax*2; i++ {
			saveFiring(db, firingRecord{ID: firingKey(time.Now()), Kind: "giveup-retried"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("enqueueing blocked on a full queue")
	}
	close(block)
	flushFirings(5 * time.Second)
}

// The phrase-list checks reach the record through the trail, with the trail's
// own detail as their trigger context. The judge's kinds do not, because the
// judge files them itself with both readings, and neither do guardrail kinds.
func TestACheckFiringIsRecordedFromTheTrail(t *testing.T) {
	app, db := firingApp(t)
	turn := firingTurn(app, AgentRecord{ID: "a1"})
	turn.turnDiag("giveup-retried", "The turn stopped with 2 unaddressed tool error(s) and rounds to spare.")
	turn.turnDiag("unkept-claim-corrected", "recorded by the judge, not here")
	turn.turnDiag("guardrail-blocked", "recorded where the warden's verdict is")
	turn.turnDiag("tool-mention-uncorrected", "The reply again named a tool in prose without calling it.")

	recs := flushed(t, db)
	if len(recs) != 2 {
		t.Fatalf("want the two check firings, got %d: %+v", len(recs), recs)
	}
	byKind := map[string]firingRecord{}
	for _, r := range recs {
		byKind[r.Kind] = r
	}
	g, ok := byKind["giveup-retried"]
	if !ok || g.Source != firingSourceCheck || g.Finding != "giveup" || !strings.Contains(g.Detail, "2 unaddressed tool error") {
		t.Errorf("giveup not recorded with its trigger context: %+v", g)
	}
	if u := byKind["tool-mention-uncorrected"]; u.Outcome != firingLetStand {
		t.Errorf("an uncorrected check is let stand: %+v", u)
	}
}

// A guardrail block an appeal later lifted is the one place a guardrail is
// known to have been wrong, and it is filed as overturned.
func TestAnUpheldAppealOverturnsTheBlock(t *testing.T) {
	app, db := firingApp(t)
	turn := firingTurn(app, AgentRecord{ID: "a1"})
	turn.recordGuardrailFiring("only tell jokes when asked twice", guardHookPreOutput, "asked once", "Why did the chicken", firingActed)
	turn.recordGuardrailFiring("never discuss pricing", guardHookPreOutput, "named a price", "40 dollars", firingActed)
	flushed(t, db)
	turn.settleGuardrailFiring("only tell jokes when asked twice", guardHookPreOutput, firingReading{Verdict: "COMPLY", Why: "asked twice"}, true, "appealed; the block was lifted")

	var joke, price firingRecord
	for _, r := range flushed(t, db) {
		switch r.Rule {
		case "only tell jokes when asked twice":
			joke = r
		case "never discuss pricing":
			price = r
		}
	}
	if !joke.Overturned || joke.Confirm == nil || joke.Confirm.Verdict != "COMPLY" || joke.Kind != "guardrail:"+guardHookPreOutput {
		t.Errorf("the appealed block was not overturned: %+v", joke)
	}
	if price.Overturned || price.Confirm != nil {
		t.Errorf("an appeal against one rule settled another: %+v", price)
	}
	if joke.Reply == "" {
		t.Error("a pre-output block keeps the reply it stopped")
	}
}

// The admin view: tallies over the window, overturned cases, recent firings,
// and a record exported in the judge's own evidence shape. Admin only.
func TestTheAdminEndpointReturnsCountsAndRecords(t *testing.T) {
	app, _, _ := newTestOrchestrate(t)
	db := app.DB
	now := time.Now()
	for i, r := range []firingRecord{
		{Kind: "unkept-claim-corrected", Finding: "unkept-claim", Arm: "no tools ran", Outcome: firingActed, Source: firingSourceJudge,
			First: firingReading{Verdict: "UNKEPT"}, Confirm: &firingReading{Verdict: "UNKEPT"}, Reply: "Here you go.", ToolErrors: 1},
		{Kind: "turn-judge-overturned", Finding: "unkept-claim", Arm: "no tools ran", Outcome: firingOverturned, Overturned: true, Source: firingSourceJudge,
			First: firingReading{Verdict: "UNKEPT"}, Confirm: &firingReading{Verdict: "KEPT"}, Reply: "It's just past midnight."},
		{Kind: "machinery-corrected", Finding: "machinery", Arm: "background job started", Outcome: firingActed, Source: firingSourceJudge,
			First: firingReading{Verdict: "MACHINERY"}, Backgrounded: true},
		{Kind: "giveup-retried", Finding: "giveup", Outcome: firingActed, Source: firingSourceCheck, First: firingReading{Verdict: "FLAGGED"}},
	} {
		r.At = now.Add(time.Duration(i) * time.Second)
		r.ID = firingKey(r.At)
		saveFiring(db, r)
	}
	flushed(t, db)

	get := func(url string, admin bool) *httptest.ResponseRecorder {
		t.Helper()
		AuthDB().Set(AuthTable, "user:alice", AuthUser{Username: "alice", Admin: admin})
		r := asUser(httptest.NewRequest(http.MethodGet, url, nil), "alice")
		w := httptest.NewRecorder()
		if strings.Contains(url, "/export") {
			app.handleConsoleFiringExport(w, r)
		} else {
			app.handleConsoleFirings(w, r)
		}
		return w
	}
	if w := get("/api/console/judge-records", false); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin read the deployment's firings: %d", w.Code)
	}
	w := get("/api/console/judge-records", true)
	if w.Code != http.StatusOK {
		t.Fatalf("admin read failed: %d %s", w.Code, w.Body.String())
	}
	var sum firingSummary
	if err := json.Unmarshal(w.Body.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Total != 4 || sum.Overturned != 1 || len(sum.Records) != 4 || len(sum.Overturns) != 1 {
		t.Fatalf("totals wrong: %+v", sum)
	}
	if sum.Records[0].Kind != "giveup-retried" {
		t.Errorf("recent firings should be newest first, got %q first", sum.Records[0].Kind)
	}
	counts := map[string]firingCount{}
	for _, c := range sum.ByKind {
		counts[c.Key] = c
	}
	if c := counts["unkept-claim"]; c.Firings != 2 || c.Overturned != 1 || c.Rate != "50%" {
		t.Errorf("per-kind count or overturn rate wrong: %+v", c)
	}
	arms := map[string]firingCount{}
	for _, c := range sum.ByArm {
		arms[c.Key] = c
	}
	if arms["no tools ran"].Firings != 2 || arms["background job started"].Firings != 1 {
		t.Errorf("per-arm counts wrong: %+v", sum.ByArm)
	}
	if _, ok := arms[""]; ok {
		t.Error("checks with no arm should not be tallied under an empty arm")
	}
	if g := sum.Overturns[0].Group; g != "unkept-claim - no tools ran" {
		t.Errorf("overturned cases should be grouped by finding and arm: %q", g)
	}

	// Filters narrow the list and not the tallies.
	w = get("/api/console/judge-records?overturned=1", true)
	json.Unmarshal(w.Body.Bytes(), &sum)
	if len(sum.Records) != 1 || !sum.Records[0].Overturned || sum.Total != 4 {
		t.Errorf("overturned filter: %d records, total %d", len(sum.Records), sum.Total)
	}
	w = get("/api/console/judge-records?kind=machinery", true)
	json.Unmarshal(w.Body.Bytes(), &sum)
	if len(sum.Records) != 1 || sum.Records[0].Finding != "machinery" {
		t.Errorf("kind filter: %+v", sum.Records)
	}

	// Export as test case: the record in the judge's own evidence shape.
	var target firingRow
	for _, r := range sum.Overturns {
		target = r
	}
	w = get("/api/console/judge-records/export?id="+target.ID, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("export: %d %q", w.Code, w.Header().Get("Content-Disposition"))
	}
	var tc struct {
		Evidence   TurnClaimEvidence `json:"evidence"`
		NotKept    []string          `json:"not_kept"`
		Overturned bool              `json:"overturned"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tc); err != nil {
		t.Fatal(err)
	}
	if tc.Evidence.Reply != "It's just past midnight." || !tc.Overturned {
		t.Errorf("export lost the evidence: %+v", tc)
	}
	if len(tc.NotKept) == 0 || tc.NotKept[0] != "Request" {
		t.Errorf("the export should name what the record does not keep: %v", tc.NotKept)
	}
	if w := get("/api/console/judge-records/export?id=nope", true); w.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", w.Code)
	}
}
